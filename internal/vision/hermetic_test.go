package vision

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func syntheticHermeticWorker(t *testing.T) (*Worker, string) {
	t.Helper()
	root := t.TempDir()
	for _, name := range []string{"python/bin/python3", "worker/worker.py", "media/ffmpeg", "media/ffprobe"} {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		mode := os.FileMode(0o644)
		if strings.Contains(name, "python") || strings.Contains(name, "ffmpeg") || strings.Contains(name, "ffprobe") {
			mode = 0o755
		}
		if err := os.WriteFile(path, []byte("synthetic-runtime-artifact"), mode); err != nil {
			t.Fatal(err)
		}
	}
	models := newTestModels(t)
	stateDir := t.TempDir()
	python := filepath.Join(root, "python/bin/python3")
	workerPath := filepath.Join(root, "worker/worker.py")
	ffmpeg := filepath.Join(root, "media/ffmpeg")
	ffprobe := filepath.Join(root, "media/ffprobe")
	w := NewWorker(Config{
		WorkerCmd:           python,
		WorkerArgs:          []string{"-I", "-B", workerPath},
		Hermetic:            true,
		RuntimePackRoot:     root,
		ProcessEnv:          []string{"PATH=" + filepath.Dir(ffmpeg), "HOME=" + stateDir, "TMPDIR=" + stateDir, "GEOCAM_VIDEO_FFMPEG_PATH=" + ffmpeg, "GEOCAM_VIDEO_FFPROBE_PATH=" + ffprobe, "GEOCAM_EDGE_YOLO_MODELS_DIR=" + models.dir},
		ModelPackAuthorized: true,
		ModelsDir:           models.dir,
		PersonModel:         "yolo11s-pose.pt",
		VehicleModel:        "yolo11n.pt",
	}, models, nil)
	return w, root
}

func TestHermeticWorkerRejectsUnverifiedRuntimeInputs(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Worker, string)
		want   error
	}{
		{name: "host Python", mutate: func(w *Worker, _ string) { w.cfg.WorkerCmd = "/usr/bin/python3" }, want: ErrHermeticRuntimePackInvalid},
		{name: "host FFmpeg", mutate: func(w *Worker, _ string) { w.cfg.ProcessEnv[3] = "GEOCAM_VIDEO_FFMPEG_PATH=/opt/homebrew/bin/ffmpeg" }, want: ErrHermeticRuntimePackInvalid},
		{name: "host Homebrew on parent PATH is ignored", mutate: func(_ *Worker, _ string) { t.Setenv("PATH", "/opt/homebrew/bin") }, want: nil},
		{name: "FFmpeg missing", mutate: func(w *Worker, _ string) { _ = os.Remove(w.cfg.ProcessEnvPathForTest("GEOCAM_VIDEO_FFMPEG_PATH")) }, want: ErrHermeticRuntimePackInvalid},
		{name: "PYTHONPATH contamination rejected", mutate: func(w *Worker, _ string) {
			w.cfg.ProcessEnv = append(w.cfg.ProcessEnv, "PYTHONPATH=/tmp/contamination")
		}, want: ErrHermeticRuntimePackInvalid},
		{name: "temporary directory outside worker state rejected", mutate: func(w *Worker, _ string) {
			w.cfg.ProcessEnv[2] = "TMPDIR=" + t.TempDir()
		}, want: ErrHermeticRuntimePackInvalid},
		{name: "unreviewed worker arguments rejected", mutate: func(w *Worker, _ string) {
			w.cfg.WorkerArgs = append(w.cfg.WorkerArgs, "--arbitrary-runtime-override")
		}, want: ErrHermeticRuntimePackInvalid},
		{name: "model pack unauthorized", mutate: func(w *Worker, _ string) { w.cfg.ModelPackAuthorized = false }, want: ErrHermeticModelPackUnauthorized},
		{name: "models directory missing", mutate: func(w *Worker, _ string) {
			w.cfg.ModelsDir = filepath.Join(t.TempDir(), "missing")
			w.models.dir = w.cfg.ModelsDir
		}, want: ErrHermeticModelsUnavailable},
		{name: "escaping symlink", mutate: func(w *Worker, root string) {
			path := filepath.Join(root, "media/ffmpeg")
			outside := filepath.Join(t.TempDir(), "ffmpeg")
			if err := os.WriteFile(outside, []byte("host"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, path); err != nil {
				t.Fatal(err)
			}
		}, want: ErrHermeticRuntimePackInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w, root := syntheticHermeticWorker(t)
			tt.mutate(w, root)
			err := w.validateHermeticConfig()
			if tt.want == nil {
				if err != nil {
					t.Fatalf("unexpected validation error: %v", err)
				}
				return
			}
			if !errors.Is(err, tt.want) {
				t.Fatalf("expected %v, got %v", tt.want, err)
			}
		})
	}
}

func TestHermeticWorkerUsesReplacementEnvironment(t *testing.T) {
	w, root := syntheticHermeticWorker(t)
	hostBin := t.TempDir()
	for _, name := range []string{"python3", "brew", "ffmpeg"} {
		if err := os.WriteFile(filepath.Join(hostBin, name), []byte("host"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", hostBin)
	t.Setenv("PYTHONPATH", "/tmp/host-contamination")
	if err := w.validateHermeticConfig(); err != nil {
		t.Fatalf("validate hermetic fixture: %v", err)
	}
	cmd := w.newExecCommand(w.cfg.WorkerArgs)
	if cmd.Path != filepath.Join(root, "python/bin/python3") {
		t.Fatalf("host interpreter selected: %s", cmd.Path)
	}
	if cmd.Args[1] != "-I" || cmd.Args[2] != "-B" || cmd.Env == nil {
		t.Fatalf("isolated command contract missing: args=%#v env=%#v", cmd.Args, cmd.Env)
	}
	for _, item := range cmd.Env {
		if strings.HasPrefix(item, "PYTHONPATH=") || strings.HasPrefix(item, "PATH="+hostBin) || strings.HasPrefix(item, "PYTHONHOME=") {
			t.Fatalf("host environment leaked into child: %q", item)
		}
	}
}

func TestHermeticWorkerStartFailsClosedWithoutModelAuthorization(t *testing.T) {
	w, _ := syntheticHermeticWorker(t)
	w.cfg.ModelPackAuthorized = false
	err := w.Start(context.Background())
	if !errors.Is(err, ErrHermeticModelPackUnauthorized) {
		t.Fatalf("expected model authorization gate, got %v", err)
	}
	if w.Status().State != StateError {
		t.Fatalf("expected error state, got %s", w.Status().State)
	}
}

func TestHermeticWorkerStartFailsClosedWithoutBundledPython(t *testing.T) {
	w, _ := syntheticHermeticWorker(t)
	w.cfg.WorkerCmd = ""
	err := w.Start(context.Background())
	if !errors.Is(err, ErrHermeticRuntimePackInvalid) {
		t.Fatalf("expected missing bundled Python rejection, got %v", err)
	}
	if w.Status().State != StateError {
		t.Fatalf("expected error state, got %s", w.Status().State)
	}
}

func (c Config) ProcessEnvPathForTest(key string) string {
	for _, item := range c.ProcessEnv {
		name, value, ok := strings.Cut(item, "=")
		if ok && name == key {
			return value
		}
	}
	return ""
}
