// Package rtsptest is a minimal one-shot RTSP Digest-auth credential test
// client (Hito F Bloque 2). It implements ONLY: TCP connect, an
// unauthenticated DESCRIBE, receiving the RFC 2617 Digest challenge,
// computing the response, and resending DESCRIBE with Authorization.
//
// EXPLICITLY OUT OF SCOPE, and not implemented here: SETUP, PLAY, RTP,
// RTCP, frame/video handling, reconnect or health loops — that is Hito G.
package rtsptest

import (
	"bufio"
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// State is the classification of a one-shot RTSP credential test.
type State string

const (
	StateValid       State = "VALID"
	StateInvalid     State = "INVALID"
	StateUnreachable State = "UNREACHABLE"
	StateError       State = "ERROR"
)

// Result is the outcome of TestDescribe. It never carries the plaintext
// password, the computed Authorization header, or a raw credentialed URI.
type Result struct {
	State State
	Err   error
}

// digestParamRe matches both quoted (realm="X") and unquoted (qop=auth)
// challenge parameters — RFC 2617 allows qop/algorithm/stale unquoted, and
// real IP cameras commonly send qop=auth without quotes.
var digestParamRe = regexp.MustCompile(`(\w+)\s*=\s*(?:"([^"]*)"|([^,\s]+))`)

// ParseTarget splits a full rtsp:// URL into a dial address ("host:port")
// and a request path. Any userinfo in rawURL (rtsp://user:pass@host/...)
// is discarded by net/url itself — it never reaches addr or path, so
// credentials in a caller-supplied URL can never propagate into a log or
// result derived from them.
func ParseTarget(rawURL string) (addr, path string, err error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", "", fmt.Errorf("rtsptest: parse RTSP URL: %w", err)
	}
	if u.Scheme != "rtsp" {
		return "", "", fmt.Errorf("rtsptest: unsupported scheme %q", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return "", "", errors.New("rtsptest: RTSP URL missing host")
	}
	port := u.Port()
	if port == "" {
		port = "554"
	}
	return net.JoinHostPort(host, port), u.EscapedPath(), nil
}

// TestDescribe performs a one-shot RTSP DESCRIBE against addr ("host:port")
// for rtspPath ("/path..."), expecting an RFC 2617 Digest challenge (with or
// without qop) and authenticating with username/password. Every network
// operation is bounded by timeout so a hung or silent device cannot block
// indefinitely.
func TestDescribe(ctx context.Context, addr, rtspPath, username, password string, timeout time.Duration) Result {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	uri := "rtsp://" + addr + rtspPath

	// One TCP connection for the whole flow: many embedded RTSP servers (IP
	// cameras) tie the digest nonce they issue to the connection that
	// received it and reject an otherwise-correct digest sent over a new
	// connection.
	dialer := &net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return Result{State: classify(err), Err: err}
	}
	defer conn.Close()

	cseq := 1
	status, challenge, err := sendDescribe(conn, addr, rtspPath, timeout, "", cseq)
	if err != nil {
		return Result{State: classify(err), Err: err}
	}
	if status == 200 {
		return Result{State: StateValid}
	}
	if status != 401 {
		return Result{State: StateError, Err: fmt.Errorf("rtsptest: unexpected status %d to unauthenticated DESCRIBE", status)}
	}

	params, err := parseDigestChallenge(challenge)
	if err != nil {
		return Result{State: StateError, Err: err}
	}

	// At most 2 authenticated attempts: a nonce can go stale mid-flow, in
	// which case the server's second 401 carries stale=true plus a fresh
	// nonce. Retry once against that fresh nonce, never loop further.
	for attempt := 0; attempt < 2; attempt++ {
		authHeader, err := buildDigestAuth(username, password, "DESCRIBE", uri, params)
		if err != nil {
			return Result{State: StateError, Err: err}
		}

		cseq++
		status2, challenge2, err := sendDescribe(conn, addr, rtspPath, timeout, authHeader, cseq)
		if err != nil {
			return Result{State: classify(err), Err: err}
		}
		switch status2 {
		case 200:
			return Result{State: StateValid}
		case 401:
			if attempt == 0 {
				if params2, perr := parseDigestChallenge(challenge2); perr == nil && params2["stale"] == "true" {
					params = params2
					continue
				}
			}
			return Result{State: StateInvalid}
		default:
			return Result{State: StateError, Err: fmt.Errorf("rtsptest: unexpected status %d to authenticated DESCRIBE", status2)}
		}
	}
	return Result{State: StateInvalid}
}

// sendDescribe sends a DESCRIBE request (optionally with an Authorization
// header) over an already-connected conn and returns the status code and the
// WWW-Authenticate header value (empty if absent). conn is reused across
// every attempt of a single TestDescribe call; the caller owns closing it.
func sendDescribe(conn net.Conn, addr, rtspPath string, timeout time.Duration, authHeader string, cseq int) (int, string, error) {
	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		return 0, "", err
	}

	var req strings.Builder
	fmt.Fprintf(&req, "DESCRIBE rtsp://%s%s RTSP/1.0\r\n", addr, rtspPath)
	fmt.Fprintf(&req, "CSeq: %d\r\n", cseq)
	req.WriteString("Accept: application/sdp\r\n")
	if authHeader != "" {
		req.WriteString("Authorization: " + authHeader + "\r\n")
	}
	req.WriteString("\r\n")

	if _, err := conn.Write([]byte(req.String())); err != nil {
		return 0, "", err
	}

	reader := bufio.NewReader(conn)
	statusLine, err := reader.ReadString('\n')
	if err != nil {
		return 0, "", err
	}
	fields := strings.Fields(statusLine)
	if len(fields) < 2 {
		return 0, "", fmt.Errorf("rtsptest: malformed status line %q", strings.TrimSpace(statusLine))
	}
	status, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0, "", fmt.Errorf("rtsptest: malformed status code %q", fields[1])
	}

	var wwwAuth string
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return 0, "", err
		}
		trimmed := strings.TrimRight(line, "\r\n")
		if trimmed == "" {
			break
		}
		if colon := strings.Index(trimmed, ":"); colon != -1 {
			name := strings.TrimSpace(trimmed[:colon])
			value := strings.TrimSpace(trimmed[colon+1:])
			if strings.EqualFold(name, "WWW-Authenticate") {
				wwwAuth = value
			}
		}
	}

	return status, wwwAuth, nil
}

// parseDigestChallenge extracts realm/nonce/qop/opaque from a
// WWW-Authenticate: Digest ... header value.
func parseDigestChallenge(header string) (map[string]string, error) {
	if !strings.HasPrefix(strings.TrimSpace(header), "Digest") {
		return nil, fmt.Errorf("rtsptest: WWW-Authenticate is not a Digest challenge")
	}
	params := map[string]string{}
	for _, m := range digestParamRe.FindAllStringSubmatch(header, -1) {
		v := m[2]
		if v == "" {
			v = m[3]
		}
		params[strings.ToLower(m[1])] = v
	}
	if params["realm"] == "" || params["nonce"] == "" {
		return nil, errors.New("rtsptest: digest challenge missing realm or nonce")
	}
	return params, nil
}

// buildDigestAuth computes the RFC 2617 Authorization header value:
//
//	HA1 = MD5(username:realm:password)
//	HA2 = MD5(method:uri)
//	response = MD5(HA1:nonce:HA2)                              (no qop)
//	response = MD5(HA1:nonce:nc:cnonce:qop:HA2)                (qop=auth)
//
// The plaintext password is consumed only to compute HA1: it never appears
// in the returned header string or any error.
func buildDigestAuth(username, password, method, uri string, params map[string]string) (string, error) {
	realm := params["realm"]
	nonce := params["nonce"]
	qop := firstQop(params["qop"])

	ha1 := md5Hex(username + ":" + realm + ":" + password)
	ha2 := md5Hex(method + ":" + uri)

	var response, cnonce, nc string
	if qop != "" {
		cnonceBytes := make([]byte, 8)
		if _, err := rand.Read(cnonceBytes); err != nil {
			return "", fmt.Errorf("rtsptest: generate cnonce: %w", err)
		}
		cnonce = hex.EncodeToString(cnonceBytes)
		nc = "00000001"
		response = md5Hex(ha1 + ":" + nonce + ":" + nc + ":" + cnonce + ":" + qop + ":" + ha2)
	} else {
		response = md5Hex(ha1 + ":" + nonce + ":" + ha2)
	}

	var b strings.Builder
	fmt.Fprintf(&b, `Digest username="%s", realm="%s", nonce="%s", uri="%s", response="%s"`,
		username, realm, nonce, uri, response)
	if opaque := params["opaque"]; opaque != "" {
		fmt.Fprintf(&b, `, opaque="%s"`, opaque)
	}
	if qop != "" {
		fmt.Fprintf(&b, `, qop=%s, nc=%s, cnonce="%s"`, qop, nc, cnonce)
	}
	return b.String(), nil
}

func firstQop(qop string) string {
	if qop == "" {
		return ""
	}
	parts := strings.Split(qop, ",")
	return strings.TrimSpace(parts[0])
}

func md5Hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

func classify(err error) State {
	var netErr net.Error
	if errors.As(err, &netErr) {
		return StateUnreachable
	}
	return StateError
}
