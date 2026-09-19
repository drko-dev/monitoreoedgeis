package rtsptest

import (
	"bufio"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

// dialRaw performs a minimal hand-rolled RTSP handshake against the
// Simulator using nothing but net.Conn, so this test does not depend on the
// production rtsp client (kept in internal/rtsp; see that package's own test
// for the real-client end-to-end check).
func dialRaw(t *testing.T, addr string) net.Conn {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func sendRequest(t *testing.T, conn net.Conn, reader *bufio.Reader, method, uri string, cseq int, extraHeaders string) (status int, headers map[string]string, body string) {
	t.Helper()
	req := method + " " + uri + " RTSP/1.0\r\nCSeq: " + strconv.Itoa(cseq) + "\r\n" + extraHeaders + "\r\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		t.Fatalf("write %s: %v", method, err)
	}
	statusLine, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read status: %v", err)
	}
	parts := strings.Fields(statusLine)
	if len(parts) < 2 {
		t.Fatalf("malformed status line: %q", statusLine)
	}
	status = atoiMust(t, parts[1])

	headers = make(map[string]string)
	contentLength := 0
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read header: %v", err)
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if idx := strings.Index(line, ":"); idx != -1 {
			k := strings.ToLower(strings.TrimSpace(line[:idx]))
			v := strings.TrimSpace(line[idx+1:])
			headers[k] = v
			if k == "content-length" {
				contentLength = atoiMust(t, v)
			}
		}
	}
	if contentLength > 0 {
		buf := make([]byte, contentLength)
		if _, err := readFull(reader, buf); err != nil {
			t.Fatalf("read body: %v", err)
		}
		body = string(buf)
	}
	return status, headers, body
}

func readFull(r *bufio.Reader, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := r.Read(buf[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

func atoiMust(t *testing.T, s string) int {
	t.Helper()
	n, err := strconv.Atoi(s)
	if err != nil {
		t.Fatalf("not a number: %q", s)
	}
	return n
}

func TestSimulator_DescribeSetupPlayTeardown(t *testing.T) {
	sim, err := NewSimulator(Options{AutoPacketCount: 3, AutoPacketInterval: 5 * time.Millisecond})
	if err != nil {
		t.Fatalf("NewSimulator: %v", err)
	}
	defer sim.Close()

	conn := dialRaw(t, sim.Addr())
	reader := bufio.NewReader(conn)

	status, _, body := sendRequest(t, conn, reader, "DESCRIBE", "rtsp://"+sim.Addr()+"/live", 1, "")
	if status != 200 {
		t.Fatalf("DESCRIBE: expected 200, got %d", status)
	}
	if !strings.Contains(body, "m=video") {
		t.Fatalf("DESCRIBE body missing video track: %q", body)
	}

	status, headers, _ := sendRequest(t, conn, reader, "SETUP", "rtsp://"+sim.Addr()+"/live/track1", 2, "Transport: RTP/AVP/TCP;interleaved=0-1\r\n")
	if status != 200 {
		t.Fatalf("SETUP: expected 200, got %d", status)
	}
	if !strings.Contains(headers["transport"], "interleaved=0-1") {
		t.Fatalf("SETUP: unexpected Transport header: %q", headers["transport"])
	}

	status, _, _ = sendRequest(t, conn, reader, "PLAY", "rtsp://"+sim.Addr()+"/live", 3, "Session: rtsptest-session-1\r\n")
	if status != 200 {
		t.Fatalf("PLAY: expected 200, got %d", status)
	}

	for i := 0; i < 3; i++ {
		magic, err := reader.ReadByte()
		if err != nil {
			t.Fatalf("read interleaved magic %d: %v", i, err)
		}
		if magic != '$' {
			t.Fatalf("expected interleaved magic '$', got %q", magic)
		}
		hdr := make([]byte, 3)
		if _, err := readFull(reader, hdr); err != nil {
			t.Fatalf("read interleaved header %d: %v", i, err)
		}
		length := int(hdr[1])<<8 | int(hdr[2])
		payload := make([]byte, length)
		if _, err := readFull(reader, payload); err != nil {
			t.Fatalf("read interleaved payload %d: %v", i, err)
		}
		if !strings.HasPrefix(string(payload), "rtp-packet-") {
			t.Fatalf("unexpected payload %d: %q", i, payload)
		}
	}

	status, _, _ = sendRequest(t, conn, reader, "TEARDOWN", "rtsp://"+sim.Addr()+"/live", 4, "Session: rtsptest-session-1\r\n")
	if status != 200 {
		t.Fatalf("TEARDOWN: expected 200, got %d", status)
	}
}

func TestSimulator_DigestAuthRequired(t *testing.T) {
	sim, err := NewSimulator(Options{Username: "admin", Password: "secret"})
	if err != nil {
		t.Fatalf("NewSimulator: %v", err)
	}
	defer sim.Close()

	conn := dialRaw(t, sim.Addr())
	reader := bufio.NewReader(conn)

	status, headers, _ := sendRequest(t, conn, reader, "DESCRIBE", "rtsp://"+sim.Addr()+"/live", 1, "")
	if status != 401 {
		t.Fatalf("expected 401 without credentials, got %d", status)
	}
	if !strings.Contains(headers["www-authenticate"], "Digest") {
		t.Fatalf("expected Digest challenge, got %q", headers["www-authenticate"])
	}
}

func TestSimulator_ManualSendPacketAndCutStream(t *testing.T) {
	sim, err := NewSimulator(Options{})
	if err != nil {
		t.Fatalf("NewSimulator: %v", err)
	}
	defer sim.Close()

	conn := dialRaw(t, sim.Addr())
	reader := bufio.NewReader(conn)

	sendRequest(t, conn, reader, "DESCRIBE", "rtsp://"+sim.Addr()+"/live", 1, "")
	sendRequest(t, conn, reader, "SETUP", "rtsp://"+sim.Addr()+"/live/track1", 2, "")
	sendRequest(t, conn, reader, "PLAY", "rtsp://"+sim.Addr()+"/live", 3, "")

	if err := sim.SendPacket([]byte("manual-packet")); err != nil {
		t.Fatalf("SendPacket: %v", err)
	}

	magic, err := reader.ReadByte()
	if err != nil || magic != '$' {
		t.Fatalf("expected interleaved magic, got %q err=%v", magic, err)
	}
	hdr := make([]byte, 3)
	if _, err := readFull(reader, hdr); err != nil {
		t.Fatalf("read header: %v", err)
	}
	length := int(hdr[1])<<8 | int(hdr[2])
	payload := make([]byte, length)
	if _, err := readFull(reader, payload); err != nil {
		t.Fatalf("read payload: %v", err)
	}
	if string(payload) != "manual-packet" {
		t.Fatalf("unexpected payload: %q", payload)
	}

	sim.CutStream()
	if _, err := reader.ReadByte(); err == nil {
		t.Fatalf("expected connection closed after CutStream, but read succeeded")
	}
}
