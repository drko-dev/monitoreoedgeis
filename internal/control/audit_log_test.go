package control

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/drko-dev/monitoreoedgeis/internal/transport"
)

// TestExecuteCommandLogsOutcomeWithoutLeakingCredential covers Hito S / S11:
// every dispatched control command (allowlisted-type-only, see the switch in
// execute()) must produce a structured slog security-event line, and it must
// never carry the device credential passed to New(), regardless of outcome.
func TestExecuteCommandLogsOutcomeWithoutLeakingCredential(t *testing.T) {
	const secretCredential = "s11-secret-credential-do-not-log"

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	m := New(nil, &fakeExecutor{}, "device-1", secretCredential)

	cases := []struct {
		name, kind       string
		wantStatus, code string
	}{
		{"status", "request_status", "succeeded", ""},
		{"unknown", "shell", "failed", "UNKNOWN_COMMAND"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			buf.Reset()
			cmd := &transport.ControlCommand{ID: "cmd-" + tc.name, CommandType: tc.kind}
			state, _, code, err := m.ExecuteCommand(context.Background(), cmd)
			if err != nil {
				t.Fatalf("ExecuteCommand: %v", err)
			}
			if state != tc.wantStatus || code != tc.code {
				t.Fatalf("got state=%q code=%q, want state=%q code=%q", state, code, tc.wantStatus, tc.code)
			}

			out := buf.String()
			if !strings.Contains(out, "control command executed") {
				t.Fatalf("expected a security event log line, got: %s", out)
			}
			if !strings.Contains(out, "cmd-"+tc.name) || !strings.Contains(out, tc.kind) {
				t.Fatalf("expected command_id/command_type in log line, got: %s", out)
			}
			if strings.Contains(out, secretCredential) {
				t.Fatalf("log line leaks the device credential: %s", out)
			}
		})
	}
}
