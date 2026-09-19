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

func TestAgentFullEdgeServiceGatedOnMode(t *testing.T) {
	// ModeCloud: FullEdgeService should be nil
	cfgCloud := testConfig(t)
	cfgCloud.ProcessingMode = config.ModeCloud
	aCloud := New(cfgCloud)
	if aCloud.FullEdgeService() != nil {
		t.Errorf("expected nil FullEdgeService in ModeCloud")
	}

	// ModeEdge: FullEdgeService should be non-nil
	cfgEdge := testConfig(t)
	cfgEdge.ProcessingMode = config.ModeEdge
	aEdge := New(cfgEdge)
	if aEdge.FullEdgeService() == nil {
		t.Fatalf("expected non-nil FullEdgeService in ModeEdge")
	}
	if aEdge.FullEdgeService().Hardware().CurrentDevice() != "cpu" &&
		aEdge.FullEdgeService().Hardware().CurrentDevice() != "cuda" {
		t.Errorf("unexpected CurrentDevice: %s", aEdge.FullEdgeService().Hardware().CurrentDevice())
	}
}

func TestAgentDegradedOnCorruptControlLedger(t *testing.T) {
	cfg := testConfig(t)
	cfg.SaaSURL = "https://saas.example.com"
	// Create identity and credentials so enrollment passes
	identPath := filepath.Join(cfg.DataDir, "identity.json")
	_ = os.WriteFile(identPath, []byte(`{"edge_id":"550e8400-e29b-41d4-a716-446655440000","created_at":"2026-09-18T00:00:00Z","schema_version":1}`), 0o600)
	credsPath := filepath.Join(cfg.DataDir, "credentials.json")
	_ = os.WriteFile(credsPath, []byte(`{"edge_id":"550e8400-e29b-41d4-a716-446655440000","device_id":"dev-1","credential":"cred-1","enrolled_at":"2026-09-18T00:00:00Z","schema_version":1}`), 0o600)

	// Corrupt control_ledger.json
	ledgerPath := filepath.Join(cfg.DataDir, "control_ledger.json")
	if err := os.WriteFile(ledgerPath, []byte("{invalid-json"), 0o600); err != nil {
		t.Fatal(err)
	}

	a := New(cfg)
	if a.controlErr == nil {
		t.Fatal("expected controlErr != nil due to corrupt ledger, got nil")
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()

	waitForState(t, a, health.StateDegraded)
	cancel()
	_ = <-done
}

// TestAgentDegradedOnCorruptRemoteConfigState: a corrupt
// remote_config_state.json (Y8) must never be silently discarded or
// defaulted — newRemoteConfigModule wraps remoteconfig.ErrCorruptState into
// remoteConfigErr, and the agent must fail closed exactly like the identity,
// credentials and control-ledger stores (mirrors
// TestAgentDegradedOnCorruptControlLedger).
func TestAgentDegradedOnCorruptRemoteConfigState(t *testing.T) {
	cfg := testConfig(t)
	cfg.SaaSURL = "https://saas.example.com"
	identPath := filepath.Join(cfg.DataDir, "identity.json")
	_ = os.WriteFile(identPath, []byte(`{"edge_id":"550e8400-e29b-41d4-a716-446655440000","created_at":"2026-09-18T00:00:00Z","schema_version":1}`), 0o600)
	credsPath := filepath.Join(cfg.DataDir, "credentials.json")
	_ = os.WriteFile(credsPath, []byte(`{"edge_id":"550e8400-e29b-41d4-a716-446655440000","device_id":"dev-1","credential":"cred-1","enrolled_at":"2026-09-18T00:00:00Z","schema_version":1}`), 0o600)

	statePath := filepath.Join(cfg.DataDir, "remote_config_state.json")
	corrupt := []byte("{truncated-json")
	if err := os.WriteFile(statePath, corrupt, 0o600); err != nil {
		t.Fatal(err)
	}

	a := New(cfg)
	if a.remoteConfigErr == nil {
		t.Fatal("expected remoteConfigErr != nil due to corrupt remote_config_state.json, got nil")
	}
	if a.RemoteConfig() != nil {
		t.Error("expected nil RemoteConfig module when the store failed to open")
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()

	waitForState(t, a, health.StateDegraded)
	cancel()
	_ = <-done

	// The corrupt file itself must be preserved untouched for diagnosis —
	// nothing may overwrite it with a fresh default state.
	got, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("re-reading remote_config_state.json: %v", err)
	}
	if string(got) != string(corrupt) {
		t.Errorf("remote_config_state.json was modified: before=%q after=%q", corrupt, got)
	}
}
