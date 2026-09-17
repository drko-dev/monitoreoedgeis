package processing

import (
	"context"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/rtsp"
)

type noopHealthSink struct{}

func (noopHealthSink) SetVideoPipeline(VideoPipelineSummary) {}

func newTestManager(cfg Config) (*Manager, *rtsp.Manager) {
	rtspMgr := rtsp.NewManager(rtsp.Config{Enabled: true}, nil, nil)
	return NewManager(cfg, rtspMgr, noopHealthSink{}, nil), rtspMgr
}

func TestManager_StartStopLifecycle(t *testing.T) {
	mgr, rtspMgr := newTestManager(Config{QueueDepth: 8, RingBufferSize: 4, MaxConcurrentPipelines: 2, TargetFPS: 5})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := rtspMgr.Start(ctx); err != nil {
		t.Fatalf("rtsp manager Start: %v", err)
	}
	if err := mgr.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer stopCancel()
	if err := mgr.Stop(stopCtx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	_ = rtspMgr.Stop(stopCtx)
}

// TestManager_OnPacketNoOpAfterStop is the direct regression test for the
// BLOCKER raised in review: after Stop() completes, OnPacket must be a
// harmless no-op — never a send on a closed channel, never a panic.
func TestManager_OnPacketNoOpAfterStop(t *testing.T) {
	mgr, rtspMgr := newTestManager(Config{QueueDepth: 8, RingBufferSize: 4, MaxConcurrentPipelines: 2})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = rtspMgr.Start(ctx)
	_ = mgr.Start(ctx)

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer stopCancel()
	if err := mgr.Stop(stopCtx); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	mgr.OnPacket("unknown-cam", []byte{0x65, 0x01}, time.Now())
}

// TestManager_ConcurrentOnPacketAndStop is the -race lifecycle test required
// by review point 5: a producer hammering OnPacket concurrently with Stop()
// must never panic or trip the race detector, regardless of interleaving.
func TestManager_ConcurrentOnPacketAndStop(t *testing.T) {
	mgr, rtspMgr := newTestManager(Config{QueueDepth: 8, RingBufferSize: 4, MaxConcurrentPipelines: 2})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = rtspMgr.Start(ctx)
	_ = mgr.Start(ctx)

	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				mgr.OnPacket("cam1", []byte{0x65, 0x01}, time.Now())
			}
		}
	}()

	time.Sleep(20 * time.Millisecond)
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer stopCancel()
	if err := mgr.Stop(stopCtx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	close(stop)
	wg.Wait()
}

func TestManager_NoGoroutineLeakAfterStop(t *testing.T) {
	mgr, rtspMgr := newTestManager(Config{QueueDepth: 8, RingBufferSize: 4, MaxConcurrentPipelines: 2})

	before := runtime.NumGoroutine()

	ctx, cancel := context.WithCancel(context.Background())
	_ = rtspMgr.Start(ctx)
	_ = mgr.Start(ctx)

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer stopCancel()
	if err := mgr.Stop(stopCtx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	_ = rtspMgr.Stop(stopCtx)
	cancel()

	var after int
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		after = runtime.NumGoroutine()
		if after <= before+1 { // small scheduler slack tolerance
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if after > before+1 {
		t.Fatalf("possible goroutine leak: before=%d after=%d", before, after)
	}
}

// recordingHealthSink captures the last VideoPipelineSummary published, so
// tests can assert on its CloudBuffer field (Milestone I6).
type recordingHealthSink struct {
	mu      sync.Mutex
	summary VideoPipelineSummary
}

func (r *recordingHealthSink) SetVideoPipeline(s VideoPipelineSummary) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.summary = s
}

func (r *recordingHealthSink) get() VideoPipelineSummary {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.summary
}

// bufferReportingSink is a minimal Sink that also implements
// CloudBufferReporter, standing in for cloudsink.CloudSink without
// importing it (would cycle back to this package).
type bufferReportingSink struct {
	stats CloudBufferStats
}

func (s *bufferReportingSink) Name() string                       { return "cloud" }
func (s *bufferReportingSink) Route(f Frame) error                { return nil }
func (s *bufferReportingSink) CloudBufferStats() CloudBufferStats { return s.stats }

func TestManager_PublishStatus_IncludesCloudBufferStatsFromReportingSink(t *testing.T) {
	rtspMgr := rtsp.NewManager(rtsp.Config{Enabled: true}, nil, nil)
	health := &recordingHealthSink{}
	extra := &bufferReportingSink{stats: CloudBufferStats{BufferedFrames: 3, BufferedBytes: 1024, ReplayedFrames: 5, DroppedFull: 1, CorruptEntries: 2}}
	mgr := NewManager(Config{QueueDepth: 8, RingBufferSize: 4, MaxConcurrentPipelines: 2}, rtspMgr, health, nil, extra)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = rtspMgr.Start(ctx)
	if err := mgr.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer stopCancel()
		_ = mgr.Stop(stopCtx)
	}()

	mgr.publishStatus()

	got := health.get().CloudBuffer
	if got == nil {
		t.Fatal("VideoPipelineSummary.CloudBuffer = nil, want a snapshot from the reporting sink")
	}
	if *got != extra.stats {
		t.Fatalf("CloudBuffer = %+v, want %+v", *got, extra.stats)
	}
}

func TestManager_PublishStatus_CloudBufferNilWithoutReportingSink(t *testing.T) {
	rtspMgr := rtsp.NewManager(rtsp.Config{Enabled: true}, nil, nil)
	health := &recordingHealthSink{}
	mgr := NewManager(Config{QueueDepth: 8, RingBufferSize: 4, MaxConcurrentPipelines: 2}, rtspMgr, health, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = rtspMgr.Start(ctx)
	if err := mgr.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer stopCancel()
		_ = mgr.Stop(stopCtx)
	}()

	mgr.publishStatus()

	if got := health.get().CloudBuffer; got != nil {
		t.Fatalf("CloudBuffer = %+v, want nil (no sink implements CloudBufferReporter)", *got)
	}
}
