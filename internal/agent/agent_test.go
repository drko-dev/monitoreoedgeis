package agent

import (
	"context"
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/config"
	"github.com/drko-dev/monitoreoedgeis/internal/health"
)

func TestAgentLifecycle(t *testing.T) {
	a := New(&config.Config{
		EdgeID:            "edge-test",
		ProcessingMode:    config.ModeCloud,
		LogLevel:          "error", // keep test output quiet
		HeartbeatInterval: 30 * time.Second,
		DataDir:           t.TempDir(),
	})

	if got := a.Health().State(); got != health.StateStarting {
		t.Fatalf("initial state = %q, want %q", got, health.StateStarting)
	}

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()

	// Run must reach READY before it blocks on ctx.Done().
	waitForState(t, a, health.StateReady)

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() error = %v, want nil on cancellation", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run() did not return after context cancellation")
	}

	if got := a.Health().State(); got != health.StateStopping {
		t.Errorf("final state = %q, want %q", got, health.StateStopping)
	}
}

func waitForState(t *testing.T, a *Agent, want health.State) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if a.Health().State() == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("agent never reached state %q (current %q)", want, a.Health().State())
}

func TestVersionIsSet(t *testing.T) {
	if Version == "" {
		t.Error("Version is empty")
	}
}
