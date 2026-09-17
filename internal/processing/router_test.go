package processing

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type blockingSink struct {
	name    string
	release chan struct{}
	routed  atomicCounter
}

type atomicCounter struct {
	mu sync.Mutex
	n  int
}

func (c *atomicCounter) inc() {
	c.mu.Lock()
	c.n++
	c.mu.Unlock()
}
func (c *atomicCounter) get() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

func (s *blockingSink) Name() string { return s.name }
func (s *blockingSink) Route(f Frame) error {
	<-s.release // blocks until the test lets it through
	s.routed.inc()
	return nil
}

func TestRouter_SlowSinkDoesNotBlockFastSink(t *testing.T) {
	slow := &blockingSink{name: "slow", release: make(chan struct{})}
	fast := NewDebugSink()

	r := NewRouter([]Sink{slow, fast}, 2, nil)
	// Registration order matters: close(slow.release) must run BEFORE
	// r.Stop(), since Stop() waits for the slow worker to exit and that
	// worker is parked inside Route() until release is closed. Deferred
	// calls run LIFO, so Stop() is deferred first (runs last).
	defer r.Stop()
	defer close(slow.release)

	// Interleave dispatches with a short pause so the fast sink's worker
	// (which drains instantly) never overflows its own bounded queue,
	// while the slow sink's worker stays permanently parked in Route()
	// after consuming the first frame, so its queue fills and overflows.
	for i := 0; i < 6; i++ {
		r.Dispatch(Frame{Seq: uint64(i)})
		time.Sleep(5 * time.Millisecond)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && fast.Count() < 6 {
		time.Sleep(10 * time.Millisecond)
	}
	if fast.Count() != 6 {
		t.Fatalf("fast sink received %d frames, want 6 (must not be blocked by slow sink)", fast.Count())
	}

	if r.Dropped("slow") == 0 {
		t.Fatal("expected at least one frame dropped for the slow sink's full queue")
	}
}

func TestRouter_DebugSinkTracksCountAndLast(t *testing.T) {
	sink := NewDebugSink()
	r := NewRouter([]Sink{sink}, 4, nil)
	defer r.Stop()

	r.Dispatch(Frame{Seq: 1})
	r.Dispatch(Frame{Seq: 2})

	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) && sink.Count() < 2 {
		time.Sleep(5 * time.Millisecond)
	}
	if sink.Count() != 2 {
		t.Fatalf("Count() = %d, want 2", sink.Count())
	}
	if sink.Last().Seq != 2 {
		t.Fatalf("Last().Seq = %d, want 2", sink.Last().Seq)
	}
}

type erroringSink struct{}

func (erroringSink) Name() string        { return "erroring" }
func (erroringSink) Route(f Frame) error { return errors.New("boom") }

func TestRouter_SinkErrorDoesNotStopWorker(t *testing.T) {
	r := NewRouter([]Sink{erroringSink{}}, 4, nil)
	defer r.Stop()

	// Must not panic or deadlock even though every Route call errors.
	for i := 0; i < 3; i++ {
		r.Dispatch(Frame{Seq: uint64(i)})
	}
	time.Sleep(50 * time.Millisecond)
}

// closingSink implements the optional sinkCloser hook (Milestone I6 uses
// this for cloudsink.CloudSink's buffer drain goroutine).
type closingSink struct {
	closed atomic.Int64
}

func (s *closingSink) Name() string        { return "closing" }
func (s *closingSink) Route(f Frame) error { return nil }
func (s *closingSink) Close()              { s.closed.Add(1) }

func TestRouter_Stop_ClosesSinksThatImplementSinkCloser(t *testing.T) {
	cs := &closingSink{}
	r := NewRouter([]Sink{cs, NewDebugSink()}, 4, nil)

	r.Stop()

	if got := cs.closed.Load(); got != 1 {
		t.Fatalf("closingSink.Close() called %d times, want exactly 1", got)
	}
}
