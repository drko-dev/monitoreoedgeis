package remoteconfig

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/config"
	"github.com/drko-dev/monitoreoedgeis/internal/processing"
	"github.com/drko-dev/monitoreoedgeis/internal/rtsp"
	"github.com/drko-dev/monitoreoedgeis/internal/vision"
)

// fakeDecoder implements processing.VideoDecoder for testing without ffmpeg.
type fakeDecoder struct {
	mu           sync.Mutex
	frames       chan processing.DecodedFrame
	done         chan struct{}
	closed       bool
	closeCount   int
	decodedCount int64
	droppedCount int64
	width        int
	height       int
}

func newFakeDecoder(w, h int) *fakeDecoder {
	return &fakeDecoder{
		frames: make(chan processing.DecodedFrame, 16),
		done:   make(chan struct{}),
		width:  w,
		height: h,
	}
}

func (d *fakeDecoder) Push(au processing.AccessUnit) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return fmt.Errorf("decoder closed")
	}
	d.decodedCount++
	// Generate dummy YUV420p data
	yLen := d.width * d.height
	uvLen := (d.width / 2) * (d.height / 2)
	data := make([]byte, yLen+2*uvLen)
	for i := range data {
		data[i] = 128
	}

	frame := processing.DecodedFrame{
		Width:            d.width,
		Height:           d.height,
		DecodedAt:        time.Now(),
		SourceReceivedAt: au.ReceivedAt,
		PipelineSeq:      uint64(d.decodedCount),
		Data:             data,
	}
	select {
	case d.frames <- frame:
	default:
		d.droppedCount++
	}
	return nil
}

func (d *fakeDecoder) Frames() <-chan processing.DecodedFrame { return d.frames }
func (d *fakeDecoder) Done() <-chan struct{}                  { return d.done }
func (d *fakeDecoder) DecodedCount() int64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.decodedCount
}
func (d *fakeDecoder) DroppedCount() int64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.droppedCount
}
func (d *fakeDecoder) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.closed {
		d.closed = true
		d.closeCount++
		close(d.done)
	}
	return nil
}

// testSink collects routed frames for assertions.
type testSink struct {
	name   string
	mu     sync.Mutex
	frames []processing.Frame
}

func newTestSink(name string) *testSink {
	return &testSink{name: name}
}

func (s *testSink) Name() string { return s.name }
func (s *testSink) Route(f processing.Frame) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.frames = append(s.frames, f)
	return nil
}
func (s *testSink) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.frames)
}
func (s *testSink) Last() (processing.Frame, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.frames) == 0 {
		return processing.Frame{}, false
	}
	return s.frames[len(s.frames)-1], true
}

func setupTestRuntime(t *testing.T, initialMode config.ProcessingMode) (*RuntimeAdapter, *processing.Manager, *rtsp.Manager, *testSink, func() *fakeDecoder) {
	t.Helper()
	rtspCfg := rtsp.Config{
		StreamRole:    "sub",
		DialTimeout:   time.Second,
		PacketTimeout: time.Second,
		Enabled:       true,
	}
	rtspMgr := rtsp.NewManager(rtspCfg, nil, slog.Default())
	rtspMgr.SetTargets([]rtsp.CameraTarget{
		{CandidateKey: "cam-front", Addr: "192.168.1.10:554", RTSPPath: "/live"},
		{CandidateKey: "cam-back", Addr: "192.168.1.11:554", RTSPPath: "/live"},
	})

	sink := newTestSink("cloud-sink")
	procCfg := processing.Config{
		Enabled:                true,
		TargetFPS:              5.0,
		OutputWidth:            640,
		OutputHeight:           360,
		RingBufferSize:         30,
		QueueDepth:             16,
		DecodeQueueDepth:       4,
		MaxConcurrentPipelines: 4,
		Hybrid: processing.HybridConfig{
			Enabled:         initialMode == config.ModeHybrid,
			BlockSize:       16,
			MotionThreshold: 8.0,
			MinChangedArea:  0.05,
		},
	}

	videoMgr := processing.NewManager(procCfg, rtspMgr, nil, slog.Default(), sink)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	_ = videoMgr.Start(ctx)
	t.Cleanup(func() { _ = videoMgr.Stop(context.Background()) })

	var decMu sync.Mutex
	var activeDec *fakeDecoder
	decFactory := func() (processing.VideoDecoder, error) {
		decMu.Lock()
		defer decMu.Unlock()
		activeDec = newFakeDecoder(640, 360)
		return activeDec, nil
	}
	getDec := func() *fakeDecoder {
		decMu.Lock()
		defer decMu.Unlock()
		return activeDec
	}

	// Start pipeline for cam-front with fake decoder
	err := videoMgr.RestartCameraPipeline(ctx, "cam-front", procCfg)
	if err != nil {
		t.Fatalf("setup pipeline: %v", err)
	}
	pipe := videoMgr.Pipeline("cam-front")
	if pipe == nil {
		t.Fatalf("expected active pipeline for cam-front")
	}
	pipe.SetDecoderFactory(decFactory)
	// Restart again to pick up the fake decoder factory
	_ = videoMgr.RestartCameraPipeline(ctx, "cam-front", procCfg)

	modelMgr := vision.NewModelManager(t.TempDir(), SupportedPersonModel, SupportedVehicleModel)

	adapter := NewRuntimeAdapter(
		initialMode,
		videoMgr,
		rtspMgr,
		modelMgr,
		slog.Default(),
		WithCloudSinkFactory(func() processing.Sink {
			return sink
		}),
		WithVisionSinkFactory(func() (processing.Sink, func(ctx context.Context) error, func(ctx context.Context) error) {
			vs := newTestSink("vision-sink")
			return vs, func(ctx context.Context) error { return nil }, func(ctx context.Context) error { return nil }
		}),
	)

	return adapter, videoMgr, rtspMgr, sink, getDec
}

// 1. FPS remoto modifica sampler
func TestTargeted_1_RemoteFPSModifiesSampler(t *testing.T) {
	adapter, videoMgr, _, _, _ := setupTestRuntime(t, config.ModeCloud)
	ctx := context.Background()

	pipe := videoMgr.Pipeline("cam-front")
	if pipe == nil {
		t.Fatal("pipeline nil")
	}
	if got := pipe.Sampler().TargetFPS(); got != 5.0 {
		t.Fatalf("initial target fps: got %f, want 5.0", got)
	}

	// Apply global FPS change
	newFPS := 15.0
	err := adapter.Apply(ctx, Config{
		TargetFPS: &newFPS,
	})
	if err != nil {
		t.Fatalf("apply failed: %v", err)
	}

	if got := pipe.Sampler().TargetFPS(); got != 15.0 {
		t.Fatalf("after global apply: got %f, want 15.0", got)
	}

	// Apply per-camera FPS override
	camFPS := 10.0
	err = adapter.Apply(ctx, Config{
		Cameras: map[string]CameraConfig{
			"cam-front": {TargetFPS: &camFPS},
		},
	})
	if err != nil {
		t.Fatalf("apply per-camera failed: %v", err)
	}

	if got := pipe.Sampler().TargetFPS(); got != 10.0 {
		t.Fatalf("after per-camera apply: got %f, want 10.0", got)
	}
}

// 2. resolution modifica output real
func TestTargeted_2_ResolutionModifiesRealOutput(t *testing.T) {
	adapter, _, _, sink, getDec := setupTestRuntime(t, config.ModeCloud)
	ctx := context.Background()

	time.Sleep(20 * time.Millisecond)
	dec := getDec()
	if dec == nil {
		t.Fatal("expected active decoder")
	}

	// Push access unit through fake decoder to produce a 640x360 frame
	_ = dec.Push(processing.AccessUnit{ReceivedAt: time.Now()})
	time.Sleep(50 * time.Millisecond)

	frame, ok := sink.Last()
	if !ok {
		t.Fatal("expected frame at sink")
	}
	if frame.OutputWidth != 640 || frame.OutputHeight != 360 {
		t.Fatalf("initial frame resolution: got %dx%d, want 640x360", frame.OutputWidth, frame.OutputHeight)
	}

	// Apply new resolution: 320x180
	newW, newH := 320, 180
	err := adapter.Apply(ctx, Config{
		OutputWidth:  &newW,
		OutputHeight: &newH,
	})
	if err != nil {
		t.Fatalf("apply resolution failed: %v", err)
	}

	time.Sleep(50 * time.Millisecond)
	curDec := getDec()
	if curDec == nil {
		t.Fatal("expected active decoder after resolution apply")
	}

	// Push AU and verify new frame output resolution
	_ = curDec.Push(processing.AccessUnit{ReceivedAt: time.Now()})
	time.Sleep(50 * time.Millisecond)

	newFrame, ok := sink.Last()
	if !ok {
		t.Fatal("expected frame after resolution apply")
	}
	if newFrame.OutputWidth != 320 || newFrame.OutputHeight != 180 {
		t.Fatalf("after resolution apply: got %dx%d, want 320x180", newFrame.OutputWidth, newFrame.OutputHeight)
	}
}

// 3. cloud → hybrid
func TestTargeted_3_CloudToHybridTransition(t *testing.T) {
	adapter, videoMgr, _, _, _ := setupTestRuntime(t, config.ModeCloud)
	ctx := context.Background()

	pipe := videoMgr.Pipeline("cam-front")
	if pipe.MotionDetector() != nil {
		t.Fatal("motion detector should be nil in cloud mode")
	}

	hybridMode := config.ModeHybrid
	err := adapter.Apply(ctx, Config{
		ProcessingMode: &hybridMode,
	})
	if err != nil {
		t.Fatalf("apply cloud->hybrid failed: %v", err)
	}

	pipe = videoMgr.Pipeline("cam-front")
	if pipe.MotionDetector() == nil {
		t.Fatal("motion detector should be non-nil after transition to hybrid mode")
	}
	if adapter.CurrentConfig().ProcessingMode == nil || *adapter.CurrentConfig().ProcessingMode != config.ModeHybrid {
		t.Fatalf("current mode: got %v, want hybrid", adapter.CurrentConfig().ProcessingMode)
	}
}

// 4. hybrid → edge
func TestTargeted_4_HybridToEdgeTransition(t *testing.T) {
	adapter, videoMgr, _, _, _ := setupTestRuntime(t, config.ModeHybrid)
	ctx := context.Background()

	pipe := videoMgr.Pipeline("cam-front")
	if pipe.MotionDetector() == nil {
		t.Fatal("expected motion detector in hybrid mode")
	}

	edgeMode := config.ModeEdge
	err := adapter.Apply(ctx, Config{
		ProcessingMode: &edgeMode,
	})
	if err != nil {
		t.Fatalf("apply hybrid->edge failed: %v", err)
	}

	pipe = videoMgr.Pipeline("cam-front")
	if pipe.MotionDetector() != nil {
		t.Fatal("motion detector should be nil in edge mode")
	}
	sinks := videoMgr.ExtraSinks()
	if len(sinks) != 1 || sinks[0].Name() != "vision-sink" {
		t.Fatalf("expected router extra sinks to be [vision-sink], got %v", sinks)
	}
}

// 5. edge → cloud
func TestTargeted_5_EdgeToCloudTransition(t *testing.T) {
	adapter, videoMgr, _, _, _ := setupTestRuntime(t, config.ModeEdge)
	ctx := context.Background()

	cloudMode := config.ModeCloud
	err := adapter.Apply(ctx, Config{
		ProcessingMode: &cloudMode,
	})
	if err != nil {
		t.Fatalf("apply edge->cloud failed: %v", err)
	}

	sinks := videoMgr.ExtraSinks()
	if len(sinks) != 1 || sinks[0].Name() != "cloud-sink" {
		t.Fatalf("expected router extra sinks to be [cloud-sink], got %v", sinks)
	}
	pipe := videoMgr.Pipeline("cam-front")
	if pipe.MotionDetector() != nil {
		t.Fatal("motion detector should be nil in cloud mode")
	}
}

// 6. ROI válida aplica
func TestTargeted_6_ValidROIApplies(t *testing.T) {
	adapter, videoMgr, _, _, _ := setupTestRuntime(t, config.ModeHybrid)
	ctx := context.Background()

	validROIs := []config.HybridROI{
		{XMin: 0.1, YMin: 0.2, XMax: 0.8, YMax: 0.9},
	}

	err := adapter.Apply(ctx, Config{
		HybridROIs: validROIs,
	})
	if err != nil {
		t.Fatalf("apply valid ROI failed: %v", err)
	}

	pipe := videoMgr.Pipeline("cam-front")
	detector := pipe.MotionDetector()
	if detector == nil {
		t.Fatal("expected motion detector in hybrid mode")
	}
	rois := detector.ROIs()
	if len(rois) != 1 || rois[0].XMin != 0.1 || rois[0].XMax != 0.8 {
		t.Fatalf("expected applied ROI [{0.1, 0.2, 0.8, 0.9}], got %+v", rois)
	}
}

// 7. ROI inválida no modifica runtime
func TestTargeted_7_InvalidROINoRuntimeModification(t *testing.T) {
	adapter, videoMgr, _, _, _ := setupTestRuntime(t, config.ModeHybrid)
	ctx := context.Background()

	initialROIs := []config.HybridROI{
		{XMin: 0.1, YMin: 0.1, XMax: 0.5, YMax: 0.5},
	}
	_ = adapter.Apply(ctx, Config{HybridROIs: initialROIs})

	// Try invalid ROI: min >= max
	invalidROIs := []config.HybridROI{
		{XMin: 0.8, YMin: 0.8, XMax: 0.2, YMax: 0.2},
	}
	err := adapter.Apply(ctx, Config{
		HybridROIs: invalidROIs,
	})
	if err == nil {
		t.Fatal("expected error applying invalid ROI, got nil")
	}

	pipe := videoMgr.Pipeline("cam-front")
	rois := pipe.MotionDetector().ROIs()
	if len(rois) != 1 || rois[0].XMin != 0.1 || rois[0].XMax != 0.5 {
		t.Fatalf("runtime ROI was modified despite invalid config: %+v", rois)
	}
}

// 8. modelo conocido acepta
func TestTargeted_8_KnownModelAccepted(t *testing.T) {
	adapter, _, _, _, _ := setupTestRuntime(t, config.ModeEdge)
	ctx := context.Background()

	person := SupportedPersonModel
	vehicle := SupportedVehicleModel

	err := adapter.Apply(ctx, Config{
		PersonModel:  &person,
		VehicleModel: &vehicle,
	})
	if err != nil {
		t.Fatalf("known models rejected: %v", err)
	}

	cur := adapter.CurrentConfig()
	if cur.PersonModel == nil || *cur.PersonModel != SupportedPersonModel {
		t.Fatalf("expected person model %s, got %v", SupportedPersonModel, cur.PersonModel)
	}
	if cur.VehicleModel == nil || *cur.VehicleModel != SupportedVehicleModel {
		t.Fatalf("expected vehicle model %s, got %v", SupportedVehicleModel, cur.VehicleModel)
	}
}

// 9. modelo desconocido rechaza
func TestTargeted_9_UnknownModelRejected(t *testing.T) {
	adapter, _, _, _, _ := setupTestRuntime(t, config.ModeEdge)
	ctx := context.Background()

	unknown := "yolov8n.pt"
	err := adapter.Apply(ctx, Config{
		PersonModel: &unknown,
	})
	if err == nil {
		t.Fatal("expected error for unknown person model, got nil")
	}

	maliciousURL := "http://evil.com/weights.pt"
	err = adapter.Apply(ctx, Config{
		PersonModel: &maliciousURL,
	})
	if err == nil {
		t.Fatal("expected error for url person model, got nil")
	}

	maliciousPath := "/tmp/yolo.pt"
	err = adapter.Apply(ctx, Config{
		VehicleModel: &maliciousPath,
	})
	if err == nil {
		t.Fatal("expected error for path vehicle model, got nil")
	}
}

// 10. pipeline start failure permite rollback
func TestTargeted_10_PipelineStartFailurePermitsRollback(t *testing.T) {
	adapter, videoMgr, _, _, _ := setupTestRuntime(t, config.ModeCloud)
	ctx := context.Background()

	oldPipe := videoMgr.Pipeline("cam-front")
	oldFPS := oldPipe.Sampler().TargetFPS()

	// Simulate post-apply health check failure
	failHealth := true
	adapter.healthCheck = func(ctx context.Context) error {
		if failHealth {
			return fmt.Errorf("simulated pipeline health failure")
		}
		return nil
	}

	newFPS := 25.0
	err := adapter.Apply(ctx, Config{
		TargetFPS: &newFPS,
	})
	if err == nil {
		t.Fatal("expected apply failure due to health check, got nil")
	}

	// Verify runtime rolled back to oldFPS
	curPipe := videoMgr.Pipeline("cam-front")
	if got := curPipe.Sampler().TargetFPS(); got != oldFPS {
		t.Fatalf("pipeline did not rollback: got %f, want %f", got, oldFPS)
	}

	// Now succeed with health check and test explicit Rollback()
	failHealth = false
	err = adapter.Apply(ctx, Config{
		TargetFPS: &newFPS,
	})
	if err != nil {
		t.Fatalf("expected successful apply with clean health check: %v", err)
	}
	if got := videoMgr.Pipeline("cam-front").Sampler().TargetFPS(); got != 25.0 {
		t.Fatalf("expected applied 25.0, got %f", got)
	}

	// Explicit Rollback()
	err = adapter.Rollback(ctx)
	if err != nil {
		t.Fatalf("explicit rollback failed: %v", err)
	}
	if got := videoMgr.Pipeline("cam-front").Sampler().TargetFPS(); got != oldFPS {
		t.Fatalf("explicit rollback did not restore old fps: got %f, want %f", got, oldFPS)
	}
}

// 11. no queda segundo RTSP/decoder/pipeline
func TestTargeted_11_NoDuplicatePipelineOrDecoder(t *testing.T) {
	adapter, videoMgr, rtspMgr, _, getDec := setupTestRuntime(t, config.ModeCloud)
	ctx := context.Background()

	initialDec := getDec()
	initialSupervisors := len(rtspMgr.Snapshot())
	if initialSupervisors != 2 {
		t.Fatalf("expected 2 supervisors, got %d", initialSupervisors)
	}

	// Apply multiple resolution changes requiring pipeline recreation
	for _, res := range [][2]int{{320, 180}, {640, 360}, {800, 600}} {
		w, h := res[0], res[1]
		err := adapter.Apply(ctx, Config{
			OutputWidth:  &w,
			OutputHeight: &h,
		})
		if err != nil {
			t.Fatalf("apply resolution %dx%d failed: %v", w, h, err)
		}
	}

	// 1. Verify RTSP supervisor count is unchanged
	afterSupervisors := len(rtspMgr.Snapshot())
	if afterSupervisors != initialSupervisors {
		t.Fatalf("RTSP supervisors count changed: got %d, want %d", afterSupervisors, initialSupervisors)
	}

	// 2. Verify pipeline count for cam-front is exactly 1
	active := videoMgr.ActivePipelines()
	count := 0
	for _, key := range active {
		if key == "cam-front" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 pipeline for cam-front, found %d", count)
	}

	// 3. Verify original fake decoder was closed
	if initialDec != nil && !initialDec.closed {
		t.Fatal("original decoder was not closed during pipeline recreation")
	}
}

// 12. config parcial nunca queda activa
func TestTargeted_12_PartialConfigNeverActive(t *testing.T) {
	adapter, videoMgr, _, _, _ := setupTestRuntime(t, config.ModeCloud)
	ctx := context.Background()

	pipe := videoMgr.Pipeline("cam-front")
	initialFPS := pipe.Sampler().TargetFPS()

	// Provide valid TargetFPS (20.0), but invalid ROI (XMin = 2.0) and unknown camera
	newFPS := 20.0
	badROI := []config.HybridROI{{XMin: 2.0, YMin: 0.1, XMax: 3.0, YMax: 0.5}}
	err := adapter.Apply(ctx, Config{
		TargetFPS:  &newFPS,
		HybridROIs: badROI,
	})
	if err == nil {
		t.Fatal("expected validation failure on partial config, got nil")
	}

	// Check that TargetFPS was NEVER updated
	if got := pipe.Sampler().TargetFPS(); got != initialFPS {
		t.Fatalf("TargetFPS was partially updated to %f despite invalid ROI, expected %f", got, initialFPS)
	}

	// Test disallowed fields rejection via JSON
	disallowedJSON := []byte(`{
		"target_fps": 15.0,
		"tenant_id": "malicious-tenant-123"
	}`)
	_, err = ParseAndValidateJSON(disallowedJSON, []string{"cam-front", "cam-back"})
	if err == nil {
		t.Fatal("expected rejection of disallowed tenant_id in JSON, got nil")
	}
}

// 4. Partial config merge conserves effective config
func TestTargeted_04_PartialConfigMergeConservesEffectiveConfig(t *testing.T) {
	adapter, videoMgr, _, _, _ := setupTestRuntime(t, config.ModeHybrid)
	pipe := videoMgr.Pipeline("cam-front")
	ctx := context.Background()

	// Apply 1: only TargetFPS = 15.0
	fps1 := 15.0
	if err := adapter.Apply(ctx, Config{TargetFPS: &fps1}); err != nil {
		t.Fatalf("first apply failed: %v", err)
	}

	cur1 := adapter.CurrentConfig()
	if cur1.TargetFPS == nil || *cur1.TargetFPS != 15.0 {
		t.Fatalf("TargetFPS = %v, want 15.0", cur1.TargetFPS)
	}
	if cur1.OutputWidth == nil || *cur1.OutputWidth != 640 {
		t.Fatalf("OutputWidth = %v, want 640 (preserved)", cur1.OutputWidth)
	}
	if cur1.OutputHeight == nil || *cur1.OutputHeight != 360 {
		t.Fatalf("OutputHeight = %v, want 360 (preserved)", cur1.OutputHeight)
	}

	// Apply 2: only resolution change (1280x720)
	w2, h2 := 1280, 720
	if err := adapter.Apply(ctx, Config{OutputWidth: &w2, OutputHeight: &h2}); err != nil {
		t.Fatalf("second apply failed: %v", err)
	}

	// Verify effective configuration merged: both TargetFPS (15.0) AND Resolution (1280x720) are present
	cur2 := adapter.CurrentConfig()
	if cur2.TargetFPS == nil || *cur2.TargetFPS != 15.0 {
		t.Fatalf("TargetFPS = %v, want 15.0 (conserved from apply 1)", cur2.TargetFPS)
	}
	if cur2.OutputWidth == nil || *cur2.OutputWidth != 1280 {
		t.Fatalf("OutputWidth = %v, want 1280", cur2.OutputWidth)
	}
	if cur2.OutputHeight == nil || *cur2.OutputHeight != 720 {
		t.Fatalf("OutputHeight = %v, want 720", cur2.OutputHeight)
	}
	if got := pipe.Sampler().TargetFPS(); got != 15.0 {
		t.Fatalf("runtime pipe TargetFPS = %f, want 15.0", got)
	}
}

// 5. Failed second partial apply restores previous effective config
func TestTargeted_05_FailedSecondPartialApplyRestoresPreviousEffectiveConfig(t *testing.T) {
	adapter, videoMgr, _, _, _ := setupTestRuntime(t, config.ModeHybrid)
	pipe := videoMgr.Pipeline("cam-front")
	ctx := context.Background()

	// Apply 1: change TargetFPS to 20.0
	fps1 := 20.0
	if err := adapter.Apply(ctx, Config{TargetFPS: &fps1}); err != nil {
		t.Fatalf("first apply failed: %v", err)
	}
	if got := pipe.Sampler().TargetFPS(); got != 20.0 {
		t.Fatalf("pipe TargetFPS = %f, want 20.0", got)
	}

	// Apply 2: try to apply invalid ROI that fails validation
	badROI := []config.HybridROI{{XMin: 2.0, YMin: 0.1, XMax: 3.0, YMax: 0.5}}
	err := adapter.Apply(ctx, Config{
		HybridROIs: badROI,
	})
	if err == nil {
		t.Fatal("expected apply failure due to invalid ROI")
	}

	// Verify effective config is still the state from Apply 1
	cur := adapter.CurrentConfig()
	if cur.TargetFPS == nil || *cur.TargetFPS != 20.0 {
		t.Fatalf("TargetFPS = %v, want 20.0 preserved", cur.TargetFPS)
	}
	if got := pipe.Sampler().TargetFPS(); got != 20.0 {
		t.Fatalf("runtime pipe TargetFPS = %f, want 20.0 preserved", got)
	}

	// Also test health check failure rollback restores previous effective config
	failAdapter := NewRuntimeAdapter(
		config.ModeHybrid,
		adapter.videoManager,
		adapter.rtspManager,
		adapter.modelManager,
		slog.Default(),
		WithHealthCheck(func(ctx context.Context) error {
			return fmt.Errorf("simulated health check failure")
		}),
	)
	// Try applying a change on failAdapter
	fps3 := 25.0
	err = failAdapter.Apply(ctx, Config{TargetFPS: &fps3})
	if err == nil {
		t.Fatal("expected error from health check failure")
	}
	// Verify current config on failAdapter was rolled back to initial effective config
	curFail := failAdapter.CurrentConfig()
	if curFail.TargetFPS == nil || *curFail.TargetFPS != 20.0 {
		t.Fatalf("TargetFPS after failed apply = %v, want 20.0", curFail.TargetFPS)
	}
}

// 10. knownCameras empty + camera override = reject
func TestTargeted_10_KnownCamerasEmptyRejectsCameraOverride(t *testing.T) {
	// 1. Standalone Validate with knownCameras = nil or empty
	camFPS := 15.0
	cfg := Config{
		Cameras: map[string]CameraConfig{
			"cam-any": {
				TargetFPS: &camFPS,
			},
		},
	}

	err := Validate(cfg, nil)
	if err == nil {
		t.Fatal("expected error when validating camera override against empty knownCameras, got nil")
	}
	if !strings.Contains(err.Error(), "camera \"cam-any\" is not known by runtime") {
		t.Fatalf("unexpected error message: %v", err)
	}

	err = Validate(cfg, []string{})
	if err == nil {
		t.Fatal("expected error when validating camera override against []string{}, got nil")
	}

	// 2. RuntimeAdapter with no known cameras must reject camera override
	adapterNoCameras := NewRuntimeAdapter(
		config.ModeCloud,
		nil,
		nil,
		nil,
		slog.Default(),
	)

	err = adapterNoCameras.Apply(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected adapter.Apply to reject camera override when runtime knows no cameras")
	}
	if !strings.Contains(err.Error(), "camera \"cam-any\" is not known by runtime") {
		t.Fatalf("unexpected adapter.Apply error message: %v", err)
	}
}
