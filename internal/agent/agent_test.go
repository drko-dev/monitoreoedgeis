package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/config"
	"github.com/drko-dev/monitoreoedgeis/internal/health"
)

var errBoom = errors.New("boom")

func testConfig(t *testing.T) *config.Config {
	t.Helper()
	return &config.Config{
		EdgeID:            "edge-test",
		ProcessingMode:    config.ModeCloud,
		LogLevel:          "error", // keep test output quiet
		HeartbeatInterval: 30 * time.Second,
		DataDir:           t.TempDir(),
		HealthAddr:        "127.0.0.1:0", // random free port, avoids collisions
	}
}

func TestAgentLifecycle(t *testing.T) {
	a := New(testConfig(t))

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

// TestAgentDegradedOnCorruptIdentity: a corrupt identity.json must never be
// silently regenerated. Run must never reach READY, and must stay running
// (DEGRADED) instead of crash-looping, so /status stays reachable.
func TestAgentDegradedOnCorruptIdentity(t *testing.T) {
	cfg := testConfig(t)
	cfg.EdgeID = "" // force the persisted-identity path
	if err := os.WriteFile(filepath.Join(cfg.DataDir, "identity.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}

	a := New(cfg)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()

	waitForState(t, a, health.StateDegraded)

	// Give Run a moment to (incorrectly) flip to READY, to make sure it doesn't.
	time.Sleep(20 * time.Millisecond)
	if got := a.Health().State(); got != health.StateDegraded {
		t.Errorf("state = %q, want %q (must never reach READY on a corrupt identity)", got, health.StateDegraded)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run() did not return after context cancellation")
	}
}

// TestAgentDegradedOnCorruptCredentials: a corrupt credentials.json must
// never be silently ignored. Run must never reach READY, and must stay
// running (DEGRADED) so /status stays reachable — mirrors
// TestAgentDegradedOnCorruptIdentity for internal/credentials.
func TestAgentDegradedOnCorruptCredentials(t *testing.T) {
	cfg := testConfig(t)
	if err := os.WriteFile(filepath.Join(cfg.DataDir, "credentials.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}

	a := New(cfg)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()

	waitForState(t, a, health.StateDegraded)

	time.Sleep(20 * time.Millisecond)
	if got := a.Health().State(); got != health.StateDegraded {
		t.Errorf("state = %q, want %q (must never reach READY on corrupt credentials)", got, health.StateDegraded)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run() did not return after context cancellation")
	}
}

// fakeModule is a minimal Module used to exercise the manager in isolation.
type fakeModule struct {
	name      string
	startErr  error
	startedAt *[]string
	stoppedAt *[]string
}

func (m *fakeModule) Name() string { return m.name }
func (m *fakeModule) Start(context.Context) error {
	if m.startedAt != nil {
		*m.startedAt = append(*m.startedAt, m.name)
	}
	return m.startErr
}
func (m *fakeModule) Stop(context.Context) error {
	if m.stoppedAt != nil {
		*m.stoppedAt = append(*m.stoppedAt, m.name)
	}
	return nil
}

func TestAgentDegradedOnModuleStartFailure(t *testing.T) {
	cfg := testConfig(t)
	a := New(cfg)

	// Replace the wired modules with one that always fails to start, so the
	// test does not depend on real network conditions.
	a.modules = newModuleManager(a.health.SetModuleState, &fakeModule{name: "boom", startErr: errBoom})

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()

	waitForState(t, a, health.StateDegraded)

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() error = %v, want nil (degraded is not a Run() error)", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run() did not return after context cancellation")
	}

	if got := a.Health().Snapshot().Modules["boom"]; got != "failed" {
		t.Errorf("module state = %q, want %q", got, "failed")
	}
}
