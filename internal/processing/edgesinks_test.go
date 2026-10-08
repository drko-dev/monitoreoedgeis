package processing

import (
	"sync"
	"testing"
	"time"
)

// gatedSink records routed frames and blocks each Route until released.
type gatedSink struct {
	mu      sync.Mutex
	keys    []string
	gate    chan struct{}
	started chan struct{}
}

func newGatedSink() *gatedSink {
	return &gatedSink{gate: make(chan struct{}), started: make(chan struct{}, 64)}
}

func (g *gatedSink) Name() string { return "gated" }
func (g *gatedSink) Route(f Frame) error {
	g.started <- struct{}{}
	<-g.gate
	g.mu.Lock()
	g.keys = append(g.keys, f.CandidateKey)
	g.mu.Unlock()
	return nil
}

func (g *gatedSink) routed() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.keys...)
}

func TestFreshSinkServesCamerasRoundRobinAndKeepsLatest(t *testing.T) {
	inner := newGatedSink()
	s := NewFreshSink(inner, 0, nil, nil)
	defer func() { close(inner.gate); s.Close() }()

	now := time.Now()
	// Worker busy on the first "fast" frame...
	_ = s.Route(Frame{CandidateKey: "fast", Seq: 1, Timestamp: now})
	<-inner.started
	// ...while the fast camera floods and the slow one sends one frame.
	for i := 2; i <= 20; i++ {
		_ = s.Route(Frame{CandidateKey: "fast", Seq: uint64(i), Timestamp: now})
	}
	_ = s.Route(Frame{CandidateKey: "slow", Seq: 1, Timestamp: now})

	inner.gate <- struct{}{} // finish fast#1
	<-inner.started
	inner.gate <- struct{}{} // next served
	<-inner.started
	inner.gate <- struct{}{}

	deadline := time.Now().Add(2 * time.Second)
	for len(inner.routed()) < 3 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	got := inner.routed()
	if len(got) != 3 || got[0] != "fast" || got[1] != "slow" || got[2] != "fast" {
		t.Fatalf("routed = %v, want [fast slow fast]: slow camera must get its turn before the flood", got)
	}
	st := s.Status()
	if st["fast"].DroppedSuperseded != 18 {
		t.Fatalf("fast superseded = %d, want 18 (only the latest pending frame is kept)", st["fast"].DroppedSuperseded)
	}
	if st["slow"].DroppedSuperseded != 0 || st["slow"].Processed != 1 {
		t.Fatalf("slow status = %+v, want processed=1 without drops", st["slow"])
	}
}

func TestFreshSinkDropsStaleFrames(t *testing.T) {
	inner := NewDebugSink()
	s := NewFreshSink(inner, time.Second, func(string) (float64, bool) { return 5, true }, nil)
	defer s.Close()

	_ = s.Route(Frame{CandidateKey: "cam", Timestamp: time.Now().Add(-3 * time.Second)})
	_ = s.Route(Frame{CandidateKey: "other", Timestamp: time.Now()})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		st := s.Status()
		if st["cam"].DroppedStale == 1 && st["other"].Processed == 1 {
			if inner.Count() != 1 {
				t.Fatalf("inner routed %d frames, want only the fresh one", inner.Count())
			}
			if st["other"].ConfiguredFPS != 5 {
				t.Fatalf("configured fps = %v, want 5", st["other"].ConfiguredFPS)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("status = %+v, want cam stale-dropped and other processed", s.Status())
}

func TestFreshSinkRouteNeverBlocks(t *testing.T) {
	inner := newGatedSink()
	s := NewFreshSink(inner, 0, nil, nil)
	defer func() { close(inner.gate); s.Close() }()

	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			_ = s.Route(Frame{CandidateKey: "cam", Timestamp: time.Now()})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Route blocked behind a busy inner sink")
	}
}

// pacedRate feeds n frames at srcFPS through a PacedSink at fps and returns
// how many it forwarded.
func pacedForwarded(t *testing.T, s *PacedSink, key string, srcFPS float64, n int, start time.Time) int {
	t.Helper()
	inner := s.inner.(*DebugSink)
	before := inner.Count()
	step := time.Duration(float64(time.Second) / srcFPS)
	for i := 0; i < n; i++ {
		_ = s.Route(Frame{CandidateKey: key, Timestamp: start.Add(time.Duration(i) * step)})
	}
	return int(inner.Count() - before)
}

func TestPacedSinkCapsEachCameraIndependently(t *testing.T) {
	s := NewPacedSink(NewDebugSink(), 2, nil)
	start := time.Unix(1000, 0)

	// 10 s of a 15 FPS stream (inference raised to 15) -> 20 preview frames.
	if got := pacedForwarded(t, s, "a", 15, 150, start); got != 20 {
		t.Fatalf("15 FPS input forwarded %d frames in 10 s, want 20 (2 FPS)", got)
	}
	// Another camera at 5 FPS is paced on its own schedule.
	if got := pacedForwarded(t, s, "b", 5, 50, start); got != 20 {
		t.Fatalf("5 FPS input forwarded %d frames in 10 s, want 20 (2 FPS)", got)
	}
	// Below the cap every frame passes.
	if got := pacedForwarded(t, s, "c", 1, 10, start); got != 10 {
		t.Fatalf("1 FPS input forwarded %d of 10, want all", got)
	}
	st := s.Status()
	if st["a"].Throttled != 130 || st["a"].ConfiguredFPS != 2 {
		t.Fatalf("a status = %+v, want 130 throttled at configured 2", st["a"])
	}
}

func TestPacedSinkDoesNotBurstAfterGap(t *testing.T) {
	s := NewPacedSink(NewDebugSink(), 2, nil)
	start := time.Unix(1000, 0)
	pacedForwarded(t, s, "a", 15, 15, start)
	// 30 s gap (e.g. Live View was active), then 1 s of frames: at most 2.
	if got := pacedForwarded(t, s, "a", 15, 15, start.Add(31*time.Second)); got > 2 {
		t.Fatalf("forwarded %d frames in 1 s after a gap, want <= 2 (no banked burst)", got)
	}
}
