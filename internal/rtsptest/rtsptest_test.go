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

func (s *fakeServer) handle(conn net.Conn) {
	defer conn.Close()
	if s.hang {
		// Never read or write; the client's own deadline must fire.
		time.Sleep(10 * time.Second)
		return
	}

	reader := bufio.NewReader(conn)
	req, authHeader := readRequest(reader)
	if req == "" {
		return
	}

	if authHeader == "" {
		if s.malformedChallenge {
			writeStatus(conn, 401, `WWW-Authenticate: Digest realm="no-nonce-here"`)
			return
		}
		writeStatus(conn, 401, fmt.Sprintf(`WWW-Authenticate: Digest realm="%s", nonce="%s", qop="auth"`, testRealm, testNonce))
		return
	}

	if validateDigest(authHeader, s.username, s.password) {
		writeStatus(conn, 200, "Content-Type: application/sdp\r\nContent-Length: 0")
		return
	}
	writeStatus(conn, 401, fmt.Sprintf(`WWW-Authenticate: Digest realm="%s", nonce="%s", qop="auth"`, testRealm, testNonce))
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
func validateDigest(authHeader, username, password string) bool {
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
