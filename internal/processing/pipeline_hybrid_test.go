package processing

import (
	"context"
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/rtsp"
)

// hybridTestFrames builds the yuv420p Data for 4 consecutive 8x8 decoded
// frames (Y plane 100 uniform, then a hard block-(0,0) change on frame 3,
// held on frame 4) used by the hybrid pipeline acceptance tests below.
// seq is 1-based, matching fakeDecoder's PipelineSeq.
func hybridTestFrame(seq int64) []byte {
	const w, h = 8, 8
	y := makeYPlane(100, 0, -1, -1)
	if seq >= 3 {
		y = makeYPlane(100, 220, 0, 0)
	}
	full := make([]byte, w*h*3/2) // Y + U + V (chroma content irrelevant here)
	copy(full, y)
	return full
}

func newHybridConfig() HybridConfig {
	return HybridConfig{
		Enabled:         true,
		MotionThreshold: 10,
		MinChangedArea:  0.2,
		BlockSize:       4,
	}
}

// TestPipeline_HybridFiltersNonCandidateFrames is Hito J's J1/J3 end-to-end
// acceptance test: with hybrid mode enabled, only frames the motion
// evaluator flags as candidates reach the Router/sink; the rest are
// filtered before Dispatch (never counted as framesDropped).
func TestPipeline_HybridFiltersNonCandidateFrames(t *testing.T) {
	cfg := Config{
		RingBufferSize: 10,
		QueueDepth:     16,
		Hybrid:         newHybridConfig(),
	}
	desc := rtsp.StreamDescriptor{CandidateKey: "cam1", Codec: "H264", Width: 8, Height: 8, StreamRole: "sub"}

	debug := NewDebugSink()
	router := NewRouter([]Sink{debug}, 8, nil)
	defer router.Stop()

	p := newCameraPipeline("cam1", desc, cfg, router, nil)
	fd := newFakeDecoder(8, 8)
	fd.dataFn = hybridTestFrame
	p.decoderFactory = func() (VideoDecoder, error) { return fd, nil }

	ctx, cancel := context.WithCancel(context.Background())
	p.Start(ctx)

	const n = 4 // seq1,2: stable (no candidate); seq3: motion (candidate); seq4: stable again
	for i := 0; i < n; i++ {
		pkt := buildRTPPacket(true, 96, uint16(i), uint32(i*3000), 1, nil, 0, 0, []byte{0x65, byte(i)})
		p.OnPacket(pkt, time.Now())
		time.Sleep(5 * time.Millisecond)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		st := p.Status()
		if st.Hybrid != nil && st.Hybrid.FramesEvaluated >= n {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	status := p.Status()
	if status.Hybrid == nil {
		t.Fatal("expected non-nil Hybrid status block when Hybrid.Enabled")
	}
	if status.Hybrid.FramesEvaluated != n {
		t.Fatalf("FramesEvaluated = %d, want %d", status.Hybrid.FramesEvaluated, n)
	}
	if status.Hybrid.MotionCandidates != 1 {
		t.Fatalf("MotionCandidates = %d, want 1 (only seq3 changed)", status.Hybrid.MotionCandidates)
	}
	if status.Hybrid.FramesFiltered != n-1 {
		t.Fatalf("FramesFiltered = %d, want %d", status.Hybrid.FramesFiltered, n-1)
	}
	if debug.Count() != 1 {
		t.Fatalf("debug sink received %d frames, want 1 (only the motion-candidate frame is dispatched)", debug.Count())
	}
	// A hybrid-filtered frame is a policy decision, never an error: it
	// must not inflate FramesDropped.
	if status.FramesDropped != 0 {
		t.Fatalf("FramesDropped = %d, want 0 -- hybrid filtering must not count as a drop", status.FramesDropped)
	}

	cancel()
	p.Wait()
}

// TestPipeline_CloudModeDispatchesEveryFrame is the explicit J1 no-
// regression acceptance test: with Hybrid left at its zero value (as
// config.ModeCloud wiring produces), every sampled frame is dispatched,
// unfiltered, exactly as before Milestone J.
func TestPipeline_CloudModeDispatchesEveryFrame(t *testing.T) {
	cfg := Config{
		RingBufferSize: 10,
		QueueDepth:     16,
		// Hybrid left zero-valued: Enabled defaults to false.
	}
	desc := rtsp.StreamDescriptor{CandidateKey: "cam1", Codec: "H264", Width: 8, Height: 8, StreamRole: "sub"}

	debug := NewDebugSink()
	router := NewRouter([]Sink{debug}, 8, nil)
	defer router.Stop()

	p := newCameraPipeline("cam1", desc, cfg, router, nil)
	fd := newFakeDecoder(8, 8)
	fd.dataFn = hybridTestFrame // same varying content as the hybrid test
	p.decoderFactory = func() (VideoDecoder, error) { return fd, nil }

	ctx, cancel := context.WithCancel(context.Background())
	p.Start(ctx)

	const n = 4
	for i := 0; i < n; i++ {
		pkt := buildRTPPacket(true, 96, uint16(i), uint32(i*3000), 1, nil, 0, 0, []byte{0x65, byte(i)})
		p.OnPacket(pkt, time.Now())
		time.Sleep(5 * time.Millisecond)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && debug.Count() < n {
		time.Sleep(10 * time.Millisecond)
	}

	if debug.Count() != n {
		t.Fatalf("cloud-mode (no hybrid) debug sink received %d frames, want %d -- every frame must dispatch", debug.Count(), n)
	}
	if status := p.Status(); status.Hybrid != nil {
		t.Fatalf("Hybrid status must be nil when Hybrid.Enabled is false, got %+v", status.Hybrid)
	}

	cancel()
	p.Wait()
}

// TestPipeline_HybridShutdownCleansUp verifies the pipeline still closes
// cleanly on context cancellation with the hybrid evaluator active
// (J8 shutdown acceptance).
func TestPipeline_HybridShutdownCleansUp(t *testing.T) {
	cfg := Config{RingBufferSize: 5, QueueDepth: 16, Hybrid: newHybridConfig()}
	desc := rtsp.StreamDescriptor{CandidateKey: "cam1", Codec: "H264", Width: 8, Height: 8}

	router := NewRouter([]Sink{NewDebugSink()}, 8, nil)
	defer router.Stop()

	p := newCameraPipeline("cam1", desc, cfg, router, nil)
	fd := newFakeDecoder(8, 8)
	fd.dataFn = hybridTestFrame
	p.decoderFactory = func() (VideoDecoder, error) { return fd, nil }

	ctx, cancel := context.WithCancel(context.Background())
	p.Start(ctx)

	pkt := buildRTPPacket(true, 96, 0, 0, 1, nil, 0, 0, []byte{0x65, 0})
	p.OnPacket(pkt, time.Now())
	time.Sleep(20 * time.Millisecond)

	cancel()
	done := make(chan struct{})
	go func() { p.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("pipeline did not shut down cleanly with hybrid evaluator active")
	}
}

// TestPipeline_HybridFailSafeDispatchesUnevaluableFrame is the fail-safe
// acceptance test for Hito J's integration fix: a frame the motion
// detector cannot evaluate safely (here, a decoder reporting 0x0
// dimensions) must never be silently dropped -- it is dispatched as a
// candidate, counted in MotionCandidates (never FramesFiltered), and
// carries CandidateReason "failsafe_unevaluable".
func TestPipeline_HybridFailSafeDispatchesUnevaluableFrame(t *testing.T) {
	cfg := Config{
		RingBufferSize: 10,
		QueueDepth:     16,
		Hybrid:         newHybridConfig(),
	}
	desc := rtsp.StreamDescriptor{CandidateKey: "cam1", Codec: "H264", Width: 0, Height: 0, StreamRole: "sub"}

	debug := NewDebugSink()
	router := NewRouter([]Sink{debug}, 8, nil)
	defer router.Stop()

	p := newCameraPipeline("cam1", desc, cfg, router, nil)
	fd := newFakeDecoder(0, 0) // 0x0 "frame" the motion detector cannot evaluate
	p.decoderFactory = func() (VideoDecoder, error) { return fd, nil }

	ctx, cancel := context.WithCancel(context.Background())
	p.Start(ctx)

	pkt := buildRTPPacket(true, 96, 0, 0, 1, nil, 0, 0, []byte{0x65, 0})
	p.OnPacket(pkt, time.Now())

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		st := p.Status()
		if st.Hybrid != nil && st.Hybrid.FramesEvaluated >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	status := p.Status()
	if status.Hybrid == nil {
		t.Fatal("expected non-nil Hybrid status block when Hybrid.Enabled")
	}
	if status.Hybrid.MotionCandidates != 1 {
		t.Fatalf("MotionCandidates = %d, want 1 (fail-safe must count as a candidate)", status.Hybrid.MotionCandidates)
	}
	if status.Hybrid.FramesFiltered != 0 {
		t.Fatalf("FramesFiltered = %d, want 0 -- fail-safe is not a filtering decision", status.Hybrid.FramesFiltered)
	}
	if debug.Count() != 1 {
		t.Fatalf("debug sink received %d frames, want 1 -- an unevaluable frame must still be dispatched", debug.Count())
	}
	if got := debug.Last().CandidateReason; got != "failsafe_unevaluable" {
		t.Fatalf("CandidateReason = %q, want %q", got, "failsafe_unevaluable")
	}

	cancel()
	p.Wait()
}
