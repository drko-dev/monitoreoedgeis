package processing

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/rtsp"
)

type recordingTap struct {
	mu     sync.Mutex
	want   bool
	frames []Frame
}

func (r *recordingTap) Wants(string, time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.want
}

func (r *recordingTap) Offer(f Frame) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.frames = append(r.frames, f)
}

func (r *recordingTap) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.frames)
}

func runLiveTapPipeline(t *testing.T, tap *recordingTap, n int) *DebugSink {
	t.Helper()
	cfg := Config{TargetFPS: 5, RingBufferSize: 4, QueueDepth: 64, LiveTap: tap}
	desc := rtsp.StreamDescriptor{CandidateKey: "cam1", Codec: "H264", Width: 4, Height: 4, FPS: 30, StreamRole: "sub"}
	debug := NewDebugSink()
	router := NewRouter([]Sink{debug}, 64, nil)
	t.Cleanup(router.Stop)
	p := newCameraPipeline("cam1", desc, cfg, router, nil)
	fd := newFakeDecoder(4, 4)
	p.decoderFactory = func() (VideoDecoder, error) { return fd, nil }
	ctx, cancel := context.WithCancel(context.Background())
	p.Start(ctx)
	for i := 0; i < n; i++ {
		p.OnPacket(buildRTPPacket(true, 96, uint16(i), uint32(i*3000), 1, nil, 0, 0, []byte{0x65, byte(i)}), time.Now())
		time.Sleep(10 * time.Millisecond)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && fd.DecodedCount() < int64(n) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	cancel()
	p.Wait()
	return debug
}

// Live View taps every decoded frame the tap asks for, ahead of the
// TargetFPS sampler: preview/inference (the router) stays at TargetFPS.
func TestPipeline_LiveTapIndependentOfTargetFPS(t *testing.T) {
	tap := &recordingTap{want: true}
	debug := runLiveTapPipeline(t, tap, 30)
	if got := tap.count(); got != 30 {
		t.Fatalf("live tap got %d frames, want all 30 decoded", got)
	}
	// ~300ms of frames at TargetFPS=5 => 2-3 routed frames, never 30.
	if got := debug.Count(); got < 1 || got > 4 {
		t.Fatalf("router got %d frames, want TargetFPS-paced (1-4)", got)
	}
	if f := tap.frames[0]; f.CandidateKey != "cam1" || f.CorrelationID == "" || len(f.Data) == 0 {
		t.Fatalf("live frame missing metadata: %+v", f)
	}
}

// Without live demand nothing is offered, and preview/inference is unchanged.
func TestPipeline_LiveTapIdleOffersNothing(t *testing.T) {
	tap := &recordingTap{want: false}
	debug := runLiveTapPipeline(t, tap, 30)
	if got := tap.count(); got != 0 {
		t.Fatalf("idle live tap got %d frames, want 0", got)
	}
	if debug.Count() == 0 {
		t.Fatal("router (inference/preview) got no frames without a live viewer")
	}
}
