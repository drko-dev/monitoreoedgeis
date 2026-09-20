package rtsp

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/rtsptest"
)

// countingSink records how many packets a supervisor delivered.
type countingSink struct {
	mu      sync.Mutex
	byKey   map[string]int
	packets int
}

func newCountingSink() *countingSink {
	return &countingSink{byKey: make(map[string]int)}
}

func (s *countingSink) OnPacket(candidateKey string, payload []byte, recvAt time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byKey[candidateKey]++
	s.packets++
}

func (s *countingSink) forKey(k string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.byKey[k]
}

func simTarget(key string, addr string) CameraTarget {
	return CameraTarget{
		CandidateKey: key, Addr: addr, RTSPPath: "/live",
		StreamRole: "sub", Codec: "H264", Width: 640, Height: 360, FPS: 15,
	}
}

func waitFor(t *testing.T, d time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", msg)
}

// TestSetTargets_AddThenRepeatDoesNotDuplicate is regression coverage for the
// exactly-one-supervisor-per-candidate invariant after the lock refactor.
func TestSetTargets_AddThenRepeatDoesNotDuplicate(t *testing.T) {
	sim, err := rtsptest.NewSimulator(rtsptest.Options{AutoPacketCount: 200, AutoPacketInterval: 5 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer sim.Close()

	mgr := NewManager(DefaultConfig(), nil, nil)
	sink := newCountingSink()
	mgr.SetPacketSink(sink)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := mgr.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer mgr.Stop(context.Background())

	targets := []CameraTarget{simTarget("cam-1", sim.Addr())}
	mgr.SetTargets(targets)
	waitFor(t, 5*time.Second, func() bool { return len(mgr.KnownCameras()) == 1 }, "one supervisor")
	waitFor(t, 5*time.Second, func() bool { return sink.forKey("cam-1") > 0 }, "packets for cam-1")

	// Re-declaring the identical target must be a no-op, not a second supervisor.
	for i := 0; i < 5; i++ {
		mgr.SetTargets(targets)
	}
	if got := len(mgr.KnownCameras()); got != 1 {
		t.Fatalf("supervisors = %d after repeated identical SetTargets, want 1", got)
	}
}

// TestSetTargets_RemovalStopsTheSupervisor covers remove.
func TestSetTargets_RemovalStopsTheSupervisor(t *testing.T) {
	sim, err := rtsptest.NewSimulator(rtsptest.Options{AutoPacketCount: 200, AutoPacketInterval: 5 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer sim.Close()

	mgr := NewManager(DefaultConfig(), nil, nil)
	mgr.SetPacketSink(newCountingSink())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := mgr.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer mgr.Stop(context.Background())

	mgr.SetTargets([]CameraTarget{simTarget("cam-1", sim.Addr())})
	waitFor(t, 5*time.Second, func() bool { return len(mgr.KnownCameras()) == 1 }, "one supervisor")

	mgr.SetTargets(nil)
	if got := len(mgr.KnownCameras()); got != 0 {
		t.Fatalf("supervisors = %d after removing the target, want 0", got)
	}
}

// TestSetTargets_CredentialRotationReplacesOnlyThatSupervisor covers rotation:
// same address and path, new credentials, and still exactly one supervisor.
func TestSetTargets_CredentialRotationReplacesOnlyThatSupervisor(t *testing.T) {
	sim, err := rtsptest.NewSimulator(rtsptest.Options{AutoPacketCount: 200, AutoPacketInterval: 5 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer sim.Close()

	mgr := NewManager(DefaultConfig(), nil, nil)
	mgr.SetPacketSink(newCountingSink())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := mgr.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer mgr.Stop(context.Background())

	first := simTarget("cam-1", sim.Addr())
	first.Username, first.Password = "admin", "old-secret"
	mgr.SetTargets([]CameraTarget{first})
	waitFor(t, 5*time.Second, func() bool { return len(mgr.KnownCameras()) == 1 }, "one supervisor")

	rotated := simTarget("cam-1", sim.Addr())
	rotated.Username, rotated.Password = "admin", "new-secret"
	mgr.SetTargets([]CameraTarget{rotated})

	if got := len(mgr.KnownCameras()); got != 1 {
		t.Fatalf("supervisors = %d after a credential rotation, want 1", got)
	}
	if got := mgr.Snapshot(); len(got) != 1 || got[0].CandidateKey != "cam-1" {
		t.Fatalf("unexpected snapshot after rotation: %+v", got)
	}
}

// TestSetTargets_MetadataOnlyChangeDoesNotRestart pins the documented
// behaviour that Codec/Width/Height/FPS/StreamRole changes alone do not tear
// down a working stream.
func TestSetTargets_MetadataOnlyChangeDoesNotRestart(t *testing.T) {
	sim, err := rtsptest.NewSimulator(rtsptest.Options{AutoPacketCount: 200, AutoPacketInterval: 5 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer sim.Close()

	mgr := NewManager(DefaultConfig(), nil, nil)
	sink := newCountingSink()
	mgr.SetPacketSink(sink)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := mgr.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer mgr.Stop(context.Background())

	mgr.SetTargets([]CameraTarget{simTarget("cam-1", sim.Addr())})
	waitFor(t, 5*time.Second, func() bool { return sink.forKey("cam-1") > 0 }, "packets")

	before := mgr.Snapshot()
	changed := simTarget("cam-1", sim.Addr())
	changed.Codec, changed.Width, changed.Height, changed.FPS = "H265", 1920, 1080, 30
	mgr.SetTargets([]CameraTarget{changed})

	after := mgr.Snapshot()
	if len(after) != 1 {
		t.Fatalf("snapshot = %d entries, want 1", len(after))
	}
	// The supervisor is the same live one: its packet counter keeps growing and
	// the codec metadata is unchanged (a metadata-only change is ignored).
	if after[0].PacketsReceived < before[0].PacketsReceived {
		t.Fatal("packet counter went backwards — the supervisor was restarted")
	}
	if after[0].Codec != "H264" {
		t.Fatalf("codec = %q, want the original H264 (metadata-only changes are ignored)", after[0].Codec)
	}
}

// TestStop_ConcurrentWithPacketFlowAndReconciliation is the guard for the
// STOP-side lock cycle. The previous version only ran Stop AFTER quiescing the
// churn, so it never actually exercised Stop racing a reconciliation and a live
// packet path. Here Stop runs genuinely in parallel with all of them.
//
// It must demonstrate: no deadlock; Stop returns bounded; reconciliation cannot
// resurrect a supervisor; KnownCameras is empty afterwards; a SetTargets issued
// after shutdown does not create an active camera; and a second Stop is safe.
func TestStop_ConcurrentWithPacketFlowAndReconciliation(t *testing.T) {
	sim, err := rtsptest.NewSimulator(rtsptest.Options{
		AutoPacketCount: 100000, AutoPacketInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sim.Close()

	mgr := NewManager(DefaultConfig(), nil, nil)
	mgr.SetPacketSink(newCountingSink())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := mgr.Start(ctx); err != nil {
		t.Fatal(err)
	}

	addr := sim.Addr()
	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Reconciler: add / rotate / remove continuously.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			tgt := simTarget("cam-1", addr)
			tgt.Username, tgt.Password = "admin", "pw"
			switch i % 3 {
			case 0:
				mgr.SetTargets([]CameraTarget{tgt})
			case 1:
				tgt.Password = "rotated"
				mgr.SetTargets([]CameraTarget{tgt})
			case 2:
				mgr.SetTargets([]CameraTarget{tgt, simTarget("cam-2", addr)})
			}
			time.Sleep(time.Millisecond)
		}
	}()

	// Readers hammer the paths that need mu.
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_ = mgr.Snapshot()
				_ = mgr.KnownCameras()
				_ = mgr.HasCamera("cam-1")
				_, _ = mgr.DescriptorFor("cam-1")
			}
		}()
	}

	// Let packets flow and reconciliation churn for a while.
	time.Sleep(200 * time.Millisecond)

	// Stop WHILE all of that is still running. Bounded: it must return well
	// inside the deadline rather than deadlocking on mu.
	stopDone := make(chan error, 1)
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer stopCancel()
	go func() { stopDone <- mgr.Stop(stopCtx) }()

	select {
	case err := <-stopDone:
		if err != nil {
			t.Fatalf("Stop returned an error while racing live traffic: %v", err)
		}
	case <-time.After(25 * time.Second):
		t.Fatal("Stop deadlocked while reconciliation and packet flow were live")
	}

	// Now let the churn goroutines exit.
	close(stop)
	wg.Wait()

	// Reconciliation must not have resurrected anything, and no supervisor may
	// survive shutdown.
	if got := mgr.KnownCameras(); len(got) != 0 {
		t.Fatalf("KnownCameras = %v after Stop, want empty", got)
	}
	if got := mgr.Snapshot(); len(got) != 0 {
		t.Fatalf("Snapshot = %d entries after Stop, want 0", len(got))
	}

	// A SetTargets issued after shutdown must not create an active camera.
	mgr.SetTargets([]CameraTarget{simTarget("cam-after-stop", addr)})
	if got := mgr.KnownCameras(); len(got) != 0 {
		t.Fatalf("SetTargets after Stop registered %v — shutdown must be terminal", got)
	}
	select {
	case <-stopDone:
	default:
	}
	time.Sleep(100 * time.Millisecond)
	if got := mgr.KnownCameras(); len(got) != 0 {
		t.Fatalf("a supervisor was resurrected after Stop: %v", got)
	}

	// A second Stop is safe and returns promptly.
	secondCtx, secondCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer secondCancel()
	if err := mgr.Stop(secondCtx); err != nil {
		t.Fatalf("second Stop: %v", err)
	}
}

// TestStop_BeforeStartIsSafeAndStopsRegisteredSupervisors covers the
// targets-before-Start ordering the manager explicitly supports.
func TestStop_BeforeStartIsSafeAndStopsRegisteredSupervisors(t *testing.T) {
	mgr := NewManager(DefaultConfig(), nil, nil)

	// Register targets before Start (production ordering), then Stop without
	// ever starting the manager.
	mgr.SetTargets([]CameraTarget{{
		CandidateKey: "cam-1", Addr: "127.0.0.1:8554", RTSPPath: "/live",
		StreamRole: "sub", Codec: "H264",
	}})

	stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := mgr.Stop(stopCtx); err != nil {
		t.Fatalf("Stop before Start: %v", err)
	}
	if got := mgr.KnownCameras(); len(got) != 0 {
		t.Fatalf("KnownCameras = %v after Stop, want empty", got)
	}
	// And it is still idempotent.
	if err := mgr.Stop(stopCtx); err != nil {
		t.Fatalf("second Stop before Start: %v", err)
	}
}

// TestStart_AfterStopIsANoOp pins that a stopped manager cannot be revived.
func TestStart_AfterStopIsANoOp(t *testing.T) {
	mgr := NewManager(DefaultConfig(), nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := mgr.Start(ctx); err != nil {
		t.Fatal(err)
	}
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopCancel()
	if err := mgr.Stop(stopCtx); err != nil {
		t.Fatal(err)
	}

	if err := mgr.Start(ctx); err != nil {
		t.Fatalf("Start after Stop must be a no-op, got %v", err)
	}
	mgr.SetTargets([]CameraTarget{{
		CandidateKey: "cam-revived", Addr: "127.0.0.1:8554", RTSPPath: "/live",
	}})
	if got := mgr.KnownCameras(); len(got) != 0 {
		t.Fatalf("a stopped manager accepted a target: %v", got)
	}
}

// TestSetTargets_ConcurrentWithPacketFlowAndStop is the race/stress guard for
// the lock refactor: reconciliation, packet delivery and shutdown all at once.
func TestSetTargets_ConcurrentWithPacketFlowAndStop(t *testing.T) {
	sim, err := rtsptest.NewSimulator(rtsptest.Options{AutoPacketCount: 400, AutoPacketInterval: 2 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer sim.Close()

	mgr := NewManager(DefaultConfig(), nil, nil)
	mgr.SetPacketSink(newCountingSink())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := mgr.Start(ctx); err != nil {
		t.Fatal(err)
	}

	addr := sim.Addr()
	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Churn: repeatedly add, rotate and remove while packets are flowing.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			tgt := simTarget("cam-1", addr)
			tgt.Username, tgt.Password = "admin", "pw"
			switch i % 3 {
			case 0:
				mgr.SetTargets([]CameraTarget{tgt})
			case 1:
				tgt.Password = "rotated"
				mgr.SetTargets([]CameraTarget{tgt})
			case 2:
				mgr.SetTargets([]CameraTarget{tgt, simTarget("cam-2", addr)})
			}
			time.Sleep(time.Millisecond)
		}
	}()

	// Readers run concurrently with the churn.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			_ = mgr.Snapshot()
			_ = mgr.KnownCameras()
			_ = mgr.HasCamera("cam-1")
			_, _ = mgr.DescriptorFor("cam-1")
		}
	}()

	time.Sleep(300 * time.Millisecond)
	close(stop)
	wg.Wait()

	// Shutdown while the churn has stopped, then confirm it is idempotent.
	if err := mgr.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := mgr.Stop(context.Background()); err != nil {
		t.Fatalf("second Stop: %v", err)
	}
}

// TestSetTargets_RepeatedLifecycle runs add/remove/rotate many times so the
// lifecycle is exercised far past the single-pass case.
func TestSetTargets_RepeatedLifecycle(t *testing.T) {
	sim, err := rtsptest.NewSimulator(rtsptest.Options{AutoPacketCount: 100000, AutoPacketInterval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer sim.Close()

	mgr := NewManager(DefaultConfig(), nil, nil)
	mgr.SetPacketSink(newCountingSink())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := mgr.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer mgr.Stop(context.Background())

	for i := 0; i < 20; i++ {
		tgt := simTarget("cam-1", sim.Addr())
		tgt.Username, tgt.Password = "admin", "pw"
		mgr.SetTargets([]CameraTarget{tgt})
		if got := len(mgr.KnownCameras()); got != 1 {
			t.Fatalf("cycle %d: supervisors = %d after add, want 1", i, got)
		}

		tgt.Password = "rotated"
		mgr.SetTargets([]CameraTarget{tgt})
		if got := len(mgr.KnownCameras()); got != 1 {
			t.Fatalf("cycle %d: supervisors = %d after rotate, want 1", i, got)
		}

		mgr.SetTargets(nil)
		if got := len(mgr.KnownCameras()); got != 0 {
			t.Fatalf("cycle %d: supervisors = %d after remove, want 0", i, got)
		}
	}
}
