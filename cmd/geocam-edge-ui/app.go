package main

import (
	"context"

	"github.com/drko-dev/monitoreoedgeis/internal/installer"
)

// App is the desktop application bridge. It exposes strictly typed, audited methods
// to the Wails frontend. It contains NO arbitrary command execution, NO raw filesystem
// bindings, and NO secret access.
type App struct {
	ctx       context.Context
	installer *installer.Service
}

// NewApp creates a new desktop App instance backed by the Go installer facade.
func NewApp() *App {
	return &App{
		installer: installer.NewService("", ""),
	}
}

// startup is called by Wails when the application starts.
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
}

// GetSystemReport returns sanitized, non-secret host environment information.
func (a *App) GetSystemReport() (*installer.SystemReport, error) {
	return a.installer.GetSystemReport(a.ctx)
}

// GetInstallerState returns the derived state of the Edge installer state machine.
func (a *App) GetInstallerState() (*installer.InstallerState, error) {
	return a.installer.GetInstallerState(a.ctx)
}

// ClaimDevice executes the secure one-time enrollment flow.
func (a *App) ClaimDevice(req installer.ClaimRequest) (*installer.ClaimResult, error) {
	return a.installer.ClaimDevice(a.ctx, req)
}

// GetProcessingModeOptions returns the three selectable processing modes
// with their real, observed capability on this host.
func (a *App) GetProcessingModeOptions() ([]installer.ProcessingModeOption, error) {
	return a.installer.GetProcessingModeOptions(a.ctx)
}

// GetCurrentProcessingMode returns the processing mode this Edge is
// configured for and, when reachable, actually running.
func (a *App) GetCurrentProcessingMode() (*installer.CurrentProcessingMode, error) {
	return a.installer.GetCurrentProcessingMode(a.ctx)
}

// ValidateProcessingMode checks a requested mode against real host facts.
func (a *App) ValidateProcessingMode(req installer.ProcessingModeRequest) (*installer.ProcessingModeOption, error) {
	return a.installer.ValidateProcessingMode(a.ctx, req)
}

// PlanProcessingMode describes what ApplyProcessingMode would do, without
// mutating any configuration.
func (a *App) PlanProcessingMode(req installer.ProcessingModeRequest) (*installer.ProcessingModePlan, error) {
	return a.installer.PlanProcessingMode(a.ctx, req)
}

// ApplyProcessingMode atomically persists the requested processing mode and
// verifies the write before reporting success.
func (a *App) ApplyProcessingMode(req installer.ProcessingModeRequest) (*installer.ProcessingModeApplyResult, error) {
	return a.installer.ApplyProcessingMode(a.ctx, req)
}

// DiscoverCameras runs one bounded local network scan for IP cameras.
func (a *App) DiscoverCameras() (*installer.DiscoverCamerasResult, error) {
	return a.installer.DiscoverCameras(a.ctx)
}

// GetDiscoveredCamera returns one previously discovered candidate by its
// opaque candidate key.
func (a *App) GetDiscoveredCamera(candidateKey string) (*installer.OnboardingCandidate, error) {
	return a.installer.GetDiscoveredCamera(a.ctx, candidateKey)
}

// TestCameraCredentials validates a candidate's credentials over ONVIF and
// RTSP without persisting anything.
func (a *App) TestCameraCredentials(req installer.TestCameraCredentialsRequest) (*installer.CameraValidationResult, error) {
	return a.installer.TestCameraCredentials(a.ctx, req)
}

// PlanCameraOnboarding previews an onboarding without mutating anything.
func (a *App) PlanCameraOnboarding(req installer.CameraOnboardingRequest) (*installer.CameraOnboardingPlan, error) {
	return a.installer.PlanCameraOnboarding(a.ctx, req)
}

// ApplyCameraOnboarding asks the SaaS to authoritatively onboard the camera.
func (a *App) ApplyCameraOnboarding(req installer.CameraOnboardingRequest) (*installer.CameraOnboardingApplyResult, error) {
	return a.installer.ApplyCameraOnboarding(a.ctx, req)
}

// CancelCameraOnboarding reverses a SaaS onboarding operation this Edge just created.
func (a *App) CancelCameraOnboarding(operationID int64) error {
	return a.installer.CancelCameraOnboarding(a.ctx, operationID)
}
