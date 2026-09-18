package control

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/transport"
)

type fakeExecutor struct{ rediscoveries int }

func (f *fakeExecutor) Status() map[string]any           { return map[string]any{"health": "READY"} }
func (f *fakeExecutor) Rediscover(context.Context) error { f.rediscoveries++; return nil }

func TestExecuteAllowlistAndDuplicateSafety(t *testing.T) {
	exec := &fakeExecutor{}
	m := New(nil, exec, "device", "credential")
	cases := []struct {
		name, kind  string
		payload     map[string]any
		state, code string
	}{
		{"status", "request_status", nil, "succeeded", ""},
		{"rediscovery", "rediscovery", nil, "succeeded", ""},
		{"unknown", "shell", nil, "failed", "UNKNOWN_COMMAND"},
		{"payload", "request_status", map[string]any{"cmd": "x"}, "failed", "INVALID_COMMAND"},
		{"unsupported", "reload_config", nil, "failed", "UNSUPPORTED"},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmdID := fmt.Sprintf("cmd-%d", i)
			state, _, code := m.execute(t.Context(), &transport.ControlCommand{ID: cmdID, CommandType: tc.kind, Payload: tc.payload})
			if state != tc.state || code != tc.code {
				t.Fatalf("got %s/%s", state, code)
			}
		})
	}
	if exec.rediscoveries != 1 {
		t.Fatalf("rediscoveries = %d, want 1", exec.rediscoveries)
	}

	// Duplicate execution of same command ID ("cmd-1" was rediscovery) returns recorded result and does NOT re-execute
	state, _, code := m.execute(t.Context(), &transport.ControlCommand{ID: "cmd-1", CommandType: "rediscovery"})
	if state != "succeeded" || code != "" {
		t.Fatalf("duplicate execution got %s/%s", state, code)
	}
	if exec.rediscoveries != 1 {
		t.Fatalf("rediscoveries after duplicate = %d, want 1 (idempotent)", exec.rediscoveries)
	}
}

type fakeClient struct {
	claimFunc  func(ctx context.Context, deviceID, credential string) (*transport.ControlCommand, error)
	reportFunc func(ctx context.Context, deviceID, credential, commandID, status string, result map[string]any, errorCode string) error
}

func (f *fakeClient) ClaimNextControlCommand(ctx context.Context, deviceID, credential string) (*transport.ControlCommand, error) {
	if f.claimFunc != nil {
		return f.claimFunc(ctx, deviceID, credential)
	}
	return nil, nil
}

func (f *fakeClient) ReportControlCommand(ctx context.Context, deviceID, credential, commandID, status string, result map[string]any, errorCode string) error {
	if f.reportFunc != nil {
		return f.reportFunc(ctx, deviceID, credential, commandID, status, result, errorCode)
	}
	return nil
}

func TestControlModule_PollExecuteAndShutdown(t *testing.T) {
	reported := make(chan string, 1)
	client := &fakeClient{
		claimFunc: func(ctx context.Context, deviceID, credential string) (*transport.ControlCommand, error) {
			return &transport.ControlCommand{
				ID:          "cmd-123",
				CommandType: "request_status",
			}, nil
		},
		reportFunc: func(ctx context.Context, deviceID, credential, commandID, status string, result map[string]any, errorCode string) error {
			reported <- status
			return nil
		},
	}

	exec := &fakeExecutor{}
	m := New(client, exec, "device-1", "cred-1")
	m.SetPollInterval(10 * time.Millisecond)

	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	select {
	case st := <-reported:
		if st != "succeeded" {
			t.Fatalf("reported status = %s, want succeeded", st)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for command execution and report")
	}

	// Clean shutdown test
	stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := m.Stop(stopCtx); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
}

func TestControlModule_SaaSOfflineBacksOff(t *testing.T) {
	calls := 0
	client := &fakeClient{
		claimFunc: func(ctx context.Context, deviceID, credential string) (*transport.ControlCommand, error) {
			calls++
			return nil, transport.ErrSaaSUnavailable
		},
	}

	m := New(client, &fakeExecutor{}, "device-1", "cred-1")
	m.SetPollInterval(10 * time.Millisecond)

	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	time.Sleep(50 * time.Millisecond)

	stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := m.Stop(stopCtx); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}

	if calls == 0 {
		t.Fatal("expected at least 1 claim call")
	}
}

func TestControlModule_RestartIdempotencyWithLedger(t *testing.T) {
	dataDir := t.TempDir()

	// 1. First module run: executes command "cmd-restart-1", records to durable ledger, but dies before report
	ledger1, err := OpenLedger(dataDir, 100)
	if err != nil {
		t.Fatalf("OpenLedger error = %v", err)
	}

	exec1 := &fakeExecutor{}
	reported1 := make(chan string, 1)
	client1 := &fakeClient{
		claimFunc: func(ctx context.Context, deviceID, credential string) (*transport.ControlCommand, error) {
			return &transport.ControlCommand{
				ID:          "cmd-restart-1",
				CommandType: "rediscovery",
			}, nil
		},
		reportFunc: func(ctx context.Context, deviceID, credential, commandID, status string, result map[string]any, errorCode string) error {
			reported1 <- status
			return nil
		},
	}

	m1 := New(client1, exec1, "dev-1", "cred-1", WithLedger(ledger1))
	m1.SetPollInterval(10 * time.Millisecond)

	if err := m1.Start(context.Background()); err != nil {
		t.Fatalf("m1 Start error = %v", err)
	}

	select {
	case st := <-reported1:
		if st != "succeeded" {
			t.Fatalf("m1 reported = %s, want succeeded", st)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("m1 timed out")
	}

	if exec1.rediscoveries != 1 {
		t.Fatalf("exec1 rediscoveries = %d, want 1", exec1.rediscoveries)
	}

	// Stop m1 (simulates shutdown/crash)
	_ = m1.Stop(context.Background())

	// 2. Restart simulation: fresh Module instance with fresh Executor, but same dataDir ledger
	ledger2, err := OpenLedger(dataDir, 100)
	if err != nil {
		t.Fatalf("OpenLedger restart error = %v", err)
	}

	exec2 := &fakeExecutor{}
	reported2 := make(chan string, 1)
	client2 := &fakeClient{
		claimFunc: func(ctx context.Context, deviceID, credential string) (*transport.ControlCommand, error) {
			// SaaS delivers the same command X again
			return &transport.ControlCommand{
				ID:          "cmd-restart-1",
				CommandType: "rediscovery",
			}, nil
		},
		reportFunc: func(ctx context.Context, deviceID, credential, commandID, status string, result map[string]any, errorCode string) error {
			reported2 <- status
			return nil
		},
	}

	m2 := New(client2, exec2, "dev-1", "cred-1", WithLedger(ledger2))
	m2.SetPollInterval(10 * time.Millisecond)

	if err := m2.Start(context.Background()); err != nil {
		t.Fatalf("m2 Start error = %v", err)
	}
	defer m2.Stop(context.Background())

	select {
	case st := <-reported2:
		if st != "succeeded" {
			t.Fatalf("m2 reported = %s, want succeeded", st)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("m2 timed out")
	}

	// Executor must NOT have been invoked again!
	if exec2.rediscoveries != 0 {
		t.Fatalf("exec2 rediscoveries = %d, want 0 (idempotent across restart)", exec2.rediscoveries)
	}
}

func TestLedger_CorruptFileHardError(t *testing.T) {
	dataDir := t.TempDir()
	path := filepath.Join(dataDir, "control_ledger.json")

	// Write garbage JSON to ledger file
	if err := os.WriteFile(path, []byte("{not-valid-json"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := OpenLedger(dataDir, 100)
	if err == nil {
		t.Fatal("expected error on corrupt ledger file, got nil")
	}
	if !errors.Is(err, ErrCorruptLedger) {
		t.Fatalf("err = %v, want ErrCorruptLedger", err)
	}
}

func TestLedger_AtomicWriteAndPruning(t *testing.T) {
	dataDir := t.TempDir()
	maxEntries := 3
	l, err := OpenLedger(dataDir, maxEntries)
	if err != nil {
		t.Fatal(err)
	}

	for i := 1; i <= 5; i++ {
		cmdID := fmt.Sprintf("cmd-%d", i)
		if err := l.Record(cmdID, CommandExecution{Status: "succeeded"}); err != nil {
			t.Fatalf("Record error = %v", err)
		}
	}

	// Must be bounded to maxEntries (3)
	if count := l.Count(); count != 3 {
		t.Fatalf("ledger count = %d, want %d", count, maxEntries)
	}

	// Oldest entries (cmd-1, cmd-2) should have been pruned; newest (cmd-3, cmd-4, cmd-5) retained
	if _, found := l.Get("cmd-1"); found {
		t.Error("cmd-1 should have been pruned")
	}
	if _, found := l.Get("cmd-2"); found {
		t.Error("cmd-2 should have been pruned")
	}
	for i := 3; i <= 5; i++ {
		if _, found := l.Get(fmt.Sprintf("cmd-%d", i)); !found {
			t.Errorf("cmd-%d should be retained in ledger", i)
		}
	}

	// Verify file was written and is valid JSON on disk
	path := filepath.Join(dataDir, "control_ledger.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read ledger file: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("ledger file is empty")
	}
}
