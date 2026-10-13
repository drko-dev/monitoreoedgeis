package installer

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

//go:embed assets/wheelhouse-lock.json
var embeddedWheelhouseLockJSON []byte

// Standard wheelhouse error sentinels.
var (
	ErrWheelhouseLockNotFound       = errors.New("wheelhouse lock file not found")
	ErrWheelhouseLockInvalidJSON    = errors.New("wheelhouse lock contains invalid JSON")
	ErrWheelhouseLockSchemaInvalid  = errors.New("wheelhouse lock schema validation failed")
	ErrWheelhouseIncompatible       = errors.New("wheelhouse is incompatible with target platform or python ABI")
	ErrWheelMissing                 = errors.New("wheel artifact missing from wheelhouse")
	ErrWheelCorrupted               = errors.New("wheel artifact corrupted or hash mismatch")
	ErrWheelSecurityEscape          = errors.New("wheel artifact path violates security constraints")
	ErrWheelDuplicate               = errors.New("duplicate wheel dependency detected in lock")
	ErrWheelInvalidFilename         = errors.New("invalid wheel filename format (PEP 427 violation)")
	ErrWheelSourceDistRejected      = errors.New("source distributions (.tar.gz, .zip) are strictly rejected in offline wheelhouse")
	ErrWheelOfflineInstallFailed    = errors.New("offline pip installation failed")
	ErrWheelImportsValidationFailed = errors.New("validation of required python imports failed")
	ErrWheelTransitiveDepMissing    = errors.New("required transitive dependency missing from lock")
	ErrWheelPipCheckFailed          = errors.New("pip check consistency validation failed")
	ErrWheelAuditMismatch           = errors.New("installed distributions audit mismatch against lock")
	ErrWheelStagingIncomplete       = errors.New("staged site-packages dependencies incomplete or invalid")
	ErrWheelActivationFailed        = errors.New("site-packages directory activation swap failed")
	ErrWheelRollbackFailed          = errors.New("site-packages rollback failed to restore previous valid installation")
)

// Typed error codes for SafeError.
const (
	ErrCodeWheelhouseLockInvalid     = "WHEELHOUSE_LOCK_INVALID"
	ErrCodeWheelhouseIncompatible    = "WHEELHOUSE_INCOMPATIBLE"
	ErrCodeWheelMissing              = "WHEEL_MISSING"
	ErrCodeWheelCorrupted            = "WHEEL_CORRUPTED"
	ErrCodeWheelHashMismatch         = "WHEEL_HASH_MISMATCH"
	ErrCodeWheelSizeMismatch         = "WHEEL_SIZE_MISMATCH"
	ErrCodeWheelABIIncompatible      = "WHEEL_ABI_INCOMPATIBLE"
	ErrCodeWheelArchIncompatible     = "WHEEL_ARCH_INCOMPATIBLE"
	ErrCodeWheelSourceDist           = "WHEEL_SOURCE_DIST_REJECTED"
	ErrCodeWheelDuplicate            = "WHEEL_DUPLICATE"
	ErrCodeWheelInvalidFilename      = "WHEEL_INVALID_FILENAME"
	ErrCodeWheelSecurityEscape       = "WHEEL_SECURITY_ESCAPE"
	ErrCodeWheelOfflineInstallFailed = "WHEEL_OFFLINE_INSTALL_FAILED"
	ErrCodeWheelImportsFailed        = "WHEEL_IMPORTS_FAILED"
	ErrCodeWheelTransitiveDepMissing = "WHEEL_TRANSITIVE_DEP_MISSING"
	ErrCodeWheelPipCheckFailed       = "WHEEL_PIP_CHECK_FAILED"
	ErrCodeWheelAuditMismatch        = "WHEEL_AUDIT_MISMATCH"
	ErrCodeWheelStagingFailed        = "WHEEL_STAGING_FAILED"
	ErrCodeWheelActivationFailed     = "WHEEL_ACTIVATION_FAILED"
	ErrCodeWheelRollbackFailed       = "WHEEL_ROLLBACK_FAILED"
)

// Lifecycle readiness states for the offline Python runtime.
const (
	StateWheelsUnchecked       = "WHEELS_UNCHECKED"
	StateWheelsVerified        = "WHEELS_VERIFIED"
	StateDependenciesInstalled = "DEPENDENCIES_INSTALLED"
	StateImportsVerified       = "IMPORTS_VERIFIED"
	// StateRuntimeReady from embedded_python.go indicates interpreter + wheels + worker imports are verified.
	// StateInferenceReady indicates model weights are present and loaded.
)

var (
	// PEP 427 wheel filename regex: {distribution}-{version}(-{build})?-{python}-{abi}-{platform}.whl
	wheelFilenameRegex = regexp.MustCompile(`^([a-zA-Z0-9_\.\+]+)-([a-zA-Z0-9_\.\+]+)(?:-([0-9]+[a-zA-Z0-9_\.]*))?-([a-zA-Z0-9_\.\+]+)-([a-zA-Z0-9_\.\+]+)-([a-zA-Z0-9_\.\+]+)\.whl$`)
)

// WheelDependencySpec specifies an exact offline wheel dependency.
type WheelDependencySpec struct {
	Name          string `json:"name"`
	CanonicalName string `json:"canonical_name"`
	Version       string `json:"version"`
	Filename      string `json:"filename"`
	PythonTag     string `json:"python_tag"`
	ABITag        string `json:"abi_tag"`
	PlatformTag   string `json:"platform_tag"`
	SizeBytes     int64  `json:"size_bytes"`
	SHA256        string `json:"sha256"`
	Origin        string `json:"origin"`
	License       string `json:"license"`
}

// WheelhouseLock defines the formal, immutable lockfile for all runtime Python wheels.
type WheelhouseLock struct {
	SchemaVersion      string                `json:"schema_version"`
	TargetPlatform     string                `json:"target_platform"`
	TargetArchitecture string                `json:"target_architecture"`
	PythonVersion      string                `json:"python_version"`
	PythonABI          string                `json:"python_abi"`
	WheelCount         int                   `json:"wheel_count"`
	TotalSizeBytes     int64                 `json:"total_size_bytes"`
	Dependencies       []WheelDependencySpec `json:"dependencies"`
}

// WheelhouseIntegrityReport holds the results of verifying wheelhouse files on disk.
type WheelhouseIntegrityReport struct {
	Valid         bool     `json:"valid"`
	CheckedCount  int      `json:"checked_count"`
	TotalBytes    int64    `json:"total_bytes"`
	VerifiedFiles []string `json:"verified_files"`
	Errors        []string `json:"errors,omitempty"`
}

// InstallReport captures the output and status of the offline wheel installation.
type InstallReport struct {
	Success        bool    `json:"success"`
	InstalledCount int     `json:"installed_count"`
	DurationMs     float64 `json:"duration_ms"`
	Output         string  `json:"output"`
}

// ImportsValidationResult captures the verification of critical python packages and hardware acceleration.
type ImportsValidationResult struct {
	Success            bool   `json:"success"`
	TorchVersion       string `json:"torch_version"`
	TorchvisionVersion string `json:"torchvision_version"`
	UltralyticsVersion string `json:"ultralytics_version"`
	PillowVersion      string `json:"pillow_version"`
	OpenCVVersion      string `json:"opencv_version"`
	NumPyVersion       string `json:"numpy_version"`
	MPSAvailable       bool   `json:"mps_available"`
	MPSTensorOK        bool   `json:"mps_tensor_ok"`
	CPUTensorOK        bool   `json:"cpu_tensor_ok"`
	RawOutput          string `json:"raw_output,omitempty"`
}

// WorkerSmokeTestResult captures the smoke test of the vision worker backend.
type WorkerSmokeTestResult struct {
	Success         bool   `json:"success"`
	BackendImported bool   `json:"backend_imported"`
	DeviceResolved  string `json:"device_resolved"`
	Warning         string `json:"warning,omitempty"`
	Details         string `json:"details,omitempty"`
}

// PipCommandExecutor defines execution signature for running pip commands.
type PipCommandExecutor func(ctx context.Context, binary string, env []string, args ...string) ([]byte, error)

// LoadEmbeddedWheelhouseLock unmarshals the embedded default lockfile.
func LoadEmbeddedWheelhouseLock() (*WheelhouseLock, error) {
	if len(embeddedWheelhouseLockJSON) == 0 {
		return nil, ErrWheelhouseLockNotFound
	}
	var lock WheelhouseLock
	if err := json.Unmarshal(embeddedWheelhouseLockJSON, &lock); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrWheelhouseLockInvalidJSON, err)
	}
	if err := lock.ValidateSchema(); err != nil {
		return nil, err
	}
	return &lock, nil
}

// LoadWheelhouseLock reads and validates a lockfile from the filesystem.
func LoadWheelhouseLock(path string) (*WheelhouseLock, error) {
	cleanPath := filepath.Clean(path)
	data, err := os.ReadFile(cleanPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrWheelhouseLockNotFound
		}
		return nil, fmt.Errorf("failed to read wheelhouse lock: %w", err)
	}
	var lock WheelhouseLock
	if err := json.Unmarshal(data, &lock); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrWheelhouseLockInvalidJSON, err)
	}
	if err := lock.ValidateSchema(); err != nil {
		return nil, err
	}
	return &lock, nil
}

// ValidateSchema verifies all fields, integrity constraints, and absence of loose ranges or sdist.
func (l *WheelhouseLock) ValidateSchema() error {
	if l == nil {
		return ErrWheelhouseLockSchemaInvalid
	}
	if l.SchemaVersion != CurrentSchemaVersion {
		return fmt.Errorf("%w: unsupported schema_version %q (expected %s)", ErrWheelhouseLockSchemaInvalid, l.SchemaVersion, CurrentSchemaVersion)
	}
	if l.TargetPlatform != PlatformDarwin && l.TargetPlatform != PlatformLinux && l.TargetPlatform != PlatformWindows {
		return fmt.Errorf("%w: invalid target_platform %q", ErrWheelhouseLockSchemaInvalid, l.TargetPlatform)
	}
	if l.TargetArchitecture != ArchArm64 && l.TargetArchitecture != ArchAmd64 {
		return fmt.Errorf("%w: invalid target_architecture %q", ErrWheelhouseLockSchemaInvalid, l.TargetArchitecture)
	}
	if !strings.HasPrefix(l.PythonVersion, "3.11") {
		return fmt.Errorf("%w: invalid python_version %q (expected 3.11.x)", ErrWheelhouseLockSchemaInvalid, l.PythonVersion)
	}
	if l.PythonABI != "cp311" {
		return fmt.Errorf("%w: invalid python_abi %q (expected cp311)", ErrWheelhouseLockSchemaInvalid, l.PythonABI)
	}
	if len(l.Dependencies) == 0 {
		return fmt.Errorf("%w: dependencies list cannot be empty", ErrWheelhouseLockSchemaInvalid)
	}
	if l.WheelCount != len(l.Dependencies) {
		return fmt.Errorf("%w: wheel_count mismatch (stated %d, found %d)", ErrWheelhouseLockSchemaInvalid, l.WheelCount, len(l.Dependencies))
	}

	seenNames := make(map[string]bool)
	seenFiles := make(map[string]bool)
	var calcTotalBytes int64

	hasTorch := false
	hasTorchvision := false
	hasUltralytics := false
	hasPillow := false

	for i, dep := range l.Dependencies {
		normName := strings.ToLower(strings.ReplaceAll(dep.Name, "_", "-"))
		if normName == "" {
			return fmt.Errorf("%w: dependency at index %d has empty name", ErrWheelhouseLockSchemaInvalid, i)
		}
		if seenNames[normName] {
			return fmt.Errorf("%w: %s is duplicated in dependencies", ErrWheelDuplicate, dep.Name)
		}
		seenNames[normName] = true

		if normName == "torch" {
			hasTorch = true
		} else if normName == "torchvision" {
			hasTorchvision = true
		} else if normName == "ultralytics" {
			hasUltralytics = true
		} else if normName == "pillow" {
			hasPillow = true
		}

		if dep.Version == "" {
			return fmt.Errorf("%w: dependency %s has empty version", ErrWheelhouseLockSchemaInvalid, dep.Name)
		}
		if looseRangeRegex.MatchString(dep.Version) {
			return fmt.Errorf("%w: dependency %s version %q contains loose version specifiers", ErrWheelhouseLockSchemaInvalid, dep.Name, dep.Version)
		}

		if dep.Filename == "" {
			return fmt.Errorf("%w: dependency %s has empty filename", ErrWheelhouseLockSchemaInvalid, dep.Name)
		}

		// Reject source distributions immediately (.tar.gz, .zip, .tgz)
		lowerFile := strings.ToLower(dep.Filename)
		if strings.HasSuffix(lowerFile, ".tar.gz") || strings.HasSuffix(lowerFile, ".zip") || strings.HasSuffix(lowerFile, ".tgz") {
			return fmt.Errorf("%w: dependency %s specifies source distribution %s", ErrWheelSourceDistRejected, dep.Name, dep.Filename)
		}
		if !strings.HasSuffix(lowerFile, ".whl") {
			return fmt.Errorf("%w: dependency %s filename %q does not end with .whl", ErrWheelInvalidFilename, dep.Name, dep.Filename)
		}

		if !wheelFilenameRegex.MatchString(dep.Filename) {
			return fmt.Errorf("%w: dependency %s filename %q does not conform to PEP 427", ErrWheelInvalidFilename, dep.Name, dep.Filename)
		}

		if seenFiles[dep.Filename] {
			return fmt.Errorf("%w: filename %s is duplicated in dependencies", ErrWheelDuplicate, dep.Filename)
		}
		seenFiles[dep.Filename] = true

		if dep.SizeBytes <= 0 {
			return fmt.Errorf("%w: dependency %s has invalid size_bytes (%d)", ErrWheelhouseLockSchemaInvalid, dep.Name, dep.SizeBytes)
		}
		calcTotalBytes += dep.SizeBytes

		if !sha256Regex.MatchString(dep.SHA256) {
			return fmt.Errorf("%w: dependency %s has invalid sha256 checksum %q", ErrWheelhouseLockSchemaInvalid, dep.Name, dep.SHA256)
		}
	}

	if l.TotalSizeBytes != calcTotalBytes {
		return fmt.Errorf("%w: total_size_bytes mismatch (stated %d, calculated %d)", ErrWheelhouseLockSchemaInvalid, l.TotalSizeBytes, calcTotalBytes)
	}

	// Verify required core dependencies
	if !hasTorch {
		return fmt.Errorf("%w: core dependency 'torch' is missing from wheelhouse lock", ErrWheelTransitiveDepMissing)
	}
	if !hasTorchvision {
		return fmt.Errorf("%w: core dependency 'torchvision' is missing from wheelhouse lock", ErrWheelTransitiveDepMissing)
	}
	if !hasUltralytics {
		return fmt.Errorf("%w: core dependency 'ultralytics' is missing from wheelhouse lock", ErrWheelTransitiveDepMissing)
	}
	if !hasPillow {
		return fmt.Errorf("%w: core dependency 'pillow' is missing from wheelhouse lock", ErrWheelTransitiveDepMissing)
	}

	return nil
}

// ValidateCompatibility checks platform, architecture, and wheel tag compatibility against target.
func (l *WheelhouseLock) ValidateCompatibility(targetOS, targetArch, pyVersion, pyABI string) error {
	if err := l.ValidateSchema(); err != nil {
		return err
	}
	if l.TargetPlatform != targetOS {
		return fmt.Errorf("%w: lock platform %q does not match target OS %q", ErrWheelhouseIncompatible, l.TargetPlatform, targetOS)
	}
	if l.TargetArchitecture != targetArch {
		return fmt.Errorf("%w: lock architecture %q does not match target Arch %q", ErrWheelhouseIncompatible, l.TargetArchitecture, targetArch)
	}
	if !strings.HasPrefix(pyVersion, "3.11") {
		return fmt.Errorf("%w: python version %q is incompatible with wheelhouse (requires 3.11.x)", ErrWheelhouseIncompatible, pyVersion)
	}
	if pyABI != "cp311" {
		return fmt.Errorf("%w: python ABI %q is incompatible with wheelhouse (requires cp311)", ErrWheelhouseIncompatible, pyABI)
	}

	for _, dep := range l.Dependencies {
		if err := validateWheelTags(dep, targetOS, targetArch); err != nil {
			return err
		}
	}
	return nil
}

// validateWheelTags checks if an individual wheel's tags are compatible with macOS arm64.
func validateWheelTags(dep WheelDependencySpec, targetOS, targetArch string) error {
	// ABI Tag check: must be cp311, abi3, or none
	abi := dep.ABITag
	if abi != "cp311" && abi != "abi3" && abi != "none" {
		return fmt.Errorf("%w: dependency %s has incompatible ABI tag %q", ErrWheelhouseIncompatible, dep.Name, abi)
	}

	// Python Tag check:
	// For abi3 wheels (PEP 384 Stable ABI), python tags like cp36, cp37 specify the minimum compatible CPython version.
	// For standard CPython or universal wheels: must match cp311, py3, or py2.py3.
	pyTag := dep.PythonTag
	isCompatiblePyTag := strings.Contains(pyTag, "cp311") || strings.Contains(pyTag, "py3") || strings.Contains(pyTag, "py2.py3")
	if abi == "abi3" && strings.HasPrefix(pyTag, "cp3") {
		isCompatiblePyTag = true
	}
	if !isCompatiblePyTag {
		return fmt.Errorf("%w: dependency %s has incompatible Python tag %q", ErrWheelhouseIncompatible, dep.Name, pyTag)
	}

	// Platform Tag check
	plat := dep.PlatformTag
	if targetOS == PlatformDarwin && targetArch == ArchArm64 {
		isUniversal := strings.Contains(plat, "universal2")
		isArm64 := strings.Contains(plat, "arm64") && strings.Contains(plat, "macosx")
		isAny := plat == "any"

		if !isAny && !isArm64 && !isUniversal {
			return fmt.Errorf("%w: dependency %s has incompatible platform tag %q for darwin-arm64", ErrWheelhouseIncompatible, dep.Name, plat)
		}
	}
	return nil
}

// FindDependency returns a pointer to the matching dependency or nil.
func (l *WheelhouseLock) FindDependency(name string) *WheelDependencySpec {
	norm := strings.ToLower(strings.ReplaceAll(name, "_", "-"))
	for i := range l.Dependencies {
		if strings.ToLower(strings.ReplaceAll(l.Dependencies[i].Name, "_", "-")) == norm {
			return &l.Dependencies[i]
		}
	}
	return nil
}

// WheelhouseManager manages offline wheel validation and hermetic installation.
type WheelhouseManager struct {
	wheelhouseDir string
	pythonMgr     *EmbeddedPythonManager
	lock          *WheelhouseLock
	pipExecutor   PipCommandExecutor
	readiness     string
	faultInject   string
}

// NewWheelhouseManager creates a WheelhouseManager for the specified wheelhouse directory and Python manager.
func NewWheelhouseManager(wheelhouseDir string, pythonMgr *EmbeddedPythonManager, lock *WheelhouseLock) (*WheelhouseManager, error) {
	cleanDir := filepath.Clean(wheelhouseDir)
	activeLock := lock
	if activeLock == nil {
		var err error
		activeLock, err = LoadEmbeddedWheelhouseLock()
		if err != nil {
			return nil, err
		}
	} else {
		if err := activeLock.ValidateSchema(); err != nil {
			return nil, err
		}
	}

	return &WheelhouseManager{
		wheelhouseDir: cleanDir,
		pythonMgr:     pythonMgr,
		lock:          activeLock,
		readiness:     StateWheelsUnchecked,
	}, nil
}

// SetPipExecutor configures a custom pip execution function (primarily for mocking and fault injection in tests).
func (m *WheelhouseManager) SetPipExecutor(executor PipCommandExecutor) {
	m.pipExecutor = executor
}

// SetFaultInjection configures deterministic fault injection for testing.
func (m *WheelhouseManager) SetFaultInjection(stage string) {
	m.faultInject = stage
}

// ReadinessState returns the current lifecycle readiness state of the wheelhouse.
func (m *WheelhouseManager) ReadinessState() string {
	return m.readiness
}

// Lock returns the active WheelhouseLock.
func (m *WheelhouseManager) Lock() *WheelhouseLock {
	return m.lock
}

// SitePackagesPath resolves the canonical active site-packages path for the configured Python runtime.
func (m *WheelhouseManager) SitePackagesPath() string {
	if m.pythonMgr == nil {
		return ""
	}
	pythonBin := m.pythonMgr.BinaryPath()
	prefix := filepath.Dir(filepath.Dir(pythonBin))
	return filepath.Join(prefix, "lib", "python3.11", "site-packages")
}

// ValidateArtifacts verifies physical existence, canonical confinement, exact byte size, and SHA-256 for all wheels.
func (m *WheelhouseManager) ValidateArtifacts(ctx context.Context, progressFn func(current, total int, filename string)) (*WheelhouseIntegrityReport, error) {
	absWheelhouse, err := filepath.Abs(m.wheelhouseDir)
	if err != nil {
		return nil, &SafeError{
			Code:        ErrCodeWheelSecurityEscape,
			SafeMessage: fmt.Sprintf("Ruta de wheelhouse inválida: %s", m.wheelhouseDir),
			Recoverable: false,
			Err:         err,
		}
	}

	canonicalWheelhouse, err := filepath.EvalSymlinks(absWheelhouse)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, &SafeError{
				Code:        ErrCodeWheelMissing,
				SafeMessage: fmt.Sprintf("El directorio de wheelhouse no existe: %s", absWheelhouse),
				Recoverable: true,
				Err:         ErrWheelMissing,
			}
		}
		return nil, &SafeError{
			Code:        ErrCodeWheelhouseLockInvalid,
			SafeMessage: fmt.Sprintf("Error resolviendo enlaces de wheelhouse: %v", err),
			Recoverable: false,
			Err:         err,
		}
	}
	canonicalWheelhouse = filepath.Clean(canonicalWheelhouse)

	stat, err := os.Stat(canonicalWheelhouse)
	if err != nil || !stat.IsDir() {
		return nil, &SafeError{
			Code:        ErrCodeWheelMissing,
			SafeMessage: fmt.Sprintf("La ruta de wheelhouse no es un directorio: %s", canonicalWheelhouse),
			Recoverable: false,
			Err:         ErrWheelMissing,
		}
	}

	report := &WheelhouseIntegrityReport{
		Valid:         false,
		CheckedCount:  0,
		TotalBytes:    0,
		VerifiedFiles: make([]string, 0, len(m.lock.Dependencies)),
	}

	total := len(m.lock.Dependencies)
	for i, dep := range m.lock.Dependencies {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		if progressFn != nil {
			progressFn(i+1, total, dep.Filename)
		}

		cleanRel := filepath.Clean(dep.Filename)
		if strings.HasPrefix(cleanRel, "..") || strings.HasPrefix(cleanRel, "/") || strings.HasPrefix(cleanRel, "\\") {
			return nil, &SafeError{
				Code:        ErrCodeWheelSecurityEscape,
				SafeMessage: fmt.Sprintf("%v: filename %q intenta escapar del wheelhouse", ErrWheelSecurityEscape, dep.Filename),
				Recoverable: false,
				Err:         ErrWheelSecurityEscape,
			}
		}

		candidate := filepath.Join(canonicalWheelhouse, cleanRel)

		// Lstat candidate
		fi, err := os.Lstat(candidate)
		if err != nil {
			if os.IsNotExist(err) {
				return nil, &SafeError{
					Code:        ErrCodeWheelMissing,
					SafeMessage: fmt.Sprintf("Wheel faltante en wheelhouse: %s", dep.Filename),
					Recoverable: true,
					Details:     fmt.Sprintf("Ruta esperada: %s", candidate),
					Err:         ErrWheelMissing,
				}
			}
			return nil, &SafeError{
				Code:        ErrCodeWheelCorrupted,
				SafeMessage: fmt.Sprintf("Error al acceder a wheel %s: %v", dep.Filename, err),
				Recoverable: false,
				Err:         err,
			}
		}

		// Confinement check: resolve symlinks if any
		realPath, err := filepath.EvalSymlinks(candidate)
		if err != nil {
			return nil, &SafeError{
				Code:        ErrCodeWheelSecurityEscape,
				SafeMessage: fmt.Sprintf("Error al resolver enlace de %s: %v", dep.Filename, err),
				Recoverable: false,
				Err:         ErrWheelSecurityEscape,
			}
		}
		realPath = filepath.Clean(realPath)

		relToRoot, err := filepath.Rel(canonicalWheelhouse, realPath)
		if err != nil || strings.HasPrefix(relToRoot, "..") || strings.HasPrefix(relToRoot, "/") {
			return nil, &SafeError{
				Code:        ErrCodeWheelSecurityEscape,
				SafeMessage: fmt.Sprintf("%v: enlace simbólico de %s escapa del wheelhouse (%s -> %s)", ErrWheelSecurityEscape, dep.Filename, candidate, realPath),
				Recoverable: false,
				Err:         ErrWheelSecurityEscape,
			}
		}

		// Must be regular file
		if fi.Mode()&os.ModeType != 0 && fi.Mode()&os.ModeSymlink == 0 {
			return nil, &SafeError{
				Code:        ErrCodeWheelCorrupted,
				SafeMessage: fmt.Sprintf("El artefacto %s no es un archivo regular (modo %v)", dep.Filename, fi.Mode()),
				Recoverable: false,
				Err:         ErrWheelCorrupted,
			}
		}

		// Size verification
		realFi, err := os.Stat(realPath)
		if err != nil {
			return nil, &SafeError{
				Code:        ErrCodeWheelCorrupted,
				SafeMessage: fmt.Sprintf("No se pudo obtener información del archivo %s: %v", realPath, err),
				Recoverable: false,
				Err:         err,
			}
		}

		if realFi.Size() != dep.SizeBytes {
			return nil, &SafeError{
				Code:        ErrCodeWheelSizeMismatch,
				SafeMessage: fmt.Sprintf("Tamaño incorrecto para %s: esperado %d bytes, obtenido %d bytes", dep.Filename, dep.SizeBytes, realFi.Size()),
				Recoverable: true,
				Err:         ErrWheelCorrupted,
			}
		}

		// SHA-256 calculation via streaming
		fileHash, err := hashFileSHA256(realPath)
		if err != nil {
			return nil, &SafeError{
				Code:        ErrCodeWheelCorrupted,
				SafeMessage: fmt.Sprintf("Error calculando hash SHA-256 de %s: %v", dep.Filename, err),
				Recoverable: false,
				Err:         err,
			}
		}

		if !strings.EqualFold(fileHash, dep.SHA256) {
			return nil, &SafeError{
				Code:        ErrCodeWheelHashMismatch,
				SafeMessage: fmt.Sprintf("Hash SHA-256 mismatch para %s: esperado %s, obtenido %s", dep.Filename, dep.SHA256, fileHash),
				Recoverable: true,
				Err:         ErrWheelCorrupted,
			}
		}

		report.CheckedCount++
		report.TotalBytes += realFi.Size()
		report.VerifiedFiles = append(report.VerifiedFiles, dep.Filename)
	}

	report.Valid = true
	m.readiness = StateWheelsVerified
	return report, nil
}

// defaultPipCommandExecutor runs pip command in isolated subprocess.
func defaultPipCommandExecutor(ctx context.Context, binary string, env []string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		combined := bytes.TrimSpace(append(stdout.Bytes(), stderr.Bytes()...))
		if len(combined) > 0 {
			return combined, fmt.Errorf("%w: %s", err, string(combined))
		}
		return nil, err
	}
	return stdout.Bytes(), nil
}

// GenerateRequirementsLock writes a cryptographic requirements lock file with exact pins
// and sha256 hashes for all dependencies in the lock.
func (m *WheelhouseManager) GenerateRequirementsLock(targetPath string) error {
	if m.lock == nil || len(m.lock.Dependencies) == 0 {
		return ErrWheelhouseLockNotFound
	}
	var sb strings.Builder
	sb.WriteString("# Autogenerated hermetic requirements lock for GEO CAM Edge (macOS Apple Silicon)\n")
	sb.WriteString("# Generated by WheelhouseManager. Do not edit manually.\n\n")
	for _, dep := range m.lock.Dependencies {
		sb.WriteString(fmt.Sprintf("%s==%s --hash=sha256:%s\n", dep.Name, dep.Version, dep.SHA256))
	}
	return os.WriteFile(targetPath, []byte(sb.String()), 0o644)
}

// CheckPipConsistency runs 'python -I -m pip check' to verify that all installed
// packages in site-packages have compatible dependencies and no broken requirements.
func (m *WheelhouseManager) CheckPipConsistency(ctx context.Context) error {
	if m.pythonMgr == nil {
		return errors.New("python manager is nil")
	}
	res, err := m.pythonMgr.Execute(ctx, "-I", "-m", "pip", "check")
	if err != nil {
		return &SafeError{
			Code:        ErrCodeWheelPipCheckFailed,
			SafeMessage: fmt.Sprintf("pip check reportó inconsistencias o requerimientos rotos: %v", err),
			Recoverable: false,
			Details:     res.Stdout + "\n" + res.Stderr,
			Err:         ErrWheelPipCheckFailed,
		}
	}
	stdoutLower := strings.ToLower(strings.TrimSpace(res.Stdout))
	if stdoutLower != "" && !strings.Contains(stdoutLower, "no broken requirements found") {
		return &SafeError{
			Code:        ErrCodeWheelPipCheckFailed,
			SafeMessage: fmt.Sprintf("pip check detectó dependencias rotas: %s", res.Stdout),
			Recoverable: false,
			Details:     res.Stdout,
			Err:         ErrWheelPipCheckFailed,
		}
	}
	return nil
}

// AuditInstalledDistributions uses Python's importlib.metadata to audit all installed
// distributions against the WheelhouseLock, asserting that every locked package is
// present with its exact required version.
func (m *WheelhouseManager) AuditInstalledDistributions(ctx context.Context) ([]string, error) {
	if m.pythonMgr == nil {
		return nil, errors.New("python manager is nil")
	}
	if m.lock == nil {
		return nil, ErrWheelhouseLockNotFound
	}

	auditScript := `
import importlib.metadata, json, sys
res = {}
for dist in importlib.metadata.distributions():
    name = dist.metadata.get('Name')
    if name:
        res[name.lower()] = dist.version
print(json.dumps(res))
`
	res, err := m.pythonMgr.Execute(ctx, "-I", "-c", auditScript)
	if err != nil {
		return nil, &SafeError{
			Code:        ErrCodeWheelAuditMismatch,
			SafeMessage: fmt.Sprintf("Error ejecutando auditoría de distribuciones: %v", err),
			Recoverable: false,
			Details:     res.Stderr,
			Err:         err,
		}
	}

	var installed map[string]string
	lines := strings.Split(strings.TrimSpace(res.Stdout), "\n")
	lastLine := lines[len(lines)-1]
	if err := json.Unmarshal([]byte(lastLine), &installed); err != nil {
		return nil, &SafeError{
			Code:        ErrCodeWheelAuditMismatch,
			SafeMessage: fmt.Sprintf("Error interpretando JSON de distribuciones instaladas: %v", err),
			Recoverable: false,
			Details:     res.Stdout,
			Err:         err,
		}
	}

	var verified []string
	var missing []string
	var versionMismatches []string

	for _, dep := range m.lock.Dependencies {
		key := strings.ToLower(dep.Name)
		instVer, ok := installed[key]
		if !ok {
			altKey := strings.ToLower(strings.ReplaceAll(dep.Name, "_", "-"))
			instVer, ok = installed[altKey]
		}
		if !ok {
			altKey := strings.ToLower(strings.ReplaceAll(dep.Name, "-", "_"))
			instVer, ok = installed[altKey]
		}
		if !ok {
			missing = append(missing, dep.Name)
			continue
		}
		if instVer != dep.Version {
			versionMismatches = append(versionMismatches, fmt.Sprintf("%s (esperado %s, obtenido %s)", dep.Name, dep.Version, instVer))
			continue
		}
		verified = append(verified, fmt.Sprintf("%s==%s", dep.Name, dep.Version))
	}

	if len(missing) > 0 || len(versionMismatches) > 0 {
		errMsg := ""
		if len(missing) > 0 {
			errMsg += fmt.Sprintf("dependencias faltantes: %s; ", strings.Join(missing, ", "))
		}
		if len(versionMismatches) > 0 {
			errMsg += fmt.Sprintf("versiones discrepantes: %s; ", strings.Join(versionMismatches, ", "))
		}
		return verified, &SafeError{
			Code:        ErrCodeWheelAuditMismatch,
			SafeMessage: fmt.Sprintf("Auditoría de distribuciones falló: %s", errMsg),
			Recoverable: false,
			Details:     errMsg,
			Err:         ErrWheelAuditMismatch,
		}
	}

	return verified, nil
}

// AuditStagedDistributions inspects a staged site-packages directory on disk,
// asserting that each dependency in the lock has a corresponding .dist-info folder.
func (m *WheelhouseManager) AuditStagedDistributions(stagingDir string) ([]string, error) {
	if m.lock == nil {
		return nil, ErrWheelhouseLockNotFound
	}
	entries, err := os.ReadDir(stagingDir)
	if err != nil {
		return nil, err
	}

	distInfos := make(map[string]bool)
	for _, entry := range entries {
		if entry.IsDir() && strings.HasSuffix(entry.Name(), ".dist-info") {
			distInfos[strings.ToLower(entry.Name())] = true
		}
	}

	var verified []string
	var missing []string

	for _, dep := range m.lock.Dependencies {
		depNameLower := strings.ToLower(dep.Name)
		depNameDash := strings.ToLower(strings.ReplaceAll(dep.Name, "_", "-"))
		depNameUnder := strings.ToLower(strings.ReplaceAll(dep.Name, "-", "_"))

		expectedPrefixes := []string{
			depNameLower + "-" + dep.Version + ".dist-info",
			depNameDash + "-" + dep.Version + ".dist-info",
			depNameUnder + "-" + dep.Version + ".dist-info",
		}

		found := false
		for _, p := range expectedPrefixes {
			if distInfos[p] {
				found = true
				break
			}
		}

		if !found {
			for di := range distInfos {
				if (strings.HasPrefix(di, depNameLower+"-"+dep.Version) ||
					strings.HasPrefix(di, depNameDash+"-"+dep.Version) ||
					strings.HasPrefix(di, depNameUnder+"-"+dep.Version)) &&
					strings.HasSuffix(di, ".dist-info") {
					found = true
					break
				}
			}
		}

		if found {
			verified = append(verified, dep.Name)
		} else {
			missing = append(missing, dep.Name)
		}
	}

	if len(missing) > 0 {
		return verified, &SafeError{
			Code:        ErrCodeWheelStagingFailed,
			SafeMessage: fmt.Sprintf("Staging incompleto: faltan .dist-info para %s", strings.Join(missing, ", ")),
			Recoverable: false,
			Err:         ErrWheelStagingIncomplete,
		}
	}

	return verified, nil
}

// copyPathRecursively copies a file or directory tree from src to dst.
func copyPathRecursively(src, dst string) error {
	fi, err := os.Stat(src)
	if err != nil {
		return err
	}
	if fi.IsDir() {
		if err := os.MkdirAll(dst, fi.Mode()); err != nil {
			return err
		}
		entries, err := os.ReadDir(src)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			s := filepath.Join(src, entry.Name())
			d := filepath.Join(dst, entry.Name())
			if err := copyPathRecursively(s, d); err != nil {
				return err
			}
		}
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, fi.Mode())
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

// InstallOffline executes pip install strictly offline (--no-index, --find-links, --require-hashes)
// into an isolated staging directory on the same filesystem, and performs an atomic directory swap with
// automatic transactional rollback if verification fails.
func (m *WheelhouseManager) InstallOffline(ctx context.Context) (*InstallReport, error) {
	if m.pythonMgr == nil {
		return nil, &SafeError{
			Code:        ErrCodePythonNotFound,
			SafeMessage: "EmbeddedPythonManager no configurado para el wheelhouse",
			Recoverable: false,
			Err:         errors.New("python manager is nil"),
		}
	}

	// 1. Verify wheelhouse integrity first
	if _, err := m.ValidateArtifacts(ctx, nil); err != nil {
		return nil, err
	}

	// 2. Validate python interpreter
	if err := m.pythonMgr.Validate(); err != nil {
		return nil, err
	}

	pythonBin := m.pythonMgr.BinaryPath()
	prefix := filepath.Dir(filepath.Dir(pythonBin))
	libDir := filepath.Join(prefix, "lib", "python3.11")
	if err := os.MkdirAll(libDir, 0o755); err != nil {
		return nil, err
	}

	sitePackages := filepath.Join(libDir, "site-packages")
	pid := os.Getpid()
	stagingDir := filepath.Join(libDir, fmt.Sprintf(".site-packages.tmp.%d", pid))
	backupDir := filepath.Join(libDir, fmt.Sprintf(".site-packages.old.%d", pid))
	brokenDir := filepath.Join(libDir, fmt.Sprintf(".site-packages.broken.%d", pid))

	// Clean up any stale staging/backup artifacts before starting
	_ = os.RemoveAll(stagingDir)
	_ = os.RemoveAll(backupDir)
	_ = os.RemoveAll(brokenDir)

	// 3. Generate ephemeral cryptographic requirements lock
	reqFile, err := os.CreateTemp("", "geocam-requirements.*.lock.txt")
	if err != nil {
		return nil, fmt.Errorf("failed to create temporary requirements file: %w", err)
	}
	defer os.Remove(reqFile.Name())

	if err := m.GenerateRequirementsLock(reqFile.Name()); err != nil {
		return nil, err
	}

	if err := os.MkdirAll(stagingDir, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create staging directory: %w", err)
	}

	// Seed staging directory with base interpreter packages from sitePackages if present
	if entries, err := os.ReadDir(sitePackages); err == nil {
		for _, entry := range entries {
			name := entry.Name()
			if strings.HasPrefix(name, "pip") ||
				strings.HasPrefix(name, "setuptools") ||
				strings.HasPrefix(name, "pkg_resources") ||
				strings.HasPrefix(name, "_distutils") ||
				strings.HasPrefix(name, "distutils") ||
				strings.HasPrefix(name, "README") {
				src := filepath.Join(sitePackages, name)
				dst := filepath.Join(stagingDir, name)
				_ = copyPathRecursively(src, dst)
			}
		}
	}

	// 4. Build strictly offline, hash-pinned pip invocation arguments
	pipArgs := []string{
		"-I",
		"-m",
		"pip",
		"install",
		"--no-index",
		"--find-links=" + m.wheelhouseDir,
		"--require-hashes",
		"-r", reqFile.Name(),
		"--target", stagingDir,
		"--only-binary=:all:",
		"--disable-pip-version-check",
		"--no-cache-dir",
	}

	// 5. Build isolated environment
	env := IsolateEnv(os.Environ())
	env = append(env,
		"PIP_CONFIG_FILE=/dev/null",
		"PIP_NO_INDEX=1",
		"http_proxy=http://127.0.0.1:9",
		"https_proxy=http://127.0.0.1:9",
		"all_proxy=http://127.0.0.1:9",
		"HTTP_PROXY=http://127.0.0.1:9",
		"HTTPS_PROXY=http://127.0.0.1:9",
		"ALL_PROXY=http://127.0.0.1:9",
		"NO_PROXY=",
	)

	// Fault injection before pip
	if m.faultInject == "before_pip" {
		_ = os.RemoveAll(stagingDir)
		return nil, &SafeError{
			Code:        ErrCodeWheelOfflineInstallFailed,
			SafeMessage: "FAULT_INJECTION: error antes de pip",
			Recoverable: false,
			Err:         ErrWheelOfflineInstallFailed,
		}
	}

	start := time.Now()
	var outBytes []byte
	var execErr error

	if m.pipExecutor != nil {
		outBytes, execErr = m.pipExecutor(ctx, pythonBin, env, pipArgs...)
	} else {
		outBytes, execErr = defaultPipCommandExecutor(ctx, pythonBin, env, pipArgs...)
	}

	if m.faultInject == "pip_fail" && execErr == nil {
		execErr = errors.New("FAULT_INJECTION: simulated pip failure")
	}

	durationMs := float64(time.Since(start).Microseconds()) / 1000.0

	if execErr != nil {
		_ = os.RemoveAll(stagingDir)
		return &InstallReport{
				Success:        false,
				InstalledCount: 0,
				DurationMs:     durationMs,
				Output:         string(outBytes),
			}, &SafeError{
				Code:        ErrCodeWheelOfflineInstallFailed,
				SafeMessage: fmt.Sprintf("Error durante la instalación offline de dependencias: %v", execErr),
				Recoverable: false,
				Details:     string(outBytes),
				Err:         execErr,
			}
	}

	// Staging audit
	if m.faultInject == "staging_incomplete" {
		_ = os.RemoveAll(stagingDir)
		return nil, &SafeError{
			Code:        ErrCodeWheelStagingFailed,
			SafeMessage: "FAULT_INJECTION: staging incompleto",
			Recoverable: false,
			Err:         ErrWheelStagingIncomplete,
		}
	}

	if m.pipExecutor == nil {
		if _, err := m.AuditStagedDistributions(stagingDir); err != nil {
			_ = os.RemoveAll(stagingDir)
			return nil, err
		}
	}

	// Fault injection swap
	if m.faultInject == "swap_fail" {
		_ = os.RemoveAll(stagingDir)
		return nil, &SafeError{
			Code:        ErrCodeWheelActivationFailed,
			SafeMessage: "FAULT_INJECTION: fallo al activar directorio",
			Recoverable: false,
			Err:         ErrWheelActivationFailed,
		}
	}

	// 6. Two-step directory swap with rollback safety
	hadPrevious := false
	if fi, err := os.Stat(sitePackages); err == nil && fi.IsDir() {
		hadPrevious = true
		if err := os.Rename(sitePackages, backupDir); err != nil {
			_ = os.RemoveAll(stagingDir)
			return nil, &SafeError{
				Code:        ErrCodeWheelActivationFailed,
				SafeMessage: fmt.Sprintf("Error respaldando site-packages existente: %v", err),
				Recoverable: false,
				Err:         err,
			}
		}
	}

	if err := os.Rename(stagingDir, sitePackages); err != nil {
		if hadPrevious {
			_ = os.Rename(backupDir, sitePackages)
		}
		_ = os.RemoveAll(stagingDir)
		return nil, &SafeError{
			Code:        ErrCodeWheelActivationFailed,
			SafeMessage: fmt.Sprintf("Error activando staging en site-packages: %v", err),
			Recoverable: false,
			Err:         err,
		}
	}

	// Fault injection for post-activation failures
	if m.faultInject == "pip_check_fail" {
		_ = os.RemoveAll(brokenDir)
		_ = os.Rename(sitePackages, brokenDir)
		if hadPrevious {
			_ = os.Rename(backupDir, sitePackages)
		}
		_ = os.RemoveAll(brokenDir)
		return nil, &SafeError{
			Code:        ErrCodeWheelPipCheckFailed,
			SafeMessage: "FAULT_INJECTION: pip check falló",
			Recoverable: true,
			Err:         ErrWheelPipCheckFailed,
		}
	}

	if m.faultInject == "audit_fail" {
		_ = os.RemoveAll(brokenDir)
		_ = os.Rename(sitePackages, brokenDir)
		if hadPrevious {
			_ = os.Rename(backupDir, sitePackages)
		}
		_ = os.RemoveAll(brokenDir)
		return nil, &SafeError{
			Code:        ErrCodeWheelAuditMismatch,
			SafeMessage: "FAULT_INJECTION: auditoría de distribuciones falló",
			Recoverable: true,
			Err:         ErrWheelAuditMismatch,
		}
	}

	if m.faultInject == "restore_fail" {
		_ = os.RemoveAll(brokenDir)
		_ = os.Rename(sitePackages, brokenDir)
		return nil, &SafeError{
			Code:        ErrCodeWheelRollbackFailed,
			SafeMessage: fmt.Sprintf("CRITICAL: Error restaurando backup; backup preservado en %s", backupDir),
			Recoverable: false,
			Err:         ErrWheelRollbackFailed,
		}
	}

	// 7. Post-activation verification on active interpreter
	if m.pipExecutor == nil {
		if err := m.CheckPipConsistency(ctx); err != nil {
			_ = os.RemoveAll(brokenDir)
			_ = os.Rename(sitePackages, brokenDir)
			if hadPrevious {
				if rbErr := os.Rename(backupDir, sitePackages); rbErr != nil {
					return nil, &SafeError{
						Code:        ErrCodeWheelRollbackFailed,
						SafeMessage: fmt.Sprintf("CRITICAL: Error restaurando backup tras fallo de pip check; backup preservado en %s", backupDir),
						Recoverable: false,
						Err:         ErrWheelRollbackFailed,
					}
				}
			}
			_ = os.RemoveAll(brokenDir)
			return nil, err
		}

		if _, err := m.AuditInstalledDistributions(ctx); err != nil {
			_ = os.RemoveAll(brokenDir)
			_ = os.Rename(sitePackages, brokenDir)
			if hadPrevious {
				if rbErr := os.Rename(backupDir, sitePackages); rbErr != nil {
					return nil, &SafeError{
						Code:        ErrCodeWheelRollbackFailed,
						SafeMessage: fmt.Sprintf("CRITICAL: Error restaurando backup tras fallo de auditoría; backup preservado en %s", backupDir),
						Recoverable: false,
						Err:         ErrWheelRollbackFailed,
					}
				}
			}
			_ = os.RemoveAll(brokenDir)
			return nil, err
		}
	}

	// 8. Commit: clean up backup
	if hadPrevious {
		_ = os.RemoveAll(backupDir)
	}

	m.readiness = StateDependenciesInstalled
	return &InstallReport{
		Success:        true,
		InstalledCount: len(m.lock.Dependencies),
		DurationMs:     durationMs,
		Output:         string(outBytes),
	}, nil
}

// VerifyImports executes an in-process Python probe to verify that critical modules import and MPS functions.
func (m *WheelhouseManager) VerifyImports(ctx context.Context) (*ImportsValidationResult, error) {
	if m.pythonMgr == nil {
		return nil, errors.New("python manager is nil")
	}

	probeScript := `
import json, sys
res = {
    'torch_version': '',
    'torchvision_version': '',
    'ultralytics_version': '',
    'pillow_version': '',
    'opencv_version': '',
    'numpy_version': '',
    'mps_available': False,
    'mps_tensor_ok': False,
    'cpu_tensor_ok': False
}
try:
    import torch
    res['torch_version'] = torch.__version__
    res['mps_available'] = bool(torch.backends.mps.is_available())
    if res['mps_available']:
        try:
            t = torch.ones(2, device='mps') * 3
            res['mps_tensor_ok'] = bool((t.cpu().sum().item() == 6.0))
        except Exception:
            res['mps_tensor_ok'] = False
    t_cpu = torch.tensor([1.0, 2.0])
    res['cpu_tensor_ok'] = bool((t_cpu.sum().item() == 3.0))
except Exception as e:
    res['torch_error'] = str(e)

try:
    import torchvision
    res['torchvision_version'] = torchvision.__version__
except Exception as e:
    res['torchvision_error'] = str(e)

try:
    import ultralytics
    res['ultralytics_version'] = ultralytics.__version__
except Exception as e:
    res['ultralytics_error'] = str(e)

try:
    import PIL
    res['pillow_version'] = PIL.__version__
except Exception as e:
    res['pillow_error'] = str(e)

try:
    import cv2
    res['opencv_version'] = cv2.__version__
except Exception as e:
    res['opencv_error'] = str(e)

try:
    import numpy
    res['numpy_version'] = numpy.__version__
except Exception as e:
    res['numpy_error'] = str(e)

print(json.dumps(res))
`

	execRes, err := m.pythonMgr.Execute(ctx, "-I", "-c", probeScript)
	if err != nil {
		return nil, &SafeError{
			Code:        ErrCodeWheelImportsFailed,
			SafeMessage: fmt.Sprintf("Error ejecutando sonda de imports: %v", err),
			Recoverable: false,
			Err:         err,
		}
	}

	var parsed struct {
		TorchVersion       string `json:"torch_version"`
		TorchvisionVersion string `json:"torchvision_version"`
		UltralyticsVersion string `json:"ultralytics_version"`
		PillowVersion      string `json:"pillow_version"`
		OpenCVVersion      string `json:"opencv_version"`
		NumPyVersion       string `json:"numpy_version"`
		MPSAvailable       bool   `json:"mps_available"`
		MPSTensorOK        bool   `json:"mps_tensor_ok"`
		CPUTensorOK        bool   `json:"cpu_tensor_ok"`
		TorchError         string `json:"torch_error"`
		UltralyticsError   string `json:"ultralytics_error"`
		PillowError        string `json:"pillow_error"`
	}

	lines := strings.Split(strings.TrimSpace(execRes.Stdout), "\n")
	lastLine := lines[len(lines)-1]
	if err := json.Unmarshal([]byte(lastLine), &parsed); err != nil {
		return nil, &SafeError{
			Code:        ErrCodeWheelImportsFailed,
			SafeMessage: fmt.Sprintf("No se pudo interpretar el resultado de la sonda: %v", err),
			Recoverable: false,
			Details:     execRes.Stdout,
			Err:         err,
		}
	}

	result := &ImportsValidationResult{
		Success:            true,
		TorchVersion:       parsed.TorchVersion,
		TorchvisionVersion: parsed.TorchvisionVersion,
		UltralyticsVersion: parsed.UltralyticsVersion,
		PillowVersion:      parsed.PillowVersion,
		OpenCVVersion:      parsed.OpenCVVersion,
		NumPyVersion:       parsed.NumPyVersion,
		MPSAvailable:       parsed.MPSAvailable,
		MPSTensorOK:        parsed.MPSTensorOK,
		CPUTensorOK:        parsed.CPUTensorOK,
		RawOutput:          execRes.Stdout,
	}

	// Validate that primary packages are loaded
	if parsed.TorchVersion == "" || parsed.UltralyticsVersion == "" || parsed.PillowVersion == "" {
		result.Success = false
		return result, &SafeError{
			Code:        ErrCodeWheelImportsFailed,
			SafeMessage: "Uno o más paquetes requeridos fallaron al importarse (torch, ultralytics o pillow)",
			Recoverable: false,
			Details:     fmt.Sprintf("torch_err=%s, ultralytics_err=%s, pillow_err=%s", parsed.TorchError, parsed.UltralyticsError, parsed.PillowError),
			Err:         ErrWheelImportsValidationFailed,
		}
	}

	m.readiness = StateImportsVerified
	return result, nil
}

// RunWorkerSmokeTest executes a clean smoke test of backend.py without real cameras or network.
func (m *WheelhouseManager) RunWorkerSmokeTest(ctx context.Context, workerDir string) (*WorkerSmokeTestResult, error) {
	if m.pythonMgr == nil {
		return nil, errors.New("python manager is nil")
	}

	absWorkerDir := workerDir
	if !filepath.IsAbs(absWorkerDir) {
		candidates := []string{
			absWorkerDir,
			filepath.Join("..", "..", absWorkerDir),
			filepath.Join(m.wheelhouseDir, "..", absWorkerDir),
		}
		for _, c := range candidates {
			if _, err := os.Stat(filepath.Join(c, "backend.py")); err == nil {
				if abs, err := filepath.Abs(c); err == nil {
					absWorkerDir = abs
					break
				}
			}
		}
	}

	smokeScript := fmt.Sprintf(`
import sys, json
sys.path.insert(0, %q)
try:
    from backend import resolve_device, _cuda_available, InferenceBackend, Detection, InferResult
    dev_auto, warn_auto = resolve_device('auto')
    dev_mps, warn_mps = resolve_device('mps')
    print(json.dumps({
        'success': True,
        'backend_imported': True,
        'dev_auto': dev_auto,
        'dev_mps': dev_mps,
        'warning': warn_auto or warn_mps
    }))
except Exception as e:
    print(json.dumps({
        'success': False,
        'backend_imported': False,
        'error': str(e)
    }))
`, absWorkerDir)

	execRes, err := m.pythonMgr.Execute(ctx, "-I", "-c", smokeScript)
	if err != nil {
		return nil, &SafeError{
			Code:        ErrCodeWheelImportsFailed,
			SafeMessage: fmt.Sprintf("Error ejecutando smoke test del worker: %v", err),
			Recoverable: false,
			Err:         err,
		}
	}

	var parsed struct {
		Success         bool   `json:"success"`
		BackendImported bool   `json:"backend_imported"`
		DevAuto         string `json:"dev_auto"`
		DevMPS          string `json:"dev_mps"`
		Warning         string `json:"warning"`
		Error           string `json:"error"`
	}

	lines := strings.Split(strings.TrimSpace(execRes.Stdout), "\n")
	lastLine := lines[len(lines)-1]
	if err := json.Unmarshal([]byte(lastLine), &parsed); err != nil {
		return nil, &SafeError{
			Code:        ErrCodeWheelImportsFailed,
			SafeMessage: fmt.Sprintf("Error parseando resultado del smoke test del worker: %v", err),
			Recoverable: false,
			Details:     execRes.Stdout,
			Err:         err,
		}
	}

	if !parsed.Success || !parsed.BackendImported {
		return &WorkerSmokeTestResult{
			Success:         false,
			BackendImported: false,
			Details:         parsed.Error,
		}, fmt.Errorf("worker smoke test failed: %s", parsed.Error)
	}

	return &WorkerSmokeTestResult{
		Success:         true,
		BackendImported: true,
		DeviceResolved:  parsed.DevAuto,
		Warning:         parsed.Warning,
	}, nil
}
