package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/drko-dev/monitoreoedgeis/internal/installer"
	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// App is the desktop application bridge. It exposes strictly typed, audited methods
// to the Wails frontend. It contains NO arbitrary command execution, NO raw filesystem
// bindings, and NO secret access.
type App struct {
	ctx         context.Context
	installer   *installer.Service
	preparation installer.FullEdgePreparationAPI
	// onPrepared runs after a committed Runtime Pack transaction, e.g. to
	// point the daemon at the installed runtime. Nil for default builds.
	onPrepared func() error
}

// NewApp creates a new desktop App instance backed by the Go installer facade.
func NewApp() *App {
	return &App{
		installer:   installer.NewService("", ""),
		preparation: installer.NewWailsSafeFullEdgePreparationAPI(installer.UnavailableFullEdgePreparation{}),
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

// GetSaaSURL returns the configured SaaS server URL to prefill the enrollment form.
func (a *App) GetSaaSURL() string {
	return a.installer.GetSaaSURL(a.ctx)
}

// GetFFmpegStatus returns the current FFmpeg and FFprobe dependency status.
func (a *App) GetFFmpegStatus() (*installer.DependencyStatus, error) {
	return a.installer.ResolveFFmpeg(a.ctx), nil
}

// InstallFFmpeg triggers the safe platform package installation of FFmpeg (e.g. Homebrew on macOS).
func (a *App) InstallFFmpeg() (*installer.DependencyStatus, error) {
	res, err := a.installer.InstallFFmpeg(a.ctx)
	return res, structuredError(err)
}

// GetVisionRuntimeStatus returns the current status of the Full Edge vision runtime.
func (a *App) GetVisionRuntimeStatus() (*installer.VisionRuntimeStatus, error) {
	res, err := a.installer.GetVisionRuntimeStatus(a.ctx)
	return res, structuredError(err)
}

// GetFullEdgePreparationStatus reads the opt-in offline Runtime Pack flow.
// Unlike GetVisionRuntimeStatus, this never probes host Python or FFmpeg.
func (a *App) GetFullEdgePreparationStatus() (installer.FullEdgePreparationSnapshot, error) {
	snapshot, err := a.preparationAPI().Status(a.requestContext())
	return validatedPreparationSnapshot(snapshot, err)
}

// StartFullEdgePreparation starts the offline flow using an idempotency key.
// The default adapter is intentionally unavailable until H3/H4/H6 are integrated.
func (a *App) StartFullEdgePreparation(requestID string) (installer.FullEdgePreparationSnapshot, error) {
	snapshot, err := a.preparationAPI().Start(a.requestContext(), requestID)
	a.runOnPrepared(snapshot, err)
	return validatedPreparationSnapshot(snapshot, err)
}

func (a *App) CancelFullEdgePreparation(operationID string) (installer.FullEdgePreparationSnapshot, error) {
	snapshot, err := a.preparationAPI().Cancel(a.requestContext(), operationID)
	return validatedPreparationSnapshot(snapshot, err)
}

func (a *App) GetFullEdgePreparationDiagnostic(operationID string) (installer.FullEdgePreparationDiagnostic, error) {
	return a.preparationAPI().Diagnostic(a.requestContext(), operationID)
}

func (a *App) RecoverFullEdgeRuntime(operationID string) (installer.FullEdgePreparationSnapshot, error) {
	snapshot, err := a.preparationAPI().Recover(a.requestContext(), operationID)
	return validatedPreparationSnapshot(snapshot, err)
}

func (a *App) runOnPrepared(snapshot installer.FullEdgePreparationSnapshot, err error) {
	if err != nil || a.onPrepared == nil || snapshot.Stage != "TRANSACTION_COMMITTED" {
		return
	}
	if hookErr := a.onPrepared(); hookErr != nil {
		fmt.Fprintln(os.Stderr, "[GEO-CAM Edge] installed runtime not wired to daemon:", hookErr)
	}
}

func validatedPreparationSnapshot(snapshot installer.FullEdgePreparationSnapshot, err error) (installer.FullEdgePreparationSnapshot, error) {
	// Wails is a trust boundary even when its backend reports an error:
	// it must never relay an invalid/READY status alongside a rejected Promise.
	// Some frontends may retain the returned data on error, and an explicitly
	// injected backend is not authorization to report fake certification.
	sanitized, sanitizeErr := installer.ValidateAndSanitizeWailsSnapshot(snapshot, err)
	return sanitized, structuredError(sanitizeErr)
}

func blockedPreparationSnapshot(code string) installer.FullEdgePreparationSnapshot {
	return installer.BlockedPreparationSnapshot(code)
}

func (a *App) preparationAPI() installer.FullEdgePreparationAPI {
	if a.preparation == nil {
		return installer.UnavailableFullEdgePreparation{}
	}
	return a.preparation
}

func (a *App) requestContext() context.Context {
	if a.ctx == nil {
		return context.Background()
	}
	return a.ctx
}

// PrepareVisionRuntime provisions the Full Edge python environment safely.
func (a *App) PrepareVisionRuntime() (*installer.VisionRuntimeStatus, error) {
	res, err := a.installer.PrepareVisionRuntime(a.ctx)
	return res, structuredError(err)
}

// ImportModel copies and validates a local model file into the managed models directory.
func (a *App) ImportModel(modelName, sourcePath string) (*installer.VisionRuntimeStatus, error) {
	res, err := a.installer.ImportModel(a.ctx, modelName, sourcePath)
	return res, structuredError(err)
}

// SelectAndImportModel opens a native file dialog to select and import a model file.
func (a *App) SelectAndImportModel(modelName string) (*installer.VisionRuntimeStatus, error) {
	filePath, err := wailsRuntime.OpenFileDialog(a.ctx, wailsRuntime.OpenDialogOptions{
		Title: "Seleccionar archivo de modelo PyTorch (" + modelName + ")",
		Filters: []wailsRuntime.FileFilter{
			{
				DisplayName: "PyTorch Models (*.pt)",
				Pattern:     "*.pt",
			},
			{
				DisplayName: "All Files (*.*)",
				Pattern:     "*.*",
			},
		},
	})
	if err != nil {
		return nil, structuredError(err)
	}
	if filePath == "" {
		// User cancelled dialog, return current status without error
		return a.installer.GetVisionRuntimeStatus(a.ctx)
	}
	return a.ImportModel(modelName, filePath)
}

// TestSaaSConnection tests connectivity to a GEO CAM SaaS server.
func (a *App) TestSaaSConnection(rawURL string) (*installer.ServerCheckResult, error) {
	return a.installer.TestSaaSConnection(a.ctx, rawURL)
}

// ClaimDevice executes the secure one-time enrollment flow.
func (a *App) ClaimDevice(req installer.ClaimRequest) (*installer.ClaimResult, error) {
	res, err := a.installer.ClaimDevice(a.ctx, req)
	return res, structuredError(err)
}

// structuredError encodes an installer.SafeError as JSON. Wails rejects the
// frontend promise with err.Error() only, so without this the UI cannot tell
// an invalid code from a local failure and shows every error as a network one.
func structuredError(err error) error {
	var safe *installer.SafeError
	if !errors.As(err, &safe) {
		return err
	}
	b, jsonErr := json.Marshal(safe)
	if jsonErr != nil {
		return err
	}
	return errors.New(string(b))
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

// GetCommissioningStatus inspects all lifecycle, daemon service, HTTP health, and SaaS sync layers.
func (a *App) GetCommissioningStatus() (*installer.CommissioningStatus, error) {
	res, err := a.installer.GetCommissioningStatus(a.ctx)
	return res, structuredError(err)
}

// CommissionDaemon executes the multi-step verified activation and commissioning flow.
func (a *App) CommissionDaemon() (*installer.CommissionResult, error) {
	res, err := a.installer.CommissionDaemon(a.ctx)
	return res, structuredError(err)
}
