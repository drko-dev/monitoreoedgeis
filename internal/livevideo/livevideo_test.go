package livevideo

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/processing"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type fakeSink struct {
	mu    sync.Mutex
	n     int
	err   error
	block chan struct{}
}

func (s *fakeSink) Name() string { return "edge-live" }
func (s *fakeSink) Route(processing.Frame) error {
	if s.block != nil {
		<-s.block
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.n++
	return s.err
}
func (s *fakeSink) count() int { s.mu.Lock(); defer s.mu.Unlock(); return s.n }

func newTestController(t *testing.T, fps float64, sink processing.Sink) (*Controller, *fakeClock) {
	t.Helper()
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	c := newWithClock(fps, 30, 10*time.Second, nil, clk.now)
	if sink != nil {
		c.SetSink(sink)
	}
	t.Cleanup(c.Close)
	return c, clk
}

// emitted counts how many of n frames at srcFPS over 1s pass Wants.
func emitted(c *Controller, key string, start time.Time, srcFPS int) int {
	n := 0
	for i := 0; i < srcFPS; i++ {
		if c.Wants(key, start.Add(time.Duration(i)*time.Second/time.Duration(srcFPS))) {
			n++
		}
	}
	return n
}

func TestNoViewerNoLive(t *testing.T) {
	c, clk := newTestController(t, 15, &fakeSink{})
	if n := emitted(c, "cam", clk.now(), 30); n != 0 {
		t.Fatalf("live emitted %d frames without a viewer", n)
	}
	if st := c.Status(); st.Active || st.ActiveViewers != 0 {
		t.Fatalf("status active without viewer: %+v", st)
	}
}

func TestViewerActivatesLiveAtTargetFPS(t *testing.T) {
	c, clk := newTestController(t, 15, &fakeSink{})
	c.Demand("cam", 1)
	if n := emitted(c, "cam", clk.now(), 30); n < 14 || n > 16 {
		t.Fatalf("live emitted %d frames/s from a 30 FPS source, want ~15", n)
	}
	st := c.Status()
	if !st.Active || st.ActiveViewers != 1 || st.TargetFPS != 15 || len(st.CandidateKeys) != 1 || st.CandidateKeys[0] != "cam" {
		t.Fatalf("status = %+v", st)
	}
	// Demand is per camera: another camera stays in preview.
	if n := emitted(c, "other", clk.now().Add(2*time.Second), 30); n != 0 {
		t.Fatalf("camera without viewer emitted %d live frames", n)
	}
}

func TestZeroTargetFPSUsesSourceFPSCapped(t *testing.T) {
	c, clk := newTestController(t, 0, &fakeSink{})
	c.Demand("cam", 1)
	if n := emitted(c, "cam", clk.now(), 25); n != 25 {
		t.Fatalf("native: emitted %d of 25", n)
	}
	if n := emitted(c, "cam", clk.now().Add(time.Second), 60); n < 29 || n > 31 {
		t.Fatalf("native capped: emitted %d of 60, want ~30", n)
	}
}

func TestLastViewerLeavesFallsBackAfterIdleTimeout(t *testing.T) {
	var mu sync.Mutex
	var published []Status
	c, clk := newTestController(t, 15, &fakeSink{})
	c.publish = func(s Status) { mu.Lock(); published = append(published, s); mu.Unlock() }
	c.Demand("cam", 2)
	clk.add(3 * time.Second)
	c.Demand("cam", 0) // last viewer closed the modal
	if !c.Active("cam") {
		t.Fatal("live stopped before the idle timeout")
	}
	clk.add(6 * time.Second) // 9s after last demand
	if !c.Wants("cam", clk.now()) {
		t.Fatal("live stopped before the idle timeout")
	}
	clk.add(2 * time.Second) // 11s after last demand
	if c.Wants("cam", clk.now()) || c.Active("cam") {
		t.Fatal("live still active after the idle timeout")
	}
	if st := c.Status(); st.Active || st.ActiveViewers != 0 {
		t.Fatalf("status after fallback = %+v", st)
	}
	mu.Lock()
	last := published[len(published)-1]
	mu.Unlock()
	if last.Active {
		t.Fatalf("fallback not published: %+v", last)
	}
	// A new viewer re-arms it.
	c.Demand("cam", 1)
	if !c.Active("cam") {
		t.Fatal("new viewer did not re-activate live")
	}
}

func TestDemandIgnoredWithoutUplink(t *testing.T) {
	c, _ := newTestController(t, 15, nil)
	c.Demand("cam", 1)
	if c.Active("cam") {
		t.Fatal("live activated with no uplink (not in ModeEdge)")
	}
	c.SetSink(&fakeSink{})
	c.Demand("cam", 1)
	c.SetSink(nil) // left ModeEdge
	if c.Active("cam") {
		t.Fatal("removing the uplink kept live demand")
	}
}

func TestOfferNeverBlocksAndCountsResults(t *testing.T) {
	sink := &fakeSink{block: make(chan struct{})}
	c, _ := newTestController(t, 15, sink)
	done := make(chan struct{})
	go func() {
		for i := 0; i < 50; i++ {
			c.Offer(processing.Frame{CandidateKey: "cam"})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Offer blocked on a slow uplink")
	}
	if st := c.Status(); st.FramesDropped < 40 {
		t.Fatalf("dropped = %d, want most of 50 dropped", st.FramesDropped)
	}
	close(sink.block)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && c.Status().FramesSent == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if st := c.Status(); st.FramesSent == 0 || st.LastFrameAt == nil {
		t.Fatalf("status after upload = %+v", st)
	}
	sink.mu.Lock()
	sink.err = errors.New("503")
	sink.mu.Unlock()
	c.Offer(processing.Frame{CandidateKey: "cam"})
	for time.Now().Before(deadline.Add(time.Second)) && c.Status().FramesFailed == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if c.Status().FramesFailed == 0 {
		t.Fatal("failed upload not counted")
	}
}

// concurrencySink records how many Route calls overlap.
type concurrencySink struct {
	mu        sync.Mutex
	inFlight  int
	maxFlight int
}

func (s *concurrencySink) Name() string { return "edge-live" }
func (s *concurrencySink) Route(processing.Frame) error {
	s.mu.Lock()
	s.inFlight++
	s.maxFlight = max(s.maxFlight, s.inFlight)
	s.mu.Unlock()
	time.Sleep(30 * time.Millisecond)
	s.mu.Lock()
	s.inFlight--
	s.mu.Unlock()
	return nil
}

func TestUploadsOverlapToHideUplinkLatency(t *testing.T) {
	sink := &concurrencySink{}
	c, _ := newTestController(t, 15, sink)
	for i := 0; i < 20; i++ {
		c.Offer(processing.Frame{CandidateKey: "cam"})
		time.Sleep(5 * time.Millisecond)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && c.Status().FramesSent+c.Status().FramesDropped < 20 {
		time.Sleep(5 * time.Millisecond)
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if sink.maxFlight < 2 || sink.maxFlight > uploadWorkers {
		t.Fatalf("max uploads in flight = %d, want 2..%d", sink.maxFlight, uploadWorkers)
	}
}

func TestPacerKeepsJitteredSourceAtTargetRate(t *testing.T) {
	p := newPacer(15)
	at := time.Unix(1_700_000_000, 0)
	emitted := 0
	// ~14.3 FPS source whose decode timestamps alternate 40 ms / 100 ms.
	for i := 0; i < 300; i++ {
		if p.allow(at) {
			emitted++
		}
		at = at.Add(time.Duration(40+60*(i%2)) * time.Millisecond)
	}
	if emitted < 285 {
		t.Fatalf("emitted %d of 300 jittered frames, want >= 285", emitted)
	}
	// Never faster than the target: a 60 FPS burst still yields ~15 FPS.
	p, emitted = newPacer(15), 0
	for i := 0; i < 600; i++ {
		if p.allow(at.Add(time.Duration(i) * time.Second / 60)) {
			emitted++
		}
	}
	if emitted < 148 || emitted > 152 {
		t.Fatalf("emitted %d over 10s of 60 FPS input, want ~150", emitted)
	}
}
