package vision

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// newTestModels creates a ModelManager rooted at a temp dir containing two
// empty files named like the real weights — enough for ModelManager.Ready(),
// since K3's Status/Ready check presence, not that Ultralytics can load
// them (the fake worker never actually loads a model).
func newTestModels(t *testing.T) *ModelManager {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"yolo11s-pose.pt", "yolo11n.pt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("fake"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return NewModelManager(dir, "yolo11s-pose.pt", "yolo11n.pt")
}

// baseTestConfig returns a Config that re-execs this test binary as the fake
// worker (see fakeworker_test.go), with extraEnv layered on top of the
// current process's environment.
func baseTestConfig(t *testing.T, extraEnv ...string) Config {
	t.Helper()
	socket := filepath.Join(t.TempDir(), "vision.sock")
	return Config{
		WorkerCmd:         os.Args[0],
		WorkerArgs:        []string{"-test.run=^TestMain$"},
		Device:            "cpu",
		ImgSize:           640,
		PersonConfidence:  0.5,
		VehicleConfidence: 0.5,
		NMSIoU:            0.45,
		SocketPath:        socket,
		StartTimeout:      5 * time.Second,
		InferTimeout:      2 * time.Second,
	}
}

func startWorkerWithEnv(t *testing.T, cfg Config, env ...string) (*Worker, *ModelManager) {
	t.Helper()
	os.Setenv("GEOCAM_VISION_FAKE_WORKER", "1")
	for i := 0; i+1 < len(env); i += 2 {
		os.Setenv(env[i], env[i+1])
	}
	t.Cleanup(func() {
		os.Unsetenv("GEOCAM_VISION_FAKE_WORKER")
		for i := 0; i+1 < len(env); i += 2 {
			os.Unsetenv(env[i])
		}
	})
	models := newTestModels(t)
	w := NewWorker(cfg, models, nil)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := w.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return w, models
}

func waitForState(t *testing.T, w *Worker, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if w.Status().State == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("worker never reached state %q, last=%q", want, w.Status().State)
}

// TestWorker_NotConfigured covers K3's "worker not configured" path: no
// WorkerCmd means the worker never spawns anything and reports a clear
// terminal state, not a crash loop.
func TestWorker_NotConfigured(t *testing.T) {
	models := newTestModels(t)
	w := NewWorker(Config{}, models, nil)
	if err := w.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := w.Status().State; got != StateNotConfigured {
		t.Fatalf("state = %q, want %q", got, StateNotConfigured)
	}
	if w.Ready() {
		t.Fatal("Ready() = true for an unconfigured worker")
	}
}

// TestWorker_ModelMissing covers K3's explicit NOT_READY/model_missing
// contract: a configured worker with no weight files on disk must never
// silently download anything, and must report model_missing.
func TestWorker_ModelMissing(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "vision.sock")
	cfg := Config{
		WorkerCmd:    os.Args[0],
		StartTimeout: 2 * time.Second,
		InferTimeout: 2 * time.Second,
		SocketPath:   socket,
	}
	models := NewModelManager(t.TempDir(), "yolo11s-pose.pt", "yolo11n.pt") // empty dir
	w := NewWorker(cfg, models, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := w.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForState(t, w, StateModelMissing, 2*time.Second)
	if models.Ready() {
		t.Fatal("ModelManager.Ready() = true with no files on disk")
	}
}

// TestWorker_StartAndInfer covers K2's start/health/inference-request path
// end to end via the fake worker.
func TestWorker_StartAndInfer(t *testing.T) {
	cfg := baseTestConfig(t)
	w, _ := startWorkerWithEnv(t, cfg)
	waitForState(t, w, StateReady, 5*time.Second)

	result, err := w.Infer(context.Background(), InferRequest{CandidateKey: "cam-1", FrameSeq: 42, Timestamp: time.Now()})
	if err != nil {
		t.Fatalf("Infer: %v", err)
	}
	if len(result.Detections) != 1 || result.Detections[0].Type != DetectionTypePerson {
		t.Fatalf("unexpected detections: %+v", result.Detections)
	}
	if w.Status().InferenceCount != 1 {
		t.Fatalf("InferenceCount = %d, want 1", w.Status().InferenceCount)
	}
}

// TestWorker_HealthRejected covers a worker process that starts but reports
// itself not ready (e.g. a real model failed to load) — must never be
// treated as Ready.
func TestWorker_HealthRejected(t *testing.T) {
	cfg := baseTestConfig(t)
	w, _ := startWorkerWithEnv(t, cfg, "GEOCAM_VISION_FAKE_REFUSE_HEALTH", "1")
	waitForState(t, w, StateError, 5*time.Second)
	if w.Ready() {
		t.Fatal("Ready() = true after a rejected health handshake")
	}
}

// TestWorker_InferTimeout covers K4's per-request timeout: a worker that
// hangs on a request must fail that call, not the whole supervisor.
func TestWorker_InferTimeout(t *testing.T) {
	cfg := baseTestConfig(t)
	cfg.InferTimeout = 200 * time.Millisecond
	w, _ := startWorkerWithEnv(t, cfg, "GEOCAM_VISION_FAKE_INFER_DELAY_MS", "2000")
	waitForState(t, w, StateReady, 5*time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.InferTimeout)
	defer cancel()
	_, err := w.Infer(ctx, InferRequest{CandidateKey: "cam-1", FrameSeq: 1, Timestamp: time.Now()})
	if err == nil {
		t.Fatal("Infer succeeded despite a hung worker")
	}
	if w.Status().InferenceErrors != 1 {
		t.Fatalf("InferenceErrors = %d, want 1", w.Status().InferenceErrors)
	}
}

// TestWorker_RestartsOnCrash covers K2's restart-with-backoff: a worker
// process that dies mid-request must be respawned, not left dead.
func TestWorker_RestartsOnCrash(t *testing.T) {
	cfg := baseTestConfig(t)
	w, _ := startWorkerWithEnv(t, cfg, "GEOCAM_VISION_FAKE_CRASH_ON_INFER", "1")
	waitForState(t, w, StateReady, 5*time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, _ = w.Infer(ctx, InferRequest{CandidateKey: "cam-1", FrameSeq: 1, Timestamp: time.Now()}) // triggers the crash

	waitForState(t, w, StateRestarting, 3*time.Second)
	if w.Status().Restarts < 1 {
		t.Fatalf("Restarts = %d, want >= 1", w.Status().Restarts)
	}
}

// TestWorker_GracefulShutdown covers K2's bounded shutdown: Stop must
// return promptly and leave the worker Stopped, never hang forever.
func TestWorker_GracefulShutdown(t *testing.T) {
	cfg := baseTestConfig(t)
	w, _ := startWorkerWithEnv(t, cfg)
	waitForState(t, w, StateReady, 5*time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := w.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if got := w.Status().State; got != StateStopped {
		t.Fatalf("state after Stop = %q, want %q", got, StateStopped)
	}
}
