package rtsp

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

// Session represents an active RTSP session streaming over interleaved TCP.
type Session struct {
	conn       net.Conn
	reader     *bufio.Reader
	addr       string
	rtspPath   string
	baseURI    string
	username   string
	password   string
	sessionID  string
	cseq       int
	authParams map[string]string
}

// Dial establishes a TCP connection, completes DESCRIBE -> SETUP -> PLAY over
// interleaved TCP, and returns an active Session ready to read RTP/RTCP packets.
func Dial(ctx context.Context, addr, rtspPath, username, password string, timeout time.Duration) (*Session, error) {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	baseURI := "rtsp://" + addr + rtspPath

	dialer := &net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("rtsp: dial %s: %w", addr, err)
	}

	s := &Session{
		conn:     conn,
		reader:   bufio.NewReader(conn),
		addr:     addr,
		rtspPath: rtspPath,
		baseURI:  baseURI,
		username: username,
		password: password,
		cseq:     1,
	}

	if err := s.handshake(timeout); err != nil {
		_ = conn.Close()
		return nil, err
	}

	return s, nil
}

func (s *Session) handshake(timeout time.Duration) error {
	// 1. DESCRIBE
	sdpBody, err := s.describe(timeout)
	if err != nil {
		return fmt.Errorf("rtsp: describe: %w", err)
	}

	// 2. Extract video control track from SDP
	setupURI := s.extractVideoSetupURI(sdpBody)

	// 3. SETUP
	if err := s.setup(setupURI, timeout); err != nil {
		return fmt.Errorf("rtsp: setup: %w", err)
	}

	// 4. PLAY
	if err := s.play(timeout); err != nil {
		return fmt.Errorf("rtsp: play: %w", err)
	}

	return nil
}

func (s *Session) describe(timeout time.Duration) (string, error) {
	status, headers, body, err := s.sendRequest("DESCRIBE", s.baseURI, map[string]string{"Accept": "application/sdp"}, timeout)
	if err != nil {
		return "", err
	}

	if status == 401 {
		challenge := headers["www-authenticate"]
		params, perr := parseDigestChallenge(challenge)
		if perr != nil {
			return "", fmt.Errorf("%w: parse challenge: %v", ErrAuthFailed, perr)
		}
		s.authParams = params

		// Retry with auth
		status, headers, body, err = s.sendRequest("DESCRIBE", s.baseURI, map[string]string{"Accept": "application/sdp"}, timeout)
		if err != nil {
			return "", err
		}
		if status == 401 {
			if staleChallenge := headers["www-authenticate"]; staleChallenge != "" {
				if staleParams, serr := parseDigestChallenge(staleChallenge); serr == nil && staleParams["stale"] == "true" {
					s.authParams = staleParams
					status, headers, body, err = s.sendRequest("DESCRIBE", s.baseURI, map[string]string{"Accept": "application/sdp"}, timeout)
					if err != nil {
						return "", err
					}
				}
			}
		}
	}

	if status != 200 {
		return "", fmt.Errorf("rtsp: describe status %d", status)
	}

	return body, nil
}

func (s *Session) setup(setupURI string, timeout time.Duration) error {
	extraHeaders := map[string]string{
		"Transport": "RTP/AVP/TCP;unicast;interleaved=0-1",
	}
	status, headers, _, err := s.sendRequest("SETUP", setupURI, extraHeaders, timeout)
	if err != nil {
		return err
	}

	if status == 401 && s.authParams != nil {
		status, headers, _, err = s.sendRequest("SETUP", setupURI, extraHeaders, timeout)
		if err != nil {
			return err
		}
	}

	if status != 200 {
		return fmt.Errorf("%w: status %d", ErrSetupFailed, status)
	}

	rawSession := headers["session"]
	if rawSession == "" {
		return fmt.Errorf("%w: missing Session header in response", ErrSetupFailed)
	}
	// Session value might be "12345678;timeout=60"
	s.sessionID = strings.TrimSpace(strings.Split(rawSession, ";")[0])
	return nil
}

func (s *Session) play(timeout time.Duration) error {
	extraHeaders := map[string]string{
		"Session": s.sessionID,
		"Range":   "npt=0.000-",
	}
	status, _, _, err := s.sendRequest("PLAY", s.baseURI, extraHeaders, timeout)
	if err != nil {
		return err
	}

	if status == 401 && s.authParams != nil {
		status, _, _, err = s.sendRequest("PLAY", s.baseURI, extraHeaders, timeout)
		if err != nil {
			return err
		}
	}

	if status != 200 {
		return fmt.Errorf("%w: status %d", ErrPlayFailed, status)
	}

	return nil
}

// ReadPacket reads a single interleaved RTP or RTCP packet ($ + channel + len + data).
// If no packet is received within timeout, returns ErrTimeout.
func (s *Session) ReadPacket(timeout time.Duration) (channel int, payload []byte, err error) {
	for {
		if timeout > 0 {
			if err := s.conn.SetDeadline(time.Now().Add(timeout)); err != nil {
				return 0, nil, err
			}
		}

		b, err := s.reader.ReadByte()
		if err != nil {
			if isTimeout(err) {
				return 0, nil, ErrTimeout
			}
			return 0, nil, err
		}

		// Interleaved binary data packet prefix is '$' (0x24)
		if b == '$' {
			chanByte, err := s.reader.ReadByte()
			if err != nil {
				if isTimeout(err) {
					return 0, nil, ErrTimeout
				}
				return 0, nil, err
			}

			var length uint16
			if err := binary.Read(s.reader, binary.BigEndian, &length); err != nil {
				if isTimeout(err) {
					return 0, nil, ErrTimeout
				}
				return 0, nil, err
			}

			buf := make([]byte, length)
			if _, err := io.ReadFull(s.reader, buf); err != nil {
				if isTimeout(err) {
					return 0, nil, ErrTimeout
				}
				return 0, nil, err
			}

			return int(chanByte), buf, nil
		}

		// Non-$ byte: could be RTSP control message response line, skip line
		_, _ = s.reader.ReadString('\n')
	}
}

// Teardown sends TEARDOWN and closes the connection.
func (s *Session) Teardown(timeout time.Duration) error {
	if s.conn == nil {
		return nil
	}
	defer s.conn.Close()

	if s.sessionID != "" {
		extraHeaders := map[string]string{
			"Session": s.sessionID,
		}
		_, _, _, _ = s.sendRequest("TEARDOWN", s.baseURI, extraHeaders, timeout)
	}
	return nil
}

// Close terminates the TCP connection without waiting for TEARDOWN.
func (s *Session) Close() error {
	if s.conn != nil {
		return s.conn.Close()
	}
	return nil
}

func (s *Session) sendRequest(method, uri string, extraHeaders map[string]string, timeout time.Duration) (int, map[string]string, string, error) {
	if timeout > 0 {
		if err := s.conn.SetDeadline(time.Now().Add(timeout)); err != nil {
			return 0, nil, "", err
		}
	}

	var req strings.Builder
	fmt.Fprintf(&req, "%s %s RTSP/1.0\r\n", method, uri)
	fmt.Fprintf(&req, "CSeq: %d\r\n", s.cseq)
	s.cseq++

	if s.authParams != nil && s.username != "" && s.password != "" {
		authHeader, err := buildDigestAuth(s.username, s.password, method, uri, s.authParams)
		if err == nil {
			fmt.Fprintf(&req, "Authorization: %s\r\n", authHeader)
		}
	}

	for k, v := range extraHeaders {
		fmt.Fprintf(&req, "%s: %s\r\n", k, v)
	}
	req.WriteString("\r\n")

	if _, err := s.conn.Write([]byte(req.String())); err != nil {
		return 0, nil, "", err
	}

	statusLine, err := s.reader.ReadString('\n')
	if err != nil {
		return 0, nil, "", err
	}

	fields := strings.Fields(statusLine)
	if len(fields) < 2 {
		return 0, nil, "", fmt.Errorf("rtsp: malformed status line %q", strings.TrimSpace(statusLine))
	}
	statusCode, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0, nil, "", fmt.Errorf("rtsp: malformed status code %q", fields[1])
	}

	headers := make(map[string]string)
	for {
		line, err := s.reader.ReadString('\n')
		if err != nil {
			return 0, nil, "", err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		colon := strings.Index(line, ":")
		if colon != -1 {
			k := strings.ToLower(strings.TrimSpace(line[:colon]))
			v := strings.TrimSpace(line[colon+1:])
			headers[k] = v
		}
	}

	contentLen := 0
	if clStr, ok := headers["content-length"]; ok {
		if cl, err := strconv.Atoi(clStr); err == nil && cl > 0 {
			contentLen = cl
		}
	}

	var body string
	if contentLen > 0 {
		buf := make([]byte, contentLen)
		if _, err := io.ReadFull(s.reader, buf); err != nil {
			return 0, nil, "", err
		}
		body = string(buf)
	}

	return statusCode, headers, body, nil
}

func (s *Session) extractVideoSetupURI(sdp string) string {
	lines := strings.Split(sdp, "\n")
	inVideo := false
	var controlURI string

	for _, rawLine := range lines {
		line := strings.TrimRight(rawLine, "\r\n")
		if strings.HasPrefix(line, "m=video") {
			inVideo = true
			continue
		} else if strings.HasPrefix(line, "m=") {
			inVideo = false
			continue
		}

		if inVideo && strings.HasPrefix(line, "a=control:") {
			controlURI = strings.TrimSpace(strings.TrimPrefix(line, "a=control:"))
			break
		}
	}

	if controlURI == "" {
		// Fallback: search any control after m=video or default to track1
		for _, rawLine := range lines {
			line := strings.TrimRight(rawLine, "\r\n")
			if strings.HasPrefix(line, "a=control:") {
				val := strings.TrimSpace(strings.TrimPrefix(line, "a=control:"))
				if val != "*" && !strings.HasPrefix(val, "rtsp://") {
					controlURI = val
				}
			}
		}
	}

	if controlURI == "" || controlURI == "*" {
		controlURI = "track1"
	}

	if strings.HasPrefix(controlURI, "rtsp://") {
		return controlURI
	}

	base := strings.TrimRight(s.baseURI, "/")
	control := strings.TrimLeft(controlURI, "/")
	return base + "/" + control
}

func isTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	return false
}
