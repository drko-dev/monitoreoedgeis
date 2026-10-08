package rtsp

import (
	"bufio"
	"net"
	"strings"
	"testing"
	"time"
)

func TestParseSessionTimeout(t *testing.T) {
	for raw, want := range map[string]time.Duration{
		"12345678;timeout=30": 30 * time.Second,
		"abc; Timeout = 5":    5 * time.Second,
		"abc":                 defaultSessionTimeout,
		"abc;timeout=bogus":   defaultSessionTimeout,
	} {
		if got := parseSessionTimeout(raw); got != want {
			t.Errorf("parseSessionTimeout(%q) = %v, want %v", raw, got, want)
		}
	}
}

// A camera that tears PLAY down after its session timeout must receive a
// GET_PARAMETER keepalive, and ReadPacket must skip the interleaved reply
// (including its body) and keep delivering RTP.
func TestReadPacketSendsKeepaliveAndSkipsReply(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	s := &Session{
		conn:           client,
		reader:         bufio.NewReader(client),
		sessionID:      "S1",
		sessionTimeout: 2 * time.Second,
		lastKeepalive:  time.Now().Add(-2 * time.Second),
	}

	got := make(chan string, 1)
	go func() {
		r := bufio.NewReader(server)
		line, _ := r.ReadString('\n')
		for {
			l, err := r.ReadString('\n')
			if err != nil || strings.TrimSpace(l) == "" {
				break
			}
			if strings.HasPrefix(l, "Session:") {
				line += l
			}
		}
		got <- line
		body := "x: y\r\n"
		_, _ = server.Write([]byte("RTSP/1.0 200 OK\r\nCSeq: 1\r\nSession: S1\r\nContent-Length: 6\r\n\r\n" + body))
		_, _ = server.Write([]byte{'$', 0, 0, 3, 'r', 't', 'p'})
	}()

	ch, payload, err := s.ReadPacket(3 * time.Second)
	if err != nil {
		t.Fatalf("ReadPacket: %v", err)
	}
	if ch != 0 || string(payload) != "rtp" {
		t.Fatalf("got channel %d payload %q, want 0 \"rtp\"", ch, payload)
	}
	req := <-got
	if !strings.HasPrefix(req, "GET_PARAMETER ") || !strings.Contains(req, "Session: S1") {
		t.Fatalf("keepalive request = %q", req)
	}
	if time.Since(s.lastKeepalive) > time.Second {
		t.Fatal("keepalive time not refreshed")
	}
}
