package main

import (
	"context"
	"strings"
	"testing"
)

func TestAppBindingsReturnSafeData(t *testing.T) {
	app := NewApp()
	app.startup(context.Background())

	report, err := app.GetSystemReport()
	if err != nil {
		t.Fatalf("GetSystemReport error: %v", err)
	}
	if report == nil {
		t.Fatal("Expected report not to be nil")
	}

	if report.OS == "" || report.Arch == "" {
		t.Errorf("Expected OS and Arch to be populated: OS=%q, Arch=%q", report.OS, report.Arch)
	}

	if report.OwnsInstanceLock {
		t.Error("App binding must not claim instance lock")
	}

	state, err := app.GetInstallerState()
	if err != nil {
		t.Fatalf("GetInstallerState error: %v", err)
	}
	if state == nil {
		t.Fatal("Expected state not to be nil")
	}

	if state.State == "" || state.ReasonCode == "" {
		t.Errorf("Expected valid state and reason code: %+v", state)
	}

	// Verify no secrets in state message
	for _, kw := range []string{"password", "token", "secret", "private_key"} {
		if strings.Contains(strings.ToLower(state.SafeMessage), kw) {
			t.Errorf("Safe message leaks keyword %q: %s", kw, state.SafeMessage)
		}
	}
}
