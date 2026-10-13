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
	ManifestType   string            `json:"manifest_type,omitempty"`
	Platform       string            `json:"platform"`
	Architecture   string            `json:"architecture"`
	Python         PythonSpec        `json:"python"`
	Worker         WorkerSpec        `json:"worker"`
	Binaries       []BinarySpec      `json:"binaries"`
	Dependencies   []DependencySpec  `json:"dependencies"`
	Compatibility  CompatibilitySpec `json:"compatibility"`
}

// Manifest type classifications.
const (
	ManifestTypeTemplate = "template"
	ManifestTypeRelease  = "release"
)

// IsTemplate reports whether this manifest is a specification template rather than a sealed release pack.
func (m *RuntimeManifest) IsTemplate() bool {
	if m == nil {
		return false
	}
	return m.ManifestType == ManifestTypeTemplate || m.ManifestType == ""
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
			Err:         ErrManifestSchemaInvalid,
		}
	}

	var m RuntimeManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, &SafeError{
			Code:        "MANIFEST_INVALID_JSON",
			SafeMessage: fmt.Sprintf("%v: %v", ErrManifestInvalidJSON, err),
			Recoverable: false,
			Details:     err.Error(),
			Err:         ErrManifestInvalidJSON,
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
				Err:         ErrManifestNotFound,
			}
		}
		return nil, &SafeError{
			Code:        "MANIFEST_READ_ERROR",
			SafeMessage: "Error al leer el archivo de manifiesto de runtime.",
			Recoverable: true,
			Details:     err.Error(),
			Err:         err,
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

// canonicalizeManifestPath normalizes a relative manifest path by trimming spaces,
// converting separators to forward slashes, cleaning redundant elements, and stripping any leading './'.
func canonicalizeManifestPath(p string) string {
	trimmed := strings.TrimSpace(p)
	cleaned := filepath.Clean(filepath.ToSlash(trimmed))
	return strings.TrimPrefix(cleaned, "./")
}

// isPathConfined verifies that targetPath is physically inside or equal to baseDir.
// It uses filepath.Rel and component-level verification rather than string prefix checks,
// preventing partial prefix matches (e.g. /app/runtime matching /app/runtime_escape).
func isPathConfined(baseDir, targetPath string) (bool, error) {
	cleanBase := filepath.Clean(baseDir)
	cleanTarget := filepath.Clean(targetPath)
	rel, err := filepath.Rel(cleanBase, cleanTarget)
	if err != nil {
		return false, err
	}
	if rel == "." {
		return true, nil
	}
	if filepath.IsAbs(rel) {
		return false, nil
	}
	parts := strings.FieldsFunc(rel, func(r rune) bool {
		return r == '/' || r == '\\'
	})
	for _, part := range parts {
		if part == ".." {
			return false, nil
		}
	}
	return true, nil
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
			Err:         ErrManifestSchemaInvalid,
		}
	}

	if strings.TrimSpace(m.RuntimeVersion) == "" {
		return &SafeError{
			Code:        "INVALID_RUNTIME_VERSION",
			SafeMessage: fmt.Sprintf("%v: runtime_version es obligatorio", ErrManifestSchemaInvalid),
			Recoverable: false,
			Err:         ErrManifestSchemaInvalid,
		}
	}

	validPlatforms := map[string]bool{PlatformDarwin: true, PlatformLinux: true, PlatformWindows: true}
	if !validPlatforms[m.Platform] {
		return &SafeError{
			Code:        "INVALID_PLATFORM",
			SafeMessage: fmt.Sprintf("%v: platform %q no es válida (esperada: darwin, linux, windows)", ErrManifestSchemaInvalid, m.Platform),
			Recoverable: false,
			Err:         ErrManifestSchemaInvalid,
		}
	}

	validArchs := map[string]bool{ArchArm64: true, ArchAmd64: true}
	if !validArchs[m.Architecture] {
		return &SafeError{
			Code:        "INVALID_ARCHITECTURE",
			SafeMessage: fmt.Sprintf("%v: architecture %q no es válida (esperada: arm64, amd64)", ErrManifestSchemaInvalid, m.Architecture),
			Recoverable: false,
			Err:         ErrManifestSchemaInvalid,
		}
	}

	// Python spec validation
	if strings.TrimSpace(m.Python.Version) == "" || !strings.HasPrefix(m.Python.Version, "3.") {
		return &SafeError{
			Code:        "INVALID_PYTHON_VERSION",
			SafeMessage: fmt.Sprintf("%v: python.version %q inválida (debe ser Python 3.x)", ErrManifestSchemaInvalid, m.Python.Version),
			Recoverable: false,
			Err:         ErrManifestSchemaInvalid,
		}
	}
	if strings.TrimSpace(m.Python.ABI) == "" {
		return &SafeError{
			Code:        "INVALID_PYTHON_ABI",
			SafeMessage: fmt.Sprintf("%v: python.abi no puede estar vacío", ErrManifestSchemaInvalid),
			Recoverable: false,
			Err:         ErrManifestSchemaInvalid,
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
			Err:         ErrManifestSchemaInvalid,
		}
	}

	// Global artifact path tracker for cross-spec collision detection
	seenAllArtifactPaths := make(map[string]string)
	canonPyPath := canonicalizeManifestPath(m.Python.RelativeBinaryPath)
	seenAllArtifactPaths[canonPyPath] = "python.relative_binary_path"

	// Worker spec validation
	if strings.TrimSpace(m.Worker.Version) == "" {
		return &SafeError{
			Code:        "INVALID_WORKER_VERSION",
			SafeMessage: fmt.Sprintf("%v: worker.version no puede estar vacío", ErrManifestSchemaInvalid),
			Recoverable: false,
			Err:         ErrManifestSchemaInvalid,
		}
	}
	if err := validateRelativePath(m.Worker.Entrypoint, "worker.entrypoint"); err != nil {
		return err
	}
	canonWorkerPath := canonicalizeManifestPath(m.Worker.Entrypoint)
	if prev, exists := seenAllArtifactPaths[canonWorkerPath]; exists {
		return &SafeError{
			Code:        "DUPLICATE_ARTIFACT_PATH",
			SafeMessage: fmt.Sprintf("%v: worker.entrypoint (%q) colisiona con %s", ErrManifestSchemaInvalid, m.Worker.Entrypoint, prev),
			Recoverable: false,
			Err:         ErrManifestSchemaInvalid,
		}
	}
	seenAllArtifactPaths[canonWorkerPath] = "worker.entrypoint"

	if err := validateSHA256(m.Worker.SHA256, "worker.sha256"); err != nil {
		return err
	}
	if m.Worker.SizeBytes <= 0 {
		return &SafeError{
			Code:        "INVALID_SIZE",
			SafeMessage: fmt.Sprintf("%v: worker.size_bytes debe ser mayor a 0 (%d)", ErrManifestSchemaInvalid, m.Worker.SizeBytes),
			Recoverable: false,
			Err:         ErrManifestSchemaInvalid,
		}
	}

	// Binaries validation & duplicate check
	seenBinaryNames := make(map[string]bool)
	seenBinaryPaths := make(map[string]bool)
	for i, b := range m.Binaries {
		cleanName := strings.TrimSpace(b.Name)
		if cleanName == "" {
			return &SafeError{
				Code:        "INVALID_BINARY_NAME",
				SafeMessage: fmt.Sprintf("%v: binaries[%d].name no puede estar vacío", ErrManifestSchemaInvalid, i),
				Recoverable: false,
				Err:         ErrManifestSchemaInvalid,
			}
		}
		if seenBinaryNames[cleanName] {
			return &SafeError{
				Code:        "DUPLICATE_BINARY",
				SafeMessage: fmt.Sprintf("%v: binary duplicado con nombre %q", ErrManifestSchemaInvalid, b.Name),
				Recoverable: false,
				Err:         ErrManifestSchemaInvalid,
			}
		}
		seenBinaryNames[cleanName] = true

		if err := validateRelativePath(b.RelativePath, fmt.Sprintf("binaries[%d].relative_path", i)); err != nil {
			return err
		}
		canonBinPath := canonicalizeManifestPath(b.RelativePath)
		if seenBinaryPaths[canonBinPath] {
			return &SafeError{
				Code:        "DUPLICATE_BINARY_PATH",
				SafeMessage: fmt.Sprintf("%v: relative_path duplicado en binaries (%q)", ErrManifestSchemaInvalid, b.RelativePath),
				Recoverable: false,
				Err:         ErrManifestSchemaInvalid,
			}
		}
		seenBinaryPaths[canonBinPath] = true

		if prev, exists := seenAllArtifactPaths[canonBinPath]; exists {
			return &SafeError{
				Code:        "DUPLICATE_ARTIFACT_PATH",
				SafeMessage: fmt.Sprintf("%v: binaries[%d].relative_path (%q) colisiona con %s", ErrManifestSchemaInvalid, i, b.RelativePath, prev),
				Recoverable: false,
				Err:         ErrManifestSchemaInvalid,
			}
		}
		seenAllArtifactPaths[canonBinPath] = fmt.Sprintf("binaries[%d] (%s)", i, b.Name)

		if strings.TrimSpace(b.Version) == "" {
			return &SafeError{
				Code:        "INVALID_BINARY_VERSION",
				SafeMessage: fmt.Sprintf("%v: binaries[%d].version no puede estar vacío", ErrManifestSchemaInvalid, i),
				Recoverable: false,
				Err:         ErrManifestSchemaInvalid,
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
				Err:         ErrManifestSchemaInvalid,
			}
		}
	}

	// Dependencies validation & duplicate check
	seenDepNames := make(map[string]bool)
	seenDepFiles := make(map[string]bool)
	seenDepPaths := make(map[string]bool)
	for i, d := range m.Dependencies {
		cleanName := strings.TrimSpace(d.Name)
		if cleanName == "" {
			return &SafeError{
				Code:        "INVALID_DEP_NAME",
				SafeMessage: fmt.Sprintf("%v: dependencies[%d].name no puede estar vacío", ErrManifestSchemaInvalid, i),
				Recoverable: false,
				Err:         ErrManifestSchemaInvalid,
			}
		}
		if seenDepNames[cleanName] {
			return &SafeError{
				Code:        "DUPLICATE_DEPENDENCY",
				SafeMessage: fmt.Sprintf("%v: dependencia duplicada con nombre %q", ErrManifestSchemaInvalid, d.Name),
				Recoverable: false,
				Err:         ErrManifestSchemaInvalid,
			}
		}
		seenDepNames[cleanName] = true

		// Exact version pin enforcement: open ranges (<, >, =, ~) are strictly forbidden
		if strings.TrimSpace(d.Version) == "" || looseRangeRegex.MatchString(d.Version) {
			return &SafeError{
				Code: "INVALID_VERSION_PIN",
				SafeMessage: fmt.Sprintf("%v: dependencies[%d].version %q debe ser una versión exacta fija sin rangos ni operadores",
					ErrManifestSchemaInvalid, i, d.Version),
				Recoverable: false,
				Err:         ErrManifestSchemaInvalid,
			}
		}

		cleanDepFilename := filepath.Base(strings.TrimSpace(d.Filename))
		if cleanDepFilename == "" || cleanDepFilename == "." {
			return &SafeError{
				Code:        "INVALID_DEP_FILENAME",
				SafeMessage: fmt.Sprintf("%v: dependencies[%d].filename no puede estar vacío", ErrManifestSchemaInvalid, i),
				Recoverable: false,
				Err:         ErrManifestSchemaInvalid,
			}
		}
		if seenDepFiles[cleanDepFilename] {
			return &SafeError{
				Code:        "DUPLICATE_DEP_FILENAME",
				SafeMessage: fmt.Sprintf("%v: filename duplicado en dependencies (%q)", ErrManifestSchemaInvalid, d.Filename),
				Recoverable: false,
				Err:         ErrManifestSchemaInvalid,
			}
		}
		seenDepFiles[cleanDepFilename] = true

		if err := validateRelativePath(d.RelativePath, fmt.Sprintf("dependencies[%d].relative_path", i)); err != nil {
			return err
		}
		canonDepPath := canonicalizeManifestPath(d.RelativePath)
		if seenDepPaths[canonDepPath] {
			return &SafeError{
				Code:        "DUPLICATE_DEP_PATH",
				SafeMessage: fmt.Sprintf("%v: relative_path duplicado en dependencies (%q)", ErrManifestSchemaInvalid, d.RelativePath),
				Recoverable: false,
				Err:         ErrManifestSchemaInvalid,
			}
		}
		seenDepPaths[canonDepPath] = true

		if prev, exists := seenAllArtifactPaths[canonDepPath]; exists {
			return &SafeError{
				Code:        "DUPLICATE_ARTIFACT_PATH",
				SafeMessage: fmt.Sprintf("%v: dependencies[%d].relative_path (%q) colisiona con %s", ErrManifestSchemaInvalid, i, d.RelativePath, prev),
				Recoverable: false,
				Err:         ErrManifestSchemaInvalid,
			}
		}
		seenAllArtifactPaths[canonDepPath] = fmt.Sprintf("dependencies[%d] (%s)", i, d.Name)

		if err := validateSHA256(d.SHA256, fmt.Sprintf("dependencies[%d].sha256", i)); err != nil {
			return err
		}
		if d.SizeBytes <= 0 {
			return &SafeError{
				Code:        "INVALID_SIZE",
				SafeMessage: fmt.Sprintf("%v: dependencies[%d].size_bytes debe ser mayor a 0", ErrManifestSchemaInvalid, i),
				Recoverable: false,
				Err:         ErrManifestSchemaInvalid,
			}
		}
	}

	// Compatibility spec
	if strings.TrimSpace(m.Compatibility.EdgeMinVersion) == "" {
		return &SafeError{
			Code:        "INVALID_COMPATIBILITY",
			SafeMessage: fmt.Sprintf("%v: compatibility.edge_min_version es obligatorio", ErrManifestSchemaInvalid),
			Recoverable: false,
			Err:         ErrManifestSchemaInvalid,
		}
	}
	if strings.TrimSpace(m.Compatibility.ModelsPackMinVersion) == "" {
		return &SafeError{
			Code:        "INVALID_COMPATIBILITY",
			SafeMessage: fmt.Sprintf("%v: compatibility.models_pack_min_version es obligatorio", ErrManifestSchemaInvalid),
			Recoverable: false,
			Err:         ErrManifestSchemaInvalid,
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
			Err:         ErrManifestIncompatible,
		}
	}
	if m.Architecture != targetArch {
		return &SafeError{
			Code: "INCOMPATIBLE_ARCHITECTURE",
			SafeMessage: fmt.Sprintf("%v: arquitectura del manifiesto %q no coincide con la arquitectura destino %q",
				ErrManifestIncompatible, m.Architecture, targetArch),
			Recoverable: false,
			Err:         ErrManifestIncompatible,
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
//
// Security & TOCTOU Architectural Consideration:
// Path validation resolves and verifies physical locations component-by-component,
// strictly forbidding escaping symlinks, cyclic links, broken links, or path traversal at check time.
// However, path validation alone does not eliminate Time-of-Check to Time-of-Use (TOCTOU) risks
// under concurrent modification by other processes. Absolute activation safety is guaranteed in
// Milestone H6 (Staging & Activation) through private directory isolation (0700 permissions)
// and atomic filesystem swap (os.Rename).
func (m *RuntimeManifest) ValidateArtifacts(rootDir string) (*ArtifactsValidationReport, error) {
	return m.ValidateArtifactsWithProgress(rootDir, nil)
}

// ValidateArtifactsWithProgress verifies all files in rootDir, optionally notifying progress.
// It verifies the physical canonical path of each component against the canonicalized root.
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

	// Canonicalize the root itself by evaluating all symbolic links in its path
	canonicalRoot, err := filepath.EvalSymlinks(absRoot)
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
			SafeMessage: fmt.Sprintf("Error al acceder al directorio raíz del runtime: %v", err),
			Recoverable: false,
			Details:     err.Error(),
		}
	}
	canonicalRoot = filepath.Clean(canonicalRoot)

	rootInfo, err := os.Stat(canonicalRoot)
	if err != nil {
		return nil, &SafeError{
			Code:        "ROOT_DIR_ERROR",
			SafeMessage: fmt.Sprintf("Error al verificar directorio raíz del runtime: %v", err),
			Recoverable: true,
			Details:     err.Error(),
		}
	}
	if !rootInfo.IsDir() {
		return nil, &SafeError{
			Code:        "ROOT_NOT_A_DIRECTORY",
			SafeMessage: fmt.Sprintf("La ruta del runtime no es un directorio: %s", canonicalRoot),
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

		cleanRel := canonicalizeManifestPath(target.RelativePath)
		if err := validateRelativePath(cleanRel, "relative_path"); err != nil {
			res.Error = fmt.Sprintf("%v: ruta no permitida (%q)", ErrArtifactSecurity, target.RelativePath)
			report.Results = append(report.Results, res)
			return report, &SafeError{
				Code:        "SECURITY_TRAVERSAL",
				SafeMessage: fmt.Sprintf("%v: %s", ErrArtifactSecurity, target.RelativePath),
				Recoverable: false,
				Err:         ErrArtifactSecurity,
			}
		}

		parts := strings.FieldsFunc(cleanRel, func(r rune) bool {
			return r == '/' || r == '\\'
		})
		if len(parts) == 0 {
			res.Error = fmt.Sprintf("%v: ruta vacía", ErrArtifactSecurity)
			report.Results = append(report.Results, res)
			return report, &SafeError{
				Code:        "SECURITY_TRAVERSAL",
				SafeMessage: fmt.Sprintf("%v: ruta vacía", ErrArtifactSecurity),
				Recoverable: false,
				Err:         ErrArtifactSecurity,
			}
		}

		// Resolve each path component physically, ensuring no parent or leaf symlink escapes canonicalRoot
		currentPhysical := canonicalRoot
		for partIdx, part := range parts {
			isLeaf := (partIdx == len(parts)-1)
			candidate := filepath.Join(currentPhysical, part)

			// Check existence before following symlink
			_, err := os.Lstat(candidate)
			if err != nil {
				if os.IsNotExist(err) {
					res.Error = fmt.Sprintf("%v: archivo no encontrado (%s)", ErrArtifactMissing, target.RelativePath)
					report.Results = append(report.Results, res)
					return report, &SafeError{
						Code:        "ARTIFACT_MISSING",
						SafeMessage: fmt.Sprintf("%v: %s", ErrArtifactMissing, target.RelativePath),
						Recoverable: true,
						Err:         ErrArtifactMissing,
					}
				}
				res.Error = err.Error()
				report.Results = append(report.Results, res)
				return report, &SafeError{
					Code:        "ARTIFACT_ACCESS_ERROR",
					SafeMessage: fmt.Sprintf("Error al verificar archivo %s", target.RelativePath),
					Recoverable: true,
					Details:     err.Error(),
					Err:         err,
				}
			}

			// Evaluate symbolic links on candidate to obtain real physical target
			resolvedPart, err := filepath.EvalSymlinks(candidate)
			if err != nil {
				res.Error = fmt.Sprintf("%v: symlink inválido, roto o cíclico en %s", ErrArtifactSecurity, target.RelativePath)
				report.Results = append(report.Results, res)
				return report, &SafeError{
					Code:        "SECURITY_BROKEN_SYMLINK",
					SafeMessage: fmt.Sprintf("%v: symlink inválido en %s", ErrArtifactSecurity, target.RelativePath),
					Recoverable: false,
					Err:         ErrArtifactSecurity,
				}
			}

			// Confinement check: resolved component MUST remain strictly inside canonicalRoot
			confined, err := isPathConfined(canonicalRoot, resolvedPart)
			if err != nil || !confined {
				res.Error = fmt.Sprintf("%v: symlink escapa del runtime pack", ErrArtifactSecurity)
				report.Results = append(report.Results, res)
				return report, &SafeError{
					Code:        "SECURITY_ESCAPING_SYMLINK",
					SafeMessage: fmt.Sprintf("%v: symlink en %s apunta fuera del directorio autorizado", ErrArtifactSecurity, target.RelativePath),
					Recoverable: false,
					Err:         ErrArtifactSecurity,
				}
			}

			// For intermediate path components, ensure it is a directory
			if !isLeaf {
				partStat, err := os.Stat(resolvedPart)
				if err != nil || !partStat.IsDir() {
					res.Error = fmt.Sprintf("%v: componente intermedio en %s no es un directorio", ErrArtifactCorrupted, target.RelativePath)
					report.Results = append(report.Results, res)
					return report, &SafeError{
						Code:        "ARTIFACT_NOT_DIRECTORY",
						SafeMessage: fmt.Sprintf("%v: componente intermedio en %s no es un directorio", ErrArtifactCorrupted, target.RelativePath),
						Recoverable: false,
						Err:         ErrArtifactCorrupted,
					}
				}
			}

			currentPhysical = resolvedPart
		}

		// Inspect the physical target file
		targetFileInfo, err := os.Stat(currentPhysical)
		if err != nil {
			if os.IsNotExist(err) {
				res.Error = fmt.Sprintf("%v: archivo no encontrado", ErrArtifactMissing)
				report.Results = append(report.Results, res)
				return report, &SafeError{
					Code:        "ARTIFACT_MISSING",
					SafeMessage: fmt.Sprintf("%v: %s", ErrArtifactMissing, target.RelativePath),
					Recoverable: true,
					Err:         ErrArtifactMissing,
				}
			}
			res.Error = err.Error()
			report.Results = append(report.Results, res)
			return report, &SafeError{
				Code:        "ARTIFACT_ACCESS_ERROR",
				SafeMessage: fmt.Sprintf("Error al verificar archivo %s", target.RelativePath),
				Recoverable: true,
				Details:     err.Error(),
				Err:         err,
			}
		}

		if !targetFileInfo.Mode().IsRegular() {
			res.Error = fmt.Sprintf("%v: no es un archivo regular", ErrArtifactCorrupted)
			report.Results = append(report.Results, res)
			return report, &SafeError{
				Code:        "ARTIFACT_NOT_REGULAR",
				SafeMessage: fmt.Sprintf("%v: %s no es un archivo regular", ErrArtifactCorrupted, target.RelativePath),
				Recoverable: false,
				Err:         ErrArtifactCorrupted,
			}
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
				Err:         ErrArtifactCorrupted,
			}
		}

		// Streaming SHA-256 calculation on the physical file
		fileHash, err := hashFileSHA256(currentPhysical)
		if err != nil {
			res.Error = err.Error()
			report.Results = append(report.Results, res)
			return report, &SafeError{
				Code:        "HASH_COMPUTATION_FAILED",
				SafeMessage: fmt.Sprintf("Error calculando hash SHA-256 de %s", target.RelativePath),
				Recoverable: true,
				Details:     err.Error(),
				Err:         err,
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
				Err:         ErrArtifactCorrupted,
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
