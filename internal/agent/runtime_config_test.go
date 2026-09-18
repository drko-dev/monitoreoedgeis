package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/config"
	"github.com/drko-dev/monitoreoedgeis/internal/health"
	"github.com/drko-dev/monitoreoedgeis/internal/remoteconfig"
	"github.com/drko-dev/monitoreoedgeis/internal/vision"
)

func setupTestAgentWithPipeline(t *testing.T, initialMode config.ProcessingMode) (*Agent, *config.Config) {
	t.Helper()
	cfg := testConfig(t)
	cfg.ProcessingMode = initialMode
	cfg.ConnectivityEnabled = true
	cfg.VideoPipelineEnabled = true
	cfg.VideoTargetFPS = 10
	cfg.VideoOutputWidth = 640
	cfg.VideoOutputHeight = 360
	cfg.VideoRingBufferSize = 10
	cfg.LocalEventBacklogMaxOperations = 100
	cfg.LocalEventBacklogMaxBytes = 1024 * 1024
	cfg.EdgeMaxClipSizeBytes = 5 * 1024 * 1024
	cfg.SaaSURL = "https://saas.example.com"

	// Mock enrollment credentials
	identPath := filepath.Join(cfg.DataDir, "identity.json")
	_ = os.WriteFile(identPath, []byte(`{"edge_id":"550e8400-e29b-41d4-a716-446655440000","created_at":"2026-09-18T00:00:00Z","schema_version":1}`), 0o600)
	credsPath := filepath.Join(cfg.DataDir, "credentials.json")
	_ = os.WriteFile(credsPath, []byte(`{"edge_id":"550e8400-e29b-41d4-a716-446655440000","device_id":"dev-1","credential":"cred-1","enrolled_at":"2026-09-18T00:00:00Z","schema_version":1}`), 0o600)

	// Mock YOLO models
	modelsDir := filepath.Join(cfg.DataDir, "models")
	_ = os.MkdirAll(modelsDir, 0o755)
	_ = os.WriteFile(filepath.Join(modelsDir, "yolo11s-pose.pt"), []byte("pose-weights"), 0o644)
	_ = os.WriteFile(filepath.Join(modelsDir, "yolo11n.pt"), []byte("vehicle-weights"), 0o644)
	cfg.EdgeYOLOModelsDir = modelsDir
	cfg.EdgeYOLOPersonModel = "yolo11s-pose.pt"
	cfg.EdgeYOLOVehicleModel = "yolo11n.pt"

	a := New(cfg)
	if a.RuntimeApplier() == nil {
		t.Fatal("expected non-nil RuntimeApplier")
	}
	return a, cfg
}

// 1. Real startup cloud -> edge creates VisionSink
func TestTargeted_01_RealStartupCloudToEdgeCreatesVisionSink(t *testing.T) {
	a, _ := setupTestAgentWithPipeline(t, config.ModeCloud)
	ctx := context.Background()

	// Initial mode is cloud
	if a.Health().ProcessingMode() != "cloud" {
		t.Fatalf("expected initial mode cloud, got %s", a.Health().ProcessingMode())
	}

	// Apply transition to edge mode
	edgeMode := config.ModeEdge
	if err := a.RuntimeApplier().Apply(ctx, remoteconfig.Config{ProcessingMode: &edgeMode}); err != nil {
		t.Fatalf("Apply cloud->edge failed: %v", err)
	}

	// Reported mode should now be edge
	if got := a.Health().ProcessingMode(); got != "edge" {
		t.Fatalf("Health().ProcessingMode() = %q, want %q", got, "edge")
	}
	if got := a.Health().Snapshot().ProcessingMode; got != "edge" {
		t.Fatalf("Snapshot.ProcessingMode = %q, want %q", got, "edge")
	}

	// FullEdgeService is now exposed
	if a.FullEdgeService() == nil {
		t.Fatal("expected non-nil FullEdgeService in edge mode")
	}

	// VideoManager routers have vision sink
	sinks := a.VideoManager().ExtraSinks()
	if len(sinks) != 1 || sinks[0].Name() != "edge-vision" {
		t.Fatalf("expected exactly 1 edge-vision sink in VideoManager, got: %+v", sinks)
	}
}

// 2. Real startup edge -> cloud creates CloudSink
func TestTargeted_02_RealStartupEdgeToCloudCreatesCloudSink(t *testing.T) {
	a, _ := setupTestAgentWithPipeline(t, config.ModeEdge)
	ctx := context.Background()

	// Initial mode is edge
	if a.Health().ProcessingMode() != "edge" {
		t.Fatalf("expected initial mode edge, got %s", a.Health().ProcessingMode())
	}
	sinks := a.VideoManager().ExtraSinks()
	if len(sinks) != 1 || sinks[0].Name() != "edge-vision" {
		t.Fatalf("expected edge-vision sink initially, got: %+v", sinks)
	}

	// Apply transition to cloud mode
	cloudMode := config.ModeCloud
	if err := a.RuntimeApplier().Apply(ctx, remoteconfig.Config{ProcessingMode: &cloudMode}); err != nil {
		t.Fatalf("Apply edge->cloud failed: %v", err)
	}

	// Reported mode should now be cloud
	if got := a.Health().ProcessingMode(); got != "cloud" {
		t.Fatalf("Health().ProcessingMode() = %q, want %q", got, "cloud")
	}
	if a.FullEdgeService() != nil {
		t.Fatalf("expected nil FullEdgeService in cloud mode")
	}

	// VideoManager routers have cloud sink
	sinks = a.VideoManager().ExtraSinks()
	if len(sinks) != 1 || sinks[0].Name() != "cloud" {
		t.Fatalf("expected exactly 1 cloud sink in VideoManager, got: %+v", sinks)
	}
}

// 3. Cloud startup -> edge produces LocalEvent using FullEdge consumer
func TestTargeted_03_CloudStartupToEdgeProducesLocalEvent(t *testing.T) {
	a, _ := setupTestAgentWithPipeline(t, config.ModeCloud)
	ctx := context.Background()

	// Initially in cloud mode, FullEdge consumer is wired
	consumer := a.FullEdgeConsumer()
	if consumer == nil {
		t.Fatal("expected non-nil FullEdgeConsumer on Agent startup even in cloud mode")
	}

	// Apply transition to edge mode
	edgeMode := config.ModeEdge
	if err := a.RuntimeApplier().Apply(ctx, remoteconfig.Config{ProcessingMode: &edgeMode}); err != nil {
		t.Fatalf("Apply cloud->edge failed: %v", err)
	}

	feSvc := a.FullEdgeService()
	if feSvc == nil {
		t.Fatal("expected non-nil FullEdgeService after transitioning to edge")
	}

	// Simulate detection produced by vision inference
	res := vision.InferenceResult{
		CandidateKey:  "cam-front",
		FrameSeq:      101,
		Timestamp:     time.Now().UTC(),
		InferenceMS:   15.2,
		Device:        "cpu",
		CorrelationID: "corr-101",
		Detections: []vision.Detection{
			{ClassID: vision.ClassIDPerson, Label: "person", Type: vision.DetectionTypePerson, Confidence: 0.95, BBox: [4]float64{0.1, 0.1, 0.4, 0.4}},
		},
	}
	fakeJPEG := []byte("jpeg-data-stream")
	consumer.ConsumeInference(res, fakeJPEG)

	// Verify local event was created in event store
	events, err := feSvc.Store().ListPending()
	if err != nil {
		t.Fatalf("ListPending failed: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 LocalEvent stored, got %d", len(events))
	}
	if events[0].CandidateKey != "cam-front" {
		t.Errorf("candidateKey = %q, want cam-front", events[0].CandidateKey)
	}
}

// 6. /status mode changes with runtime
func TestTargeted_06_StatusModeChangesWithRuntime(t *testing.T) {
	a, _ := setupTestAgentWithPipeline(t, config.ModeCloud)
	ctx := context.Background()

	handler := health.Handler(a.Health())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	handler.ServeHTTP(rec, req)

	var snap1 health.Snapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &snap1); err != nil {
		t.Fatalf("unmarshal /status response: %v", err)
	}
	if snap1.ProcessingMode != "cloud" {
		t.Fatalf("expected /status processing_mode = 'cloud', got %q", snap1.ProcessingMode)
	}

	// Apply edge mode
	edgeMode := config.ModeEdge
	if err := a.RuntimeApplier().Apply(ctx, remoteconfig.Config{ProcessingMode: &edgeMode}); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/status", nil)
	handler.ServeHTTP(rec2, req2)

	var snap2 health.Snapshot
	if err := json.Unmarshal(rec2.Body.Bytes(), &snap2); err != nil {
		t.Fatalf("unmarshal /status response: %v", err)
	}
	if snap2.ProcessingMode != "edge" {
		t.Fatalf("expected /status processing_mode = 'edge' after runtime apply, got %q", snap2.ProcessingMode)
	}
}

// 7. Heartbeat mode changes with runtime
func TestTargeted_07_HeartbeatModeChangesWithRuntime(t *testing.T) {
	a, _ := setupTestAgentWithPipeline(t, config.ModeCloud)
	ctx := context.Background()

	// Initial heartbeat snapshot
	snapCloud := a.Health().Snapshot()
	if snapCloud.ProcessingMode != "cloud" {
		t.Fatalf("initial snapshot processing_mode = %q, want 'cloud'", snapCloud.ProcessingMode)
	}

	// Apply edge mode
	edgeMode := config.ModeEdge
	if err := a.RuntimeApplier().Apply(ctx, remoteconfig.Config{ProcessingMode: &edgeMode}); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	// Heartbeat request payload reads Snapshot().ProcessingMode
	snapEdge := a.Health().Snapshot()
	if snapEdge.ProcessingMode != "edge" {
		t.Fatalf("after apply, snapshot processing_mode = %q, want 'edge'", snapEdge.ProcessingMode)
	}
}

// 8. Rollback restores reported mode
func TestTargeted_08_RollbackRestoresReportedMode(t *testing.T) {
	a, _ := setupTestAgentWithPipeline(t, config.ModeCloud)
	ctx := context.Background()

	// Apply transition to edge mode
	edgeMode := config.ModeEdge
	if err := a.RuntimeApplier().Apply(ctx, remoteconfig.Config{ProcessingMode: &edgeMode}); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}
	if a.Health().ProcessingMode() != "edge" {
		t.Fatalf("processing mode not switched to edge")
	}

	// Rollback
	if err := a.RuntimeApplier().Rollback(ctx); err != nil {
		t.Fatalf("Rollback failed: %v", err)
	}

	// Verify mode restored to cloud
	if got := a.Health().ProcessingMode(); got != "cloud" {
		t.Fatalf("Health().ProcessingMode() after rollback = %q, want 'cloud'", got)
	}
	if got := a.Health().Snapshot().ProcessingMode; got != "cloud" {
		t.Fatalf("Snapshot.ProcessingMode after rollback = %q, want 'cloud'", got)
	}
	if a.FullEdgeService() != nil {
		t.Fatalf("expected FullEdgeService to be nil after rollback to cloud")
	}
}

// 9. Vision worker uses shared ModelManager
func TestTargeted_09_VisionWorkerUsesSharedModelManager(t *testing.T) {
	a, _ := setupTestAgentWithPipeline(t, config.ModeCloud)
	ctx := context.Background()

	sharedMM := a.ModelManager()
	if sharedMM == nil {
		t.Fatal("expected non-nil shared ModelManager on Agent")
	}

	// Apply edge mode
	edgeMode := config.ModeEdge
	if err := a.RuntimeApplier().Apply(ctx, remoteconfig.Config{ProcessingMode: &edgeMode}); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	// Router sinks contains vision sink
	sinks := a.VideoManager().ExtraSinks()
	if len(sinks) == 0 {
		t.Fatal("no sinks found")
	}
	vs, ok := sinks[0].(*vision.Sink)
	if !ok {
		t.Fatalf("sink is not *vision.Sink: %T", sinks[0])
	}

	// Verify vision sink uses the EXACT SAME shared ModelManager pointer
	if vs.ModelManager() != sharedMM {
		t.Fatalf("VisionSink ModelManager (%p) != Agent ModelManager (%p)", vs.ModelManager(), sharedMM)
	}
	if vs.Worker().ModelManager() != sharedMM {
		t.Fatalf("VisionWorker ModelManager (%p) != Agent ModelManager (%p)", vs.Worker().ModelManager(), sharedMM)
	}
}
