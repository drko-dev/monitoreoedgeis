package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/drko-dev/monitoreoedgeis/internal/installer"
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

func TestFullEdgePreparationBindingsFailClosedUntilIntegrated(t *testing.T) {
	app := NewApp()
	app.startup(context.Background())

	snapshot, err := app.GetFullEdgePreparationStatus()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.State != installer.PreparationBlockedError || snapshot.CertifiedReady() || snapshot.CanStart {
		t.Fatalf("unexpected default preparation status: %+v", snapshot)
	}

	started, err := app.StartFullEdgePreparation("request-1")
	if !errors.Is(err, installer.ErrPreparationNotWired) || started.CertifiedReady() {
		t.Fatalf("expected fail-closed start, snapshot=%+v err=%v", started, err)
	}

	diagnostic, err := app.GetFullEdgePreparationDiagnostic("")
	if err != nil || diagnostic.Code != "PREPARATION_NOT_INTEGRATED" {
		t.Fatalf("unexpected diagnostic: %+v, %v", diagnostic, err)
	}
}

type invalidReadyPreparation struct {
	installer.UnavailableFullEdgePreparation
}

func (invalidReadyPreparation) Status(context.Context) (installer.FullEdgePreparationSnapshot, error) {
	return installer.FullEdgePreparationSnapshot{State: installer.PreparationRuntimeReady, ProgressPercent: 100}, nil
}

func TestAppRejectsUncertifiedRuntimeReadySnapshot(t *testing.T) {
	app := NewApp()
	app.preparation = invalidReadyPreparation{}

	snapshot, err := app.GetFullEdgePreparationStatus()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.State != installer.StateFailed || snapshot.CertifiedReady() || snapshot.ErrorCode != "INVALID_PREPARATION_STATUS" {
		t.Fatalf("invalid backend readiness must fail closed: %+v", snapshot)
	}
}

type staticStateAdapter struct {
	installer.UnavailableFullEdgePreparation
	snapshot installer.FullEdgePreparationSnapshot
}

func (s staticStateAdapter) Status(context.Context) (installer.FullEdgePreparationSnapshot, error) {
	return s.snapshot, nil
}

func TestAppFullEdgePreparation_AllEightStatesAndComponents(t *testing.T) {
	states := []installer.FullEdgePreparationState{
		installer.StateNotPrepared,
		installer.StateChecking,
		installer.StatePreparing,
		installer.StateVerifying,
		installer.StateRecoveryRequired,
		installer.StateFailed,
		installer.StateCancelled,
	}

	for _, st := range states {
		snap := installer.FullEdgePreparationSnapshot{
			State:           st,
			ProgressPercent: 10,
			Components:      installer.DefaultComponentProgress(),
		}
		app := NewApp()
		app.preparation = staticStateAdapter{snapshot: snap}
		got, err := app.GetFullEdgePreparationStatus()
		if err != nil {
			t.Fatalf("state %s returned error: %v", st, err)
		}
		if got.State != st {
			t.Fatalf("expected state %s, got %s", st, got.State)
		}
		if len(got.Components) != 6 {
			t.Fatalf("expected 6 components for state %s, got %d", st, len(got.Components))
		}
	}
}
