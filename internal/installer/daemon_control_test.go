package installer

import (
	"context"
	"errors"
	"testing"

	"github.com/drko-dev/monitoreoedgeis/internal/service"
)

// withServiceScope overrides service install detection for the duration of
// a test, restoring the real detector afterwards. It never touches a real
// LaunchAgents/systemd path.
func withServiceScope(t *testing.T, installed bool, scope ServiceScope) {
	t.Helper()
	previous := serviceInstallScopeDetector
	serviceInstallScopeDetector = func() (bool, ServiceScope) { return installed, scope }
	t.Cleanup(func() { serviceInstallScopeDetector = previous })
}

// withRestartCommand overrides the real service manager call for the
// duration of a test so no test ever shells out to launchctl/systemctl/SCM.
func withRestartCommand(t *testing.T, fn func(action string, opts service.Options) error) {
	t.Helper()
	previous := restartCommand
	restartCommand = fn
	t.Cleanup(func() { restartCommand = previous })
}

func TestGetDaemonStatusNotInstalledMeansRestartUnavailable(t *testing.T) {
	withServiceScope(t, false, ServiceScopeNone)
	svc := newIsolatedService(t)

	status, err := svc.GetDaemonStatus(context.Background())
	if err != nil {
		t.Fatalf("GetDaemonStatus: %v", err)
	}
	if status.ServiceInstalled || status.RestartAvailable || status.RestartRequiresElevation {
		t.Fatalf("status = %+v, want not installed and restart unavailable", status)
	}
}

func TestGetDaemonStatusUserScopeNeedsNoElevation(t *testing.T) {
	withServiceScope(t, true, ServiceScopeUser)
	svc := newIsolatedService(t)

	status, err := svc.GetDaemonStatus(context.Background())
	if err != nil {
		t.Fatalf("GetDaemonStatus: %v", err)
	}
	if !status.ServiceInstalled || status.RestartRequiresElevation || !status.RestartAvailable {
		t.Fatalf("status = %+v, want installed, no elevation required, restart available", status)
	}
}

func TestGetDaemonStatusSystemScopeRequiresElevationWithoutIt(t *testing.T) {
	withServiceScope(t, true, ServiceScopeSystem)
	svc := newIsolatedService(t)

	status, err := svc.GetDaemonStatus(context.Background())
	if err != nil {
		t.Fatalf("GetDaemonStatus: %v", err)
	}
	if !status.ServiceInstalled || !status.RestartRequiresElevation || status.RestartAvailable {
		t.Fatalf("status = %+v, want installed, elevation required, restart NOT available (test runs unprivileged)", status)
	}
}

func TestRestartDaemonNotInstalledIsActionRequiredWithoutInvokingServiceManager(t *testing.T) {
	withServiceScope(t, false, ServiceScopeNone)
	called := false
	withRestartCommand(t, func(string, service.Options) error {
		called = true
		return nil
	})
	svc := newIsolatedService(t)

	result, err := svc.RestartDaemon(context.Background())
	if err != nil {
		t.Fatalf("RestartDaemon: %v", err)
	}
	if result.Outcome != RestartOutcomeActionRequired {
		t.Fatalf("Outcome = %s, want ACTION_REQUIRED", result.Outcome)
	}
	if called {
		t.Fatal("RestartDaemon invoked the service manager despite no service being installed")
	}
}

func TestRestartDaemonSystemScopeIsActionRequiredWithoutElevationOrInvocation(t *testing.T) {
	withServiceScope(t, true, ServiceScopeSystem)
	called := false
	withRestartCommand(t, func(string, service.Options) error {
		called = true
		return nil
	})
	svc := newIsolatedService(t)

	result, err := svc.RestartDaemon(context.Background())
	if err != nil {
		t.Fatalf("RestartDaemon: %v", err)
	}
	if result.Outcome != RestartOutcomeActionRequired {
		t.Fatalf("Outcome = %s, want ACTION_REQUIRED", result.Outcome)
	}
	if called {
		t.Fatal("RestartDaemon invoked the service manager without required elevation -- this must never happen")
	}
}

func TestRestartDaemonUserScopeSucceeds(t *testing.T) {
	withServiceScope(t, true, ServiceScopeUser)
	var gotAction string
	withRestartCommand(t, func(action string, _ service.Options) error {
		gotAction = action
		return nil
	})
	svc := newIsolatedService(t)

	result, err := svc.RestartDaemon(context.Background())
	if err != nil {
		t.Fatalf("RestartDaemon: %v", err)
	}
	if result.Outcome != RestartOutcomeSuccess {
		t.Fatalf("Outcome = %s, want SUCCESS", result.Outcome)
	}
	if gotAction != "restart" {
		t.Fatalf("service command action = %q, want \"restart\"", gotAction)
	}
}

func TestRestartDaemonUserScopeFailureIsReportedNotErrored(t *testing.T) {
	withServiceScope(t, true, ServiceScopeUser)
	withRestartCommand(t, func(string, service.Options) error {
		return errors.New("launchctl bootstrap failed")
	})
	svc := newIsolatedService(t)

	result, err := svc.RestartDaemon(context.Background())
	if err != nil {
		t.Fatalf("RestartDaemon returned a Go error instead of a typed result: %v", err)
	}
	if result.Outcome != RestartOutcomeFailed {
		t.Fatalf("Outcome = %s, want RESTART_FAILED", result.Outcome)
	}
}

func TestDetectDaemonMatchesDaemonRunningCheck(t *testing.T) {
	svc := newIsolatedService(t)
	if svc.DetectDaemon(context.Background()) {
		t.Fatal("DetectDaemon reported running against an unreachable HealthAddr")
	}
}
