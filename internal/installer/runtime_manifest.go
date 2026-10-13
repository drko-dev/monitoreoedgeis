package installer

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

//go:embed assets/runtime-manifest.json
var embeddedRuntimeManifestJSON []byte

// Schema and platform constraints.
const (
	CurrentSchemaVersion = "1.0.0"
	PlatformDarwin       = "darwin"
	PlatformLinux        = "linux"
	PlatformWindows      = "windows"
	ArchArm64            = "arm64"
	ArchAmd64            = "amd64"
)

var (
	// ErrManifestNotFound is returned when the manifest file does not exist.
	ErrManifestNotFound = errors.New("runtime manifest file not found")
	// ErrManifestInvalidJSON is returned when JSON unmarshaling fails.
	ErrManifestInvalidJSON = errors.New("runtime manifest contains invalid JSON")
	// ErrManifestSchemaInvalid is returned when schema fields violate specifications.
	ErrManifestSchemaInvalid = errors.New("runtime manifest schema validation failed")
	// ErrManifestIncompatible is returned when target OS or architecture does not match.
	ErrManifestIncompatible = errors.New("runtime manifest is incompatible with target platform or architecture")
	// ErrArtifactMissing is returned when an expected artifact is absent from the pack.
	ErrArtifactMissing = errors.New("runtime artifact missing")
	// ErrArtifactCorrupted is returned when an artifact size or SHA-256 checksum does not match.
	ErrArtifactCorrupted = errors.New("runtime artifact corrupted or hash mismatch")
	// ErrArtifactSecurity is returned when path traversal or unauthorized symlinks are detected.
	ErrArtifactSecurity = errors.New("runtime artifact path violates security constraints")
)

var (
	sha256Regex     = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)
	looseRangeRegex = regexp.MustCompile(`[<>=~*^]`)
)

// PythonSpec specifies the standalone Python interpreter embedded in the pack.
type PythonSpec struct {
	Version            string `json:"version"`
	ABI                string `json:"abi"`
	RelativeBinaryPath string `json:"relative_binary_path"`
	SHA256             string `json:"sha256"`
	SizeBytes          int64  `json:"size_bytes"`
}

// WorkerSpec specifies the Vision Worker script entrypoint and integrity.
type WorkerSpec struct {
	Version    string `json:"version"`
	Entrypoint string `json:"entrypoint"`
	SHA256     string `json:"sha256"`
	SizeBytes  int64  `json:"size_bytes"`
}

// BinarySpec specifies a standalone native executable (e.g. ffmpeg, ffprobe).
type BinarySpec struct {
	Name         string `json:"name"`
	RelativePath string `json:"relative_path"`
	Version      string `json:"version"`
	SHA256       string `json:"sha256"`
	SizeBytes    int64  `json:"size_bytes"`
}

// DependencySpec specifies an offline Python wheel dependency with exact versioning.
type DependencySpec struct {
	Name         string `json:"name"`
	Version      string `json:"version"`
	Filename     string `json:"filename"`
	RelativePath string `json:"relative_path"`
	SHA256       string `json:"sha256"`
	SizeBytes    int64  `json:"size_bytes"`
}

// CompatibilitySpec specifies version compatibility constraints.
type CompatibilitySpec struct {
	EdgeMinVersion       string `json:"edge_min_version"`
	ModelsPackMinVersion string `json:"models_pack_min_version"`
}

// RuntimeManifest defines the complete contract and cryptographic verification
// metadata for an offline Runtime Pack.
//
// TOCTOU (Time-of-Check to Time-of-Use) Architectural Consideration:
// ValidateArtifacts verifies physical files in the local filesystem at inspection time.
// In concurrent environments, files could theoretically be altered between verification
// and execution. To guarantee absolute integrity:
//  1. In Milestone H6 (Staging & Activation), unpacking and verification MUST occur in
//     an isolated temporary directory ($GEOCAM_DATA_DIR/vision-runtime.tmp.<pid>) with 0700 permissions.
//  2. Verification MUST complete before the directory is activated.
//  3. Activation MUST occur via atomic filesystem rename (os.Rename), ensuring the running
//     service only ever consumes a fully verified, sealed directory.
//  4. Executable binaries should receive read-only permissions (0555) post-staging.
type RuntimeManifest struct {
	SchemaVersion  string            `json:"schema_version"`
	RuntimeVersion string            `json:"runtime_version"`
	Platform       string            `json:"platform"`
	Architecture   string            `json:"architecture"`
	Python         PythonSpec        `json:"python"`
	Worker         WorkerSpec        `json:"worker"`
	Binaries       []BinarySpec      `json:"binaries"`
	Dependencies   []DependencySpec  `json:"dependencies"`
	Compatibility  CompatibilitySpec `json:"compatibility"`
}

// ArtifactValidationResult reports the validation outcome for a single file.
type ArtifactValidationResult struct {
	RelativePath string `json:"relative_path"`
	ExpectedHash string `json:"expected_hash"`
	ActualHash   string `json:"actual_hash,omitempty"`
	ExpectedSize int64  `json:"expected_size"`
	ActualSize   int64  `json:"actual_size,omitempty"`
	Valid        bool   `json:"valid"`
	Error        string `json:"error,omitempty"`
}

// ArtifactsValidationReport aggregates verification outcomes for all runtime pack files.
type ArtifactsValidationReport struct {
	TotalArtifacts int                        `json:"total_artifacts"`
	ValidArtifacts int                        `json:"valid_artifacts"`
	Results        []ArtifactValidationResult `json:"results"`
}

// ParseRuntimeManifest unmarshals and validates JSON data into a RuntimeManifest.
func ParseRuntimeManifest(data []byte) (*RuntimeManifest, error) {
	if len(data) == 0 {
		return nil, &SafeError{
			Code:        "MANIFEST_EMPTY",
			SafeMessage: "El manifiesto de runtime está vacío.",
			Recoverable: false,
		}
	}

	var m RuntimeManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, &SafeError{
			Code:        "MANIFEST_INVALID_JSON",
			SafeMessage: fmt.Sprintf("%v: %v", ErrManifestInvalidJSON, err),
			Recoverable: false,
			Details:     err.Error(),
		}
	}

	if err := m.ValidateSchema(); err != nil {
		return nil, err
	}

	return &m, nil
}

// LoadRuntimeManifest reads and parses a runtime manifest from a file path.
func LoadRuntimeManifest(path string) (*RuntimeManifest, error) {
	cleanPath := filepath.Clean(path)
	data, err := os.ReadFile(cleanPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, &SafeError{
				Code:        "MANIFEST_NOT_FOUND",
				SafeMessage: fmt.Sprintf("%v: %s", ErrManifestNotFound, cleanPath),
				Recoverable: true,
			}
		}
		return nil, &SafeError{
			Code:        "MANIFEST_READ_ERROR",
			SafeMessage: "Error al leer el archivo de manifiesto de runtime.",
			Recoverable: true,
			Details:     err.Error(),
		}
	}
	return ParseRuntimeManifest(data)
}

// LoadEmbeddedRuntimeManifest parses the embedded default runtime-manifest.json.
func LoadEmbeddedRuntimeManifest() (*RuntimeManifest, error) {
	if len(embeddedRuntimeManifestJSON) == 0 {
		return nil, &SafeError{
			Code:        "EMBEDDED_MANIFEST_EMPTY",
			SafeMessage: "El manifiesto de runtime embebido no está disponible.",
			Recoverable: false,
		}
	}
	return ParseRuntimeManifest(embeddedRuntimeManifestJSON)
}

// ValidateRuntimeManifest loads and validates schema integrity for a manifest file path.
func ValidateRuntimeManifest(path string) (*RuntimeManifest, error) {
	return LoadRuntimeManifest(path)
}

// validateRelativePath verifies that a path is strictly relative and does not traverse upwards.
func validateRelativePath(p, fieldName string) error {
	trimmed := strings.TrimSpace(p)
	if trimmed == "" {
		return fmt.Errorf("%w: el campo %s no puede estar vacío", ErrManifestSchemaInvalid, fieldName)
	}
	if filepath.IsAbs(trimmed) || strings.HasPrefix(trimmed, "/") || strings.HasPrefix(trimmed, "\\") {
		return fmt.Errorf("%w: el campo %s debe ser una ruta relativa, no absoluta (%q)",
			ErrArtifactSecurity, fieldName, trimmed)
	}

	clean := filepath.Clean(trimmed)
	if clean == "." || strings.HasPrefix(clean, "..") {
		return fmt.Errorf("%w: el campo %s contiene path traversal inválido (%q)",
			ErrArtifactSecurity, fieldName, trimmed)
	}

	parts := strings.FieldsFunc(clean, func(r rune) bool {
		return r == '/' || r == '\\'
	})
	for _, part := range parts {
		if part == ".." {
			return fmt.Errorf("%w: el campo %s contiene segmento de traversal '..' (%q)",
				ErrArtifactSecurity, fieldName, trimmed)
		}
	}

	return nil
}

// validateSHA256 verifies that a string is a valid 64-character hex digest.
func validateSHA256(h, fieldName string) error {
	trimmed := strings.TrimSpace(h)
	if !sha256Regex.MatchString(trimmed) {
		return fmt.Errorf("%w: el campo %s debe contener un hash SHA-256 hexadecimal de 64 caracteres válido (%q)",
			ErrManifestSchemaInvalid, fieldName, trimmed)
	}
	return nil
}

// ValidateSchema verifies all metadata, fields, and constraints of the manifest.
func (m *RuntimeManifest) ValidateSchema() error {
	if m == nil {
		return &SafeError{
			Code:        "MANIFEST_NIL",
			SafeMessage: "El manifiesto es nulo.",
			Recoverable: false,
		}
	}

	if m.SchemaVersion != CurrentSchemaVersion {
		return &SafeError{
			Code:        "UNSUPPORTED_SCHEMA_VERSION",
			SafeMessage: fmt.Sprintf("%v: schema_version %q no soportada (esperada %q)", ErrManifestSchemaInvalid, m.SchemaVersion, CurrentSchemaVersion),
			Recoverable: false,
		}
	}

	if strings.TrimSpace(m.RuntimeVersion) == "" {
		return &SafeError{
			Code:        "INVALID_RUNTIME_VERSION",
			SafeMessage: fmt.Sprintf("%v: runtime_version es obligatorio", ErrManifestSchemaInvalid),
			Recoverable: false,
		}
	}

	validPlatforms := map[string]bool{PlatformDarwin: true, PlatformLinux: true, PlatformWindows: true}
	if !validPlatforms[m.Platform] {
		return &SafeError{
			Code:        "INVALID_PLATFORM",
			SafeMessage: fmt.Sprintf("%v: platform %q no es válida (esperada: darwin, linux, windows)", ErrManifestSchemaInvalid, m.Platform),
			Recoverable: false,
		}
	}

	validArchs := map[string]bool{ArchArm64: true, ArchAmd64: true}
	if !validArchs[m.Architecture] {
		return &SafeError{
			Code:        "INVALID_ARCHITECTURE",
			SafeMessage: fmt.Sprintf("%v: architecture %q no es válida (esperada: arm64, amd64)", ErrManifestSchemaInvalid, m.Architecture),
			Recoverable: false,
		}
	}

	// Python spec validation
	if strings.TrimSpace(m.Python.Version) == "" || !strings.HasPrefix(m.Python.Version, "3.") {
		return &SafeError{
			Code:        "INVALID_PYTHON_VERSION",
			SafeMessage: fmt.Sprintf("%v: python.version %q inválida (debe ser Python 3.x)", ErrManifestSchemaInvalid, m.Python.Version),
			Recoverable: false,
		}
	}
	if strings.TrimSpace(m.Python.ABI) == "" {
		return &SafeError{
			Code:        "INVALID_PYTHON_ABI",
			SafeMessage: fmt.Sprintf("%v: python.abi no puede estar vacío", ErrManifestSchemaInvalid),
			Recoverable: false,
		}
	}
	if err := validateRelativePath(m.Python.RelativeBinaryPath, "python.relative_binary_path"); err != nil {
		return err
	}
	if err := validateSHA256(m.Python.SHA256, "python.sha256"); err != nil {
		return err
	}
	if m.Python.SizeBytes <= 0 {
		return &SafeError{
			Code:        "INVALID_SIZE",
			SafeMessage: fmt.Sprintf("%v: python.size_bytes debe ser mayor a 0 (%d)", ErrManifestSchemaInvalid, m.Python.SizeBytes),
			Recoverable: false,
		}
	}

	// Worker spec validation
	if strings.TrimSpace(m.Worker.Version) == "" {
		return &SafeError{
			Code:        "INVALID_WORKER_VERSION",
			SafeMessage: fmt.Sprintf("%v: worker.version no puede estar vacío", ErrManifestSchemaInvalid),
			Recoverable: false,
		}
	}
	if err := validateRelativePath(m.Worker.Entrypoint, "worker.entrypoint"); err != nil {
		return err
	}
	if err := validateSHA256(m.Worker.SHA256, "worker.sha256"); err != nil {
		return err
	}
	if m.Worker.SizeBytes <= 0 {
		return &SafeError{
			Code:        "INVALID_SIZE",
			SafeMessage: fmt.Sprintf("%v: worker.size_bytes debe ser mayor a 0 (%d)", ErrManifestSchemaInvalid, m.Worker.SizeBytes),
			Recoverable: false,
		}
	}

	// Binaries validation & duplicate check
	seenBinaryNames := make(map[string]bool)
	seenBinaryPaths := make(map[string]bool)
	for i, b := range m.Binaries {
		if strings.TrimSpace(b.Name) == "" {
			return &SafeError{
				Code:        "INVALID_BINARY_NAME",
				SafeMessage: fmt.Sprintf("%v: binaries[%d].name no puede estar vacío", ErrManifestSchemaInvalid, i),
				Recoverable: false,
			}
		}
		if seenBinaryNames[b.Name] {
			return &SafeError{
				Code:        "DUPLICATE_BINARY",
				SafeMessage: fmt.Sprintf("%v: binary duplicado con nombre %q", ErrManifestSchemaInvalid, b.Name),
				Recoverable: false,
			}
		}
		seenBinaryNames[b.Name] = true

		if err := validateRelativePath(b.RelativePath, fmt.Sprintf("binaries[%d].relative_path", i)); err != nil {
			return err
		}
		if seenBinaryPaths[b.RelativePath] {
			return &SafeError{
				Code:        "DUPLICATE_BINARY_PATH",
				SafeMessage: fmt.Sprintf("%v: relative_path duplicado en binaries (%q)", ErrManifestSchemaInvalid, b.RelativePath),
				Recoverable: false,
			}
		}
		seenBinaryPaths[b.RelativePath] = true

		if strings.TrimSpace(b.Version) == "" {
			return &SafeError{
				Code:        "INVALID_BINARY_VERSION",
				SafeMessage: fmt.Sprintf("%v: binaries[%d].version no puede estar vacío", ErrManifestSchemaInvalid, i),
				Recoverable: false,
			}
		}
		if err := validateSHA256(b.SHA256, fmt.Sprintf("binaries[%d].sha256", i)); err != nil {
			return err
		}
		if b.SizeBytes <= 0 {
			return &SafeError{
				Code:        "INVALID_SIZE",
				SafeMessage: fmt.Sprintf("%v: binaries[%d].size_bytes debe ser mayor a 0", ErrManifestSchemaInvalid, i),
				Recoverable: false,
			}
		}
	}

	// Dependencies validation & duplicate check
	seenDepNames := make(map[string]bool)
	seenDepFiles := make(map[string]bool)
	seenDepPaths := make(map[string]bool)
	for i, d := range m.Dependencies {
		if strings.TrimSpace(d.Name) == "" {
			return &SafeError{
				Code:        "INVALID_DEP_NAME",
				SafeMessage: fmt.Sprintf("%v: dependencies[%d].name no puede estar vacío", ErrManifestSchemaInvalid, i),
				Recoverable: false,
			}
		}
		if seenDepNames[d.Name] {
			return &SafeError{
				Code:        "DUPLICATE_DEPENDENCY",
				SafeMessage: fmt.Sprintf("%v: dependencia duplicada con nombre %q", ErrManifestSchemaInvalid, d.Name),
				Recoverable: false,
			}
		}
		seenDepNames[d.Name] = true

		// Exact version pin enforcement: open ranges (<, >, =, ~) are strictly forbidden
		if strings.TrimSpace(d.Version) == "" || looseRangeRegex.MatchString(d.Version) {
			return &SafeError{
				Code: "INVALID_VERSION_PIN",
				SafeMessage: fmt.Sprintf("%v: dependencies[%d].version %q debe ser una versión exacta fija sin rangos ni operadores",
					ErrManifestSchemaInvalid, i, d.Version),
				Recoverable: false,
			}
		}

		if strings.TrimSpace(d.Filename) == "" {
			return &SafeError{
				Code:        "INVALID_DEP_FILENAME",
				SafeMessage: fmt.Sprintf("%v: dependencies[%d].filename no puede estar vacío", ErrManifestSchemaInvalid, i),
				Recoverable: false,
			}
		}
		if seenDepFiles[d.Filename] {
			return &SafeError{
				Code:        "DUPLICATE_DEP_FILENAME",
				SafeMessage: fmt.Sprintf("%v: filename duplicado en dependencies (%q)", ErrManifestSchemaInvalid, d.Filename),
				Recoverable: false,
			}
		}
		seenDepFiles[d.Filename] = true

		if err := validateRelativePath(d.RelativePath, fmt.Sprintf("dependencies[%d].relative_path", i)); err != nil {
			return err
		}
		if seenDepPaths[d.RelativePath] {
			return &SafeError{
				Code:        "DUPLICATE_DEP_PATH",
				SafeMessage: fmt.Sprintf("%v: relative_path duplicado en dependencies (%q)", ErrManifestSchemaInvalid, d.RelativePath),
				Recoverable: false,
			}
		}
		seenDepPaths[d.RelativePath] = true

		if err := validateSHA256(d.SHA256, fmt.Sprintf("dependencies[%d].sha256", i)); err != nil {
			return err
		}
		if d.SizeBytes <= 0 {
			return &SafeError{
				Code:        "INVALID_SIZE",
				SafeMessage: fmt.Sprintf("%v: dependencies[%d].size_bytes debe ser mayor a 0", ErrManifestSchemaInvalid, i),
				Recoverable: false,
			}
		}
	}

	// Compatibility spec
	if strings.TrimSpace(m.Compatibility.EdgeMinVersion) == "" {
		return &SafeError{
			Code:        "INVALID_COMPATIBILITY",
			SafeMessage: fmt.Sprintf("%v: compatibility.edge_min_version es obligatorio", ErrManifestSchemaInvalid),
			Recoverable: false,
		}
	}
	if strings.TrimSpace(m.Compatibility.ModelsPackMinVersion) == "" {
		return &SafeError{
			Code:        "INVALID_COMPATIBILITY",
			SafeMessage: fmt.Sprintf("%v: compatibility.models_pack_min_version es obligatorio", ErrManifestSchemaInvalid),
			Recoverable: false,
		}
	}

	return nil
}

// ValidateCompatibility checks if the manifest targets the given platform and architecture.
func (m *RuntimeManifest) ValidateCompatibility(targetPlatform, targetArch string) error {
	if m == nil {
		return &SafeError{
			Code:        "MANIFEST_NIL",
			SafeMessage: "El manifiesto es nulo.",
			Recoverable: false,
		}
	}
	if m.Platform != targetPlatform {
		return &SafeError{
			Code: "INCOMPATIBLE_PLATFORM",
			SafeMessage: fmt.Sprintf("%v: plataforma del manifiesto %q no coincide con la plataforma destino %q",
				ErrManifestIncompatible, m.Platform, targetPlatform),
			Recoverable: false,
		}
	}
	if m.Architecture != targetArch {
		return &SafeError{
			Code: "INCOMPATIBLE_ARCHITECTURE",
			SafeMessage: fmt.Sprintf("%v: arquitectura del manifiesto %q no coincide con la arquitectura destino %q",
				ErrManifestIncompatible, m.Architecture, targetArch),
			Recoverable: false,
		}
	}
	return nil
}

type artifactTarget struct {
	RelativePath string
	ExpectedHash string
	ExpectedSize int64
}

// allArtifacts flattens the list of all files declared in the manifest.
func (m *RuntimeManifest) allArtifacts() []artifactTarget {
	targets := []artifactTarget{
		{
			RelativePath: m.Python.RelativeBinaryPath,
			ExpectedHash: m.Python.SHA256,
			ExpectedSize: m.Python.SizeBytes,
		},
		{
			RelativePath: m.Worker.Entrypoint,
			ExpectedHash: m.Worker.SHA256,
			ExpectedSize: m.Worker.SizeBytes,
		},
	}

	for _, b := range m.Binaries {
		targets = append(targets, artifactTarget{
			RelativePath: b.RelativePath,
			ExpectedHash: b.SHA256,
			ExpectedSize: b.SizeBytes,
		})
	}

	for _, d := range m.Dependencies {
		targets = append(targets, artifactTarget{
			RelativePath: d.RelativePath,
			ExpectedHash: d.SHA256,
			ExpectedSize: d.SizeBytes,
		})
	}

	return targets
}

// ValidateArtifacts scans and cryptographically verifies all physical files in a runtime pack directory.
func (m *RuntimeManifest) ValidateArtifacts(rootDir string) (*ArtifactsValidationReport, error) {
	return m.ValidateArtifactsWithProgress(rootDir, nil)
}

// ValidateArtifactsWithProgress verifies all files in rootDir, optionally notifying progress.
func (m *RuntimeManifest) ValidateArtifactsWithProgress(
	rootDir string,
	progress func(current, total int, path string),
) (*ArtifactsValidationReport, error) {
	if m == nil {
		return nil, &SafeError{
			Code:        "MANIFEST_NIL",
			SafeMessage: "El manifiesto es nulo.",
			Recoverable: false,
		}
	}

	absRoot, err := filepath.Abs(filepath.Clean(rootDir))
	if err != nil {
		return nil, &SafeError{
			Code:        "INVALID_ROOT_DIR",
			SafeMessage: "Ruta de directorio raíz de runtime inválida.",
			Recoverable: false,
			Details:     err.Error(),
		}
	}

	rootInfo, err := os.Stat(absRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, &SafeError{
				Code:        "ROOT_DIR_NOT_FOUND",
				SafeMessage: fmt.Sprintf("El directorio del runtime no existe: %s", absRoot),
				Recoverable: true,
			}
		}
		return nil, &SafeError{
			Code:        "ROOT_DIR_ERROR",
			SafeMessage: "Error al acceder al directorio raíz del runtime.",
			Recoverable: true,
			Details:     err.Error(),
		}
	}
	if !rootInfo.IsDir() {
		return nil, &SafeError{
			Code:        "ROOT_NOT_A_DIRECTORY",
			SafeMessage: fmt.Sprintf("La ruta del runtime no es un directorio: %s", absRoot),
			Recoverable: false,
		}
	}

	targets := m.allArtifacts()
	report := &ArtifactsValidationReport{
		TotalArtifacts: len(targets),
		Results:        make([]ArtifactValidationResult, 0, len(targets)),
	}

	for i, target := range targets {
		if progress != nil {
			progress(i+1, len(targets), target.RelativePath)
		}

		res := ArtifactValidationResult{
			RelativePath: target.RelativePath,
			ExpectedHash: target.ExpectedHash,
			ExpectedSize: target.ExpectedSize,
		}

		fullPath := filepath.Clean(filepath.Join(absRoot, target.RelativePath))

		// Confinement check: path must remain inside absRoot
		rel, err := filepath.Rel(absRoot, fullPath)
		if err != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
			res.Error = fmt.Sprintf("%v: ruta escapa del directorio raíz", ErrArtifactSecurity)
			report.Results = append(report.Results, res)
			return report, &SafeError{
				Code:        "SECURITY_TRAVERSAL",
				SafeMessage: fmt.Sprintf("%v: %s", ErrArtifactSecurity, target.RelativePath),
				Recoverable: false,
			}
		}

		// Inspection & symlink protection
		lInfo, err := os.Lstat(fullPath)
		if err != nil {
			if os.IsNotExist(err) {
				res.Error = fmt.Sprintf("%v: archivo no encontrado", ErrArtifactMissing)
				report.Results = append(report.Results, res)
				return report, &SafeError{
					Code:        "ARTIFACT_MISSING",
					SafeMessage: fmt.Sprintf("%v: %s", ErrArtifactMissing, target.RelativePath),
					Recoverable: true,
				}
			}
			res.Error = err.Error()
			report.Results = append(report.Results, res)
			return report, &SafeError{
				Code:        "ARTIFACT_ACCESS_ERROR",
				SafeMessage: fmt.Sprintf("Error al verificar archivo %s", target.RelativePath),
				Recoverable: true,
				Details:     err.Error(),
			}
		}

		var targetFileInfo os.FileInfo
		if lInfo.Mode()&os.ModeSymlink != 0 {
			// Symlink handling: ensure resolved target also stays strictly within absRoot
			resolvedPath, err := filepath.EvalSymlinks(fullPath)
			if err != nil {
				res.Error = fmt.Sprintf("%v: symlink roto o inaccesible", ErrArtifactSecurity)
				report.Results = append(report.Results, res)
				return report, &SafeError{
					Code:        "SECURITY_BROKEN_SYMLINK",
					SafeMessage: fmt.Sprintf("%v: symlink inválido en %s", ErrArtifactSecurity, target.RelativePath),
					Recoverable: false,
				}
			}

			relResolved, err := filepath.Rel(absRoot, resolvedPath)
			if err != nil || strings.HasPrefix(relResolved, "..") || filepath.IsAbs(relResolved) {
				res.Error = fmt.Sprintf("%v: symlink escapa del runtime pack", ErrArtifactSecurity)
				report.Results = append(report.Results, res)
				return report, &SafeError{
					Code:        "SECURITY_ESCAPING_SYMLINK",
					SafeMessage: fmt.Sprintf("%v: symlink en %s apunta fuera del directorio autorizado", ErrArtifactSecurity, target.RelativePath),
					Recoverable: false,
				}
			}

			targetFileInfo, err = os.Stat(resolvedPath)
			if err != nil || !targetFileInfo.Mode().IsRegular() {
				res.Error = fmt.Sprintf("%v: el destino del symlink no es un archivo regular", ErrArtifactCorrupted)
				report.Results = append(report.Results, res)
				return report, &SafeError{
					Code:        "ARTIFACT_NOT_REGULAR",
					SafeMessage: fmt.Sprintf("%v: el destino de %s no es un archivo regular", ErrArtifactCorrupted, target.RelativePath),
					Recoverable: false,
				}
			}
		} else {
			if !lInfo.Mode().IsRegular() {
				res.Error = fmt.Sprintf("%v: no es un archivo regular", ErrArtifactCorrupted)
				report.Results = append(report.Results, res)
				return report, &SafeError{
					Code:        "ARTIFACT_NOT_REGULAR",
					SafeMessage: fmt.Sprintf("%v: %s no es un archivo regular", ErrArtifactCorrupted, target.RelativePath),
					Recoverable: false,
				}
			}
			targetFileInfo = lInfo
		}

		res.ActualSize = targetFileInfo.Size()
		if res.ActualSize != target.ExpectedSize {
			res.Error = fmt.Sprintf("%v: tamaño %d bytes difiere del esperado %d bytes",
				ErrArtifactCorrupted, res.ActualSize, target.ExpectedSize)
			report.Results = append(report.Results, res)
			return report, &SafeError{
				Code: "ARTIFACT_SIZE_MISMATCH",
				SafeMessage: fmt.Sprintf("%v: tamaño de %s (%d bytes) no coincide con el manifest (%d bytes)",
					ErrArtifactCorrupted, target.RelativePath, res.ActualSize, target.ExpectedSize),
				Recoverable: false,
			}
		}

		// Streaming SHA-256 calculation
		fileHash, err := hashFileSHA256(fullPath)
		if err != nil {
			res.Error = err.Error()
			report.Results = append(report.Results, res)
			return report, &SafeError{
				Code:        "HASH_COMPUTATION_FAILED",
				SafeMessage: fmt.Sprintf("Error calculando hash SHA-256 de %s", target.RelativePath),
				Recoverable: true,
				Details:     err.Error(),
			}
		}

		res.ActualHash = fileHash
		if !strings.EqualFold(res.ActualHash, target.ExpectedHash) {
			res.Error = fmt.Sprintf("%v: hash calculado %s no coincide con el esperado %s",
				ErrArtifactCorrupted, res.ActualHash, target.ExpectedHash)
			report.Results = append(report.Results, res)
			return report, &SafeError{
				Code: "ARTIFACT_HASH_MISMATCH",
				SafeMessage: fmt.Sprintf("%v: hash de %s no coincide con el manifest",
					ErrArtifactCorrupted, target.RelativePath),
				Recoverable: false,
			}
		}

		res.Valid = true
		report.ValidArtifacts++
		report.Results = append(report.Results, res)
	}

	return report, nil
}

// hashFileSHA256 streams file contents to compute the SHA-256 hex digest without full memory buffering.
func hashFileSHA256(filePath string) (string, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	hasher := sha256.New()
	if _, err := io.Copy(hasher, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}
