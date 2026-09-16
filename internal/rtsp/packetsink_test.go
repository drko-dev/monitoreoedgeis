package rtsp

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// recordingSink records every OnPacket call it receives, for asserting which
// channel's payloads actually reached a PacketSink.
type recordingSink struct {
	mu       sync.Mutex
	payloads []string
}

func (s *recordingSink) OnPacket(candidateKey string, payload []byte, recvAt time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.payloads = append(s.payloads, string(payload))
}

func (s *recordingSink) snapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.payloads))
	copy(out, s.payloads)
	return out
}

// interleavedServer is a minimal unauthenticated RTSP/interleaved-TCP server
// that, once playing, sends alternating video (channel 0) and RTCP (channel
// 1) packets with distinguishable payload prefixes, plus an SDP fmtp line
// publishing sprop-parameter-sets so the same fixture exercises SPS/PPS
// extraction too.
type interleavedServer struct {
	listener net.Listener
	addr     string
}

const testSpropSPS = "Z0IAKeNQFAe2AtwEBAaQeJEV" // arbitrary base64, decoded bytes are what the test checks
const testSpropPPS = "aM48gA=="

func newInterleavedServer(t *testing.T) *interleavedServer {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := &interleavedServer{listener: l, addr: l.Addr().String()}
	go s.serve()
	return s
}

func (s *interleavedServer) Close() { _ = s.listener.Close() }

func (s *interleavedServer) serve() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		go s.handle(conn)
	}
}

func (s *interleavedServer) handle(conn net.Conn) {
	defer conn.Close()
	reader := bufio.NewReader(conn)
	sessionID := "abc123"

	for {
		reqLine, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		parts := strings.Fields(reqLine)
		if len(parts) < 2 {
			return
		}
		method := parts[0]

		for {
			l, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			if strings.TrimRight(l, "\r\n") == "" {
				break
			}
		}

		switch method {
		case "DESCRIBE":
			sdp := "v=0\r\n" +
				"o=- 1 1 IN IP4 127.0.0.1\r\n" +
				"s=Session\r\n" +
				"t=0 0\r\n" +
				"m=video 0 RTP/AVP 96\r\n" +
				"a=control:track1\r\n" +
				"a=rtpmap:96 H264/90000\r\n" +
				"a=fmtp:96 packetization-mode=1;sprop-parameter-sets=" + testSpropSPS + "," + testSpropPPS + "\r\n"
			resp := fmt.Sprintf("RTSP/1.0 200 OK\r\nCSeq: 1\r\nContent-Type: application/sdp\r\nContent-Length: %d\r\n\r\n%s", len(sdp), sdp)
			_, _ = conn.Write([]byte(resp))
		case "SETUP":
			resp := fmt.Sprintf("RTSP/1.0 200 OK\r\nCSeq: 2\r\nTransport: RTP/AVP/TCP;unicast;interleaved=0-1\r\nSession: %s;timeout=60\r\n\r\n", sessionID)
			_, _ = conn.Write([]byte(resp))
		case "PLAY":
			resp := fmt.Sprintf("RTSP/1.0 200 OK\r\nCSeq: 3\r\nSession: %s\r\nRange: npt=0.000-\r\n\r\n", sessionID)
			_, _ = conn.Write([]byte(resp))
			go s.sendInterleaved(conn)
		case "TEARDOWN":
			resp := fmt.Sprintf("RTSP/1.0 200 OK\r\nCSeq: 4\r\nSession: %s\r\n\r\n", sessionID)
			_, _ = conn.Write([]byte(resp))
			return
		}
	}
}

// sendInterleaved alternates channel-0 video and channel-1 RTCP frames until
// the connection breaks.
func (s *interleavedServer) sendInterleaved(conn net.Conn) {
	n := 0
	for {
		videoPayload := []byte(fmt.Sprintf("VIDEO-%d", n))
		if err := writeInterleaved(conn, 0, videoPayload); err != nil {
			return
		}
		rtcpPayload := []byte(fmt.Sprintf("RTCP-%d", n))
		if err := writeInterleaved(conn, 1, rtcpPayload); err != nil {
			return
		}
		n++
		time.Sleep(10 * time.Millisecond)
	}
}

func writeInterleaved(conn net.Conn, channel byte, payload []byte) error {
	hdr := []byte{'$', channel, 0, 0}
	binary.BigEndian.PutUint16(hdr[2:], uint16(len(payload)))
	if _, err := conn.Write(hdr); err != nil {
		return err
	}
	_, err := conn.Write(payload)
	return err
}

// TestRTSP_StreamLoopFiltersRTCP is the regression test for the BLOCKER
// raised in review: PacketSink must only ever see video RTP (the channel
// actually negotiated in SETUP), never RTCP from the sibling channel.
func TestRTSP_StreamLoopFiltersRTCP(t *testing.T) {
	srv := newInterleavedServer(t)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	sess, err := Dial(ctx, srv.addr, "/live", "", "", 2*time.Second)
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	defer sess.Teardown(1 * time.Second)

	if sess.VideoChannel() != 0 {
		t.Fatalf("expected negotiated video channel 0, got %d", sess.VideoChannel())
	}

	sup := NewSupervisor(CameraTarget{CandidateKey: "cam1"}, Config{PacketTimeout: 300 * time.Millisecond}, nil)
	sink := &recordingSink{}
	sup.SetPacketSink(sink)

	loopCtx, loopCancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer loopCancel()
	_ = sup.streamLoop(loopCtx, sess) // returns once loopCtx is done or ReadPacket times out

	payloads := sink.snapshot()
	if len(payloads) == 0 {
		t.Fatal("expected at least one packet delivered to sink")
	}
	for _, p := range payloads {
		if !strings.HasPrefix(p, "VIDEO-") {
			t.Fatalf("RTCP payload leaked into PacketSink: %q", p)
		}
	}
}

// TestRTSP_SetPacketSinkAppliesToExistingAndFutureSupervisors covers the
// concurrency requirement: SetPacketSink must reach supervisors that already
// exist AND ones SetTargets creates afterward, without a second call.
func TestRTSP_SetPacketSinkAppliesToExistingAndFutureSupervisors(t *testing.T) {
	srv1 := newInterleavedServer(t)
	defer srv1.Close()
	srv2 := newInterleavedServer(t)
	defer srv2.Close()

	cfg := Config{
		StreamRole:     StreamRoleSub,
		PacketTimeout:  300 * time.Millisecond,
		InitialBackoff: 50 * time.Millisecond,
		MaxBackoff:     200 * time.Millisecond,
		DialTimeout:    500 * time.Millisecond,
		Enabled:        true,
	}
	mgr := NewManager(cfg, &mockSink{}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := mgr.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	mgr.SetTargets([]CameraTarget{{CandidateKey: "cam1", Addr: srv1.addr, RTSPPath: "/live"}})
	waitForOnline(t, mgr, "cam1")

	sink := &recordingSink{}
	mgr.SetPacketSink(sink) // registered AFTER cam1's supervisor already exists

	waitForPayloads(t, sink)

	// cam2 is created AFTER SetPacketSink — must still receive the sink
	// without any further SetPacketSink call.
	mgr.SetTargets([]CameraTarget{
		{CandidateKey: "cam1", Addr: srv1.addr, RTSPPath: "/live"},
		{CandidateKey: "cam2", Addr: srv2.addr, RTSPPath: "/live"},
	})
	waitForOnline(t, mgr, "cam2")
	waitForPayloads(t, sink)

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer stopCancel()
	if err := mgr.Stop(stopCtx); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}
}

func waitForOnline(t *testing.T, mgr *Manager, key string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, snap := range mgr.Snapshot() {
			if snap.CandidateKey == key && snap.Status == StateOnline {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("candidate %s never reached ONLINE", key)
}

func waitForPayloads(t *testing.T, sink *recordingSink) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(sink.snapshot()) > 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("sink received no packets")
}

// TestRTSP_SupervisorDescriptorPrefersSDPCodec is the regression test for a
// real bug found during Hito H TC70 validation: this camera's ONVIF
// GetProfiles response mismapped video/audio encoder metadata, reporting
// "G711" for what was actually an H.264 video track. The video pipeline
// must trust the codec SDP actually declares (a=rtpmap), not whatever a
// separately-sourced CameraTarget.Codec says.
func TestRTSP_SupervisorDescriptorPrefersSDPCodec(t *testing.T) {
	srv := newInterleavedServer(t)
	defer srv.Close()

	cfg := Config{
		StreamRole:     StreamRoleSub,
		PacketTimeout:  300 * time.Millisecond,
		InitialBackoff: 50 * time.Millisecond,
		MaxBackoff:     200 * time.Millisecond,
		DialTimeout:    500 * time.Millisecond,
		Enabled:        true,
	}
	// Codec: "G711" deliberately mimics the real observed ONVIF bug.
	target := CameraTarget{CandidateKey: "cam1", Addr: srv.addr, RTSPPath: "/live", Codec: "G711"}
	sup := NewSupervisor(target, cfg, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sup.Start(ctx)
	defer sup.Stop()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if desc, ok := sup.Descriptor(); ok {
			if desc.Codec != "H264" {
				t.Fatalf("descriptor codec = %q, want H264 (SDP-derived, overriding ONVIF-reported G711)", desc.Codec)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("descriptor never became ready")
}

func TestRTSP_ExtractH264SpropParameterSets(t *testing.T) {
	sdp := "v=0\r\n" +
		"m=video 0 RTP/AVP 96\r\n" +
		"a=control:track1\r\n" +
		"a=fmtp:96 packetization-mode=1;sprop-parameter-sets=" + testSpropSPS + "," + testSpropPPS + "\r\n"

	nalus := extractH264SpropParameterSets(sdp)
	if len(nalus) != 2 {
		t.Fatalf("expected 2 NALUs (SPS+PPS), got %d", len(nalus))
	}
	if len(nalus[0]) == 0 || len(nalus[1]) == 0 {
		t.Fatal("expected non-empty decoded SPS/PPS bytes")
	}
}

func TestRTSP_ExtractH264SpropParameterSets_AbsentIsNil(t *testing.T) {
	sdp := "v=0\r\nm=video 0 RTP/AVP 96\r\na=control:track1\r\n"
	if nalus := extractH264SpropParameterSets(sdp); nalus != nil {
		t.Fatalf("expected nil when sprop-parameter-sets is absent, got %v", nalus)
	}
}

func TestRTSP_ExtractVideoRTPMapCodec(t *testing.T) {
	sdp := "v=0\r\n" +
		"m=audio 0 RTP/AVP 0\r\n" +
		"a=rtpmap:0 PCMU/8000\r\n" +
		"m=video 0 RTP/AVP 96\r\n" +
		"a=control:track1\r\n" +
		"a=rtpmap:96 H264/90000\r\n"

	if got := extractVideoRTPMapCodec(sdp); got != "H264" {
		t.Fatalf("extractVideoRTPMapCodec = %q, want H264", got)
	}
}

func TestRTSP_ExtractVideoRTPMapCodec_IgnoresAudioPayloadType(t *testing.T) {
	// Regression case for a real observed bug: a device whose ONVIF
	// GetProfiles response mismaps audio/video encoder metadata must not
	// fool the SDP-derived codec — a=rtpmap for the AUDIO m= line must
	// never be picked up for the video track.
	sdp := "v=0\r\n" +
		"m=audio 0 RTP/AVP 0\r\n" +
		"a=rtpmap:0 PCMU/8000\r\n" +
		"m=video 0 RTP/AVP 97\r\n" +
		"a=rtpmap:97 H265/90000\r\n"

	if got := extractVideoRTPMapCodec(sdp); got != "H265" {
		t.Fatalf("extractVideoRTPMapCodec = %q, want H265", got)
	}
}

func TestRTSP_ExtractVideoRTPMapCodec_AbsentIsEmpty(t *testing.T) {
	sdp := "v=0\r\nm=video 0 RTP/AVP 96\r\na=control:track1\r\n"
	if got := extractVideoRTPMapCodec(sdp); got != "" {
		t.Fatalf("extractVideoRTPMapCodec = %q, want empty", got)
	}
}

func TestRTSP_ParseInterleavedChannels(t *testing.T) {
	cases := []struct {
		name      string
		transport string
		wantVideo int
		wantRTCP  int
		wantOK    bool
	}{
		{"standard range", "RTP/AVP/TCP;unicast;interleaved=0-1", 0, 1, true},
		{"non-zero range", "RTP/AVP/TCP;unicast;interleaved=4-5", 4, 5, true},
		{"single value defaults rtcp to +1", "RTP/AVP/TCP;unicast;interleaved=2", 2, 3, true},
		{"missing parameter", "RTP/AVP/TCP;unicast", 0, 0, false},
		{"malformed value", "RTP/AVP/TCP;unicast;interleaved=x-y", 0, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			video, rtcp, ok := parseInterleavedChannels(tc.transport)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !tc.wantOK {
				return
			}
			if video != tc.wantVideo || rtcp != tc.wantRTCP {
				t.Fatalf("got video=%d rtcp=%d, want video=%d rtcp=%d", video, rtcp, tc.wantVideo, tc.wantRTCP)
			}
		})
	}
}
