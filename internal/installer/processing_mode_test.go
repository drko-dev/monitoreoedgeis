package installer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/drko-dev/monitoreoedgeis/internal/config"
)

// newIsolatedService returns a Service whose persistent config file is a
// fresh, isolated temp path (never the real operator config) and whose
// HealthAddr points nowhere, so checkDaemonRunning is false by default.
func newIsolatedService(t *testing.T) *Service {
	t.Helper()
	dataDir := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "edge.env")
	// PersistentFileValue/readPersistentEnvironment require an *explicit*
	// GEOCAM_CONFIG_FILE path to already exist (an explicit override missing
	// its target is treated as an operator error, unlike the default,
	// auto-derived path). Pre-create it empty so tests start from "nothing
	// configured yet", the same state a fresh install's default path is in.
	if err := os.WriteFile(configPath, nil, 0o600); err != nil {
		t.Fatalf("seed empty config file: %v", err)
	}
	t.Setenv(config.ConfigFileEnv, configPath)
	svc := NewService(dataDir, "")
	svc.HealthAddr = "127.0.0.1:1" // reserved, always refused: daemon "not running"
	return svc
}

func persistedMode(t *testing.T) string {
	t.Helper()
	mode, _, err := config.PersistentFileValue("GEOCAM_PROCESSING_MODE")
	if err != nil {
		t.Fatalf("PersistentFileValue: %v", err)
	}
	return mode
}

func TestProcessingModeMapping(t *testing.T) {
	cases := []struct {
		product ProcessingMode
		want    config.ProcessingMode
	}{
		{ProcessingModeCloud, config.ModeCloud},
		{ProcessingModeHybrid, config.ModeHybrid},
		{ProcessingModeFullEdge, config.ModeEdge},
	}
	for _, c := range cases {
		got, err := c.product.configMode()
		if err != nil || got != c.want {
			t.Errorf("%s.configMode() = %q, %v, want %q", c.product, got, err, c.want)
		}
	}
}

func TestGetProcessingModeOptionsExposesExactlyThreeNoGateway(t *testing.T) {
	svc := newIsolatedService(t)
	options, err := svc.GetProcessingModeOptions(context.Background())
	if err != nil {
		t.Fatalf("GetProcessingModeOptions: %v", err)
	}
	if len(options) != 3 {
		t.Fatalf("got %d options, want 3", len(options))
	}
	seen := map[ProcessingMode]bool{}
	for _, opt := range options {
		seen[opt.Mode] = true
		if string(opt.Mode) == "gateway" {
			t.Fatal("gateway exposed as a selectable processing mode")
		}
	}
	for _, want := range []ProcessingMode{ProcessingModeCloud, ProcessingModeHybrid, ProcessingModeFullEdge} {
		if !seen[want] {
			t.Errorf("missing option %s", want)
		}
	}
}

func TestFullEdgeUnavailableWithoutVisionRuntime(t *testing.T) {
	svc := newIsolatedService(t)
	options, err := svc.GetProcessingModeOptions(context.Background())
	if err != nil {
		t.Fatalf("GetProcessingModeOptions: %v", err)
	}
	for _, opt := range options {
		if opt.Mode == ProcessingModeFullEdge {
			if opt.Capability != CapabilityUnavailable {
				t.Errorf("Full Edge capability = %s, want UNAVAILABLE (no worker configured)", opt.Capability)
			}
			if len(opt.Blockers) == 0 {
				t.Error("Full Edge UNAVAILABLE without a blocker reason")
			}
			return
		}
	}
	t.Fatal("full_edge option not found")
}

func TestFullEdgeSupportedWithConfiguredWorkerAndModels(t *testing.T) {
	svc := newIsolatedService(t)
	modelsDir := t.TempDir()
	for _, name := range []string{config.DefaultEdgeYOLOPersonModel, config.DefaultEdgeYOLOVehicleModel} {
		if err := os.WriteFile(filepath.Join(modelsDir, name), []byte("fake"), 0o600); err != nil {
			t.Fatalf("write fake model: %v", err)
		}
	}
	if err := config.WritePersistentValues(map[string]string{
		"GEOCAM_EDGE_YOLO_WORKER_CMD": "echo", // resolvable via PATH on every CI/dev host
		"GEOCAM_EDGE_YOLO_MODELS_DIR": modelsDir,
	}); err != nil {
		t.Fatalf("WritePersistentValues: %v", err)
	}

	options, err := svc.GetProcessingModeOptions(context.Background())
	if err != nil {
		t.Fatalf("GetProcessingModeOptions: %v", err)
	}
	for _, opt := range options {
		if opt.Mode == ProcessingModeFullEdge {
			if opt.Capability != CapabilitySupported {
				t.Errorf("Full Edge capability = %s (%v), want SUPPORTED", opt.Capability, opt.Blockers)
			}
			return
		}
	}
	t.Fatal("full_edge option not found")
}

func TestFullEdgeWarnsWhenCudaRequestedButUnavailable(t *testing.T) {
	svc := newIsolatedService(t)
	modelsDir := t.TempDir()
	for _, name := range []string{config.DefaultEdgeYOLOPersonModel, config.DefaultEdgeYOLOVehicleModel} {
		if err := os.WriteFile(filepath.Join(modelsDir, name), []byte("fake"), 0o600); err != nil {
			t.Fatalf("write fake model: %v", err)
		}
	}
	if err := config.WritePersistentValues(map[string]string{
		"GEOCAM_EDGE_YOLO_WORKER_CMD": "echo",
		"GEOCAM_EDGE_YOLO_MODELS_DIR": modelsDir,
		"GEOCAM_EDGE_YOLO_DEVICE":     "cuda",
	}); err != nil {
		t.Fatalf("WritePersistentValues: %v", err)
	}

	options, err := svc.GetProcessingModeOptions(context.Background())
	if err != nil {
		t.Fatalf("GetProcessingModeOptions: %v", err)
	}
	for _, opt := range options {
		if opt.Mode == ProcessingModeFullEdge {
			// This host is not asserted to have CUDA either way; the only
			// contract we can portably assert is "never UNAVAILABLE solely
			// for requesting cuda", matching "no bloquear por falta de GPU".
			if opt.Capability == CapabilityUnavailable {
				t.Errorf("Full Edge UNAVAILABLE just because cuda was requested: %v", opt.Blockers)
			}
			return
		}
	}
	t.Fatal("full_edge option not found")
}

func TestPlanProcessingModeDoesNotMutate(t *testing.T) {
	svc := newIsolatedService(t)
	before, existedBefore, err := config.PersistentFileRaw()
	if err != nil {
		t.Fatalf("PersistentFileRaw: %v", err)
	}

	if _, err := svc.PlanProcessingMode(context.Background(), ProcessingModeRequest{Mode: ProcessingModeHybrid}); err != nil {
		t.Fatalf("PlanProcessingMode: %v", err)
	}

	after, existedAfter, err := config.PersistentFileRaw()
	if err != nil {
		t.Fatalf("PersistentFileRaw: %v", err)
	}
	if existedBefore != existedAfter || string(before) != string(after) {
		t.Fatal("PlanProcessingMode mutated the persistent config file")
	}
}

func TestApplyInvalidModeRejectedWithoutWriting(t *testing.T) {
	svc := newIsolatedService(t)
	before, _, _ := config.PersistentFileRaw()
	_, err := svc.ApplyProcessingMode(context.Background(), ProcessingModeRequest{Mode: "bogus"})
	if err == nil {
		t.Fatal("ApplyProcessingMode accepted an invalid mode")
	}
	after, _, _ := config.PersistentFileRaw()
	if string(before) != string(after) {
		t.Fatal("ApplyProcessingMode changed the config file for an invalid mode")
	}
}

func TestApplyFullEdgeBlockedWithoutWriting(t *testing.T) {
	svc := newIsolatedService(t)
	before, _, _ := config.PersistentFileRaw()
	result, err := svc.ApplyProcessingMode(context.Background(), ProcessingModeRequest{Mode: ProcessingModeFullEdge})
	if err != nil {
		t.Fatalf("ApplyProcessingMode: %v", err)
	}
	if result.Status != ApplyStatusBlocked {
		t.Fatalf("Status = %s, want BLOCKED", result.Status)
	}
	after, _, _ := config.PersistentFileRaw()
	if string(before) != string(after) {
		t.Fatal("ApplyProcessingMode changed the config file for a BLOCKED mode")
	}
}

func TestApplyCloudHybridFullEdgeCloudSequenceAlwaysEnablesPipeline(t *testing.T) {
	svc := newIsolatedService(t)
	modelsDir := t.TempDir()
	for _, name := range []string{config.DefaultEdgeYOLOPersonModel, config.DefaultEdgeYOLOVehicleModel} {
		if err := os.WriteFile(filepath.Join(modelsDir, name), []byte("fake"), 0o600); err != nil {
			t.Fatalf("write fake model: %v", err)
		}
	}
	if err := config.WritePersistentValues(map[string]string{
		"GEOCAM_EDGE_YOLO_WORKER_CMD": "echo",
		"GEOCAM_EDGE_YOLO_MODELS_DIR": modelsDir,
	}); err != nil {
		t.Fatalf("WritePersistentValues: %v", err)
	}

	sequence := []ProcessingMode{ProcessingModeCloud, ProcessingModeHybrid, ProcessingModeFullEdge, ProcessingModeCloud}
	for _, mode := range sequence {
		result, err := svc.ApplyProcessingMode(context.Background(), ProcessingModeRequest{Mode: mode})
		if err != nil {
			t.Fatalf("ApplyProcessingMode(%s): %v", mode, err)
		}
		if result.Status != ApplyStatusSuccess {
			t.Fatalf("ApplyProcessingMode(%s) status = %s, want SUCCESS (%v)", mode, result.Status, result.Warnings)
		}
		if !result.Match {
			t.Fatalf("ApplyProcessingMode(%s): Match = false", mode)
		}
		wantConfigMode, _ := mode.configMode()
		if got := persistedMode(t); got != string(wantConfigMode) {
			t.Fatalf("persisted mode = %q, want %q", got, wantConfigMode)
		}
		pipeline, _, _ := config.PersistentFileValue("GEOCAM_VIDEO_PIPELINE_ENABLED")
		if pipeline != "true" {
			t.Fatalf("ApplyProcessingMode(%s): pipeline = %q, want true", mode, pipeline)
		}
		if result.ExpectedEffectiveProfile != result.ActualEffectiveProfile {
			t.Fatalf("ApplyProcessingMode(%s): expected profile %s != actual %s", mode, result.ExpectedEffectiveProfile, result.ActualEffectiveProfile)
		}
	}
}

func TestApplyWriteFailureLeavesOldConfigIntact(t *testing.T) {
	svc := newIsolatedService(t)
	isolatedPath := os.Getenv(config.ConfigFileEnv)

	// First, a real successful apply so there is an "old config" to protect.
	if _, err := svc.ApplyProcessingMode(context.Background(), ProcessingModeRequest{Mode: ProcessingModeCloud}); err != nil {
		t.Fatalf("seed ApplyProcessingMode: %v", err)
	}
	before, existedBefore, err := config.PersistentFileRaw()
	if err != nil || !existedBefore {
		t.Fatalf("PersistentFileRaw after seed: %v, existed=%v", err, existedBefore)
	}

	// Break the write path: point GEOCAM_CONFIG_FILE at a location whose
	// parent cannot be created (a file where a directory is expected).
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	t.Setenv(config.ConfigFileEnv, filepath.Join(blocker, "edge.env"))

	_, err = svc.ApplyProcessingMode(context.Background(), ProcessingModeRequest{Mode: ProcessingModeHybrid})
	if err == nil {
		t.Fatal("ApplyProcessingMode succeeded despite an unwritable config path")
	}

	// Point back at the original isolated file and confirm the earlier
	// successful write is byte-for-byte untouched: the failed write's
	// temp-file-then-rename never got far enough to mutate anything.
	t.Setenv(config.ConfigFileEnv, isolatedPath)
	after, existedAfter, err := config.PersistentFileRaw()
	if err != nil || !existedAfter || string(after) != string(before) {
		t.Fatalf("old config changed after a failed write: before=%q after=%q existedAfter=%v err=%v", before, after, existedAfter, err)
	}
}

func TestApplyRejectsConcurrentApply(t *testing.T) {
	svc := newIsolatedService(t)
	if !svc.applyMu.TryLock() {
		t.Fatal("could not acquire applyMu for setup")
	}
	defer svc.applyMu.Unlock()

	_, err := svc.ApplyProcessingMode(context.Background(), ProcessingModeRequest{Mode: ProcessingModeCloud})
	if err == nil {
		t.Fatal("ApplyProcessingMode ran while another apply was in progress")
	}
	safeErr, ok := err.(*SafeError)
	if !ok || safeErr.Code != "APPLY_IN_PROGRESS" {
		t.Fatalf("err = %v, want APPLY_IN_PROGRESS SafeError", err)
	}
}

func TestGetCurrentProcessingModeReadsConfigWhenDaemonDown(t *testing.T) {
	svc := newIsolatedService(t)
	if _, err := svc.ApplyProcessingMode(context.Background(), ProcessingModeRequest{Mode: ProcessingModeHybrid}); err != nil {
		t.Fatalf("ApplyProcessingMode: %v", err)
	}

	current, err := svc.GetCurrentProcessingMode(context.Background())
	if err != nil {
		t.Fatalf("GetCurrentProcessingMode: %v", err)
	}
	if current.Mode != ProcessingModeHybrid || current.Source != "config" {
		t.Fatalf("current = %+v, want mode=hybrid source=config", current)
	}
}

func TestGetCurrentProcessingModeDefaultsWhenNothingConfigured(t *testing.T) {
	svc := newIsolatedService(t)
	current, err := svc.GetCurrentProcessingMode(context.Background())
	if err != nil {
		t.Fatalf("GetCurrentProcessingMode: %v", err)
	}
	if current.Source != "default" || current.Mode != ProcessingModeCloud || current.PipelineEnabled {
		t.Fatalf("current = %+v, want default cloud with pipeline disabled", current)
	}
	if current.EffectiveProfile != config.ProfileGatewayNoMedia {
		t.Fatalf("EffectiveProfile = %s, want gateway-no-media", current.EffectiveProfile)
	}
}

// fakeDaemon serves the minimal /healthz and /status a running Edge would.
func fakeDaemon(t *testing.T, mode config.ProcessingMode, profile config.Profile) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/status", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(statusSnapshot{ProcessingMode: string(mode), Profile: profile})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestGetCurrentProcessingModeReadsLiveStatusWhenDaemonRunning(t *testing.T) {
	svc := newIsolatedService(t)
	srv := fakeDaemon(t, config.ModeEdge, config.ProfileFullEdge)
	svc.HealthAddr = srv.Listener.Addr().String()

	current, err := svc.GetCurrentProcessingMode(context.Background())
	if err != nil {
		t.Fatalf("GetCurrentProcessingMode: %v", err)
	}
	if current.Mode != ProcessingModeFullEdge || current.Source != "runtime" || current.EffectiveProfile != config.ProfileFullEdge {
		t.Fatalf("current = %+v, want live full_edge/full-edge", current)
	}
}

func TestApplyRestartRequiredWhenDaemonAlreadyRunningOldMode(t *testing.T) {
	svc := newIsolatedService(t)
	srv := fakeDaemon(t, config.ModeCloud, config.ProfileGateway)
	svc.HealthAddr = srv.Listener.Addr().String()

	result, err := svc.ApplyProcessingMode(context.Background(), ProcessingModeRequest{Mode: ProcessingModeHybrid})
	if err != nil {
		t.Fatalf("ApplyProcessingMode: %v", err)
	}
	if result.Status != ApplyStatusRestartRequired || result.Match || !result.RestartRequired {
		t.Fatalf("result = %+v, want RESTART_REQUIRED with Match=false", result)
	}
	// The new config must still be persisted for the next start even though
	// the live daemon has not picked it up yet.
	if got := persistedMode(t); got != string(config.ModeHybrid) {
		t.Fatalf("persisted mode = %q, want hybrid (config must survive even though restart is pending)", got)
	}
}

func TestSystemReportNeverOwnsInstanceLock(t *testing.T) {
	svc := newIsolatedService(t)
	report, err := svc.GetSystemReport(context.Background())
	if err != nil {
		t.Fatalf("GetSystemReport: %v", err)
	}
	if report.OwnsInstanceLock {
		t.Fatal("installer Service reports owning the exclusive instance lock")
	}
}
