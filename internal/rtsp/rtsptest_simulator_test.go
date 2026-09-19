package rtsp

// Hito W3: this test drives the real Manager/Supervisor against the
// reusable internal/rtsptest.Simulator instead of an inline, package-private
// mock, proving the simulator is usable end to end by another suite without
// duplicating the production RTSP client/parser.

import (
	"context"
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/rtsptest"
)

func TestRTSP_ManagerAgainstReusableSimulator(t *testing.T) {
	sim, err := rtsptest.NewSimulator(rtsptest.Options{
		AutoPacketCount:    100,
		AutoPacketInterval: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewSimulator: %v", err)
	}
	defer sim.Close()

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
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer stopCancel()
		_ = mgr.Stop(stopCtx)
	}()

	mgr.SetTargets([]CameraTarget{{
		CandidateKey: "sim-cand-1",
		Addr:         sim.Addr(),
		RTSPPath:     "/live",
		StreamRole:   "sub",
		Codec:        "H264",
		Width:        640,
		Height:       360,
		FPS:          15.0,
	}})

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
		t.Fatalf("expected status ONLINE, got %s (err: %s)", finalSnap.Status, finalSnap.LastErrorSafe)
	}
	if finalSnap.PacketsReceived < 1 {
		t.Errorf("expected packets > 0, got %d", finalSnap.PacketsReceived)
	}

	// W3 "cortar stream" hook: simulate the camera dropping the connection
	// mid-stream and confirm the supervisor observes the disconnect.
	sim.CutStream()
	deadline = time.Now().Add(3 * time.Second)
	sawNotOnline := false
	for time.Now().Before(deadline) {
		snaps := mgr.Snapshot()
		if len(snaps) > 0 && snaps[0].Status != StateOnline {
			sawNotOnline = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !sawNotOnline {
		t.Errorf("expected supervisor to leave ONLINE after CutStream")
	}
}
