package vision

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
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

func TestHermeticSignedTrustRejectionStopsSupervisorWithoutAutomaticRetry(t *testing.T) {
	w, _ := syntheticHermeticWorker(t)
	stateDir := strings.TrimPrefix(w.cfg.ProcessEnv[1], "HOME=")
	stateDir, err := filepath.EvalSymlinks(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	socketDir := filepath.Join(stateDir, "no-worker-socket")
	w.cfg.SocketPath = filepath.Join(socketDir, "vision.sock")
	signerRevoked := errors.New("publisher signature revoked")
	var attempts atomic.Int32
	w.cfg.PreSpawnVerify = func(ctx context.Context) error {
		attempts.Add(1)
		return signerRevoked
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := w.Start(ctx); err != nil {
		t.Fatalf("synthetic hermetic worker Start should return asynchronously: %v", err)
	}
	select {
	case <-w.doneCh:
	case <-time.After(2 * time.Second):
		t.Fatal("signed trust rejection did not stop worker supervisor")
	}
	if got := w.Status(); got.State != StateError || got.Restarts != 0 ||
		!strings.Contains(got.LastError, "publisher signature revoked") {
		t.Fatalf("signed trust failure not terminal or reported: %+v", got)
	}
	if got := attempts.Load(); got != 1 {
		t.Fatalf("signed trust rechecked %d times, should not auto-retry after rejection", got)
	}
	if _, err := os.Stat(socketDir); !os.IsNotExist(err) {
		t.Fatalf("signed trust rejected but created socket path: %v", err)
	}
	if err := w.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}
