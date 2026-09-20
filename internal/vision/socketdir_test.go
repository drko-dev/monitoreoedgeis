package vision

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestEnsureSocketDir covers the directory-provisioning rule directly.
func TestEnsureSocketDir(t *testing.T) {
	t.Run("creates a missing nested parent", func(t *testing.T) {
		root := t.TempDir()
		socket := filepath.Join(root, "run", "nested", "vision-worker.sock")
		if err := ensureSocketDir(socket); err != nil {
			t.Fatalf("ensureSocketDir: %v", err)
		}
		info, err := os.Stat(filepath.Dir(socket))
		if err != nil {
			t.Fatalf("socket directory was not created: %v", err)
		}
		if !info.IsDir() {
			t.Fatalf("%s exists but is not a directory", filepath.Dir(socket))
		}
	})

	t.Run("leaves an existing directory alone", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "run")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		socket := filepath.Join(dir, "vision-worker.sock")
		if err := ensureSocketDir(socket); err != nil {
			t.Fatalf("ensureSocketDir: %v", err)
		}
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		// MkdirAll must not tighten or otherwise rewrite a directory that is
		// already there — the operator (or the appliance) owns its mode.
		if got := info.Mode().Perm(); got != 0o755 {
			t.Errorf("existing directory mode changed to %o, want 755", got)
		}
	})

	for _, tc := range []struct {
		name   string
		socket string
	}{
		{"empty path", ""},
		{"bare filename in the working directory", "vision-worker.sock"},
		{"filesystem root", "/vision-worker.sock"},
	} {
		t.Run("no-op for "+tc.name, func(t *testing.T) {
			if err := ensureSocketDir(tc.socket); err != nil {
				t.Fatalf("ensureSocketDir(%q) = %v, want nil", tc.socket, err)
			}
		})
	}
}

// TestWorkerStart_CreatesMissingSocketDirectory is the regression guard for
// the defect that made Full Edge unable to start on a fresh appliance.
//
// The production socket path is $GEOCAM_DATA_DIR/run/vision-worker.sock, and
// nothing provisioned that run/ directory: install.sh creates DATA_DIR,
// DATA_DIR/ota and DATA_DIR/ota/pending only, the Python worker just bind()s
// the path, and the Go side only removed a stale socket. So the first spawn
// failed with ENOENT, the worker never became ready, and /readyz stayed 503
// forever — which in turn made the appliance's own update.sh roll a healthy
// release back. Tests never caught it because every one of them pointed the
// socket at a t.TempDir() that already existed.
//
// The socket root is deliberately a short os.MkdirTemp directory rather than
// t.TempDir(): t.TempDir() embeds the test's own name, and a Unix socket path
// is capped at 104 bytes on macOS/BSD (108 on Linux), which that path exceeds.
// The default production path (/var/lib/geocam-edge/run/vision-worker.sock)
// is well inside the limit; a long GEOCAM_EDGE_YOLO_SOCKET_PATH would not be,
// which docs/product/COMMERCIAL_MODES.md records as a known limitation.
func TestWorkerStart_CreatesMissingSocketDirectory(t *testing.T) {
	root, err := os.MkdirTemp("", "vwsock")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })

	missingDir := filepath.Join(root, "run")
	if _, err := os.Stat(missingDir); !os.IsNotExist(err) {
		t.Fatalf("precondition failed: %s already exists", missingDir)
	}

	cfg := baseTestConfig(t)
	cfg.SocketPath = filepath.Join(missingDir, "vision-worker.sock")
	if len(cfg.SocketPath) > 100 {
		t.Fatalf("socket path %q is %d bytes, too long for this test's portability",
			cfg.SocketPath, len(cfg.SocketPath))
	}

	w, _ := startWorkerWithEnv(t, cfg)
	waitForState(t, w, StateReady, 5*time.Second)

	if _, err := os.Stat(missingDir); err != nil {
		t.Fatalf("worker reached ready but socket directory %s does not exist: %v", missingDir, err)
	}
	if _, err := os.Stat(cfg.SocketPath); err != nil {
		t.Fatalf("socket %s does not exist: %v", cfg.SocketPath, err)
	}

	stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := w.Stop(stopCtx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}
