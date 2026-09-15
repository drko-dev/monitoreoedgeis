package rtsptest

import (
	"bufio"
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testRealm = "TestCamera"
const testNonce = "0a4f113b"

// fakeServer simulates an RTSP server that challenges the first DESCRIBE
// with 401 Digest and validates the second against username/password.
type fakeServer struct {
	ln       net.Listener
	username string
	password string
	// malformedChallenge, when true, sends a 401 with a challenge missing
	// nonce/realm instead of a well-formed one.
	malformedChallenge bool
	// hang, when true, never responds (to exercise the timeout path).
	hang bool
	// unquotedQop, when true, sends qop unquoted in the challenge (as real
	// IP cameras commonly do), instead of qop="auth".
	unquotedQop bool
	// staleOnce, when true, rejects the first authenticated attempt with a
	// 401 carrying stale=true and a fresh nonce, then accepts a retry
	// against that fresh nonce.
	staleOnce bool
}

func startFakeServer(t *testing.T, s *fakeServer) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s.ln = ln
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go s.handle(conn)
		}
	}()
	return ln.Addr().String()
}

// connSeq gives every accepted connection its own nonce, so a client that
// (bug HIGH-2) presents a nonce issued on one connection over a different
// connection gets a hard mismatch instead of an accidental pass.
var connSeq int64

func (s *fakeServer) handle(conn net.Conn) {
	defer conn.Close()
	if s.hang {
		// Never read or write; the client's own deadline must fire.
		time.Sleep(10 * time.Second)
		return
	}

	nonce := fmt.Sprintf("%s-%d", testNonce, atomic.AddInt64(&connSeq, 1))
	staleSent := false
	reader := bufio.NewReader(conn)

	// Loop: a fixed client conversation on one connection is at most an
	// unauthenticated probe, an authenticated attempt, and (with staleOnce)
	// one retry — never unbounded, but more than a single request/response.
	for {
		req, authHeader := readRequest(reader)
		if req == "" {
			return
		}

		if authHeader == "" {
			if s.malformedChallenge {
				writeStatus(conn, 401, `WWW-Authenticate: Digest realm="no-nonce-here"`)
				continue
			}
			writeStatus(conn, 401, s.challengeHeader(nonce, false))
			continue
		}

		if s.staleOnce && !staleSent {
			staleSent = true
			nonce += "-fresh"
			writeStatus(conn, 401, s.challengeHeader(nonce, true))
			continue
		}

		if validateDigest(authHeader, s.username, s.password, nonce) {
			writeStatus(conn, 200, "Content-Type: application/sdp\r\nContent-Length: 0")
			return
		}
		writeStatus(conn, 401, s.challengeHeader(nonce, false))
		return
	}
}

// challengeHeader builds the WWW-Authenticate header value. qop is quoted
// unless unquotedQop is set (real IP cameras commonly send it bare).
func (s *fakeServer) challengeHeader(nonce string, stale bool) string {
	qop := `qop="auth"`
	if s.unquotedQop {
		qop = "qop=auth"
	}
	extra := ""
	if stale {
		extra = ", stale=true"
	}
	return fmt.Sprintf(`WWW-Authenticate: Digest realm="%s", nonce="%s", %s%s`, testRealm, nonce, qop, extra)
}

func readRequest(reader *bufio.Reader) (requestLine, authHeader string) {
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", ""
	}
	requestLine = strings.TrimRight(line, "\r\n")
	for {
		l, err := reader.ReadString('\n')
		if err != nil {
			return requestLine, authHeader
		}
		trimmed := strings.TrimRight(l, "\r\n")
		if trimmed == "" {
			break
		}
		if colon := strings.Index(trimmed, ":"); colon != -1 {
			name := strings.TrimSpace(trimmed[:colon])
			value := strings.TrimSpace(trimmed[colon+1:])
			if strings.EqualFold(name, "Authorization") {
				authHeader = value
			}
		}
	}
	return requestLine, authHeader
}

func writeStatus(conn net.Conn, code int, headers string) {
	fmt.Fprintf(conn, "RTSP/1.0 %d %s\r\n%s\r\n\r\n", code, statusText(code), headers)
}

func statusText(code int) string {
	if code == 200 {
		return "OK"
	}
	return "Unauthorized"
}

// validateDigest recomputes the expected RFC 2617 response server-side and
// compares it against what the client sent, to confirm TestDescribe's
// client-side computation is correct end to end.
func validateDigest(authHeader, username, password, expectedNonce string) bool {
	authHeader = strings.TrimPrefix(strings.TrimSpace(authHeader), "Digest")
	fields := map[string]string{}
	for _, part := range strings.Split(authHeader, ",") {
		part = strings.TrimSpace(part)
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			continue
		}
		key := strings.TrimSpace(kv[0])
		val := strings.Trim(strings.TrimSpace(kv[1]), `"`)
		fields[key] = val
	}
	if fields["username"] != username {
		return false
	}
	if fields["nonce"] != expectedNonce {
		return false
	}
	ha1 := testMD5Hex(username + ":" + fields["realm"] + ":" + password)
	ha2 := testMD5Hex("DESCRIBE:" + fields["uri"])
	want := testMD5Hex(ha1 + ":" + fields["nonce"] + ":" + fields["nc"] + ":" + fields["cnonce"] + ":" + fields["qop"] + ":" + ha2)
	return fields["response"] == want
}

func testMD5Hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

func TestTestDescribe_ValidCredential(t *testing.T) {
	s := &fakeServer{username: "admin", password: "correct-pw"}
	addr := startFakeServer(t, s)

	result := TestDescribe(context.Background(), addr, "/stream1", "admin", "correct-pw", 2*time.Second)
	if result.State != StateValid {
		t.Fatalf("expected VALID, got %v (err=%v)", result.State, result.Err)
	}
}

func TestTestDescribe_WrongPassword(t *testing.T) {
	s := &fakeServer{username: "admin", password: "correct-pw"}
	addr := startFakeServer(t, s)

	result := TestDescribe(context.Background(), addr, "/stream1", "admin", "wrong-pw", 2*time.Second)
	if result.State != StateInvalid {
		t.Fatalf("expected INVALID for wrong password, got %v", result.State)
	}
}

// TestTestDescribe_UnquotedQop guards HIGH-1: RFC 2617 allows qop unquoted
// in the challenge (qop=auth, no quotes), and real IP cameras commonly send
// it that way. The existing qop="auth" (quoted) case is covered by
// TestTestDescribe_ValidCredential — that was the blind spot that let this
// bug through, since the unquoted form silently fell back to the legacy
// no-qop digest formula and produced a 401 even for a correct password.
func TestTestDescribe_UnquotedQop(t *testing.T) {
	s := &fakeServer{username: "admin", password: "correct-pw", unquotedQop: true}
	addr := startFakeServer(t, s)

	result := TestDescribe(context.Background(), addr, "/stream1", "admin", "correct-pw", 2*time.Second)
	if result.State != StateValid {
		t.Fatalf("expected VALID for unquoted qop challenge, got %v (err=%v)", result.State, result.Err)
	}
}

// TestTestDescribe_StaleNonceRetry guards HIGH-2's bounded retry path: a
// server that rejects the first authenticated attempt with stale=true and a
// fresh nonce (nonce expired mid-flow) must be retried exactly once with the
// fresh nonce before giving up. It also exercises connection reuse: the fake
// server ties its nonce to the accepting connection (see connSeq), so this
// only succeeds if the authenticated attempts are sent on the same TCP
// connection that received the challenges.
func TestTestDescribe_StaleNonceRetry(t *testing.T) {
	s := &fakeServer{username: "admin", password: "correct-pw", staleOnce: true}
	addr := startFakeServer(t, s)

	result := TestDescribe(context.Background(), addr, "/stream1", "admin", "correct-pw", 2*time.Second)
	if result.State != StateValid {
		t.Fatalf("expected VALID after one stale-nonce retry, got %v (err=%v)", result.State, result.Err)
	}
}

func TestTestDescribe_MalformedChallenge(t *testing.T) {
	s := &fakeServer{malformedChallenge: true}
	addr := startFakeServer(t, s)

	result := TestDescribe(context.Background(), addr, "/stream1", "admin", "pw", 2*time.Second)
	if result.State != StateError {
		t.Fatalf("expected ERROR for malformed challenge, got %v", result.State)
	}
	if result.Err == nil {
		t.Error("expected a non-nil error describing the malformed challenge")
	}
}

func TestTestDescribe_Timeout(t *testing.T) {
	s := &fakeServer{hang: true}
	addr := startFakeServer(t, s)

	start := time.Now()
	result := TestDescribe(context.Background(), addr, "/stream1", "admin", "pw", 200*time.Millisecond)
	elapsed := time.Since(start)

	if result.State != StateUnreachable {
		t.Fatalf("expected UNREACHABLE on timeout, got %v (err=%v)", result.State, result.Err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("TestDescribe did not respect timeout, took %v", elapsed)
	}
}

func TestTestDescribe_Unreachable(t *testing.T) {
	// Nothing listens on this port.
	result := TestDescribe(context.Background(), "127.0.0.1:1", "/stream1", "admin", "pw", 500*time.Millisecond)
	if result.State != StateUnreachable {
		t.Fatalf("expected UNREACHABLE for connection refused, got %v (err=%v)", result.State, result.Err)
	}
}

func TestParseTarget_StripsUserinfo(t *testing.T) {
	addr, path, err := ParseTarget("rtsp://admin:super-secret@192.168.0.6:554/stream1")
	if err != nil {
		t.Fatalf("ParseTarget: %v", err)
	}
	if addr != "192.168.0.6:554" {
		t.Errorf("unexpected addr: %s", addr)
	}
	if path != "/stream1" {
		t.Errorf("unexpected path: %s", path)
	}
	if strings.Contains(addr+path, "super-secret") || strings.Contains(addr+path, "admin") {
		t.Fatalf("userinfo leaked into parsed target: addr=%s path=%s", addr, path)
	}
}

func TestParseTarget_DefaultPort(t *testing.T) {
	addr, _, err := ParseTarget("rtsp://192.168.0.6/stream1")
	if err != nil {
		t.Fatalf("ParseTarget: %v", err)
	}
	if addr != "192.168.0.6:554" {
		t.Errorf("expected default RTSP port 554, got %s", addr)
	}
}

func TestTestDescribe_NoSecretLeakInLogs(t *testing.T) {
	s := &fakeServer{username: "admin", password: "super-secret-pw"}
	addr := startFakeServer(t, s)

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))

	result := TestDescribe(context.Background(), addr, "/stream1", "admin", "super-secret-pw", 2*time.Second)
	logger.Info("rtsp test completed", "state", result.State, "err", result.Err)

	if strings.Contains(logBuf.String(), "super-secret-pw") {
		t.Fatalf("password leaked into logs: %s", logBuf.String())
	}
}
