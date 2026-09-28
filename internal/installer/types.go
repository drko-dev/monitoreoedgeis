package installer

// StateCode represents the high-level lifecycle state of the Edge installer.
type StateCode string

const (
	// StateNew indicates a pristine machine where no Edge configuration exists.
	StateNew StateCode = "NEW"
	// StateSystemCheck indicates pre-flight hardware and OS evaluation is occurring.
	StateSystemCheck StateCode = "SYSTEM_CHECK"
	// StateNeedsEnrollment indicates the host requires SaaS association via a claim token.
	StateNeedsEnrollment StateCode = "NEEDS_ENROLLMENT"
	// StateEnrolled indicates the device has a valid identity and local credentials.
	StateEnrolled StateCode = "ENROLLED"
	// StateActionRequired indicates non-fatal operator intervention is required (e.g. corrupt credentials).
	StateActionRequired StateCode = "ACTION_REQUIRED"
	// StateBlocked indicates a fatal hardware, architecture, or permission condition.
	StateBlocked StateCode = "BLOCKED"
)

// PrivilegeLevel represents the process execution privilege tier.
type PrivilegeLevel string

const (
	PrivilegeStandardUser  PrivilegeLevel = "STANDARD_USER"
	PrivilegeAdministrator PrivilegeLevel = "ADMINISTRATOR"
	PrivilegeRoot          PrivilegeLevel = "ROOT"
	PrivilegeUnknown       PrivilegeLevel = "UNKNOWN"
)

// SystemReport encapsulates the safe, non-sensitive hardware and runtime properties of the host.
type SystemReport struct {
	OS                string         `json:"os"`
	Arch              string         `json:"arch"`
	EdgeVersion       string         `json:"edge_version"`
	Commit            string         `json:"commit"`
	BuildDate         string         `json:"build_date"`
	Hostname          string         `json:"hostname"`
	PrivilegeLevel    PrivilegeLevel `json:"privilege_level"`
	PlatformSupported bool           `json:"platform_supported"`
	DataDir           string         `json:"data_dir"`
	ServiceInstalled  bool           `json:"service_installed"`
	DaemonRunning     bool           `json:"daemon_running"`
	OwnsInstanceLock  bool           `json:"owns_instance_lock"`
	ConfigPresent     bool           `json:"config_present"`
	Enrolled          bool           `json:"enrolled"`
	EdgeID            string         `json:"edge_id,omitempty"`
	TotalRAMMB        uint64         `json:"total_ram_mb"`
	FreeRAMMB         uint64         `json:"free_ram_mb"`
	DiskPath          string         `json:"disk_path"`
	FreeDiskGB        uint64         `json:"free_disk_gb"`
	FFmpegPresent     bool           `json:"ffmpeg_present"`
	FFprobePath       string         `json:"ffprobe_path,omitempty"`
	HasGPUSupport     bool           `json:"has_gpu_support"`
	GPUInfo           string         `json:"gpu_info,omitempty"`
}

// InstallerState communicates the current state of the installation state machine to the UI.
type InstallerState struct {
	State              StateCode `json:"state"`
	ReasonCode         string    `json:"reason_code"`
	SafeMessage        string    `json:"safe_message"`
	Recoverable        bool      `json:"recoverable"`
	NextAllowedActions []string  `json:"next_allowed_actions"`
	DeviceID           string    `json:"device_id,omitempty"`
}

// SafeError standardizes typed, sanitized error payloads suitable for presentation without leaking secrets.
type SafeError struct {
	Code        string `json:"code"`
	SafeMessage string `json:"safe_message"`
	Recoverable bool   `json:"recoverable"`
	Details     string `json:"details,omitempty"`
}

func (e *SafeError) Error() string {
	if e == nil {
		return ""
	}
	if e.Details != "" {
		return e.SafeMessage + ": " + e.Details
	}
	return e.SafeMessage
}

// Common reason codes for InstallerState
const (
	ReasonFreshInstall        = "FRESH_INSTALL"
	ReasonUnsupportedPlatform = "UNSUPPORTED_PLATFORM"
	ReasonDeviceNotEnrolled   = "DEVICE_NOT_ENROLLED"
	ReasonDeviceEnrolled      = "DEVICE_ENROLLED"
	ReasonCredentialsCorrupt  = "CREDENTIALS_CORRUPT"
	ReasonPermissionDenied    = "PERMISSION_DENIED"
	ReasonDaemonRunning       = "DAEMON_RUNNING"
)

// Allowed Actions for UI navigation
const (
	ActionProceedToEnrollment    = "PROCEED_TO_ENROLLMENT"
	ActionRefresh                = "REFRESH"
	ActionViewDashboard          = "VIEW_DASHBOARD"
	ActionReconfigure            = "RECONFIGURE"
	ActionReEnroll               = "RE_ENROLL"
	ActionFactoryReset           = "FACTORY_RESET"
	ActionConfigureProcessingMode = "CONFIGURE_PROCESSING_MODE"
)
