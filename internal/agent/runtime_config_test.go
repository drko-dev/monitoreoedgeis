package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/config"
	"github.com/drko-dev/monitoreoedgeis/internal/health"
	"github.com/drko-dev/monitoreoedgeis/internal/processing"
	"github.com/drko-dev/monitoreoedgeis/internal/remoteconfig"
	"github.com/drko-dev/monitoreoedgeis/internal/transport"
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

	_ = os.Setenv("GEOCAM_VISION_FAKE_WORKER", "1")
	cfg.EdgeYOLOWorkerCmd = os.Args[0]
	cfg.EdgeYOLOWorkerArgs = []string{"-test.run=^TestMain$"}
	sockDir, err := os.MkdirTemp("/tmp", "vsock")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
	cfg.EdgeYOLOSocketPath = filepath.Join(sockDir, "v.sock")
	cfg.EdgeYOLOStartTimeout = 2 * time.Second
	cfg.EdgeYOLOInferTimeout = 1 * time.Second

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
	if err := a.RuntimeApplier().Apply(ctx, remoteconfig.RuntimeConfig{ProcessingMode: &edgeMode}); err != nil {
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
	if err := a.RuntimeApplier().Apply(ctx, remoteconfig.RuntimeConfig{ProcessingMode: &cloudMode}); err != nil {
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
	if err := a.RuntimeApplier().Apply(ctx, remoteconfig.RuntimeConfig{ProcessingMode: &edgeMode}); err != nil {
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
	if err := a.RuntimeApplier().Apply(ctx, remoteconfig.RuntimeConfig{ProcessingMode: &edgeMode}); err != nil {
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
	if err := a.RuntimeApplier().Apply(ctx, remoteconfig.RuntimeConfig{ProcessingMode: &edgeMode}); err != nil {
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
	if err := a.RuntimeApplier().Apply(ctx, remoteconfig.RuntimeConfig{ProcessingMode: &edgeMode}); err != nil {
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
	if err := a.RuntimeApplier().Apply(ctx, remoteconfig.RuntimeConfig{ProcessingMode: &edgeMode}); err != nil {
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

// ============================================================================
// TARGET MODE OPERATIONAL BEFORE COMMIT TESTS
// ============================================================================

// 1. cloud -> edge with WorkerCmd absent fails, cloud sink retained, mode unchanged, /readyz ok
func TestTargetedOperational_01_CloudToEdgeWithoutWorkerCmdFails(t *testing.T) {
	a, cfg := setupTestAgentWithPipeline(t, config.ModeCloud)
	a.Health().Set(health.StateReady)
	cfg.EdgeYOLOWorkerCmd = ""
	ctx := context.Background()

	edgeMode := config.ModeEdge
	err := a.RuntimeApplier().Apply(ctx, remoteconfig.RuntimeConfig{ProcessingMode: &edgeMode})
	if err == nil {
		t.Fatal("expected Apply cloud->edge without WorkerCmd to fail")
	}

	// Router maintains CloudSink
	sinks := a.VideoManager().ExtraSinks()
	if len(sinks) != 1 || sinks[0].Name() != "cloud" {
		t.Fatalf("expected router to maintain cloud sink, got: %+v", sinks)
	}

	// Mode remains cloud
	if got := a.Health().ProcessingMode(); got != "cloud" {
		t.Fatalf("Health().ProcessingMode() = %q, want 'cloud'", got)
	}

	// /readyz does not fail due to false commit
	handler := health.Handler(a.Health())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("/readyz returned %d, want 200 OK", rec.Code)
	}
}

// 2. cloud -> edge with missing models fails, no orphan VisionSink, mode remains cloud
func TestTargetedOperational_02_CloudToEdgeWithoutModelsFails(t *testing.T) {
	a, cfg := setupTestAgentWithPipeline(t, config.ModeCloud)
	// Remove models dir
	_ = os.RemoveAll(cfg.EdgeYOLOModelsDir)
	_ = os.MkdirAll(cfg.EdgeYOLOModelsDir, 0o755)
	cfg.EdgeYOLOStartTimeout = 100 * time.Millisecond
	ctx := context.Background()

	edgeMode := config.ModeEdge
	err := a.RuntimeApplier().Apply(ctx, remoteconfig.RuntimeConfig{ProcessingMode: &edgeMode})
	if err == nil {
		t.Fatal("expected Apply cloud->edge with missing models to fail")
	}
	if !strings.Contains(err.Error(), "model_missing") && !strings.Contains(err.Error(), "not ready") {
		t.Fatalf("expected model_missing error, got: %v", err)
	}

	// Router maintains CloudSink without orphan VisionSink
	sinks := a.VideoManager().ExtraSinks()
	if len(sinks) != 1 || sinks[0].Name() != "cloud" {
		t.Fatalf("expected router to maintain cloud sink, got: %+v", sinks)
	}

	// Mode remains cloud
	if got := a.Health().ProcessingMode(); got != "cloud" {
		t.Fatalf("Health().ProcessingMode() = %q, want 'cloud'", got)
	}
}

// 3. cloud -> edge with worker reaching READY succeeds, router has VisionSink, mode committed
func TestTargetedOperational_03_CloudToEdgeWithWorkerReadySucceeds(t *testing.T) {
	a, _ := setupTestAgentWithPipeline(t, config.ModeCloud)
	ctx := context.Background()

	edgeMode := config.ModeEdge
	if err := a.RuntimeApplier().Apply(ctx, remoteconfig.RuntimeConfig{ProcessingMode: &edgeMode}); err != nil {
		t.Fatalf("Apply cloud->edge failed: %v", err)
	}

	// Router has VisionSink
	sinks := a.VideoManager().ExtraSinks()
	if len(sinks) != 1 || sinks[0].Name() != "edge-vision" {
		t.Fatalf("expected router to have edge-vision sink, got: %+v", sinks)
	}

	// Mode committed to edge
	if got := a.Health().ProcessingMode(); got != "edge" {
		t.Fatalf("Health().ProcessingMode() = %q, want 'edge'", got)
	}
}

// 4. edge -> cloud with nil CloudSink fails, router maintains VisionSink, mode remains edge
func TestTargetedOperational_04_EdgeToCloudWithNilCloudSinkFails(t *testing.T) {
	a, cfg := setupTestAgentWithPipeline(t, config.ModeEdge)
	// Clear SaaSURL so buildCloudSink returns nil
	cfg.SaaSURL = ""
	ctx := context.Background()

	cloudMode := config.ModeCloud
	err := a.RuntimeApplier().Apply(ctx, remoteconfig.RuntimeConfig{ProcessingMode: &cloudMode})
	if err == nil {
		t.Fatal("expected Apply edge->cloud with nil CloudSink to fail")
	}

	// Router maintains VisionSink
	sinks := a.VideoManager().ExtraSinks()
	if len(sinks) != 1 || sinks[0].Name() != "edge-vision" {
		t.Fatalf("expected router to maintain edge-vision sink, got: %+v", sinks)
	}

	// Mode remains edge
	if got := a.Health().ProcessingMode(); got != "edge" {
		t.Fatalf("Health().ProcessingMode() = %q, want 'edge'", got)
	}
}

// 5. hybrid/cloud target with valid CloudSink succeeds, router has CloudSink, mode committed
func TestTargetedOperational_05_HybridCloudTargetWithValidCloudSinkSucceeds(t *testing.T) {
	a, _ := setupTestAgentWithPipeline(t, config.ModeEdge)
	ctx := context.Background()

	hybridMode := config.ModeHybrid
	if err := a.RuntimeApplier().Apply(ctx, remoteconfig.RuntimeConfig{ProcessingMode: &hybridMode}); err != nil {
		t.Fatalf("Apply edge->hybrid failed: %v", err)
	}

	sinks := a.VideoManager().ExtraSinks()
	if len(sinks) != 1 || sinks[0].Name() != "cloud" {
		t.Fatalf("expected router to have cloud sink in hybrid mode, got: %+v", sinks)
	}
	if got := a.Health().ProcessingMode(); got != "hybrid" {
		t.Fatalf("Health().ProcessingMode() = %q, want 'hybrid'", got)
	}

	cloudMode := config.ModeCloud
	if err := a.RuntimeApplier().Apply(ctx, remoteconfig.RuntimeConfig{ProcessingMode: &cloudMode}); err != nil {
		t.Fatalf("Apply hybrid->cloud failed: %v", err)
	}
	sinks = a.VideoManager().ExtraSinks()
	if len(sinks) != 1 || sinks[0].Name() != "cloud" {
		t.Fatalf("expected router to retain cloud sink in cloud mode, got: %+v", sinks)
	}
	if got := a.Health().ProcessingMode(); got != "cloud" {
		t.Fatalf("Health().ProcessingMode() = %q, want 'cloud'", got)
	}
}

// 6. Post-swap failure triggers rollback: restores sinks, stops new worker, keeps previous mode
func TestTargetedOperational_06_PostSwapFailureRollsBackSinkAndMode(t *testing.T) {
	a, cfg := setupTestAgentWithPipeline(t, config.ModeCloud)
	ctx := context.Background()

	cloudSink := a.VideoManager().ExtraSinks()[0]

	// Create custom adapter that simulates a post-swap health check failure when switching to edge
	failHealthCheck := true
	visionStopped := false
	adapter := remoteconfig.NewRuntimeAdapter(
		config.ModeCloud,
		a.VideoManager(),
		a.RTSPManager(),
		a.ModelManager(),
		nil,
		remoteconfig.WithStartTimeout(2*time.Second),
		remoteconfig.WithCloudSinkFactory(func() processing.Sink {
			return cloudSink
		}),
		remoteconfig.WithVisionSinkFactory(func() (processing.Sink, func(ctx context.Context) error, func(ctx context.Context) error) {
			vs, mod := buildVisionSink(cfg, a.Health(), a.FullEdgeConsumer(), a.ModelManager(), nil)
			stopFn := func(ctx context.Context) error {
				visionStopped = true
				if mod != nil {
					return mod.Stop(ctx)
				}
				return nil
			}
			return vs, mod.Start, stopFn
		}),
		remoteconfig.WithHealthCheck(func(ctx context.Context) error {
			if failHealthCheck {
				return errors.New("simulated health check failure post-swap")
			}
			return nil
		}),
	)

	edgeMode := config.ModeEdge
	err := adapter.Apply(ctx, remoteconfig.RuntimeConfig{ProcessingMode: &edgeMode})
	if err == nil {
		t.Fatal("expected Apply to fail due to post-apply health check failure")
	}

	// In single authority architecture, Engine (or caller) triggers rollback upon apply failure:
	if err := adapter.Rollback(ctx); err != nil {
		t.Fatalf("Rollback failed: %v", err)
	}

	// Router must have been rolled back to cloud sink
	sinks := a.VideoManager().ExtraSinks()
	if len(sinks) != 1 || sinks[0].Name() != "cloud" {
		names := make([]string, len(sinks))
		for i, s := range sinks {
			names[i] = s.Name()
		}
		t.Fatalf("expected router to be rolled back to cloud sink, got: %+v (names: %v)", sinks, names)
	}

	// Effective mode on adapter rolled back to cloud
	cur := adapter.CurrentConfig()
	if cur.ProcessingMode == nil || *cur.ProcessingMode != config.ModeCloud {
		t.Fatalf("adapter.CurrentConfig().ProcessingMode = %v, want 'cloud'", cur.ProcessingMode)
	}

	// Vision worker was stopped during rollback
	if !visionStopped {
		t.Fatal("expected vision worker to be stopped during rollback")
	}
}

// CASO 1 (OK): Desired v1 -> control reload_config -> SyncOnce gets v1 -> RuntimeAdapter applies runtime -> ACK applied -> store marks applied v1 -> control reports succeeded.
func TestIntegration_HitoO_Case1_OK(t *testing.T) {
	var (
		serverHits  int
		ackedVer    int64
		ackedStatus string
	)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serverHits++
		switch r.URL.Path {
		case "/api/v1/edge/remote-config/next":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"config": map[string]any{
					"version": 1,
					"payload": map[string]any{
						"processing_mode": "edge",
						"target_fps":      15,
					},
				},
			})
		case "/api/v1/edge/remote-config/ack":
			var req struct {
				Version int64  `json:"version"`
				Status  string `json:"status"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			ackedVer = req.Version
			ackedStatus = req.Status
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer ts.Close()

	a, cfg := setupTestAgentWithPipeline(t, config.ModeCloud)
	cfg.SaaSURL = ts.URL
	cfg.AllowInsecureHTTP = true

	// Re-instantiate agent with test server URL
	a = New(cfg)
	if a.Control() == nil {
		t.Fatal("expected non-nil Control module")
	}
	if a.RemoteConfig() == nil {
		t.Fatal("expected non-nil RemoteConfig module")
	}

	ctx := context.Background()

	// Initial state is cloud
	if got := a.Health().ProcessingMode(); got != "cloud" {
		t.Fatalf("Health().ProcessingMode() = %q, want 'cloud'", got)
	}

	// Trigger reload_config via Control module
	cmd := &transport.ControlCommand{
		ID:          "cmd-reload-1",
		CommandType: "reload_config",
		Payload:     map[string]any{},
	}
	status, _, code, err := a.Control().ExecuteCommand(ctx, cmd)
	if err != nil {
		t.Fatalf("ExecuteCommand error: %v", err)
	}
	if status != "succeeded" {
		t.Fatalf("ExecuteCommand status = %q (code=%q), want 'succeeded'", status, code)
	}

	// Verify runtime applied
	if got := a.Health().ProcessingMode(); got != "edge" {
		t.Fatalf("Health().ProcessingMode() = %q, want 'edge'", got)
	}
	if a.FullEdgeService() == nil {
		t.Fatal("expected non-nil FullEdgeService in edge mode")
	}

	// Verify ACK sent to SaaS
	if ackedVer != 1 || ackedStatus != "applied" {
		t.Fatalf("ACK = (ver: %d, status: %q), want (1, 'applied')", ackedVer, ackedStatus)
	}

	// Verify local store
	rcStatus := a.RemoteConfig().Status()
	if rcStatus.AppliedVersion != 1 {
		t.Fatalf("store AppliedVersion = %d, want 1", rcStatus.AppliedVersion)
	}
	if rcStatus.LastApplyStatus != "applied" {
		t.Fatalf("store LastApplyStatus = %q, want 'applied'", rcStatus.LastApplyStatus)
	}
}

// CASO 2 (FAIL / ROLLBACK): Desired v2 invalid/fails -> Engine rolls back runtime -> applied_version remains v1 -> ACK rolled_back -> store marks v2 rolled_back -> control report completes.
func TestIntegration_HitoO_Case2_FailRollback(t *testing.T) {
	var (
		serverVersion int64 = 1
		ackedVer      int64
		ackedStatus   string
		ackedCode     string
	)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/edge/remote-config/next":
			w.Header().Set("Content-Type", "application/json")
			if serverVersion == 1 {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"config": map[string]any{
						"version": 1,
						"payload": map[string]any{
							"processing_mode": "cloud",
							"target_fps":      10,
						},
					},
				})
			} else {
				// v2: target mode cloud without operational cloud sink factory -> will fail apply
				_ = json.NewEncoder(w).Encode(map[string]any{
					"config": map[string]any{
						"version": 2,
						"payload": map[string]any{
							"target_fps": 9999, // invalid FPS fails validation
						},
					},
				})
			}
		case "/api/v1/edge/remote-config/ack":
			var req struct {
				Version   int64  `json:"version"`
				Status    string `json:"status"`
				ErrorCode string `json:"error_code"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			ackedVer = req.Version
			ackedStatus = req.Status
			ackedCode = req.ErrorCode
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer ts.Close()

	a, cfg := setupTestAgentWithPipeline(t, config.ModeCloud)
	cfg.SaaSURL = ts.URL
	cfg.AllowInsecureHTTP = true

	a = New(cfg)
	ctx := context.Background()

	// Step 1: Apply v1 successfully
	cmd1 := &transport.ControlCommand{
		ID:          "cmd-reload-v1",
		CommandType: "reload_config",
		Payload:     map[string]any{},
	}
	status1, _, _, err := a.Control().ExecuteCommand(ctx, cmd1)
	if err != nil || status1 != "succeeded" {
		t.Fatalf("v1 execute error: %v, status: %s", err, status1)
	}
	if a.RemoteConfig().Status().AppliedVersion != 1 {
		t.Fatalf("v1 not applied: %+v", a.RemoteConfig().Status())
	}

	// Step 2: Now SaaS serves invalid v2 (validation failure: target_fps=9999)
	serverVersion = 2
	cmd2 := &transport.ControlCommand{
		ID:          "cmd-reload-v2",
		CommandType: "reload_config",
		Payload:     map[string]any{},
	}
	status2, _, code2, err := a.Control().ExecuteCommand(ctx, cmd2)
	if err != nil {
		t.Fatalf("v2 execute error: %v", err)
	}
	// ReloadConfig succeeds in the control plane even if apply failed (command was handled and ACKed)
	if status2 != "succeeded" {
		t.Fatalf("ExecuteCommand status = %q (code=%q), want 'succeeded'", status2, code2)
	}

	// Verify ACK was sent for v2 with failure
	if ackedVer != 2 {
		t.Fatalf("ackedVer = %d, want 2", ackedVer)
	}
	if ackedStatus != "failed" {
		t.Fatalf("ackedStatus = %q, want 'failed'", ackedStatus)
	}
	if ackedCode != "VALIDATION_FAILED" {
		t.Fatalf("ackedCode = %q, want 'VALIDATION_FAILED'", ackedCode)
	}

	// Applied version remains 1, LastApplyStatus is failed
	st := a.RemoteConfig().Status()
	if st.AppliedVersion != 1 {
		t.Fatalf("expected AppliedVersion to remain 1, got: %d", st.AppliedVersion)
	}
	if st.LastApplyStatus != "failed" {
		t.Fatalf("expected LastApplyStatus to be 'failed', got: %q", st.LastApplyStatus)
	}
}
