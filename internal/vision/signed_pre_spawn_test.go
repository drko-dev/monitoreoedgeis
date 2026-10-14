package vision

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestHermeticWorkerPreSpawnVerificationRunsOnEveryAttempt(t *testing.T) {
	w, _ := syntheticHermeticWorker(t)
	socketDir := filepath.Join(t.TempDir(), "should-not-be-created")
	w.cfg.SocketPath = filepath.Join(socketDir, "vision.sock")
	rejected := errors.New("installed signed worker pack changed")
	attempts := 0
	w.cfg.PreSpawnVerify = func(ctx context.Context) error {
		attempts++
		if err := ctx.Err(); err != nil {
			return err
		}
		return rejected
	}
	for n := 1; n <= 2; n++ {
		err := w.spawnAndHandshake(context.Background())
		if !errors.Is(err, rejected) || attempts != n {
			t.Fatalf("pre-spawn check skipped on attempt %d: count=%d error=%v", n, attempts, err)
		}
		if _, err := os.Stat(socketDir); !os.IsNotExist(err) {
			t.Fatalf("attempt %d changed socket filesystem before verifying: %v", n, err)
		}
		w.mu.Lock()
		cmd := w.cmd
		w.mu.Unlock()
		if cmd != nil {
			t.Fatal("worker process was created before successful verification")
		}
	}
}

func TestHermeticWorkerPreSpawnVerificationHonorsCancellation(t *testing.T) {
	w, _ := syntheticHermeticWorker(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w.cfg.PreSpawnVerify = func(ctx context.Context) error { return ctx.Err() }
	if err := w.spawnAndHandshake(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled pre-spawn check did not fail closed: %v", err)
	}
}
