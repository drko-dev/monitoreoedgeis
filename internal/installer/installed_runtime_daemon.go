package installer

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/config"
)

// ConfigureDaemonForInstalledRuntime points the daemon's existing worker and
// FFmpeg settings at the committed Runtime Pack under dataDir, and copies the
// bundled model weights (if any) to a persistent models directory so the app
// bundle can be moved or deleted. It is idempotent. The daemon picks the new
// values up on its next (re)start.
func ConfigureDaemonForInstalledRuntime(dataDir, bundledModelsDir string) error {
	root := filepath.Join(dataDir, DefaultRuntimePackTargetName)
	python := filepath.Join(root, "python", "bin", "python3.11")
	worker := filepath.Join(root, "worker", "worker.py")
	ffmpeg := filepath.Join(root, "ffmpeg", "ffmpeg")
	for _, path := range []string{python, worker, ffmpeg} {
		if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("installed runtime incomplete: %s", path)
		}
	}
	// macOS scans freshly installed dylibs on first load (~30s for torch),
	// longer than the daemon's worker start timeout. Pay it once here; this
	// also proves the installed interpreter imports the inference stack.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	warm := exec.CommandContext(ctx, python, "-I", "-B", "-c", "import torch, torchvision, cv2, ultralytics")
	warm.Env = []string{"HOME=" + dataDir, "YOLO_CONFIG_DIR=" + dataDir, "PATH=/usr/bin:/bin"}
	if out, err := warm.CombinedOutput(); err != nil {
		return fmt.Errorf("installed runtime warm-up failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	models := filepath.Join(dataDir, "models")
	if err := copyModelWeights(bundledModelsDir, models); err != nil {
		return err
	}
	return config.WritePersistentValues(map[string]string{
		"GEOCAM_EDGE_YOLO_WORKER_CMD":  python,
		"GEOCAM_EDGE_YOLO_WORKER_ARGS": worker,
		"GEOCAM_EDGE_YOLO_MODELS_DIR":  models,
		"GEOCAM_VIDEO_FFMPEG_PATH":     ffmpeg,
	})
}

// copyModelWeights copies top-level *.pt files that are not already present.
func copyModelWeights(src, dst string) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	if src == "" {
		return nil
	}
	entries, err := os.ReadDir(src)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !e.Type().IsRegular() || !strings.HasSuffix(strings.ToLower(e.Name()), ".pt") {
			continue
		}
		target := filepath.Join(dst, e.Name())
		if _, err := os.Stat(target); err == nil {
			continue
		}
		if err := copyFileAtomic(filepath.Join(src, e.Name()), target); err != nil {
			return err
		}
	}
	return nil
}

func copyFileAtomic(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".model-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dst)
}
