package installer

import (
	"bytes"
	"context"
	"debug/macho"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Default execution parameters.
const (
	DefaultPythonTimeout = 10 * time.Second
)

// PythonReadinessState reflects the verification state of the embedded Python interpreter.
type PythonReadinessState string

const (
	PythonStateUnchecked    PythonReadinessState = "UNCHECKED"
	PythonStateMissing      PythonReadinessState = "MISSING"
	PythonStateCorrupted    PythonReadinessState = "CORRUPTED"
	PythonStateIncompatible PythonReadinessState = "INCOMPATIBLE"
	PythonStateExecutable   PythonReadinessState = "PYTHON_EXECUTABLE"
)

// Standard typed error codes for SafeError.
const (
	ErrCodePythonNotFound              = "PYTHON_NOT_FOUND"
	ErrCodePythonHashMismatch          = "PYTHON_HASH_MISMATCH"
	ErrCodePythonSizeMismatch          = "PYTHON_SIZE_MISMATCH"
	ErrCodePythonNotExecutable         = "PYTHON_NOT_EXECUTABLE"
	ErrCodePythonArchIncompatible      = "PYTHON_ARCH_INCOMPATIBLE"
	ErrCodePythonVersionIncompatible   = "PYTHON_VERSION_INCOMPATIBLE"
	ErrCodePythonABIIncompatible       = "PYTHON_ABI_INCOMPATIBLE"
	ErrCodePythonStdlibIncomplete      = "PYTHON_STDLIB_INCOMPLETE"
	ErrCodePythonExecTimeout           = "PYTHON_EXEC_TIMEOUT"
	ErrCodePythonExecFailed            = "PYTHON_EXEC_FAILED"
	ErrCodePythonSecurityEscape        = "SECURITY_ESCAPING_SYMLINK"
	ErrCodePythonSecurityBrokenSymlink = "SECURITY_BROKEN_SYMLINK"
)

// Lifecycle readiness tiers.
// PYTHON_EXECUTABLE certifies that the standalone Python 3.11 interpreter and its distribution
// are verified and runnable without host dependencies. It explicitly does NOT imply RUNTIME_READY
// or INFERENCE_READY, which require offline wheelhouse dependencies and model weights from later milestones.
const (
	StatePythonExecutable = "PYTHON_EXECUTABLE"
	StateRuntimeReady     = "RUNTIME_READY"
	StateInferenceReady   = "INFERENCE_READY"
)

// PythonRuntimeInfo captures verified interpreter metadata.
type PythonRuntimeInfo struct {
	Version    string `json:"version"`
	Major      int    `json:"major"`
	Minor      int    `json:"minor"`
	Micro      int    `json:"micro"`
	Platform   string `json:"platform"`
	Executable string `json:"executable"`
	Prefix     string `json:"prefix"`
}

// PythonExecutionResult captures the output of an isolated python command.
type PythonExecutionResult struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
}

// PythonCommandExecutor defines the function signature for executing python subprocesses.
type PythonCommandExecutor func(ctx context.Context, binary string, env []string, args ...string) ([]byte, error)

// EmbeddedPythonManager manages the bundled hermetic Python standalone interpreter.
// It enforces integrity validation (SHA-256, size, Mach-O architecture, confinement),
// process isolation, and version compatibility without mutating or depending on the host.
//
// TOCTOU Consideration:
// Validate performs in-place cryptographic and filesystem checks at inspection time.
// Absolute activation isolation against concurrent modification is finalized in H6
// through isolated staging (0700) and atomic rename (os.Rename).
type EmbeddedPythonManager struct {
	rootDir      string
	manifest     *RuntimeManifest
	execExecutor PythonCommandExecutor
}

// NewEmbeddedPythonManager creates an EmbeddedPythonManager for rootDir.
// If manifest is nil, it automatically loads the embedded default manifest.
func NewEmbeddedPythonManager(rootDir string, manifest *RuntimeManifest) (*EmbeddedPythonManager, error) {
	cleanRoot := filepath.Clean(rootDir)
	m := manifest
	if m == nil {
		var err error
		m, err = LoadEmbeddedRuntimeManifest()
		if err != nil {
			return nil, err
		}
	}
	return &EmbeddedPythonManager{
		rootDir:  cleanRoot,
		manifest: m,
	}, nil
}

// SetExecutor configures a custom command executor, primarily for testing.
func (m *EmbeddedPythonManager) SetExecutor(fn PythonCommandExecutor) {
	m.execExecutor = fn
}

// Manifest returns the associated RuntimeManifest.
func (m *EmbeddedPythonManager) Manifest() *RuntimeManifest {
	return m.manifest
}

// RootDir returns the configured runtime pack root directory.
func (m *EmbeddedPythonManager) RootDir() string {
	return m.rootDir
}

// BinaryPath resolves the canonical physical path to the embedded Python binary.
func (m *EmbeddedPythonManager) BinaryPath() string {
	if m == nil || m.manifest == nil {
		return ""
	}
	rel := canonicalizeManifestPath(m.manifest.Python.RelativeBinaryPath)
	return filepath.Join(m.rootDir, filepath.FromSlash(rel))
}

// ReadinessState returns the current lifecycle readiness state.
// Validating EmbeddedPythonManager certifies StatePythonExecutable,
// but explicitly does NOT grant StateRuntimeReady or StateInferenceReady.
func (m *EmbeddedPythonManager) ReadinessState() string {
	return StatePythonExecutable
}

// Validate checks the physical existence, symlink confinement, size, SHA-256 hash,
// executable permissions, Mach-O architecture, and standard library completeness.
func (m *EmbeddedPythonManager) Validate() error {
	if m == nil || m.manifest == nil {
		return &SafeError{
			Code:        "MANIFEST_NIL",
			SafeMessage: "El manifiesto de runtime es nulo.",
			Recoverable: false,
			Err:         ErrManifestSchemaInvalid,
		}
	}

	if err := m.manifest.ValidateSchema(); err != nil {
		return err
	}

	if err := m.manifest.ValidateCompatibility(m.manifest.Platform, m.manifest.Architecture); err != nil {
		return err
	}

	if !strings.HasPrefix(m.manifest.Python.Version, "3.11") {
		return &SafeError{
			Code:        ErrCodePythonVersionIncompatible,
			SafeMessage: fmt.Sprintf("%v: python.version %q incompatible (se requiere 3.11.x)", ErrManifestSchemaInvalid, m.manifest.Python.Version),
			Recoverable: false,
			Err:         ErrManifestSchemaInvalid,
		}
	}

	if m.manifest.Python.ABI != "cp311" {
		return &SafeError{
			Code:        ErrCodePythonABIIncompatible,
			SafeMessage: fmt.Sprintf("%v: python.abi %q incompatible (se requiere cp311)", ErrManifestSchemaInvalid, m.manifest.Python.ABI),
			Recoverable: false,
			Err:         ErrManifestSchemaInvalid,
		}
	}

	absRoot, err := filepath.Abs(m.rootDir)
	if err != nil {
		return &SafeError{
			Code:        "INVALID_ROOT_DIR",
			SafeMessage: fmt.Sprintf("Ruta de runtime pack inválida: %s", m.rootDir),
			Recoverable: false,
			Details:     err.Error(),
		}
	}

	canonicalRoot, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return &SafeError{
				Code:        "ROOT_DIR_NOT_FOUND",
				SafeMessage: fmt.Sprintf("El directorio del runtime no existe: %s", absRoot),
				Recoverable: true,
				Err:         ErrArtifactMissing,
			}
		}
		return &SafeError{
			Code:        "ROOT_DIR_ERROR",
			SafeMessage: fmt.Sprintf("Error al resolver directorio del runtime: %v", err),
			Recoverable: false,
			Err:         err,
		}
	}
	canonicalRoot = filepath.Clean(canonicalRoot)

	rootStat, err := os.Stat(canonicalRoot)
	if err != nil || !rootStat.IsDir() {
		return &SafeError{
			Code:        "ROOT_NOT_A_DIRECTORY",
			SafeMessage: fmt.Sprintf("La ruta del runtime no es un directorio: %s", canonicalRoot),
			Recoverable: false,
			Err:         ErrArtifactMissing,
		}
	}

	cleanRel := canonicalizeManifestPath(m.manifest.Python.RelativeBinaryPath)
	if err := validateRelativePath(cleanRel, "python.relative_binary_path"); err != nil {
		return &SafeError{
			Code:        ErrCodePythonSecurityEscape,
			SafeMessage: fmt.Sprintf("%v: %s", ErrArtifactSecurity, cleanRel),
			Recoverable: false,
			Err:         ErrArtifactSecurity,
		}
	}

	parts := strings.FieldsFunc(cleanRel, func(r rune) bool {
		return r == '/' || r == '\\'
	})
	if len(parts) == 0 {
		return &SafeError{
			Code:        ErrCodePythonSecurityEscape,
			SafeMessage: "Ruta de binario de Python vacía",
			Recoverable: false,
			Err:         ErrArtifactSecurity,
		}
	}

	currentPhysical := canonicalRoot
	for partIdx, part := range parts {
		isLeaf := (partIdx == len(parts)-1)
		candidate := filepath.Join(currentPhysical, part)

		if _, err := os.Lstat(candidate); err != nil {
			if os.IsNotExist(err) {
				return &SafeError{
					Code:        ErrCodePythonNotFound,
					SafeMessage: fmt.Sprintf("%v: binario Python no encontrado (%s)", ErrArtifactMissing, m.manifest.Python.RelativeBinaryPath),
					Recoverable: true,
					Err:         ErrArtifactMissing,
				}
			}
			return &SafeError{
				Code:        "ARTIFACT_ACCESS_ERROR",
				SafeMessage: fmt.Sprintf("Error al acceder a componente %s: %v", part, err),
				Recoverable: true,
				Err:         err,
			}
		}

		resolvedPart, err := filepath.EvalSymlinks(candidate)
		if err != nil {
			return &SafeError{
				Code:        ErrCodePythonSecurityBrokenSymlink,
				SafeMessage: fmt.Sprintf("%v: symlink inválido o roto en %s", ErrArtifactSecurity, m.manifest.Python.RelativeBinaryPath),
				Recoverable: false,
				Err:         ErrArtifactSecurity,
			}
		}

		confined, err := isPathConfined(canonicalRoot, resolvedPart)
		if err != nil || !confined {
			return &SafeError{
				Code:        ErrCodePythonSecurityEscape,
				SafeMessage: fmt.Sprintf("%v: symlink en %s apunta fuera del runtime pack", ErrArtifactSecurity, m.manifest.Python.RelativeBinaryPath),
				Recoverable: false,
				Err:         ErrArtifactSecurity,
			}
		}

		if !isLeaf {
			partStat, err := os.Stat(resolvedPart)
			if err != nil || !partStat.IsDir() {
				return &SafeError{
					Code:        ErrCodePythonNotFound,
					SafeMessage: fmt.Sprintf("%v: componente intermedio en %s no es un directorio", ErrArtifactCorrupted, m.manifest.Python.RelativeBinaryPath),
					Recoverable: false,
					Err:         ErrArtifactCorrupted,
				}
			}
		}

		currentPhysical = resolvedPart
	}

	resolvedBinary := currentPhysical
	targetInfo, err := os.Stat(resolvedBinary)
	if err != nil {
		if os.IsNotExist(err) {
			return &SafeError{
				Code:        ErrCodePythonNotFound,
				SafeMessage: fmt.Sprintf("%v: binario Python no encontrado", ErrArtifactMissing),
				Recoverable: true,
				Err:         ErrArtifactMissing,
			}
		}
		return &SafeError{
			Code:        "ARTIFACT_ACCESS_ERROR",
			SafeMessage: fmt.Sprintf("Error al verificar binario Python: %v", err),
			Recoverable: true,
			Err:         err,
		}
	}

	if !targetInfo.Mode().IsRegular() {
		return &SafeError{
			Code:        ErrCodePythonNotFound,
			SafeMessage: fmt.Sprintf("%v: %s no es un archivo regular", ErrArtifactCorrupted, m.manifest.Python.RelativeBinaryPath),
			Recoverable: false,
			Err:         ErrArtifactCorrupted,
		}
	}

	actualSize := targetInfo.Size()
	if m.manifest.Python.SizeBytes > 0 && actualSize != m.manifest.Python.SizeBytes {
		return &SafeError{
			Code: ErrCodePythonSizeMismatch,
			SafeMessage: fmt.Sprintf("%v: tamaño de binario Python no coincide (esperado %d, actual %d)",
				ErrArtifactCorrupted, m.manifest.Python.SizeBytes, actualSize),
			Recoverable: false,
			Err:         ErrArtifactCorrupted,
		}
	}

	if m.manifest.Python.SHA256 != "" {
		actualHash, err := hashFileSHA256(resolvedBinary)
		if err != nil {
			return &SafeError{
				Code:        "HASH_CALCULATION_ERROR",
				SafeMessage: fmt.Sprintf("Error al calcular hash de %s", m.manifest.Python.RelativeBinaryPath),
				Recoverable: true,
				Err:         err,
			}
		}
		if !strings.EqualFold(actualHash, m.manifest.Python.SHA256) {
			return &SafeError{
				Code: ErrCodePythonHashMismatch,
				SafeMessage: fmt.Sprintf("%v: hash SHA-256 de binario Python no coincide (esperado %s, actual %s)",
					ErrArtifactCorrupted, m.manifest.Python.SHA256, actualHash),
				Recoverable: false,
				Err:         ErrArtifactCorrupted,
			}
		}
	}

	// Permission verification on Unix
	if runtime.GOOS != PlatformWindows {
		if targetInfo.Mode().Perm()&0111 == 0 {
			return &SafeError{
				Code: ErrCodePythonNotExecutable,
				SafeMessage: fmt.Sprintf("el binario Python %s carece de permisos de ejecución (mode: %o)",
					m.manifest.Python.RelativeBinaryPath, targetInfo.Mode().Perm()),
				Recoverable: true,
				Err:         os.ErrPermission,
			}
		}
	}

	// Architecture & Mach-O verification
	if m.manifest.Platform == PlatformDarwin || runtime.GOOS == PlatformDarwin {
		if m.manifest.Architecture == ArchArm64 {
			if err := ValidateMachOArch(resolvedBinary); err != nil {
				return &SafeError{
					Code:        ErrCodePythonArchIncompatible,
					SafeMessage: fmt.Sprintf("el binario Python no es un ejecutable Mach-O arm64 válido: %v", err),
					Recoverable: false,
					Err:         err,
				}
			}
		}
	}

	// Dynamic library verification on macOS
	if m.manifest.Platform == PlatformDarwin || runtime.GOOS == PlatformDarwin {
		dylibPath := filepath.Join(canonicalRoot, "python", "lib", "libpython3.11.dylib")
		dylibStat, err := os.Stat(dylibPath)
		if err != nil || !dylibStat.Mode().IsRegular() {
			return &SafeError{
				Code: ErrCodePythonStdlibIncomplete,
				SafeMessage: fmt.Sprintf("distribución de Python incompleta: falta biblioteca dinámica libpython3.11.dylib (%s)",
					dylibPath),
				Recoverable: false,
				Err:         ErrArtifactCorrupted,
			}
		}
		if m.manifest.Architecture == ArchArm64 {
			if err := ValidateMachODylib(dylibPath); err != nil {
				return &SafeError{
					Code:        ErrCodePythonArchIncompatible,
					SafeMessage: fmt.Sprintf("biblioteca libpython3.11.dylib no es Mach-O arm64 válida: %v", err),
					Recoverable: false,
					Err:         err,
				}
			}
		}
	}

	// Completeness verification: standard library directory must exist
	stdlibDir := filepath.Join(canonicalRoot, "python", "lib", "python3.11")
	stdlibStat, err := os.Stat(stdlibDir)
	if err != nil || !stdlibStat.IsDir() {
		return &SafeError{
			Code: ErrCodePythonStdlibIncomplete,
			SafeMessage: fmt.Sprintf("distribución de Python incompleta: falta el directorio de biblioteca estándar (%s)",
				stdlibDir),
			Recoverable: false,
			Err:         ErrArtifactCorrupted,
		}
	}

	// Verify essential module os.py exists
	osPyPath := filepath.Join(stdlibDir, "os.py")
	if osPyStat, err := os.Stat(osPyPath); err != nil || !osPyStat.Mode().IsRegular() {
		return &SafeError{
			Code: ErrCodePythonStdlibIncomplete,
			SafeMessage: fmt.Sprintf("distribución de Python incompleta: falta módulo os.py (%s)",
				osPyPath),
			Recoverable: false,
			Err:         ErrArtifactCorrupted,
		}
	}

	// Verify native dynamic extensions directory lib-dynload exists
	dynloadDir := filepath.Join(stdlibDir, "lib-dynload")
	if dynloadStat, err := os.Stat(dynloadDir); err != nil || !dynloadStat.IsDir() {
		return &SafeError{
			Code: ErrCodePythonStdlibIncomplete,
			SafeMessage: fmt.Sprintf("distribución de Python incompleta: falta directorio lib-dynload (%s)",
				dynloadDir),
			Recoverable: false,
			Err:         ErrArtifactCorrupted,
		}
	}

	// Verify standard library contains non-empty files
	entries, err := os.ReadDir(stdlibDir)
	if err != nil || len(entries) == 0 {
		return &SafeError{
			Code: ErrCodePythonStdlibIncomplete,
			SafeMessage: fmt.Sprintf("distribución de Python incompleta: el directorio de biblioteca estándar está vacío (%s)",
				stdlibDir),
			Recoverable: false,
			Err:         ErrArtifactCorrupted,
		}
	}

	return nil
}

// ValidateMachODylib verifies that the file at path is a Mach-O 64-bit arm64 dynamic library.
func ValidateMachODylib(path string) error {
	f, err := macho.Open(path)
	if err != nil {
		fat, fatErr := macho.OpenFat(path)
		if fatErr == nil {
			defer fat.Close()
			for _, arch := range fat.Arches {
				if arch.Cpu == macho.CpuArm64 && arch.Type == macho.TypeDylib {
					return nil
				}
			}
			return fmt.Errorf("%w: universal binary does not contain arm64 dylib slice", ErrManifestIncompatible)
		}
		return fmt.Errorf("%w: binary is not a valid Mach-O dylib (%v)", ErrManifestIncompatible, err)
	}
	defer f.Close()

	if f.Type != macho.TypeDylib {
		return fmt.Errorf("%w: Mach-O file type %v is not a dylib (type %v)", ErrManifestIncompatible, f.Type, macho.TypeDylib)
	}
	if f.Cpu != macho.CpuArm64 {
		return fmt.Errorf("%w: Mach-O CPU type %v is not arm64 (0x%x)", ErrManifestIncompatible, f.Cpu, macho.CpuArm64)
	}
	return nil
}

// ValidateMachOArch verifies that the file at path is a Mach-O 64-bit arm64 executable.
func ValidateMachOArch(path string) error {
	f, err := macho.Open(path)
	if err != nil {
		fat, fatErr := macho.OpenFat(path)
		if fatErr == nil {
			defer fat.Close()
			for _, arch := range fat.Arches {
				if arch.Cpu == macho.CpuArm64 && arch.Type == macho.TypeExec {
					return nil
				}
			}
			return fmt.Errorf("%w: universal binary does not contain arm64 executable slice", ErrManifestIncompatible)
		}
		return fmt.Errorf("%w: binary is not a valid Mach-O executable (%v)", ErrManifestIncompatible, err)
	}
	defer f.Close()

	if f.Type != macho.TypeExec {
		return fmt.Errorf("%w: Mach-O file type %v is not an executable (type %v)", ErrManifestIncompatible, f.Type, macho.TypeExec)
	}
	if f.Cpu != macho.CpuArm64 {
		return fmt.Errorf("%w: Mach-O CPU type %v is not arm64 (0x%x)", ErrManifestIncompatible, f.Cpu, macho.CpuArm64)
	}
	return nil
}

// isHostPkgManagerSegment returns true if a PATH segment belongs to a host package manager
// or external Python installation (Homebrew, Conda, pyenv, asdf, etc.).
func isHostPkgManagerSegment(segment string) bool {
	lower := strings.ToLower(segment)
	patterns := []string{
		"/opt/homebrew",
		"/usr/local/bin",
		"/usr/local/sbin",
		"homebrew",
		".pyenv",
		"conda",
		"miniconda",
		"anaconda",
		".asdf",
		".nvm",
		".cargo",
		"cellar",
	}
	for _, p := range patterns {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

// IsolateEnv scrubs environment variables to ensure strict subprocess isolation.
// It removes all PYTHON* environment variables, VIRTUAL_ENV, __PYVENV_LAUNCHER__,
// filters Homebrew / external package managers from PATH, and sets PYTHONNOUSERSITE=1.
func IsolateEnv(baseEnv []string) []string {
	var clean []string
	hasPath := false

	for _, env := range baseEnv {
		parts := strings.SplitN(env, "=", 2)
		if len(parts) == 0 {
			continue
		}
		key := parts[0]
		upperKey := strings.ToUpper(key)

		// 1. Strip all PYTHON* variables (PYTHONHOME, PYTHONPATH, PYTHONUSERBASE, etc.)
		if strings.HasPrefix(upperKey, "PYTHON") {
			continue
		}

		// 2. Strip VIRTUAL_ENV and __PYVENV_LAUNCHER__
		if upperKey == "VIRTUAL_ENV" || upperKey == "__PYVENV_LAUNCHER__" {
			continue
		}

		// 3. Filter PATH to remove host package managers
		if upperKey == "PATH" {
			hasPath = true
			if len(parts) > 1 {
				rawPath := parts[1]
				sep := string(os.PathListSeparator)
				segments := strings.Split(rawPath, sep)
				var cleanSegments []string
				for _, seg := range segments {
					trimmed := strings.TrimSpace(seg)
					if trimmed == "" {
						continue
					}
					if !isHostPkgManagerSegment(trimmed) {
						cleanSegments = append(cleanSegments, trimmed)
					}
				}
				if len(cleanSegments) == 0 {
					cleanSegments = []string{"/usr/bin", "/bin", "/usr/sbin", "/sbin"}
				}
				clean = append(clean, "PATH="+strings.Join(cleanSegments, sep))
			}
			continue
		}

		clean = append(clean, env)
	}

	if !hasPath {
		clean = append(clean, "PATH=/usr/bin:/bin:/usr/sbin:/sbin")
	}

	// Enforce isolated execution flags
	clean = append(clean, "PYTHONNOUSERSITE=1")

	return clean
}

// Command creates an isolated exec.Cmd for the embedded Python interpreter.
func (m *EmbeddedPythonManager) Command(ctx context.Context, args ...string) (*exec.Cmd, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}
	bin := m.BinaryPath()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = IsolateEnv(os.Environ())
	cmd.Dir = m.rootDir
	return cmd, nil
}

func defaultPythonCommandExecutor(ctx context.Context, binary string, env []string, args ...string) ([]byte, error) {
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

func (m *EmbeddedPythonManager) execCommand(ctx context.Context, bin string, args []string) ([]byte, error) {
	env := IsolateEnv(os.Environ())
	if m.execExecutor != nil {
		return m.execExecutor(ctx, bin, env, args...)
	}
	return defaultPythonCommandExecutor(ctx, bin, env, args...)
}

// Execute runs the interpreter with the given args in isolated mode.
func (m *EmbeddedPythonManager) Execute(ctx context.Context, args ...string) (*PythonExecutionResult, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}

	bin := m.BinaryPath()
	execCtx := ctx
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		execCtx, cancel = context.WithTimeout(ctx, DefaultPythonTimeout)
		defer cancel()
	}

	outBytes, err := m.execCommand(execCtx, bin, args)
	res := &PythonExecutionResult{
		Stdout: string(outBytes),
	}
	if err != nil {
		if errors.Is(execCtx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
			return nil, &SafeError{
				Code:        ErrCodePythonExecTimeout,
				SafeMessage: "la ejecución de Python excedió el tiempo límite",
				Recoverable: true,
				Err:         err,
			}
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			res.ExitCode = exitErr.ExitCode()
			res.Stderr = string(exitErr.Stderr)
		}
		return res, &SafeError{
			Code:        ErrCodePythonExecFailed,
			SafeMessage: fmt.Sprintf("error durante la ejecución de Python: %v", err),
			Recoverable: false,
			Details:     string(outBytes),
			Err:         err,
		}
	}
	return res, nil
}

// ProbeInterpreter executes a probe script in isolated mode to inspect version, ABI, and platform.
func (m *EmbeddedPythonManager) ProbeInterpreter(ctx context.Context) (*PythonRuntimeInfo, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}

	bin := m.BinaryPath()
	probeScript := "import sys, json; print(json.dumps({'version': sys.version.split()[0], 'major': sys.version_info[0], 'minor': sys.version_info[1], 'micro': sys.version_info[2], 'platform': sys.platform, 'executable': sys.executable, 'prefix': sys.prefix}))"

	execCtx := ctx
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		execCtx, cancel = context.WithTimeout(ctx, DefaultPythonTimeout)
		defer cancel()
	}

	outBytes, err := m.execCommand(execCtx, bin, []string{"-I", "-c", probeScript})
	if err != nil {
		if errors.Is(execCtx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
			return nil, &SafeError{
				Code:        ErrCodePythonExecTimeout,
				SafeMessage: "la ejecución del intérprete Python excedió el tiempo límite",
				Recoverable: true,
				Err:         err,
			}
		}
		return nil, &SafeError{
			Code:        ErrCodePythonExecFailed,
			SafeMessage: fmt.Sprintf("falló la ejecución de prueba del intérprete Python: %v", err),
			Recoverable: false,
			Details:     string(outBytes),
			Err:         err,
		}
	}

	trimmed := strings.TrimSpace(string(outBytes))
	var info PythonRuntimeInfo
	if err := json.Unmarshal([]byte(trimmed), &info); err != nil {
		return nil, &SafeError{
			Code:        ErrCodePythonExecFailed,
			SafeMessage: fmt.Sprintf("la salida de prueba del intérprete Python no es JSON válido: %v", err),
			Recoverable: false,
			Details:     trimmed,
			Err:         err,
		}
	}

	if info.Major != 3 || info.Minor != 11 {
		return nil, &SafeError{
			Code:        ErrCodePythonVersionIncompatible,
			SafeMessage: fmt.Sprintf("versión de Python incompatible (detectada %s, requerida 3.11.x)", info.Version),
			Recoverable: false,
			Err:         ErrManifestIncompatible,
		}
	}

	return &info, nil
}

// VerifyStandardLibrary ensures core standard library modules load exclusively from within the embedded runtime.
func (m *EmbeddedPythonManager) VerifyStandardLibrary(ctx context.Context) error {
	if err := m.Validate(); err != nil {
		return err
	}

	bin := m.BinaryPath()
	code := `
import sys, os, json, hashlib, platform, math, re, time
modules = [os, json, hashlib, platform, math, re, time]
prefix = sys.prefix
for mod in modules:
    path = getattr(mod, '__file__', None)
    if path and not path.startswith(prefix):
        raise RuntimeError(f"module {mod.__name__} loaded from {path}, outside prefix {prefix}")
print("STDLIB_VERIFIED")
`
	execCtx := ctx
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		execCtx, cancel = context.WithTimeout(ctx, DefaultPythonTimeout)
		defer cancel()
	}

	outBytes, err := m.execCommand(execCtx, bin, []string{"-I", "-c", code})
	if err != nil {
		if errors.Is(execCtx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
			return &SafeError{
				Code:        ErrCodePythonExecTimeout,
				SafeMessage: "la verificación de la biblioteca estándar de Python excedió el tiempo límite",
				Recoverable: true,
				Err:         err,
			}
		}
		return &SafeError{
			Code:        ErrCodePythonStdlibIncomplete,
			SafeMessage: fmt.Sprintf("error al verificar la biblioteca estándar de Python: %v", err),
			Recoverable: false,
			Details:     string(outBytes),
			Err:         err,
		}
	}

	if !strings.Contains(string(outBytes), "STDLIB_VERIFIED") {
		return &SafeError{
			Code:        ErrCodePythonStdlibIncomplete,
			SafeMessage: "la biblioteca estándar de Python no pudo ser confirmada",
			Recoverable: false,
			Details:     string(outBytes),
			Err:         ErrArtifactCorrupted,
		}
	}

	return nil
}
