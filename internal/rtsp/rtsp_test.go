package rtsp

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// mockRTSPServer simulates an RTSP server supporting Digest auth and interleaved TCP streaming.
type mockRTSPServer struct {
	listener net.Listener
	addr     string
	username string
	password string
	realm    string
	nonce    string

	mu           sync.Mutex
	connections  []net.Conn
	packetSignal chan struct{}
}

func newMockRTSPServer(t *testing.T, username, password string) *mockRTSPServer {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	s := &mockRTSPServer{
		listener:     l,
		addr:         l.Addr().String(),
		username:     username,
		password:     password,
		realm:        "test-realm",
		nonce:        "test-nonce-1234",
		packetSignal: make(chan struct{}, 100),
	}
	go s.serve(t)
	return s
}

func (s *mockRTSPServer) Close() {
	_ = s.listener.Close()
	s.mu.Lock()
	for _, c := range s.connections {
		_ = c.Close()
	}
	s.mu.Unlock()
}

func (s *mockRTSPServer) serve(t *testing.T) {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		s.connections = append(s.connections, conn)
		s.mu.Unlock()
		go s.handleConn(conn)
	}
}

func (s *mockRTSPServer) handleConn(conn net.Conn) {
	defer conn.Close()
	reader := bufio.NewReader(conn)

	var sessionID string
	var playing atomic.Bool

	for {
		reqLine, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		reqLine = strings.TrimSpace(reqLine)
		if reqLine == "" {
			continue
		}
		parts := strings.Fields(reqLine)
		if len(parts) < 2 {
			return
		}
		method := parts[0]
		uri := parts[1]

		// Read headers
		headers := make(map[string]string)
		for {
			hLine, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			hLine = strings.TrimRight(hLine, "\r\n")
			if hLine == "" {
				break
			}
			if idx := strings.Index(hLine, ":"); idx != -1 {
				k := strings.ToLower(strings.TrimSpace(hLine[:idx]))
				v := strings.TrimSpace(hLine[idx+1:])
				headers[k] = v
			}
		}

		cseq := headers["cseq"]
		auth := headers["authorization"]

		switch method {
		case "DESCRIBE":
			if auth == "" {
				// Challenge
				resp := fmt.Sprintf("RTSP/1.0 401 Unauthorized\r\nCSeq: %s\r\nWWW-Authenticate: Digest realm=\"%s\", nonce=\"%s\"\r\n\r\n",
					cseq, s.realm, s.nonce)
				_, _ = conn.Write([]byte(resp))
				continue
			}

			// Validate digest
			expectedHA1 := md5Hex(s.username + ":" + s.realm + ":" + s.password)
			expectedHA2 := md5Hex("DESCRIBE:" + uri)
			expectedResp := md5Hex(expectedHA1 + ":" + s.nonce + ":" + expectedHA2)

			if !strings.Contains(auth, expectedResp) {
				resp := fmt.Sprintf("RTSP/1.0 401 Unauthorized\r\nCSeq: %s\r\n\r\n", cseq)
				_, _ = conn.Write([]byte(resp))
				continue
			}

			sdp := "v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=Session\r\nt=0 0\r\nm=video 0 RTP/AVP 96\r\na=control:track1\r\n"
			resp := fmt.Sprintf("RTSP/1.0 200 OK\r\nCSeq: %s\r\nContent-Type: application/sdp\r\nContent-Length: %d\r\n\r\n%s",
				cseq, len(sdp), sdp)
			_, _ = conn.Write([]byte(resp))

		case "SETUP":
			sessionID = "12345678"
			resp := fmt.Sprintf("RTSP/1.0 200 OK\r\nCSeq: %s\r\nTransport: RTP/AVP/TCP;unicast;interleaved=0-1\r\nSession: %s;timeout=60\r\n\r\n",
				cseq, sessionID)
			_, _ = conn.Write([]byte(resp))

		case "PLAY":
			resp := fmt.Sprintf("RTSP/1.0 200 OK\r\nCSeq: %s\r\nSession: %s\r\nRange: npt=0.000-\r\n\r\n",
				cseq, sessionID)
			_, _ = conn.Write([]byte(resp))
			playing.Store(true)

			// Launch packet sender
			go func() {
				pktCount := 0
				for playing.Load() {
					// Send RTP packet
					payload := []byte(fmt.Sprintf("rtp-packet-%d", pktCount))
					length := uint16(len(payload))

					hdr := []byte{'$', 0, 0, 0}
					binary.BigEndian.PutUint16(hdr[2:], length)

					if _, err := conn.Write(hdr); err != nil {
						return
					}
					if _, err := conn.Write(payload); err != nil {
						return
					}
					pktCount++
					time.Sleep(30 * time.Millisecond)
				}
			}()

		case "TEARDOWN":
			playing.Store(false)
			resp := fmt.Sprintf("RTSP/1.0 200 OK\r\nCSeq: %s\r\nSession: %s\r\n\r\n", cseq, sessionID)
			_, _ = conn.Write([]byte(resp))
			return
		}
	}
}

func TestRTSP_DialAndReadPackets(t *testing.T) {
	srv := newMockRTSPServer(t, "user1", "secretpass")
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sess, err := Dial(ctx, srv.addr, "/live", "user1", "secretpass", 2*time.Second)
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	defer sess.Teardown(1 * time.Second)

	// Read 5 packets
	for i := 0; i < 5; i++ {
		ch, payload, err := sess.ReadPacket(2 * time.Second)
		if err != nil {
			t.Fatalf("ReadPacket %d failed: %v", i, err)
		}
		if ch != 0 {
			t.Errorf("Expected channel 0, got %d", ch)
		}
		if !strings.HasPrefix(string(payload), "rtp-packet-") {
			t.Errorf("Unexpected payload: %s", string(payload))
		}
	}
}

func TestRTSP_DialAuthFailure(t *testing.T) {
	srv := newMockRTSPServer(t, "user1", "correctpass")
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := Dial(ctx, srv.addr, "/live", "user1", "wrongpass", 1*time.Second)
	if err == nil {
		t.Fatal("Expected auth error, got nil")
	}
}

func TestRTSP_ReadPacketTimeout(t *testing.T) {
	// Silent server: completes handshake but sends no packets
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	go func() {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		reader := bufio.NewReader(conn)
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			if strings.HasPrefix(line, "DESCRIBE") {
				for {
					l, _ := reader.ReadString('\n')
					if strings.TrimRight(l, "\r\n") == "" {
						break
					}
				}
				sdp := "m=video 0 RTP/AVP 96\r\na=control:track1\r\n"
				_, _ = conn.Write([]byte(fmt.Sprintf("RTSP/1.0 200 OK\r\nCSeq: 1\r\nContent-Length: %d\r\n\r\n%s", len(sdp), sdp)))
			} else if strings.HasPrefix(line, "SETUP") {
				for {
					l, _ := reader.ReadString('\n')
					if strings.TrimRight(l, "\r\n") == "" {
						break
					}
				}
				_, _ = conn.Write([]byte("RTSP/1.0 200 OK\r\nCSeq: 2\r\nSession: sess1\r\n\r\n"))
			} else if strings.HasPrefix(line, "PLAY") {
				for {
					l, _ := reader.ReadString('\n')
					if strings.TrimRight(l, "\r\n") == "" {
						break
					}
				}
				_, _ = conn.Write([]byte("RTSP/1.0 200 OK\r\nCSeq: 3\r\nSession: sess1\r\n\r\n"))
				// Do not send any packets: remain silent
				time.Sleep(2 * time.Second)
				return
			}
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	sess, err := Dial(ctx, l.Addr().String(), "/live", "", "", 1*time.Second)
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	defer sess.Close()

	// Reading with short timeout should produce ErrTimeout
	_, _, err = sess.ReadPacket(50 * time.Millisecond)
	if err != ErrTimeout {
		t.Errorf("Expected ErrTimeout, got %v", err)
	}
}

type mockSink struct {
	mu     sync.Mutex
	cams   []CameraStreamStatus
	called int
}

func (s *mockSink) SetCameras(cameras []CameraStreamStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cams = cameras
	s.called++
}

func TestRTSP_SupervisorAndManagerLifecycle(t *testing.T) {
	srv := newMockRTSPServer(t, "admin", "pass")
	defer srv.Close()

	sink := &mockSink{}
	cfg := Config{
		StreamRole:     StreamRoleSub,
		PacketTimeout:  500 * time.Millisecond,
		InitialBackoff: 50 * time.Millisecond,
		MaxBackoff:     200 * time.Millisecond,
		DialTimeout:    500 * time.Millisecond,
		Enabled:        true,
	}

	mgr := NewManager(cfg, sink, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := mgr.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	target := CameraTarget{
		CandidateKey: "test-cand-1",
		Addr:         srv.addr,
		RTSPPath:     "/live",
		Username:     "admin",
		Password:     "pass",
		StreamRole:   "sub",
		Codec:        "H264",
		Width:        640,
		Height:       360,
		FPS:          15.0,
	}

	mgr.SetTargets([]CameraTarget{target})

	// Wait for packets to arrive and status to become ONLINE
	var finalSnap CameraStreamStatus
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		snaps := mgr.Snapshot()
		if len(snaps) > 0 && snaps[0].Status == StateOnline && snaps[0].PacketsReceived > 0 {
			finalSnap = snaps[0]
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if finalSnap.Status != StateOnline {
		t.Fatalf("Expected status ONLINE, got %s (err: %s)", finalSnap.Status, finalSnap.LastErrorSafe)
	}
	if finalSnap.PacketsReceived < 1 {
		t.Errorf("Expected packets > 0, got %d", finalSnap.PacketsReceived)
	}
	if finalSnap.Codec != "H264" || finalSnap.Width != 640 {
		t.Errorf("Metadata mismatch: %v", finalSnap)
	}

	// Unregister target
	mgr.SetTargets([]CameraTarget{})
	time.Sleep(100 * time.Millisecond)
	if len(mgr.Snapshot()) != 0 {
		t.Errorf("Expected 0 supervisors after unregister, got %d", len(mgr.Snapshot()))
	}

	// Stop manager
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer stopCancel()
	if err := mgr.Stop(stopCtx); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}
}

func TestRTSP_CredentialSafety(t *testing.T) {
	// 1. ParseTarget must discard credentials in URL
	addr, path, err := ParseTarget("rtsp://admin:supersecret@192.168.1.100:554/stream1")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(addr, "supersecret") || strings.Contains(addr, "admin") {
		t.Errorf("Credentials leaked into addr: %s", addr)
	}
	if strings.Contains(path, "supersecret") || strings.Contains(path, "admin") {
		t.Errorf("Credentials leaked into path: %s", path)
	}

	// 2. CameraStreamStatus must never contain secret fields
	status := CameraStreamStatus{
		CandidateKey:  "key1",
		Status:        StateDegraded,
		LastErrorSafe: "rtsp: connection refused",
	}
	if strings.Contains(fmt.Sprintf("%+v", status), "password") {
		t.Error("CameraStreamStatus has password field")
	}
}

func TestSanitizeError(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		contains string
		omits    []string
	}{
		{
			name:     "rtsp url with password",
			input:    "dial rtsp://admin:secret123@192.168.1.50:554/live: connection refused",
			contains: "rtsp://[REDACTED]@192.168.1.50:554/live",
			omits:    []string{"secret123", "admin:"},
		},
		{
			name:     "query params with token and password",
			input:    "GET /stream?token=abc123xyz&password=supersecret failed",
			contains: "[REDACTED]",
			omits:    []string{"abc123xyz", "supersecret"},
		},
		{
			name:     "control characters stripped",
			input:    "error\x00with\r\nnewlines\tand\x1bcontrol",
			contains: "error with newlines and control",
			omits:    []string{"\x00", "\x1b"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SanitizeErrorMessage(tc.input)
			if tc.contains != "" && !strings.Contains(got, tc.contains) {
				t.Errorf("expected %q to contain %q", got, tc.contains)
			}
			for _, omit := range tc.omits {
				if strings.Contains(got, omit) {
					t.Errorf("expected %q NOT to contain %q", got, omit)
				}
			}
			if len(got) > 255 {
				t.Errorf("sanitized error length %d exceeds 255 bytes", len(got))
			}
		})
	}
}

func TestSupervisorReconnectAndTimeoutMonotonicity(t *testing.T) {
	sup := NewSupervisor(CameraTarget{CandidateKey: "cam-test"}, Config{}, slog.Default())

	if snap := sup.Snapshot(); snap.ReconnectCount != 0 || snap.TimeoutCount != 0 || snap.StallCount != 0 {
		t.Fatalf("expected initial counts to be 0, got reconnect=%d timeout=%d stall=%d",
			snap.ReconnectCount, snap.TimeoutCount, snap.StallCount)
	}

	// Increment reconnect count and verify it increases
	sup.incrementReconnect()
	sup.incrementReconnect()
	if snap := sup.Snapshot(); snap.ReconnectCount != 2 {
		t.Errorf("expected ReconnectCount=2, got %d", snap.ReconnectCount)
	}

	// Increment timeout / stall count
	sup.incrementTimeout()
	if snap := sup.Snapshot(); snap.TimeoutCount != 1 || snap.StallCount != 1 {
		t.Errorf("expected TimeoutCount=1 StallCount=1, got timeout=%d stall=%d", snap.TimeoutCount, snap.StallCount)
	}

	// Setting error or connecting should NOT reset ReconnectCount or TimeoutCount
	sup.recordError(errors.New("dial rtsp://user:pass123@host/path failed"), StateDegraded)
	snap := sup.Snapshot()
	if snap.ReconnectCount != 2 {
		t.Errorf("ReconnectCount was reset on recordError! got %d, want 2", snap.ReconnectCount)
	}
	if snap.TimeoutCount != 1 || snap.StallCount != 1 {
		t.Errorf("TimeoutCount/StallCount reset on recordError! got %d / %d", snap.TimeoutCount, snap.StallCount)
	}
	if strings.Contains(snap.LastErrorSafe, "pass123") {
		t.Errorf("password leaked in LastErrorSafe: %s", snap.LastErrorSafe)
	}
}
