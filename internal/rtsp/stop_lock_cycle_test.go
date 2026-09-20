package rtsp

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/rtsptest"
)

// blockingDescriptorSink reproduces, deterministically, the lock order that
// processing.Manager.OnPacket establishes:
//
//	processing.mu  (here: a mutex held for the duration of OnPacket)
//	  -> rtsp.Manager.DescriptorFor
//	       -> rtsp.Manager.mu
//
// A real processing.Manager is used in internal/processing's integration tests,
// but the window there is timing-dependent: OnPacket only calls DescriptorFor
// while it is CREATING a pipeline, so once a pipeline exists — or once the
// admission ceiling is reached — the call disappears and the cycle cannot close.
// This sink parks the supervisor goroutine at exactly the point where a
// concurrent Stop/SetTargets holding rtsp.Manager.mu would deadlock, so the
// regression is caught every run rather than probabilistically.
type blockingDescriptorSink struct {
	mgr *Manager

	// entered is closed once a supervisor goroutine is inside OnPacket.
	entered chan struct{}
	// release is closed by the test to let OnPacket proceed into DescriptorFor.
	release chan struct{}
	// processingMu stands in for processing.Manager.mu.
	processingMu sync.Mutex

	once sync.Once
}

func newBlockingDescriptorSink(mgr *Manager) *blockingDescriptorSink {
	return &blockingDescriptorSink{
		mgr:     mgr,
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (s *blockingDescriptorSink) OnPacket(candidateKey string, _ []byte, _ time.Time) {
	s.processingMu.Lock()
	defer s.processingMu.Unlock()

	s.once.Do(func() { close(s.entered) })
	<-s.release

	// Needs rtsp.Manager.mu. If Stop (or SetTargets) is holding it while
	// waiting for this very goroutine to unwind, this never returns.
	_, _ = s.mgr.DescriptorFor(candidateKey)
}

// TestStopDoesNotHoldMuAcrossSupervisorStop is the deterministic guard for the
// STOP-side lock cycle. With the old Stop (which held mu across sup.Stop()),
// Stop blocks waiting for the supervisor goroutine, which is blocked in
// DescriptorFor waiting for the very mu Stop holds.
func TestStopDoesNotHoldMuAcrossSupervisorStop(t *testing.T) {
	sim, err := rtsptest.NewSimulator(rtsptest.Options{
		AutoPacketCount: 100000, AutoPacketInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sim.Close()

	mgr := NewManager(DefaultConfig(), nil, nil)
	sink := newBlockingDescriptorSink(mgr)
	mgr.SetPacketSink(sink)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := mgr.Start(ctx); err != nil {
		t.Fatal(err)
	}

	mgr.SetTargets([]CameraTarget{{
		CandidateKey: "cam-1", Addr: sim.Addr(), RTSPPath: "/live",
		StreamRole: "sub", Codec: "H264", Width: 640, Height: 360, FPS: 15,
	}})

	// Wait until a supervisor goroutine is parked inside OnPacket.
	select {
	case <-sink.entered:
	case <-time.After(15 * time.Second):
		t.Fatal("no packet reached the sink, so the cycle could not be exercised")
	}

	// Start Stop; give it time to take mu and block on sup.Stop().
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer stopCancel()
	stopDone := make(chan error, 1)
	go func() { stopDone <- mgr.Stop(stopCtx) }()
	time.Sleep(200 * time.Millisecond)

	// Let the parked OnPacket proceed into DescriptorFor. If Stop holds mu while
	// waiting on this goroutine, both are now stuck.
	close(sink.release)

	select {
	case err := <-stopDone:
		if err != nil {
			t.Fatalf("Stop returned an error: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Stop deadlocked: it held rtsp.Manager.mu while waiting for a supervisor goroutine that needs that same mu (DescriptorFor)")
	}

	if got := mgr.KnownCameras(); len(got) != 0 {
		t.Fatalf("KnownCameras = %v after Stop, want empty", got)
	}
}

// TestSetTargetsDoesNotHoldMuAcrossSupervisorStop is the SET-side counterpart
// of the same cycle: reconciliation removes/replaces a supervisor, which must
// not happen while mu is held and a packet goroutine needs it.
func TestSetTargetsDoesNotHoldMuAcrossSupervisorStop(t *testing.T) {
	sim, err := rtsptest.NewSimulator(rtsptest.Options{
		AutoPacketCount: 100000, AutoPacketInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sim.Close()

	mgr := NewManager(DefaultConfig(), nil, nil)
	sink := newBlockingDescriptorSink(mgr)
	mgr.SetPacketSink(sink)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := mgr.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() {
		// Unblock anything still parked so cleanup cannot hang.
		select {
		case <-sink.release:
		default:
			close(sink.release)
		}
		stopCtx, sc := context.WithTimeout(context.Background(), 5*time.Second)
		defer sc()
		_ = mgr.Stop(stopCtx)
	}()

	mgr.SetTargets([]CameraTarget{{
		CandidateKey: "cam-1", Addr: sim.Addr(), RTSPPath: "/live",
		StreamRole: "sub", Codec: "H264", Width: 640, Height: 360, FPS: 15,
	}})

	select {
	case <-sink.entered:
	case <-time.After(15 * time.Second):
		t.Fatal("no packet reached the sink")
	}

	// Reconciliation that REMOVES the supervisor: it must stop it outside mu.
	setDone := make(chan struct{})
	go func() {
		defer close(setDone)
		mgr.SetTargets(nil)
	}()
	time.Sleep(200 * time.Millisecond)
	close(sink.release)

	select {
	case <-setDone:
	case <-time.After(15 * time.Second):
		t.Fatal("SetTargets deadlocked while removing a supervisor that needs rtsp.Manager.mu in DescriptorFor")
	}
}
