package agent

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/config"
	"github.com/drko-dev/monitoreoedgeis/internal/health"
	"github.com/drko-dev/monitoreoedgeis/internal/identity"
	"github.com/drko-dev/monitoreoedgeis/internal/platform"
)

// TestInstalledRuntimeWorkerStarts loads the persisted config written by the
// installer after a one-click preparation and starts the worker through the
// same buildVisionSink path the daemon uses. Opt-in: H5_DAEMON_CONFIG_FILE.
func TestInstalledRuntimeWorkerStarts(t *testing.T) {
	cfgFile := os.Getenv("H5_DAEMON_CONFIG_FILE")
	if cfgFile == "" {
		t.Skip("H5_DAEMON_CONFIG_FILE not set")
	}
	t.Setenv(config.ConfigFileEnv, cfgFile)
	t.Setenv("GEOCAM_DATA_DIR", t.TempDir())
	t.Setenv("GEOCAM_EDGE_YOLO_DEVICE", "mps")
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.EdgeYOLOSocketPath = t.TempDir() + "/v.sock"
	reporter := health.New("test", cfg, identity.Identity{}, platform.Info{})
	_, module := buildVisionSink(cfg, reporter, nil, nil, slog.Default())
	worker := module.(*visionModule).worker
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := worker.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer worker.Stop(context.Background())
	if err := worker.WaitForReady(ctx); err != nil {
		t.Fatalf("worker from installed runtime not ready (cmd=%s): %v", cfg.EdgeYOLOWorkerCmd, err)
	}
	t.Logf("worker READY: cmd=%s models=%s", cfg.EdgeYOLOWorkerCmd, cfg.EdgeYOLOModelsDir)
}
