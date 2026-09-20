package processing

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/rtsp"
	"github.com/drko-dev/monitoreoedgeis/internal/rtsptest"
)

// These tests only reproduce the lock cycle if the packet sink is the REAL
// processing.Manager. A plain recording sink never calls back into
// rtsp.Manager.DescriptorFor, so the cycle
//
//	Manager.Stop / SetTargets  holds rtsp.mu
//	  -> waits Supervisor
//	Supervisor goroutine
//	  -> Manager.OnPacket  holds processing.mu
//	     -> rtsp.DescriptorFor  waits rtsp.mu
//
// cannot close. That is why the guard lives here rather than in internal/rtsp.

func wireRTSPAndProcessing(t *testing.T, ceiling int) (*rtsp.Manager, *Manager, *rtsptest.Simulator) {
	t.Helper()
	sim, err := rtsptest.NewSimulator(rtsptest.Options{
		AutoPacketCount: 100000, AutoPacketInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sim.Close)

	rtspMgr := rtsp.NewManager(rtsp.DefaultConfig(), nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := rtspMgr.Start(ctx); err != nil {
		t.Fatal(err)
	}

	procMgr := NewManager(Config{
		Enabled:                true,
		TargetFPS:              5,
		RingBufferSize:         10,
		QueueDepth:             16,
		DecodeQueueDepth:       4,
		MaxConcurrentPipelines: ceiling,
	}, rtspMgr, &recordingHealthSink{}, nil)
	// Start registers procMgr as the RTSP packet sink.
	if err := procMgr.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = procMgr.Stop(context.Background()) })

	return rtspMgr, procMgr, sim
}

func camTarget(key, addr string) rtsp.CameraTarget {
	return rtsp.CameraTarget{
		CandidateKey: key, Addr: addr, RTSPPath: "/live",
		StreamRole: "sub", Codec: "H264", Width: 640, Height: 360, FPS: 15,
	}
}

// waitForPipeline waits until the processing manager is actually decoding the
// camera, which means OnPacket ran, took processing.mu and called
// rtsp.DescriptorFor — i.e. the cycle is live.
func waitForPipeline(t *testing.T, mgr *Manager, key string, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if mgr.Pipeline(key) != nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("pipeline for %s never became active, so the packet path is not live", key)
}

// TestStopRacingLivePacketPathDoesNotDeadlock is the real STOP-side lock-cycle
// guard: rtsp.Manager.Stop runs while packets are flowing through
// processing.Manager.OnPacket and reconciliation is churning.
//
// Before the fix, Stop held rtsp.mu across Supervisor.Stop() while the
// supervisor goroutine sat in OnPacket holding processing.mu and waiting for
// rtsp.mu in DescriptorFor, which deadlocked Stop.
func TestStopRacingLivePacketPathDoesNotDeadlock(t *testing.T) {
	// A high ceiling so pipeline CREATION keeps happening: the cycle only
	// exists while OnPacket is creating a pipeline, because that is the only
	// path that calls rtsp.DescriptorFor. Once a pipeline for a key exists,
	// OnPacket takes the fast path and never calls back into rtsp.Manager.
	rtspMgr, _, sim := wireRTSPAndProcessing(t, 64)
	addr := sim.Addr()

	// Continuously introduce NEW cameras, each of which forces a pipeline
	// creation (and therefore a DescriptorFor under processing.mu).
	stopChurn := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 20000; i++ {
			select {
			case <-stopChurn:
				return
			default:
			}
			rtspMgr.SetTargets([]rtsp.CameraTarget{
				camTarget("cam-"+itoa(i), addr),
				camTarget("cam-"+itoa(i+1), addr),
			})
			time.Sleep(time.Millisecond)
		}
	}()

	// Let packets flow and pipeline creation churn.
	time.Sleep(250 * time.Millisecond)

	stopCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- rtspMgr.Stop(stopCtx) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Stop returned an error while the packet path was live: %v", err)
		}
	case <-time.After(25 * time.Second):
		t.Fatal("rtsp.Manager.Stop deadlocked against a live processing.Manager.OnPacket")
	}

	close(stopChurn)
	wg.Wait()

	if got := rtspMgr.KnownCameras(); len(got) != 0 {
		t.Fatalf("KnownCameras = %v after Stop, want empty", got)
	}
	// Reconciliation must not resurrect a supervisor after shutdown.
	rtspMgr.SetTargets([]rtsp.CameraTarget{camTarget("cam-after-stop", addr)})
	time.Sleep(100 * time.Millisecond)
	if got := rtspMgr.KnownCameras(); len(got) != 0 {
		t.Fatalf("SetTargets after Stop resurrected a supervisor: %v", got)
	}
}

// TestSetTargetsRacingLivePacketPathDoesNotDeadlock is the SET-side counterpart:
// reconciliation runs while OnPacket is concurrently inside DescriptorFor.
func TestSetTargetsRacingLivePacketPathDoesNotDeadlock(t *testing.T) {
	rtspMgr, procMgr, sim := wireRTSPAndProcessing(t, 4)

	target := camTarget("cam-1", sim.Addr())
	rtspMgr.SetTargets([]rtsp.CameraTarget{target})
	waitForPipeline(t, procMgr, "cam-1", 15*time.Second)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 400; i++ {
			tgt := target
			switch i % 3 {
			case 0:
				rtspMgr.SetTargets([]rtsp.CameraTarget{tgt})
			case 1:
				tgt.Password = "rotated"
				rtspMgr.SetTargets([]rtsp.CameraTarget{tgt})
			case 2:
				rtspMgr.SetTargets([]rtsp.CameraTarget{tgt, camTarget("cam-2", sim.Addr())})
			}
			time.Sleep(time.Millisecond)
		}
	}()

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("SetTargets deadlocked against a live OnPacket")
	}
}

// TestStopIsBoundedWithPipelinesActive checks Stop returns well inside its
// deadline rather than waiting on the manager's 2s status ticker.
func TestStopIsBoundedWithPipelinesActive(t *testing.T) {
	rtspMgr, procMgr, sim := wireRTSPAndProcessing(t, 4)

	rtspMgr.SetTargets([]rtsp.CameraTarget{camTarget("cam-1", sim.Addr())})
	waitForPipeline(t, procMgr, "cam-1", 15*time.Second)

	start := time.Now()
	stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := rtspMgr.Stop(stopCtx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 8*time.Second {
		t.Fatalf("Stop took %s with one active pipeline, want bounded", elapsed)
	}
	// Idempotent.
	stopCtx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	if err := rtspMgr.Stop(stopCtx2); err != nil {
		t.Fatalf("second Stop: %v", err)
	}
}

// TestStopRacingLivePacketPath_Repeated stresses the shutdown race so a
// timing-sensitive deadlock has many chances to appear.
func TestStopRacingLivePacketPath_Repeated(t *testing.T) {
	for i := 0; i < 6; i++ {
		func() {
			rtspMgr, _, sim := wireRTSPAndProcessing(t, 64)
			addr := sim.Addr()

			stopChurn := make(chan struct{})
			var wg sync.WaitGroup
			wg.Add(1)
			go func() {
				defer wg.Done()
				for n := 0; n < 20000; n++ {
					select {
					case <-stopChurn:
						return
					default:
					}
					rtspMgr.SetTargets([]rtsp.CameraTarget{
						camTarget("cam-"+itoa(n), addr),
						camTarget("cam-"+itoa(n+1), addr),
					})
					time.Sleep(time.Millisecond)
				}
			}()

			time.Sleep(200 * time.Millisecond)

			stopCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			done := make(chan error, 1)
			go func() { done <- rtspMgr.Stop(stopCtx) }()
			select {
			case err := <-done:
				if err != nil {
					cancel()
					t.Fatalf("iteration %d: Stop: %v", i, err)
				}
			case <-time.After(20 * time.Second):
				cancel()
				t.Fatalf("iteration %d: Stop deadlocked", i)
			}
			cancel()
			close(stopChurn)
			wg.Wait()
		}()
	}
}
