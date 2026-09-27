package installer

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/agent"
	"github.com/drko-dev/monitoreoedgeis/internal/config"
	"github.com/drko-dev/monitoreoedgeis/internal/credentials"
	"github.com/drko-dev/monitoreoedgeis/internal/identity"
	"github.com/drko-dev/monitoreoedgeis/internal/platform"
)

// Service provides a safe, read-mostly application facade for the Wails desktop installer.
// It never acquires the exclusive instance lock and never leaks secrets to callers.
type Service struct {
	DataDir        string
	ConfigFilePath string
	HealthAddr     string
	httpClient     *http.Client
}

// NewService creates a configured installer service instance.
func NewService(dataDir, configFilePath string) *Service {
	if dataDir == "" {
		dataDir = config.DefaultDataDir
	}
	return &Service{
		DataDir:        dataDir,
		ConfigFilePath: configFilePath,
		HealthAddr:     config.DefaultHealthAddr,
		httpClient: &http.Client{
			Timeout: 350 * time.Millisecond,
		},
	}
}

// GetSystemReport returns non-sensitive host, environment, and Edge runtime properties.
func (s *Service) GetSystemReport(ctx context.Context) (*SystemReport, error) {
	host := platform.Detect()
	sample := platform.Collect(s.DataDir, nil)

	ffmpegPresent, ffprobePath := s.checkFFmpeg()
	gpuSupported, gpuInfo := s.checkGPU()

	configPresent := s.checkConfigPresent()
	id, _ := identity.LoadExisting(s.DataDir)
	creds, _ := credentials.Load(s.DataDir)

	enrolled := id.IsEnrolled() && creds.IsEnrolled()
	edgeID := ""
	if enrolled {
		edgeID = id.EdgeID
	}

	totalRAMMB := sample.MemTotalBytes / (1024 * 1024)
	freeRAMMB := sample.MemAvailableBytes / (1024 * 1024)

	freeDiskGB := uint64(0)
	if sample.DiskAvailableKnown {
		freeDiskGB = sample.DiskAvailableBytes / (1024 * 1024 * 1024)
	}

	daemonRunning := s.checkDaemonRunning(ctx)
	serviceInstalled := s.checkServiceInstalled()

	return &SystemReport{
		OS:                host.OS,
		Arch:              host.GOARCH,
		EdgeVersion:       agent.Version,
		Commit:            agent.Commit,
		BuildDate:         agent.BuildDate,
		Hostname:          host.Hostname,
		PrivilegeLevel:    currentPrivilegeLevel(),
		PlatformSupported: host.ArchSupported,
		DataDir:           s.DataDir,
		ServiceInstalled:  serviceInstalled,
		DaemonRunning:     daemonRunning,
		OwnsInstanceLock:  false, // Installer UI never holds the exclusive instance lock
		ConfigPresent:     configPresent,
		Enrolled:          enrolled,
		EdgeID:            edgeID,
		TotalRAMMB:        totalRAMMB,
		FreeRAMMB:         freeRAMMB,
		DiskPath:          sample.DiskDataDir,
		FreeDiskGB:        freeDiskGB,
		FFmpegPresent:     ffmpegPresent,
		FFprobePath:       ffprobePath,
		HasGPUSupport:     gpuSupported,
		GPUInfo:           gpuInfo,
	}, nil
}

// GetInstallerState evaluates the actual state of the machine and returns the derived StateCode.
func (s *Service) GetInstallerState(ctx context.Context) (*InstallerState, error) {
	report, err := s.GetSystemReport(ctx)
	if err != nil {
		return &InstallerState{
			State:              StateBlocked,
			ReasonCode:         ReasonPermissionDenied,
			SafeMessage:        "Unable to inspect host environment safely.",
			Recoverable:        false,
			NextAllowedActions: []string{ActionRefresh},
		}, nil
	}

	id, _ := identity.LoadExisting(s.DataDir)
	creds, credsErr := credentials.Load(s.DataDir)

	return DeriveInstallerState(report, id, creds, credsErr), nil
}

// DeriveInstallerState is a pure function that evaluates facts and returns the installer state.
func DeriveInstallerState(
	report *SystemReport,
	id identity.Identity,
	creds credentials.Credentials,
	credsErr error,
) *InstallerState {
	if report == nil {
		return &InstallerState{
			State:              StateBlocked,
			ReasonCode:         ReasonPermissionDenied,
			SafeMessage:        "System report is missing.",
			Recoverable:        false,
			NextAllowedActions: []string{ActionRefresh},
		}
	}

	// 1. Hardware / Platform Compatibility Barrier
	if !report.PlatformSupported {
		return &InstallerState{
			State:              StateBlocked,
			ReasonCode:         ReasonUnsupportedPlatform,
			SafeMessage:        "Current operating system or architecture is not officially supported.",
			Recoverable:        false,
			NextAllowedActions: []string{},
		}
	}

	// 2. Corrupt Credentials Check
	if credsErr != nil && errors.Is(credsErr, credentials.ErrCorrupt) {
		return &InstallerState{
			State:              StateActionRequired,
			ReasonCode:         ReasonCredentialsCorrupt,
			SafeMessage:        "Credential store on disk is corrupted. Re-enrollment or reset is required.",
			Recoverable:        true,
			NextAllowedActions: []string{ActionReEnroll, ActionFactoryReset},
		}
	}

	// 3. Fully Enrolled Device Check
	if id.IsEnrolled() && creds.IsEnrolled() {
		return &InstallerState{
			State:              StateEnrolled,
			ReasonCode:         ReasonDeviceEnrolled,
			SafeMessage:        "Device is enrolled with valid identity and credentials.",
			Recoverable:        true,
			NextAllowedActions: []string{ActionViewDashboard, ActionReconfigure, ActionRefresh},
			DeviceID:           id.EdgeID,
		}
	}

	// 4. Clean Machine / New Installation
	if !report.ConfigPresent && !id.IsEnrolled() {
		return &InstallerState{
			State:              StateNew,
			ReasonCode:         ReasonFreshInstall,
			SafeMessage:        "Fresh machine ready for GEO CAM Edge setup.",
			Recoverable:        true,
			NextAllowedActions: []string{ActionProceedToEnrollment, ActionRefresh},
		}
	}

	// 5. Unenrolled or Partially Configured Device
	return &InstallerState{
		State:              StateNeedsEnrollment,
		ReasonCode:         ReasonDeviceNotEnrolled,
		SafeMessage:        "Edge device requires SaaS enrollment.",
		Recoverable:        true,
		NextAllowedActions: []string{ActionProceedToEnrollment, ActionRefresh},
	}
}

func (s *Service) checkFFmpeg() (bool, string) {
	path, err := exec.LookPath("ffmpeg")
	if err != nil {
		return false, ""
	}
	probePath, _ := exec.LookPath("ffprobe")
	return path != "", probePath
}

func (s *Service) checkGPU() (bool, string) {
	switch runtime.GOOS {
	case "darwin":
		if runtime.GOARCH == "arm64" {
			return true, "Apple Silicon Metal Acceleration"
		}
		return false, "macOS Intel (CPU only)"
	case "linux":
		if _, err := os.Stat("/dev/nvidia0"); err == nil {
			return true, "NVIDIA GPU Detected (/dev/nvidia0)"
		}
		return false, "Generic Linux (CPU only)"
	case "windows":
		return false, "Windows Host"
	default:
		return false, "Unknown accelerator"
	}
}

func (s *Service) checkConfigPresent() bool {
	if s.ConfigFilePath != "" {
		if _, err := os.Stat(s.ConfigFilePath); err == nil {
			return true
		}
	}
	// Check common location in DataDir
	candidate := filepath.Join(s.DataDir, "config.env")
	if _, err := os.Stat(candidate); err == nil {
		return true
	}
	return false
}

func (s *Service) checkDaemonRunning(ctx context.Context) bool {
	url := "http://" + s.HealthAddr + "/healthz"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 600
}

func (s *Service) checkServiceInstalled() bool {
	switch runtime.GOOS {
	case "darwin":
		home, err := os.UserHomeDir()
		if err == nil {
			userPlist := filepath.Join(home, "Library", "LaunchAgents", "io.sidom.geocam-edge.plist")
			if _, err := os.Stat(userPlist); err == nil {
				return true
			}
		}
		daemonPlist := "/Library/LaunchDaemons/io.sidom.geocam-edge.plist"
		if _, err := os.Stat(daemonPlist); err == nil {
			return true
		}
		return false
	case "linux":
		candidates := []string{
			"/etc/systemd/system/geocam-edge.service",
			"/lib/systemd/system/geocam-edge.service",
		}
		for _, c := range candidates {
			if _, err := os.Stat(c); err == nil {
				return true
			}
		}
		return false
	case "windows":
		// Windows service check degrades safely to false without elevated probe
		return false
	default:
		return false
	}
}

// SanitizeError turns an internal error into a safe user-facing message.
func SanitizeError(err error) SafeError {
	if err == nil {
		return SafeError{
			Code:        "OK",
			SafeMessage: "Operation completed successfully.",
			Recoverable: true,
		}
	}

	var safeErr *SafeError
	if errors.As(err, &safeErr) {
		return *safeErr
	}

	msg := err.Error()
	// Redact potential connection strings or paths
	if strings.Contains(msg, "rtsp://") {
		return SafeError{
			Code:        "STREAM_ERROR",
			SafeMessage: "Failed to connect to camera stream. Check camera IP and credentials.",
			Recoverable: true,
		}
	}

	if errors.Is(err, credentials.ErrCorrupt) {
		return SafeError{
			Code:        "CREDENTIALS_CORRUPT",
			SafeMessage: "Device credential store is corrupted.",
			Recoverable: true,
		}
	}

	if os.IsPermission(err) {
		return SafeError{
			Code:        "PERMISSION_DENIED",
			SafeMessage: "Permission denied accessing local storage.",
			Recoverable: false,
		}
	}

	return SafeError{
		Code:        "INTERNAL_ERROR",
		SafeMessage: "An unexpected error occurred. Please retry.",
		Recoverable: true,
	}
}
