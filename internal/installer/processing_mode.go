package installer

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/drko-dev/monitoreoedgeis/internal/config"
	"github.com/drko-dev/monitoreoedgeis/internal/fulledge"
)

// ProcessingMode is the product-level mode the UI exposes: exactly three
// values. "Gateway" is deliberately not a fourth mode here -- it is the
// commercial name for cloud + video pipeline enabled, an effective profile
// derived from config.ProfileFor, not something an operator selects directly.
// See docs/product/COMMERCIAL_MODES.md section 1.
type ProcessingMode string

const (
	ProcessingModeCloud    ProcessingMode = "cloud"
	ProcessingModeHybrid   ProcessingMode = "hybrid"
	ProcessingModeFullEdge ProcessingMode = "full_edge"
)

// configMode translates the product-level mode to the config package's
// ProcessingMode string, the only vocabulary GEOCAM_PROCESSING_MODE
// understands.
func (m ProcessingMode) configMode() (config.ProcessingMode, error) {
	switch m {
	case ProcessingModeCloud:
		return config.ModeCloud, nil
	case ProcessingModeHybrid:
		return config.ModeHybrid, nil
	case ProcessingModeFullEdge:
		return config.ModeEdge, nil
	default:
		return "", fmt.Errorf("installer: unknown processing mode %q", m)
	}
}

func processingModeFromConfig(mode config.ProcessingMode) ProcessingMode {
	switch mode {
	case config.ModeCloud:
		return ProcessingModeCloud
	case config.ModeHybrid:
		return ProcessingModeHybrid
	case config.ModeEdge:
		return ProcessingModeFullEdge
	default:
		return ""
	}
}

// CapabilityStatus classifies whether a processing mode can actually run on
// this host, from facts this package can observe -- never an invented
// hardware minimum (docs/product/COMMERCIAL_MODES.md sections 4 and 7: no
// RAM/disk floor is established, and GPU is never a requirement).
type CapabilityStatus string

const (
	CapabilitySupported             CapabilityStatus = "SUPPORTED"
	CapabilitySupportedWithWarnings CapabilityStatus = "SUPPORTED_WITH_WARNINGS"
	CapabilityUnavailable           CapabilityStatus = "UNAVAILABLE"
)

// ProcessingModeOption describes one selectable product mode and whether
// this host can actually run it right now.
type ProcessingModeOption struct {
	Mode              ProcessingMode   `json:"mode"`
	DisplayName       string           `json:"display_name"`
	Description       string           `json:"description"`
	LocalCompute      string           `json:"local_compute"`
	NetworkDependency string           `json:"network_dependency"`
	InferenceLocation string           `json:"inference_location"`
	Capability        CapabilityStatus `json:"capability"`
	CapabilityReason  string           `json:"capability_reason,omitempty"`
	Warnings          []string         `json:"warnings,omitempty"`
	Blockers          []string         `json:"blockers,omitempty"`
}

// CurrentProcessingMode reports the processing mode this Edge is configured
// for and, when the daemon is reachable, actually running.
type CurrentProcessingMode struct {
	Mode             ProcessingMode `json:"mode"`
	PipelineEnabled  bool           `json:"pipeline_enabled"`
	EffectiveProfile config.Profile `json:"effective_profile"`
	// Source is "runtime" when read from the live daemon's /status,
	// "config" when read from the persisted config file with the daemon
	// unreachable, or "default" when nothing has been configured yet.
	Source string `json:"source"`
}

// ProcessingModeRequest is the only shape the frontend can send: a requested
// product mode, nothing else. No raw env map, file path, command, or
// arbitrary config document is representable here.
type ProcessingModeRequest struct {
	Mode ProcessingMode `json:"mode"`
}

// ProcessingModePlan is what PlanProcessingMode returns. It never mutates
// anything -- it describes what Apply would do.
type ProcessingModePlan struct {
	RequestedMode           ProcessingMode    `json:"requested_mode"`
	CurrentMode             ProcessingMode    `json:"current_mode"`
	CurrentEffectiveProfile config.Profile    `json:"current_effective_profile"`
	TargetEffectiveProfile  config.Profile    `json:"target_effective_profile"`
	ConfigChanges           map[string]string `json:"config_changes"`
	RestartRequired         bool              `json:"restart_required"`
	ComponentsRequired      []string          `json:"components_required,omitempty"`
	Warnings                []string          `json:"warnings,omitempty"`
	Blockers                []string          `json:"blockers,omitempty"`
	RollbackAvailable       bool              `json:"rollback_available"`
}

// ApplyStatus is the terminal outcome of ApplyProcessingMode.
type ApplyStatus string

const (
	// ApplyStatusSuccess: the daemon was not running, so the new config was
	// written and verified on disk. It will take effect at next start.
	ApplyStatusSuccess ApplyStatus = "SUCCESS"
	// ApplyStatusRestartRequired: the new config was written and verified on
	// disk, but a daemon is already running an older configuration and this
	// installer has no local channel to reload or restart it (see
	// docs/product/UX3_PROCESSING_MODE_CONFIGURATION.md, "Restart model").
	ApplyStatusRestartRequired ApplyStatus = "RESTART_REQUIRED"
	// ApplyStatusRolledBack: the write succeeded but reading it back
	// disagreed with what was requested, so the previous configuration was
	// restored automatically.
	ApplyStatusRolledBack ApplyStatus = "ROLLED_BACK"
	// ApplyStatusBlocked: the requested mode is UNAVAILABLE on this host, or
	// rollback itself failed after a verification mismatch. Nothing was
	// written in the first case; a manual fix is required in the second.
	ApplyStatusBlocked ApplyStatus = "BLOCKED"
)

// ProcessingModeApplyResult is the required contract: what was requested,
// what was expected, what the config now actually says, and whether they
// match. Never shows Status success when Match is false.
type ProcessingModeApplyResult struct {
	RequestedProductMode     ProcessingMode `json:"requested_product_mode"`
	ExpectedProcessingMode   string         `json:"expected_processing_mode"`
	ExpectedEffectiveProfile config.Profile `json:"expected_effective_profile"`
	ActualProcessingMode     string         `json:"actual_processing_mode"`
	ActualEffectiveProfile   config.Profile `json:"actual_effective_profile"`
	Match                    bool           `json:"match"`
	Status                   ApplyStatus    `json:"status"`
	RestartRequired          bool           `json:"restart_required"`
	RolledBack               bool           `json:"rolled_back"`
	SafeMessage              string         `json:"safe_message"`
	Warnings                 []string       `json:"warnings,omitempty"`
}

// GetProcessingModeOptions returns the three selectable product modes with
// their real, observed capability on this host.
func (s *Service) GetProcessingModeOptions(ctx context.Context) ([]ProcessingModeOption, error) {
	report, err := s.GetSystemReport(ctx)
	if err != nil {
		return nil, &SafeError{
			Code:        "SYSTEM_REPORT_FAILED",
			SafeMessage: "Unable to inspect host environment safely.",
			Recoverable: true,
			Details:     err.Error(),
		}
	}

	modes := []struct {
		mode              ProcessingMode
		displayName       string
		description       string
		localCompute      string
		networkDependency string
		inferenceLocation string
	}{
		{ProcessingModeCloud, "Cloud", "Inference runs in GEO CAM Cloud", "Lower", "Higher", "Cloud"},
		{ProcessingModeHybrid, "Hybrid", "Local motion/candidate filtering; inference runs in Cloud", "Moderate", "Reduced", "Cloud"},
		{ProcessingModeFullEdge, "Full Edge", "Inference runs on this device", "Higher", "Lowest", "Local device"},
	}

	options := make([]ProcessingModeOption, 0, len(modes))
	for _, m := range modes {
		status, reason, warnings, blockers := s.evaluateCapability(m.mode, report)
		options = append(options, ProcessingModeOption{
			Mode:              m.mode,
			DisplayName:       m.displayName,
			Description:       m.description,
			LocalCompute:      m.localCompute,
			NetworkDependency: m.networkDependency,
			InferenceLocation: m.inferenceLocation,
			Capability:        status,
			CapabilityReason:  reason,
			Warnings:          warnings,
			Blockers:          blockers,
		})
	}
	return options, nil
}

// evaluateCapability checks only facts this package can actually observe:
// platform support, ffmpeg presence (required for the local decode every
// product mode runs, per COMMERCIAL_MODES.md section 2), and, for Full Edge
// only, whether a local vision worker command and its model files are
// actually configured and present -- because without them Full Edge cannot
// infer at all, not because of an invented minimum.
func (s *Service) evaluateCapability(mode ProcessingMode, report *SystemReport) (status CapabilityStatus, reason string, warnings, blockers []string) {
	if !report.PlatformSupported {
		return CapabilityUnavailable, "Current operating system or architecture is not officially supported.",
			nil, []string{"Unsupported platform."}
	}
	if !report.FFmpegPresent {
		return CapabilityUnavailable, "ffmpeg is required for the local video pipeline and was not found on PATH.",
			nil, []string{"ffmpeg not found."}
	}

	if mode != ProcessingModeFullEdge {
		return CapabilitySupported, "", nil, nil
	}

	workerCmd, hasWorkerCmd, err := config.PersistentValue("GEOCAM_EDGE_YOLO_WORKER_CMD")
	if err != nil {
		return CapabilityUnavailable, "Stored configuration could not be read.", nil, []string{err.Error()}
	}
	workerCmd = strings.TrimSpace(workerCmd)
	if !hasWorkerCmd || workerCmd == "" {
		return CapabilityUnavailable, "Local vision runtime is not configured (GEOCAM_EDGE_YOLO_WORKER_CMD is unset).",
			nil, []string{"Local vision worker command is not configured."}
	}
	if !commandResolvable(workerCmd) {
		return CapabilityUnavailable, "Configured local vision worker command was not found.",
			nil, []string{fmt.Sprintf("Vision worker command not found: %s", workerCmd)}
	}

	modelsDir, hasModelsDir, err := config.PersistentValue("GEOCAM_EDGE_YOLO_MODELS_DIR")
	if err != nil {
		return CapabilityUnavailable, "Stored configuration could not be read.", nil, []string{err.Error()}
	}
	if !hasModelsDir || strings.TrimSpace(modelsDir) == "" {
		modelsDir = filepath.Join(report.DataDir, config.DefaultEdgeYOLOModelsDirName)
	}
	personModel, _, _ := config.PersistentValue("GEOCAM_EDGE_YOLO_PERSON_MODEL")
	if strings.TrimSpace(personModel) == "" {
		personModel = config.DefaultEdgeYOLOPersonModel
	}
	vehicleModel, _, _ := config.PersistentValue("GEOCAM_EDGE_YOLO_VEHICLE_MODEL")
	if strings.TrimSpace(vehicleModel) == "" {
		vehicleModel = config.DefaultEdgeYOLOVehicleModel
	}

	var missing []string
	if _, err := os.Stat(filepath.Join(modelsDir, personModel)); err != nil {
		missing = append(missing, personModel)
	}
	if _, err := os.Stat(filepath.Join(modelsDir, vehicleModel)); err != nil {
		missing = append(missing, vehicleModel)
	}
	if len(missing) > 0 {
		return CapabilityUnavailable, "Required model file(s) not present.",
			nil, []string{fmt.Sprintf("Missing model file(s): %s", strings.Join(missing, ", "))}
	}

	device, _, _ := config.PersistentValue("GEOCAM_EDGE_YOLO_DEVICE")
	if strings.EqualFold(strings.TrimSpace(device), "cuda") && !(fulledge.DefaultHardwareDetector{}).DetectCUDA() {
		return CapabilitySupportedWithWarnings, "CUDA was requested but is not detected on this host; the worker falls back to CPU.",
			[]string{"CUDA requested but not detected; will run on CPU."}, nil
	}

	return CapabilitySupported, "", nil, nil
}

// commandResolvable reports whether cmd names an existing, resolvable
// executable: an absolute or relative path is stat'd directly, a bare name is
// looked up on PATH.
func commandResolvable(cmd string) bool {
	if strings.ContainsRune(cmd, os.PathSeparator) {
		info, err := os.Stat(cmd)
		return err == nil && !info.IsDir()
	}
	_, err := exec.LookPath(cmd)
	return err == nil
}

// statusSnapshot mirrors only the fields of health.Snapshot this package
// needs from a live /status response.
type statusSnapshot struct {
	ProcessingMode string         `json:"processing_mode"`
	Profile        config.Profile `json:"profile"`
}

// fetchLiveStatus queries the running daemon's /status endpoint. It returns
// ok=false whenever the daemon cannot be reached or its response cannot be
// parsed -- callers must not treat that as a mode mismatch, only as "no live
// data available".
func (s *Service) fetchLiveStatus(ctx context.Context) (snap statusSnapshot, ok bool) {
	url := "http://" + s.HealthAddr + "/status"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return statusSnapshot{}, false
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return statusSnapshot{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return statusSnapshot{}, false
	}
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		return statusSnapshot{}, false
	}
	return snap, true
}

// GetCurrentProcessingMode reports the mode this Edge is configured for and,
// when the daemon is reachable, actually running. It never trusts frontend or
// in-memory state: reopening the app always re-derives this from the live
// daemon or, failing that, the persisted config file.
func (s *Service) GetCurrentProcessingMode(ctx context.Context) (*CurrentProcessingMode, error) {
	if s.checkDaemonRunning(ctx) {
		if snap, ok := s.fetchLiveStatus(ctx); ok {
			mode, err := config.ParseProcessingMode(snap.ProcessingMode)
			if err == nil {
				return &CurrentProcessingMode{
					Mode:             processingModeFromConfig(mode),
					PipelineEnabled:  snap.Profile != config.ProfileGatewayNoMedia,
					EffectiveProfile: snap.Profile,
					Source:           "runtime",
				}, nil
			}
		}
	}

	modeRaw, hasMode, err := config.PersistentFileValue("GEOCAM_PROCESSING_MODE")
	if err != nil {
		return nil, &SafeError{
			Code:        "CONFIG_INVALID",
			SafeMessage: "Stored configuration could not be read.",
			Recoverable: true,
			Details:     err.Error(),
		}
	}
	pipelineRaw, hasPipeline, err := config.PersistentFileValue("GEOCAM_VIDEO_PIPELINE_ENABLED")
	if err != nil {
		return nil, &SafeError{
			Code:        "CONFIG_INVALID",
			SafeMessage: "Stored configuration could not be read.",
			Recoverable: true,
			Details:     err.Error(),
		}
	}

	mode := config.DefaultProcessingMode
	if hasMode {
		if parsed, err := config.ParseProcessingMode(modeRaw); err == nil {
			mode = parsed
		}
	}
	pipelineEnabled := config.DefaultVideoPipelineEnabled
	if hasPipeline {
		pipelineEnabled = strings.EqualFold(pipelineRaw, "true") || pipelineRaw == "1"
	}

	source := "config"
	if !hasMode && !hasPipeline {
		source = "default"
	}

	return &CurrentProcessingMode{
		Mode:             processingModeFromConfig(mode),
		PipelineEnabled:  pipelineEnabled,
		EffectiveProfile: config.ProfileFor(mode, pipelineEnabled),
		Source:           source,
	}, nil
}

// ValidateProcessingMode checks a requested mode against real, observed host
// facts. It never mutates anything.
func (s *Service) ValidateProcessingMode(ctx context.Context, req ProcessingModeRequest) (*ProcessingModeOption, error) {
	if _, err := req.Mode.configMode(); err != nil {
		return nil, &SafeError{
			Code:        "INVALID_MODE",
			SafeMessage: "Requested processing mode is not recognized.",
			Recoverable: true,
		}
	}
	options, err := s.GetProcessingModeOptions(ctx)
	if err != nil {
		return nil, err
	}
	for i := range options {
		if options[i].Mode == req.Mode {
			return &options[i], nil
		}
	}
	return nil, &SafeError{Code: "INVALID_MODE", SafeMessage: "Requested processing mode is not recognized.", Recoverable: true}
}

// PlanProcessingMode describes what ApplyProcessingMode would do, without
// mutating any configuration.
func (s *Service) PlanProcessingMode(ctx context.Context, req ProcessingModeRequest) (*ProcessingModePlan, error) {
	targetConfigMode, err := req.Mode.configMode()
	if err != nil {
		return nil, &SafeError{Code: "INVALID_MODE", SafeMessage: "Requested processing mode is not recognized.", Recoverable: true}
	}
	option, err := s.ValidateProcessingMode(ctx, req)
	if err != nil {
		return nil, err
	}
	current, err := s.GetCurrentProcessingMode(ctx)
	if err != nil {
		return nil, err
	}

	configChanges := map[string]string{}
	if currentModeRaw, ok, _ := config.PersistentFileValue("GEOCAM_PROCESSING_MODE"); !ok || currentModeRaw != string(targetConfigMode) {
		configChanges["GEOCAM_PROCESSING_MODE"] = string(targetConfigMode)
	}
	if currentPipelineRaw, ok, _ := config.PersistentFileValue("GEOCAM_VIDEO_PIPELINE_ENABLED"); !ok || !(strings.EqualFold(currentPipelineRaw, "true") || currentPipelineRaw == "1") {
		configChanges["GEOCAM_VIDEO_PIPELINE_ENABLED"] = "true"
	}

	var componentsRequired []string
	if req.Mode == ProcessingModeFullEdge {
		componentsRequired = []string{"Local vision worker process", "Person detection model", "Vehicle detection model"}
	}

	return &ProcessingModePlan{
		RequestedMode:           req.Mode,
		CurrentMode:             current.Mode,
		CurrentEffectiveProfile: current.EffectiveProfile,
		TargetEffectiveProfile:  config.ProfileFor(targetConfigMode, true),
		ConfigChanges:           configChanges,
		RestartRequired:         s.checkDaemonRunning(ctx),
		ComponentsRequired:      componentsRequired,
		Warnings:                option.Warnings,
		Blockers:                option.Blockers,
		RollbackAvailable:       true,
	}, nil
}

// ApplyProcessingMode atomically persists the requested processing mode and
// verifies the write by reading it back before reporting success. It never
// runs two applies concurrently, never writes when the mode is UNAVAILABLE,
// and automatically restores the previous configuration if the read-back
// disagrees with what was written.
//
// It cannot reload or restart an already-running daemon (see ApplyStatus
// doc comments): this installer holds no channel to do so today, so a
// running daemon always yields RESTART_REQUIRED rather than a false SUCCESS.
func (s *Service) ApplyProcessingMode(ctx context.Context, req ProcessingModeRequest) (*ProcessingModeApplyResult, error) {
	if !s.applyMu.TryLock() {
		return nil, &SafeError{
			Code:        "APPLY_IN_PROGRESS",
			SafeMessage: "Another processing mode change is already being applied.",
			Recoverable: true,
		}
	}
	defer s.applyMu.Unlock()

	targetConfigMode, err := req.Mode.configMode()
	if err != nil {
		return nil, &SafeError{Code: "INVALID_MODE", SafeMessage: "Requested processing mode is not recognized.", Recoverable: true}
	}

	plan, err := s.PlanProcessingMode(ctx, req)
	if err != nil {
		return nil, err
	}
	if len(plan.Blockers) > 0 {
		return &ProcessingModeApplyResult{
			RequestedProductMode: req.Mode,
			Status:               ApplyStatusBlocked,
			SafeMessage:          "This mode is not available on this host.",
			Warnings:             plan.Blockers,
		}, nil
	}

	snapshot, existed, err := config.PersistentFileRaw()
	if err != nil {
		return nil, &SafeError{
			Code:        "CONFIG_INVALID",
			SafeMessage: "Unable to read current configuration before applying.",
			Recoverable: true,
			Details:     err.Error(),
		}
	}

	updates := map[string]string{
		"GEOCAM_PROCESSING_MODE":        string(targetConfigMode),
		"GEOCAM_VIDEO_PIPELINE_ENABLED": "true",
	}
	if err := config.WritePersistentValues(updates); err != nil {
		return nil, &SafeError{
			Code:        "CONFIG_WRITE_FAILED",
			SafeMessage: "Failed to write the new configuration. No change was made.",
			Recoverable: true,
			Details:     err.Error(),
		}
	}

	expectedProfile := config.ProfileFor(targetConfigMode, true)

	readMode, _, readModeErr := config.PersistentFileValue("GEOCAM_PROCESSING_MODE")
	readPipelineRaw, _, readPipelineErr := config.PersistentFileValue("GEOCAM_VIDEO_PIPELINE_ENABLED")
	readPipeline := strings.EqualFold(readPipelineRaw, "true") || readPipelineRaw == "1"

	diskVerified := readModeErr == nil && readPipelineErr == nil &&
		readMode == string(targetConfigMode) && readPipeline

	if !diskVerified {
		if rbErr := config.RestorePersistentFileRaw(snapshot, existed); rbErr != nil {
			return nil, &SafeError{
				Code:        "ROLLBACK_FAILED",
				SafeMessage: "Configuration verification failed and automatic rollback also failed. Manual intervention required.",
				Recoverable: false,
				Details:     rbErr.Error(),
			}
		}
		return &ProcessingModeApplyResult{
			RequestedProductMode:     req.Mode,
			ExpectedProcessingMode:   string(targetConfigMode),
			ExpectedEffectiveProfile: expectedProfile,
			ActualProcessingMode:     readMode,
			Match:                    false,
			Status:                   ApplyStatusRolledBack,
			RolledBack:               true,
			SafeMessage:              "Configuration was applied, but could not be verified on disk. The previous configuration was restored.",
		}, nil
	}

	actualProfile := config.ProfileFor(config.ProcessingMode(readMode), readPipeline)

	if !s.checkDaemonRunning(ctx) {
		return &ProcessingModeApplyResult{
			RequestedProductMode:     req.Mode,
			ExpectedProcessingMode:   string(targetConfigMode),
			ExpectedEffectiveProfile: expectedProfile,
			ActualProcessingMode:     readMode,
			ActualEffectiveProfile:   actualProfile,
			Match:                    true,
			Status:                   ApplyStatusSuccess,
			RestartRequired:          false,
			SafeMessage:              "Configuration saved. It will take effect the next time GEO CAM Edge starts.",
		}, nil
	}

	if snap, ok := s.fetchLiveStatus(ctx); ok && snap.ProcessingMode == string(targetConfigMode) && snap.Profile == expectedProfile {
		return &ProcessingModeApplyResult{
			RequestedProductMode:     req.Mode,
			ExpectedProcessingMode:   string(targetConfigMode),
			ExpectedEffectiveProfile: expectedProfile,
			ActualProcessingMode:     snap.ProcessingMode,
			ActualEffectiveProfile:   snap.Profile,
			Match:                    true,
			Status:                   ApplyStatusSuccess,
			RestartRequired:          false,
			SafeMessage:              "Configuration saved and already running as requested.",
		}, nil
	}

	liveMode := readMode
	liveProfile := actualProfile
	if snap, ok := s.fetchLiveStatus(ctx); ok {
		liveMode = snap.ProcessingMode
		liveProfile = snap.Profile
	}

	return &ProcessingModeApplyResult{
		RequestedProductMode:     req.Mode,
		ExpectedProcessingMode:   string(targetConfigMode),
		ExpectedEffectiveProfile: expectedProfile,
		ActualProcessingMode:     liveMode,
		ActualEffectiveProfile:   liveProfile,
		Match:                    false,
		Status:                   ApplyStatusRestartRequired,
		RestartRequired:          true,
		SafeMessage:              "Configuration saved. GEO CAM Edge must be restarted for this change to take effect.",
	}, nil
}
