package processing

import (
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/rtsp"
)

// buildPipelineSampler goes through the real constructor rather than the
// helper directly, so the assertion guards the actual production wiring (what
// newCameraPipeline hands the pipeline) and not just newPipelineSampler's own
// contract.
func buildPipelineSampler(t *testing.T, cfg Config) *Sampler {
	t.Helper()
	if cfg.RingBufferSize == 0 {
		cfg.RingBufferSize = 4
	}
	if cfg.QueueDepth == 0 {
		cfg.QueueDepth = 8
	}
	desc := rtsp.StreamDescriptor{CandidateKey: "cam1", Codec: "H264", Width: 8, Height: 8, StreamRole: "sub"}
	router := NewRouter([]Sink{NewDebugSink()}, cfg.QueueDepth, nil)
	t.Cleanup(router.Stop)
	return newCameraPipeline("cam1", desc, cfg, router, nil).sampler
}

// TestPipelineSampler_CloudModeIgnoresHybridIdleFPS is the regression guard
// for a real defect: newCameraPipeline built its sampler with
// NewAdaptiveSampler(cfg.TargetFPS, cfg.Hybrid.IdleFPS, cfg.Hybrid.IdleAfter)
// unconditionally, while NoteMotion is only ever called when a motion detector
// exists (cfg.Hybrid.Enabled). A cloud-mode Edge with
// GEOCAM_VIDEO_HYBRID_IDLE_FPS set therefore sat permanently in the sampler's
// "never saw motion" branch and emitted at IdleFPS instead of TargetFPS — the
// exact opposite of the documented contract that the Milestone J knobs do not
// change cloud mode, and a silent collision of the two commercial profiles.
func TestPipelineSampler_CloudModeIgnoresHybridIdleFPS(t *testing.T) {
	cfg := Config{
		TargetFPS: 10, // 100ms
		Hybrid: HybridConfig{
			Enabled: false, // cloud mode
			// Configured as a hybrid-only knob, but present in the same
			// config struct production wiring always populates.
			IdleFPS:   2, // 500ms
			IdleAfter: time.Second,
		},
	}

	s := buildPipelineSampler(t, cfg)
	if s.IsIdle(time.Unix(3600, 0)) {
		t.Fatal("cloud-mode pipeline reports an idle adaptive state it must not have")
	}

	t0 := time.Unix(0, 0)
	if !s.ShouldEmit(t0) {
		t.Fatal("first frame should emit")
	}
	// 100ms is the cloud-mode TargetFPS interval: it must emit here. If the
	// hybrid idle interval (500ms) had leaked in, this frame would be dropped.
	if !s.ShouldEmit(t0.Add(100 * time.Millisecond)) {
		t.Fatal("cloud mode did not emit at TargetFPS: hybrid IdleFPS leaked into the sampler")
	}
}

// TestPipelineSampler_HybridModeHonorsIdleFPS is the positive control for the
// fix above: the adaptive behavior must still be available where it is
// meaningful.
func TestPipelineSampler_HybridModeHonorsIdleFPS(t *testing.T) {
	cfg := Config{
		TargetFPS: 10, // 100ms
		Hybrid: HybridConfig{
			Enabled:   true,
			IdleFPS:   2, // 500ms
			IdleAfter: time.Second,
		},
	}

	s := buildPipelineSampler(t, cfg)
	t0 := time.Unix(0, 0)
	if !s.IsIdle(t0) {
		t.Fatal("a hybrid sampler with no motion noted yet should start idle")
	}
	if !s.ShouldEmit(t0) {
		t.Fatal("first frame should emit")
	}
	if s.ShouldEmit(t0.Add(100 * time.Millisecond)) {
		t.Fatal("idle hybrid sampler emitted at the active interval")
	}
	if !s.ShouldEmit(t0.Add(500 * time.Millisecond)) {
		t.Fatal("idle hybrid sampler did not emit at the idle interval")
	}
}

// TestPipelineSampler_NoMotionDetectorMeansNoAdaptation states the invariant
// behind both tests above in the form the defect violated: adaptive sampling
// can only be driven by NoteMotion, and NoteMotion is only reachable when a
// motion detector exists. So a pipeline with no motion detector must never
// have an idle interval at all.
func TestPipelineSampler_NoMotionDetectorMeansNoAdaptation(t *testing.T) {
	cfg := Config{
		TargetFPS: 10,
		Hybrid: HybridConfig{
			Enabled:   false,
			IdleFPS:   2,
			IdleAfter: time.Second,
		},
	}

	desc := rtsp.StreamDescriptor{CandidateKey: "cam1", Codec: "H264", Width: 8, Height: 8, StreamRole: "sub"}
	router := NewRouter([]Sink{NewDebugSink()}, 8, nil)
	defer router.Stop()
	p := newCameraPipeline("cam1", desc, cfg, router, nil)

	if p.MotionDetector() != nil {
		t.Fatal("cloud-mode pipeline somehow has a motion detector")
	}
	if p.sampler.IsIdle(time.Unix(3600, 0)) {
		t.Fatal("a pipeline with no motion detector has no source of motion feedback, so it must have no idle interval")
	}
}

// TestSampler_SetTargetFPSKeepsIdleBelowCeiling covers the second sampler
// defect: SetTargetFPS moved the active interval but never revisited the idle
// interval, so a remote-config TargetFPS reduction below the configured
// IdleFPS left the sampler emitting *faster* while idle than the new ceiling
// allowed — breaking processing.HybridConfig's documented "TargetFPS remains
// the ceiling" invariant.
func TestSampler_SetTargetFPSKeepsIdleBelowCeiling(t *testing.T) {
	s := NewAdaptiveSampler(10, 4, time.Second) // active 100ms, idle 250ms
	t0 := time.Unix(0, 0)

	// Lower the ceiling below the configured idle rate. Idle FPS is now above
	// the ceiling, so adaptive sampling must switch off entirely and the
	// sampler must honor the new 2 FPS interval alone.
	s.SetTargetFPS(2) // 500ms
	if s.IsIdle(t0) {
		t.Fatal("adaptive sampling must be disabled once IdleFPS is no longer below TargetFPS")
	}
	if !s.ShouldEmit(t0) {
		t.Fatal("first frame should emit")
	}
	if s.ShouldEmit(t0.Add(250 * time.Millisecond)) {
		t.Fatal("sampler emitted at the stale idle interval (250ms), above the new 2 FPS ceiling")
	}
	if !s.ShouldEmit(t0.Add(500 * time.Millisecond)) {
		t.Fatal("sampler did not emit at the new 2 FPS interval")
	}

	// Raising the ceiling again restores adaptive behavior.
	s.SetTargetFPS(10)
	if !s.IsIdle(t0.Add(time.Second)) {
		t.Fatal("adaptive sampling should be available again once IdleFPS is below TargetFPS")
	}
}

// TestSampler_SetTargetFPSZeroDisablesIdleAdaptation pins the degenerate case:
// targetFPS<=0 means "no sampling gate at all", so an idle interval must never
// survive to reintroduce one.
func TestSampler_SetTargetFPSZeroDisablesIdleAdaptation(t *testing.T) {
	s := NewAdaptiveSampler(10, 4, time.Second)
	s.SetTargetFPS(0)

	if s.IsIdle(time.Unix(3600, 0)) {
		t.Fatal("adaptive sampling must be off when sampling itself is disabled")
	}
	if !s.ShouldEmit(time.Unix(1, 0)) || !s.ShouldEmit(time.Unix(1, 1)) {
		t.Fatal("targetFPS<=0 must pass every frame through")
	}
}
