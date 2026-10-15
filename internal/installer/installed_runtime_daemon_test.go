package installer

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/drko-dev/monitoreoedgeis/internal/config"
)

func TestConfigureDaemonForInstalledRuntime(t *testing.T) {
	dataDir, bundled := t.TempDir(), t.TempDir()
	t.Setenv(config.ConfigFileEnv, filepath.Join(t.TempDir(), "edge.env"))
	root := filepath.Join(dataDir, DefaultRuntimePackTargetName)
	if err := ConfigureDaemonForInstalledRuntime(dataDir, bundled); err == nil {
		t.Fatal("missing installed runtime accepted")
	}
	for _, rel := range []string{"python/bin/python3.11", "worker/worker.py", "ffmpeg/ffmpeg"} {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(bundled, "yolo11n.pt"), []byte("weights"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundled, "README.txt"), []byte("no"), 0o644); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ { // idempotent
		if err := ConfigureDaemonForInstalledRuntime(dataDir, bundled); err != nil {
			t.Fatal(err)
		}
	}
	want := map[string]string{
		"GEOCAM_EDGE_YOLO_WORKER_CMD":  filepath.Join(root, "python", "bin", "python3.11"),
		"GEOCAM_EDGE_YOLO_WORKER_ARGS": `-B "` + filepath.Join(root, "worker", "worker.py") + `"`,
		"GEOCAM_EDGE_YOLO_MODELS_DIR":  filepath.Join(dataDir, "models"),
		"GEOCAM_VIDEO_FFMPEG_PATH":     filepath.Join(root, "ffmpeg", "ffmpeg"),
	}
	for key, value := range want {
		got, ok, err := config.PersistentFileValue(key)
		if err != nil || !ok || got != value {
			t.Errorf("%s = %q (%v, %v), want %q", key, got, ok, err, value)
		}
	}
	if b, err := os.ReadFile(filepath.Join(dataDir, "models", "yolo11n.pt")); err != nil || string(b) != "weights" {
		t.Fatalf("model not copied: %q %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "models", "README.txt")); !os.IsNotExist(err) {
		t.Fatal("non-weight file copied")
	}
}
