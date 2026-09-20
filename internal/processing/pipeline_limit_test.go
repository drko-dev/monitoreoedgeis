package processing

import (
	"context"
	"log/slog"
	"strconv"
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/rtsp"
	"github.com/drko-dev/monitoreoedgeis/internal/rtsptest"
)

// TestPipelineAdmissionLimitIsObservable covers the case that used to be silent:
// with the admission ceiling already reached, a camera that is delivering
// packets is NOT decoded, and until now nothing reported that at all.
//
// It uses the ceiling of 1, camera A active, then camera B delivering packets,
// and asserts B is reported rather than dropped in silence.
func TestPipelineAdmissionLimitIsObservable(t *testing.T) {
	sim, err := rtsptest.NewSimulator(rtsptest.Options{
		AutoPacketCount: 100000, AutoPacketInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sim.Close()

	rtspMgr := rtsp.NewManager(rtsp.DefaultConfig(), nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := rtspMgr.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer rtspMgr.Stop(context.Background())

	cfg := Config{
		Enabled:                true,
		TargetFPS:              5,
		RingBufferSize:         10,
		QueueDepth:             16,
		DecodeQueueDepth:       4,
		MaxConcurrentPipelines: 1, // the ceiling under test
	}
	health := &recordingHealthSink{}
	procMgr := NewManager(cfg, rtspMgr, health, nil)
	if err := procMgr.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer procMgr.Stop(context.Background())

	// Camera A gets the only pipeline slot.
	rtspMgr.SetTargets([]rtsp.CameraTarget{{
		CandidateKey: "cam-a", Addr: sim.Addr(), RTSPPath: "/live",
		StreamRole: "sub", Codec: "H264", Width: 640, Height: 360, FPS: 15,
	}})

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if len(procMgr.ActivePipelines()) == 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := len(procMgr.ActivePipelines()); got != 1 {
		t.Fatalf("active pipelines = %d for camera A, want 1", got)
	}

	// Camera B starts delivering packets, but the ceiling is already reached.
	rtspMgr.SetTargets([]rtsp.CameraTarget{
		{CandidateKey: "cam-a", Addr: sim.Addr(), RTSPPath: "/live", StreamRole: "sub", Codec: "H264", Width: 640, Height: 360, FPS: 15},
		{CandidateKey: "cam-b", Addr: sim.Addr(), RTSPPath: "/live", StreamRole: "sub", Codec: "H264", Width: 640, Height: 360, FPS: 15},
	})

	// Wait until at least one skip has been recorded.
	deadline = time.Now().Add(10 * time.Second)
	var skipped int64
	var keys []string
	for time.Now().Before(deadline) {
		skipped, keys = procMgr.limitSkipSnapshot()
		if skipped > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if skipped == 0 {
		t.Fatal("camera B delivered packets past the admission ceiling but skipped_limit stayed 0 — the drop is still silent")
	}
	if len(keys) == 0 {
		t.Fatal("skipped_limit counted drops but reported no candidate key, so an operator cannot tell which camera")
	}
	found := false
	for _, k := range keys {
		if k == "cam-b" {
			found = true
		}
		if k == "cam-a" {
			t.Fatalf("cam-a holds the admitted slot and must not be reported as skipped: %v", keys)
		}
	}
	if !found {
		t.Fatalf("skipped keys = %v, want cam-b", keys)
	}

	// No second pipeline may have started.
	if got := len(procMgr.ActivePipelines()); got != 1 {
		t.Fatalf("active pipelines = %d, want 1 — the ceiling must not be exceeded", got)
	}

	// The diagnostic reaches /status through the video pipeline summary, which
	// is published on the manager's own ticker — so wait for a publish.
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if health.lastPipelineSummary().PipelineLimit != 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	summary := health.lastPipelineSummary()
	if summary.PipelineLimit != 1 {
		t.Errorf("pipeline_limit = %d, want 1", summary.PipelineLimit)
	}
	if summary.SkippedLimit == 0 {
		t.Error("video_pipeline.skipped_limit = 0 in the health summary")
	}
	if len(summary.SkippedLimitKeys) == 0 {
		t.Error("video_pipeline.skipped_limit_keys is empty in the health summary")
	}
}

// TestRecordLimitSkip_IsBounded proves the key set cannot grow without bound,
// while the counter keeps counting.
func TestRecordLimitSkip_IsBounded(t *testing.T) {
	mgr := &Manager{cfg: Config{MaxConcurrentPipelines: 1}, logger: slog.Default()}

	const n = maxReportedSkippedKeys * 4
	for i := 0; i < n; i++ {
		mgr.recordLimitSkip("cam-" + strconv.Itoa(i))
	}

	count, keys := mgr.limitSkipSnapshot()
	if count != int64(n) {
		t.Errorf("skip count = %d, want %d (the counter is not bounded)", count, n)
	}
	if len(keys) != maxReportedSkippedKeys {
		t.Errorf("reported keys = %d, want the bounded maximum %d", len(keys), maxReportedSkippedKeys)
	}
}

// TestRecordLimitSkip_DistinctKeysRecordedOnce pins that repeated skips for the
// same camera do not duplicate its key, and never grow the set past the cap.
func TestRecordLimitSkip_DistinctKeysRecordedOnce(t *testing.T) {
	mgr := &Manager{cfg: Config{MaxConcurrentPipelines: 2}, logger: slog.Default()}

	for i := 0; i < 100; i++ {
		mgr.recordLimitSkip("cam-a")
		mgr.recordLimitSkip("cam-b")
	}

	count, keys := mgr.limitSkipSnapshot()
	if count != 200 {
		t.Errorf("skip count = %d, want 200", count)
	}
	if len(keys) != 2 {
		t.Errorf("reported keys = %v, want exactly [cam-a cam-b] with no duplicates", keys)
	}
}
