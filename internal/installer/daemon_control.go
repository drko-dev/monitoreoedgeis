package installer

import (
	"context"

	"github.com/drko-dev/monitoreoedgeis/internal/config"
	"github.com/drko-dev/monitoreoedgeis/internal/service"
)

// DaemonStatus is the narrow, safe view of the Edge daemon this installer
// can observe: whether it answers over loopback HTTP, whether a service
// manager entry exists for it, and whether this installer could actually
// restart it -- honestly, without ever attempting an operation that would
// fail for lack of privilege (see restartCommand below and
// docs/product/UX4_CAMERA_IP_ONBOARDING.md, "Daemon status/reload/restart").
type DaemonStatus struct {
	Running                  bool           `json:"running"`
	ServiceInstalled         bool           `json:"service_installed"`
	ServiceScope             ServiceScope   `json:"service_scope,omitempty"`
	RestartAvailable         bool           `json:"restart_available"`
	RestartRequiresElevation bool           `json:"restart_requires_elevation"`
	ProcessingMode           string         `json:"processing_mode,omitempty"`
	EffectiveProfile         config.Profile `json:"effective_profile,omitempty"`
}

// RestartOutcome is the terminal result of RestartDaemon.
type RestartOutcome string

const (
	RestartOutcomeSuccess        RestartOutcome = "SUCCESS"
	RestartOutcomeActionRequired RestartOutcome = "ACTION_REQUIRED"
	RestartOutcomeFailed         RestartOutcome = "RESTART_FAILED"
)

// RestartDaemonResult is a typed, safe report -- never a raw error, so the
// UI can render ACTION_REQUIRED without an exception.
type RestartDaemonResult struct {
	Outcome     RestartOutcome `json:"outcome"`
	SafeMessage string         `json:"safe_message"`
}

// restartCommand is overridable in tests so no test ever shells out to a
// real launchctl/systemctl/SCM on the machine running the test suite.
var restartCommand = service.Command

// DetectDaemon is a cheap loopback liveness probe (GET /healthz).
func (s *Service) DetectDaemon(ctx context.Context) bool {
	return s.checkDaemonRunning(ctx)
}

// GetDaemonStatus reports whether the daemon is running, whether a service
// manager entry exists for it, and whether a restart could actually be
// attempted from here without requiring privilege this installer does not
// have. It never guesses: restartAvailable is true only when the detected
// service scope needs no elevation (a user-scope LaunchAgent) or this
// process already runs at the privilege the scope requires.
func (s *Service) GetDaemonStatus(ctx context.Context) (*DaemonStatus, error) {
	running := s.checkDaemonRunning(ctx)
	installed, scope := s.serviceInstallScope()
	requiresElevation, available := restartCapability(installed, scope)

	status := &DaemonStatus{
		Running:                  running,
		ServiceInstalled:         installed,
		ServiceScope:             scope,
		RestartAvailable:         available,
		RestartRequiresElevation: requiresElevation,
	}
	if running {
		if snap, ok := s.fetchLiveStatus(ctx); ok {
			status.ProcessingMode = snap.ProcessingMode
			status.EffectiveProfile = snap.Profile
		}
	}
	return status, nil
}

// restartCapability decides, from real, observed facts only, whether a
// restart could be attempted without this process elevating itself:
//   - not installed as a managed service at all (e.g. running via `go run`
//     in dev): no safe restart channel exists here.
//   - user scope (a LaunchAgent): never needs elevation.
//   - system scope (a systemd system unit, or a future LaunchDaemon/Windows
//     service): needs the level the platform's service manager actually
//     requires, and this process must already be running at it -- this
//     installer never elevates itself.
func restartCapability(installed bool, scope ServiceScope) (requiresElevation, available bool) {
	if !installed {
		return false, false
	}
	switch scope {
	case ServiceScopeUser:
		return false, true
	case ServiceScopeSystem:
		level := currentPrivilegeLevel()
		elevated := level == PrivilegeAdministrator || level == PrivilegeRoot
		return true, elevated
	default:
		return true, false
	}
}

// RestartDaemon restarts the Edge daemon through the same narrow
// internal/service.Command("restart", ...) used by `geocam-edge service
// restart` -- never a generic shell, never ExecuteCommand(path, args), and
// never a permanent privilege escalation: if GetDaemonStatus says a restart
// is not currently available, RestartDaemon does not attempt it and returns
// ACTION_REQUIRED instead of failing loudly or lying about success.
func (s *Service) RestartDaemon(ctx context.Context) (*RestartDaemonResult, error) {
	status, err := s.GetDaemonStatus(ctx)
	if err != nil {
		return nil, err
	}
	if !status.ServiceInstalled {
		return &RestartDaemonResult{
			Outcome:     RestartOutcomeActionRequired,
			SafeMessage: "GEO CAM Edge is not registered with this host's service manager, so it cannot be restarted from here.",
		}, nil
	}
	if !status.RestartAvailable {
		return &RestartDaemonResult{
			Outcome:     RestartOutcomeActionRequired,
			SafeMessage: "Restarting GEO CAM Edge requires elevated privileges that this installer cannot request yet.",
		}, nil
	}

	if err := restartCommand("restart", service.Options{}); err != nil {
		return &RestartDaemonResult{
			Outcome:     RestartOutcomeFailed,
			SafeMessage: "GEO CAM Edge did not restart cleanly. It may need to be restarted manually.",
		}, nil
	}
	return &RestartDaemonResult{
		Outcome:     RestartOutcomeSuccess,
		SafeMessage: "GEO CAM Edge was restarted.",
	}, nil
}
