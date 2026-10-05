package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/processing"
	"github.com/drko-dev/monitoreoedgeis/internal/rtsp"
	"github.com/drko-dev/monitoreoedgeis/internal/rtsptest"
)

// TestG1_MultiCameraSupervisionIsolationAndLifecycle drives three real RTSP
// simulators (Digest auth) through the production rtsp.Manager.SetTargets ->
// processing.Manager path, the same hand-off the reconciler performs. It covers
// what the single-camera test cannot: simultaneous cameras, one camera losing
// its stream without affecting the others, reconnect, removal, a credential
// change that restarts only that camera, and secrecy of the status surfaces.
//
// UNIT/INTEGRATION only: the simulators speak real RTSP/RTP but are not
// cameras, so this proves nothing about a physical NVR (REAL_CAMERA_VERIFIED=NO).
func TestG1_MultiCameraSupervisionIsolationAndLifecycle(t *testing.T) {
	const user, pass = "gw-user", "gw-s3cret-pass"
	keys := []string{"cam-a", "cam-b", "cam-c"}

	sims := make([]*rtsptest.Simulator, len(keys))
	for i := range keys {
		sim, err := rtsptest.NewSimulator(rtsptest.Options{
			Username:           user,
			Password:           pass,
			AutoPacketCount:    100000,
			AutoPacketInterval: 20 * time.Millisecond,
		})
		if err != nil {
			t.Fatalf("NewSimulator %d: %v", i, err)
		}
		defer sim.Close()
		sims[i] = sim
	}

	rtspMgr := rtsp.NewManager(rtsp.Config{
		StreamRole:     "sub",
		PacketTimeout:  1 * time.Second,
		InitialBackoff: 50 * time.Millisecond,
		MaxBackoff:     300 * time.Millisecond,
		DialTimeout:    1 * time.Second,
		Enabled:        true,
	}, nil, nil)

	health := &recordingHealthSink{}
	procMgr := processing.NewManager(processing.Config{
		Enabled:                true,
		TargetFPS:              15,
		OutputWidth:            640,
		OutputHeight:           360,
		RingBufferSize:         10,
		QueueDepth:             16,
		DecodeQueueDepth:       4,
		MaxConcurrentPipelines: 7,
		FFmpegPath:             "ffmpeg",
		DecodeTimeout:          2 * time.Second,
	}, rtspMgr, health, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := rtspMgr.Start(ctx); err != nil {
		t.Fatalf("rtspMgr.Start: %v", err)
	}
	defer func() {
		stopCtx, c := context.WithTimeout(context.Background(), 3*time.Second)
		defer c()
		_ = rtspMgr.Stop(stopCtx)
	}()
	if err := procMgr.Start(ctx); err != nil {
		t.Fatalf("procMgr.Start: %v", err)
	}
	defer func() {
		stopCtx, c := context.WithTimeout(context.Background(), 3*time.Second)
		defer c()
		_ = procMgr.Stop(stopCtx)
	}()

	target := func(i int, password string) rtsp.CameraTarget {
		return rtsp.CameraTarget{
			CandidateKey: keys[i],
			Addr:         sims[i].Addr(),
			RTSPPath:     "/stream2",
			Username:     user,
			Password:     password,
			StreamRole:   "sub",
		}
	}
	state := func(key string) (rtsp.CameraStreamStatus, bool) {
		for _, s := range rtspMgr.Snapshot() {
			if s.CandidateKey == key {
				return s, true
			}
		}
		return rtsp.CameraStreamStatus{}, false
	}
	online := func(key string) bool {
		s, ok := state(key)
		return ok && s.Status == rtsp.StateOnline
	}

	// 1. Three cameras at once: all online, all reach processing.
	rtspMgr.SetTargets([]rtsp.CameraTarget{target(0, pass), target(1, pass), target(2, pass)})
	g1bWaitFor(t, "three supervisors online", 5*time.Second, func() bool {
		return online("cam-a") && online("cam-b") && online("cam-c")
	})
	g1bWaitFor(t, "processing pipelines for all three", 5*time.Second, func() bool {
		return len(procMgr.ActivePipelines()) == 3
	})
	g1bWaitFor(t, "health reports three cameras", 5*time.Second, func() bool {
		return health.snapshot().CameraCount == 3
	})

	// 2. One camera drops its stream: the others keep receiving packets,
	// the dropped one reconnects on its own.
	before, _ := state("cam-a")
	sims[1].CutStream()
	g1bWaitFor(t, "cam-b to register a reconnect", 5*time.Second, func() bool {
		s, ok := state("cam-b")
		return ok && s.ReconnectCount > 0
	})
	g1bWaitFor(t, "cam-a still advancing while cam-b recovers", 5*time.Second, func() bool {
		s, _ := state("cam-a")
		return s.Status == rtsp.StateOnline && s.PacketsReceived > before.PacketsReceived
	})
	g1bWaitFor(t, "cam-b back online after reconnect", 8*time.Second, func() bool {
		return online("cam-b")
	})
	if !online("cam-c") {
		t.Fatalf("cam-c must be unaffected by cam-b losing its stream")
	}

	// 3. Credential change restarts only that camera.
	cBefore, _ := state("cam-c")
	rtspMgr.SetTargets([]rtsp.CameraTarget{target(0, "rotated-wrong-pass"), target(1, pass), target(2, pass)})
	g1bWaitFor(t, "cam-a to fail auth with the rotated credential", 8*time.Second, func() bool {
		s, ok := state("cam-a")
		return ok && s.Status == rtsp.StateAuthFailed
	})
	cAfter, _ := state("cam-c")
	if cAfter.Status != rtsp.StateOnline || cAfter.ReconnectCount != cBefore.ReconnectCount {
		t.Fatalf("cam-c was disturbed by cam-a's credential change: before=%+v after=%+v", cBefore, cAfter)
	}

	// 4. Removal: the target disappears, the others stay supervised.
	rtspMgr.SetTargets([]rtsp.CameraTarget{target(0, pass), target(1, pass)})
	g1bWaitFor(t, "cam-c to be removed", 5*time.Second, func() bool {
		_, ok := state("cam-c")
		return !ok && len(rtspMgr.KnownCameras()) == 2
	})
	g1bWaitFor(t, "cam-a recovers with the right credential", 8*time.Second, func() bool {
		return online("cam-a") && online("cam-b")
	})

	// 5. No credential material on any status surface.
	snap, err := json.Marshal(rtspMgr.Snapshot())
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	sum, err := json.Marshal(health.snapshot())
	if err != nil {
		t.Fatalf("marshal health summary: %v", err)
	}
	for _, surface := range []string{string(snap), string(sum)} {
		for _, secret := range []string{user, pass, "rotated-wrong-pass"} {
			if strings.Contains(surface, secret) {
				t.Fatalf("credential material %q leaked into a status surface: %s", secret, surface)
			}
		}
	}
}
