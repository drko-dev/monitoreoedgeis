package control

import (
	"context"
	"fmt"
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
