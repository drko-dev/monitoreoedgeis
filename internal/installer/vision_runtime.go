package installer

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/config"
	"github.com/drko-dev/monitoreoedgeis/internal/fulledge"
)

//go:embed assets/vision-worker/*
var embeddedWorkerFS embed.FS

//go:embed assets/models-manifest.json
var embeddedManifestJSON []byte

// ModelState describes the physical and structural integrity of a model file.
type ModelState string

const (
	ModelStateValid        ModelState = "VALID"
	ModelStateMissing      ModelState = "MISSING"
	ModelStateCorrupted    ModelState = "CORRUPTED"
	ModelStateIncompatible ModelState = "INCOMPATIBLE"
)

// ModelSpec defines metadata and integrity constraints for an AI weight file.
type ModelSpec struct {
	Name              string `json:"name"`
	Filename          string `json:"filename"`
	Version           string `json:"version"`
	Purpose           string `json:"purpose"`
	MinSizeBytes      int64  `json:"min_size_bytes"`
	ExpectedSizeBytes int64  `json:"expected_size_bytes"`
	SHA256            string `json:"sha256"`
}

// ModelsManifest defines the pinned manifest of official models.
type ModelsManifest struct {
	PackVersion          string            `json:"pack_version"`
	WorkerCompatibility  string            `json:"worker_compatibility"`
	RuntimeCompatibility map[string]string `json:"runtime_compatibility"`
	Models               []ModelSpec       `json:"models"`
}

// LoadModelsManifest parses the embedded models-manifest.json.
func LoadModelsManifest() (*ModelsManifest, error) {
	if len(embeddedManifestJSON) == 0 {
		return nil, errors.New("embedded models manifest is empty")
	}
	var manifest ModelsManifest
	if err := json.Unmarshal(embeddedManifestJSON, &manifest); err != nil {
		return nil, fmt.Errorf("failed to parse models manifest: %w", err)
	}
	return &manifest, nil
}

// VisionRuntimeStatus reflects the inspection and readiness state of the local
// Full Edge vision runtime and its required detection models.
type VisionRuntimeStatus struct {
	PythonFound        bool              `json:"python_found"`
	PythonPath         string            `json:"python_path"`
	PythonVersion      string            `json:"python_version"`
	VenvFound          bool              `json:"venv_found"`
	VenvPath           string            `json:"venv_path"`
	WorkerScriptPath   string            `json:"worker_script_path"`
	PackagesReady      bool              `json:"packages_ready"`
	TorchAvailable     bool              `json:"torch_available"`
	TorchVersion       string            `json:"torch_version,omitempty"`
	UltralyticsReady   bool              `json:"ultralytics_ready"`
	UltralyticsVersion string            `json:"ultralytics_version,omitempty"`
	PillowReady        bool              `json:"pillow_ready"`
	Device             string            `json:"device"`
	DeviceName         string            `json:"device_name"`
	ModelsDir          string            `json:"models_dir"`
	PersonModelFound   bool              `json:"person_model_found"`
	VehicleModelFound  bool              `json:"vehicle_model_found"`
	MissingModels      []string          `json:"missing_models,omitempty"`
	CorruptedModels    []string          `json:"corrupted_models,omitempty"`
	ModelStates        map[string]string `json:"model_states,omitempty"`
	Ready              bool              `json:"ready"`
	CanProvision       bool              `json:"can_provision"`
	Stage              string            `json:"stage,omitempty"`
	Message            string            `json:"message,omitempty"`
}

type pythonProbeOutput struct {
	Missing    []string          `json:"missing"`
	Versions   map[string]string `json:"versions"`
	Device     string            `json:"device"`
	DeviceName string            `json:"device_name"`
	Python     string            `json:"python"`
}

const probeScript = `
import importlib, json, sys

missing = []
versions = {}
for mod, dist in [('ultralytics', 'ultralytics'), ('torch', 'torch'), ('PIL', 'pillow')]:
    try:
        m = importlib.import_module(mod)
        versions[mod] = getattr(m, '__version__', 'unknown')
    except Exception:
        missing.append(dist)

device = 'cpu'
device_name = 'CPU'
try:
    import torch
    if hasattr(torch.backends, 'mps') and torch.backends.mps.is_available():
        device = 'mps'
        device_name = 'Apple Silicon Metal (MPS)'
    elif torch.cuda.is_available():
        name = torch.cuda.get_device_name(0) if torch.cuda.device_count() > 0 else 'NVIDIA'
        device = 'cuda'
        device_name = f'NVIDIA CUDA ({name})'
except Exception:
    pass

print(json.dumps({
    'missing': missing,
    'versions': versions,
    'device': device,
    'device_name': device_name,
    'python': sys.version.split()[0]
}))
`

// VisionRuntimeResolver inspects host facts for Full Edge runtime readiness.
type VisionRuntimeResolver struct {
	dataDir           string
	lookPath          func(file string) (string, error)
	stat              func(name string) (os.FileInfo, error)
	execCmd           CommandExecutor
	goos              string
	goarch            string
	userHomeDir       func() (string, error)
	getConfiguredCmd  func() (string, bool, error)
	getConfiguredArgs func() (string, bool, error)
	getConfiguredDir  func() (string, bool, error)
	getConfiguredDev  func() (string, bool, error)
}

// NewVisionRuntimeResolver constructs a resolver for dataDir using OS primitives.
func NewVisionRuntimeResolver(dataDir string) *VisionRuntimeResolver {
	return &VisionRuntimeResolver{
		dataDir:     dataDir,
		lookPath:    exec.LookPath,
		stat:        os.Stat,
		execCmd:     DefaultCommandExecutor,
		goos:        runtime.GOOS,
		goarch:      runtime.GOARCH,
		userHomeDir: os.UserHomeDir,
		getConfiguredCmd: func() (string, bool, error) {
			return config.PersistentValue("GEOCAM_EDGE_YOLO_WORKER_CMD")
		},
		getConfiguredArgs: func() (string, bool, error) {
			return config.PersistentValue("GEOCAM_EDGE_YOLO_WORKER_ARGS")
		},
		getConfiguredDir: func() (string, bool, error) {
			return config.PersistentValue("GEOCAM_EDGE_YOLO_MODELS_DIR")
		},
		getConfiguredDev: func() (string, bool, error) {
			return config.PersistentValue("GEOCAM_EDGE_YOLO_DEVICE")
		},
	}
}

func (r *VisionRuntimeResolver) venvPythonPath() string {
	venvDir := filepath.Join(r.dataDir, "vision-runtime")
	if r.goos == "windows" {
		return filepath.Join(venvDir, "Scripts", "python.exe")
	}
	return filepath.Join(venvDir, "bin", "python")
}

func (r *VisionRuntimeResolver) stagedWorkerPath() string {
	return filepath.Join(r.dataDir, "vision-worker", "worker.py")
}

// ValidateModelFile checks the physical and structural integrity of a model file against a ModelSpec.
func (r *VisionRuntimeResolver) ValidateModelFile(path string, spec ModelSpec) (ModelState, string) {
	info, err := r.stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ModelStateMissing, "file does not exist"
		}
		return ModelStateMissing, err.Error()
	}
	if !info.Mode().IsRegular() {
		return ModelStateCorrupted, "not a regular file"
	}
	if info.Size() <= 0 {
		return ModelStateCorrupted, "file is empty"
	}
	if os.Getenv("GEOCAM_ALLOW_MOCK_MODELS") == "1" {
		return ModelStateValid, "valid"
	}
	if spec.MinSizeBytes > 0 && info.Size() < spec.MinSizeBytes {
		return ModelStateCorrupted, fmt.Sprintf("file size %d is below minimum threshold %d bytes", info.Size(), spec.MinSizeBytes)
	}
	if spec.SHA256 != "" && spec.ExpectedSizeBytes > 0 && info.Size() == spec.ExpectedSizeBytes {
		f, err := os.Open(path)
		if err == nil {
			defer f.Close()
			h := sha256.New()
			if _, err := io.Copy(h, f); err == nil {
				calculated := hex.EncodeToString(h.Sum(nil))
				if !strings.EqualFold(calculated, spec.SHA256) {
					return ModelStateCorrupted, fmt.Sprintf("checksum mismatch: expected %s, got %s", spec.SHA256, calculated)
				}
			}
		}
	}
	return ModelStateValid, "valid"
}

// Resolve inspects host python, venv, packages, and models.
func (r *VisionRuntimeResolver) Resolve(ctx context.Context) (*VisionRuntimeStatus, error) {
	status := &VisionRuntimeStatus{
		Device:     "cpu",
		DeviceName: "CPU",
	}

	// 1. Locate Models directory & verify model weights
	modelsDir, hasModelsDir, _ := r.getConfiguredDir()
	if !hasModelsDir || strings.TrimSpace(modelsDir) == "" {
		modelsDir = filepath.Join(r.dataDir, config.DefaultEdgeYOLOModelsDirName)
	}
	status.ModelsDir = modelsDir
	status.ModelStates = make(map[string]string)

	personModel := config.DefaultEdgeYOLOPersonModel
	vehicleModel := config.DefaultEdgeYOLOVehicleModel

	manifest, _ := LoadModelsManifest()
	var personSpec, vehicleSpec ModelSpec
	if manifest != nil {
		for _, m := range manifest.Models {
			if m.Name == personModel || m.Filename == personModel {
				personSpec = m
			}
			if m.Name == vehicleModel || m.Filename == vehicleModel {
				vehicleSpec = m
			}
		}
	}
	if personSpec.MinSizeBytes == 0 {
		personSpec = ModelSpec{Name: personModel, MinSizeBytes: 10_000_000}
	}
	if vehicleSpec.MinSizeBytes == 0 {
		vehicleSpec = ModelSpec{Name: vehicleModel, MinSizeBytes: 3_000_000}
	}

	personPath := filepath.Join(modelsDir, personModel)
	pState, _ := r.ValidateModelFile(personPath, personSpec)
	status.ModelStates[personModel] = string(pState)
	if pState == ModelStateValid {
		status.PersonModelFound = true
	} else if pState == ModelStateCorrupted {
		status.PersonModelFound = false
		status.CorruptedModels = append(status.CorruptedModels, personModel)
	} else {
		status.PersonModelFound = false
		status.MissingModels = append(status.MissingModels, personModel)
	}

	vehiclePath := filepath.Join(modelsDir, vehicleModel)
	vState, _ := r.ValidateModelFile(vehiclePath, vehicleSpec)
	status.ModelStates[vehicleModel] = string(vState)
	if vState == ModelStateValid {
		status.VehicleModelFound = true
	} else if vState == ModelStateCorrupted {
		status.VehicleModelFound = false
		status.CorruptedModels = append(status.CorruptedModels, vehicleModel)
	} else {
		status.VehicleModelFound = false
		status.MissingModels = append(status.MissingModels, vehicleModel)
	}

	// 2. Worker script location
	configuredArgs, hasArgs, _ := r.getConfiguredArgs()
	if hasArgs && strings.TrimSpace(configuredArgs) != "" {
		parts := strings.Fields(configuredArgs)
		if len(parts) > 0 {
			status.WorkerScriptPath = parts[0]
		}
	}
	if status.WorkerScriptPath == "" {
		stagedWorker := r.stagedWorkerPath()
		if _, err := r.stat(stagedWorker); err == nil {
			status.WorkerScriptPath = stagedWorker
		}
	}

	// 3. Inspect Python / Venv
	venvPy := r.venvPythonPath()
	if _, err := r.stat(venvPy); err == nil {
		status.VenvFound = true
		status.VenvPath = filepath.Dir(filepath.Dir(venvPy))
	}

	// Candidate to probe for packages
	configuredCmd, hasCmd, _ := r.getConfiguredCmd()
	var pythonToProbe string
	if hasCmd && strings.TrimSpace(configuredCmd) != "" {
		pythonToProbe = strings.TrimSpace(configuredCmd)
	} else if status.VenvFound {
		pythonToProbe = venvPy
	}

	// If no configured or venv python, find base python on the system
	basePy, baseVer := r.findBasePython(ctx)
	if basePy != "" {
		status.PythonFound = true
		status.PythonPath = basePy
		status.PythonVersion = baseVer
		status.CanProvision = r.isVersionCompatible(baseVer)
	}

	// Probe packages if we have a candidate python
	if pythonToProbe != "" {
		if out, err := r.probePython(ctx, pythonToProbe); err == nil {
			status.PythonFound = true
			status.PythonPath = pythonToProbe
			if out.Python != "" {
				status.PythonVersion = out.Python
			}
			if len(out.Missing) == 0 {
				status.PackagesReady = true
				status.TorchAvailable = true
				status.UltralyticsReady = true
				status.PillowReady = true
			} else {
				for mod, ver := range out.Versions {
					switch mod {
					case "torch":
						status.TorchAvailable = true
						status.TorchVersion = ver
					case "ultralytics":
						status.UltralyticsReady = true
						status.UltralyticsVersion = ver
					case "PIL":
						status.PillowReady = true
					}
				}
			}
			if out.Device != "" {
				status.Device = out.Device
				status.DeviceName = out.DeviceName
			}
		}
	}

	// Hardware detection override check if configured device specifies cuda
	configuredDev, hasDev, _ := r.getConfiguredDev()
	if hasDev && strings.EqualFold(strings.TrimSpace(configuredDev), "cuda") {
		if !strings.EqualFold(status.Device, "cuda") && (fulledge.DefaultHardwareDetector{}).DetectCUDA() {
			status.Device = "cuda"
			status.DeviceName = "NVIDIA CUDA"
		}
	}

	// 4. Overall Readiness evaluation
	workerScriptValid := false
	if status.WorkerScriptPath != "" {
		if _, err := r.stat(status.WorkerScriptPath); err == nil {
			workerScriptValid = true
		}
	}
	// For legacy/custom worker commands (e.g. mock binary), if command resolvable and models present
	if hasCmd && commandResolvable(configuredCmd) && status.PersonModelFound && status.VehicleModelFound {
		status.Ready = true
	} else if status.PackagesReady && workerScriptValid && status.PersonModelFound && status.VehicleModelFound {
		status.Ready = true
	}

	// Construct human-readable status message
	var msgs []string
	if !status.PackagesReady {
		if status.CanProvision {
			msgs = append(msgs, "Entorno de inferencia no preparado (requiere instalación de paquetes).")
		} else if !status.PythonFound {
			msgs = append(msgs, "Python 3 no detectado en el sistema.")
		} else {
			msgs = append(msgs, "Python detectado no es compatible con el worker.")
		}
	}
	if len(status.MissingModels) > 0 {
		msgs = append(msgs, fmt.Sprintf("Modelos faltantes en %s: %s", status.ModelsDir, strings.Join(status.MissingModels, ", ")))
	}
	if len(status.CorruptedModels) > 0 {
		msgs = append(msgs, fmt.Sprintf("Modelos corruptos detectados en %s: %s (archivo truncado o checksum inválido).", status.ModelsDir, strings.Join(status.CorruptedModels, ", ")))
	}
	if status.Ready {
		status.Message = fmt.Sprintf("Listo para inferencia local (%s)", status.DeviceName)
	} else if len(msgs) > 0 {
		status.Message = strings.Join(msgs, " ")
	}

	return status, nil
}

// SelfHealModel copies valid model content from source into target modelName atomically.
func (r *VisionRuntimeResolver) SelfHealModel(ctx context.Context, modelName string, source io.Reader, spec ModelSpec) error {
	modelsDir, hasDir, _ := r.getConfiguredDir()
	if !hasDir || strings.TrimSpace(modelsDir) == "" {
		modelsDir = filepath.Join(r.dataDir, config.DefaultEdgeYOLOModelsDirName)
	}
	if err := os.MkdirAll(modelsDir, 0o755); err != nil {
		return fmt.Errorf("failed to create models directory: %w", err)
	}

	destPath := filepath.Join(modelsDir, modelName)
	tmpPath := filepath.Join(modelsDir, fmt.Sprintf(".%s.%d.tmp", modelName, time.Now().UnixNano()))

	tmpFile, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}

	hasher := sha256.New()
	writer := io.MultiWriter(tmpFile, hasher)

	written, copyErr := io.Copy(writer, source)
	syncErr := tmpFile.Sync()
	closeErr := tmpFile.Close()

	if copyErr != nil || syncErr != nil || closeErr != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("failed while streaming model data: copy=%v, sync=%v, close=%v", copyErr, syncErr, closeErr)
	}

	allowMock := os.Getenv("GEOCAM_ALLOW_MOCK_MODELS") == "1"
	if !allowMock && spec.MinSizeBytes > 0 && written < spec.MinSizeBytes {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("self-heal rejected: written size %d bytes is below required %d bytes", written, spec.MinSizeBytes)
	}

	if !allowMock && spec.SHA256 != "" && spec.ExpectedSizeBytes > 0 && written == spec.ExpectedSizeBytes {
		calculated := hex.EncodeToString(hasher.Sum(nil))
		if !strings.EqualFold(calculated, spec.SHA256) {
			_ = os.Remove(tmpPath)
			return fmt.Errorf("self-heal rejected: sha256 mismatch (expected %s, got %s)", spec.SHA256, calculated)
		}
	}

	if err := os.Rename(tmpPath, destPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("atomic rename failed: %w", err)
	}

	return nil
}

// ImportModel validates and atomically copies a local .pt model file into the managed models directory.
func (r *VisionRuntimeResolver) ImportModel(ctx context.Context, modelName, sourcePath string) (*VisionRuntimeStatus, error) {
	allowedModels := map[string]bool{
		config.DefaultEdgeYOLOPersonModel:  true, // "yolo11s-pose.pt"
		config.DefaultEdgeYOLOVehicleModel: true, // "yolo11n.pt"
	}
	if !allowedModels[modelName] {
		return nil, &SafeError{
			Code:        "INVALID_MODEL_NAME",
			SafeMessage: fmt.Sprintf("Model name %q is not recognized or allowed.", modelName),
			Recoverable: false,
		}
	}

	cleanSrc := filepath.Clean(sourcePath)
	info, err := r.stat(cleanSrc)
	if err != nil {
		return nil, &SafeError{
			Code:        "FILE_NOT_FOUND",
			SafeMessage: fmt.Sprintf("Source model file not found: %s", cleanSrc),
			Recoverable: true,
		}
	}
	if !info.Mode().IsRegular() {
		return nil, &SafeError{
			Code:        "INVALID_FILE_TYPE",
			SafeMessage: "Selected model must be a regular file.",
			Recoverable: false,
		}
	}

	if info.Size() <= 0 {
		return nil, &SafeError{
			Code:        "EMPTY_FILE",
			SafeMessage: "Selected model file is empty.",
			Recoverable: false,
		}
	}

	minSize := int64(1)
	if os.Getenv("GEOCAM_ALLOW_MOCK_MODELS") != "1" {
		if manifest, _ := LoadModelsManifest(); manifest != nil {
			for _, m := range manifest.Models {
				if m.Name == modelName || m.Filename == modelName {
					if m.MinSizeBytes > 0 {
						minSize = m.MinSizeBytes
					}
					break
				}
			}
		}
	}
	if info.Size() < minSize {
		return nil, &SafeError{
			Code:        "CORRUPTED_MODEL",
			SafeMessage: fmt.Sprintf("El archivo de modelo seleccionado está truncado o corrupto (%d bytes, mínimo esperado %d bytes).", info.Size(), minSize),
			Recoverable: false,
		}
	}

	modelsDir, hasDir, _ := r.getConfiguredDir()
	if !hasDir || strings.TrimSpace(modelsDir) == "" {
		modelsDir = filepath.Join(r.dataDir, config.DefaultEdgeYOLOModelsDirName)
	}
	if err := os.MkdirAll(modelsDir, 0o755); err != nil {
		return nil, &SafeError{
			Code:        "MKDIR_FAILED",
			SafeMessage: "Failed to create models directory.",
			Recoverable: true,
			Details:     err.Error(),
		}
	}

	destPath := filepath.Join(modelsDir, modelName)
	tmpPath := filepath.Join(modelsDir, fmt.Sprintf(".%s.%d.tmp", modelName, time.Now().UnixNano()))

	srcFile, err := os.Open(cleanSrc)
	if err != nil {
		return nil, &SafeError{
			Code:        "READ_ERROR",
			SafeMessage: "Failed to read source model file.",
			Recoverable: true,
			Details:     err.Error(),
		}
	}
	defer srcFile.Close()

	tmpFile, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, &SafeError{
			Code:        "WRITE_ERROR",
			SafeMessage: "Failed to create destination model file.",
			Recoverable: true,
			Details:     err.Error(),
		}
	}

	_, copyErr := io.Copy(tmpFile, srcFile)
	syncErr := tmpFile.Sync()
	closeErr := tmpFile.Close()

	if copyErr != nil || syncErr != nil || closeErr != nil {
		_ = os.Remove(tmpPath)
		return nil, &SafeError{
			Code:        "COPY_FAILED",
			SafeMessage: "Failed to copy model file.",
			Recoverable: true,
		}
	}

	if err := os.Rename(tmpPath, destPath); err != nil {
		_ = os.Remove(tmpPath)
		return nil, &SafeError{
			Code:        "RENAME_FAILED",
			SafeMessage: "Failed to place model file into models directory.",
			Recoverable: true,
			Details:     err.Error(),
		}
	}

	return r.Resolve(ctx)
}

func (r *VisionRuntimeResolver) isVersionCompatible(ver string) bool {
	parts := strings.Split(ver, ".")
	if len(parts) < 2 {
		return false
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil || major != 3 {
		return false
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil || minor < 8 || minor > 13 {
		return false
	}
	return true
}

func (r *VisionRuntimeResolver) probePython(ctx context.Context, pyBin string) (*pythonProbeOutput, error) {
	out, err := r.execCmd(ctx, pyBin, "-c", probeScript)
	if err != nil {
		return nil, err
	}
	var probe pythonProbeOutput
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	lastLine := lines[len(lines)-1]
	if err := json.Unmarshal([]byte(lastLine), &probe); err != nil {
		return nil, fmt.Errorf("invalid probe json: %w (%s)", err, string(out))
	}
	return &probe, nil
}

func (r *VisionRuntimeResolver) findBasePython(ctx context.Context) (string, string) {
	var candidates []string

	if r.goos == "darwin" {
		homeDir := ""
		if r.userHomeDir != nil {
			homeDir, _ = r.userHomeDir()
		}

		// Priority 1: Homebrew on Apple Silicon (/opt/homebrew) and Intel (/usr/local)
		candidates = append(candidates,
			"/opt/homebrew/bin/python3.12",
			"/opt/homebrew/bin/python3.11",
			"/opt/homebrew/bin/python3.10",
			"/opt/homebrew/bin/python3.9",
			"/opt/homebrew/bin/python3.13",
			"/opt/homebrew/bin/python3",
			"/usr/local/bin/python3.12",
			"/usr/local/bin/python3.11",
			"/usr/local/bin/python3.10",
			"/usr/local/bin/python3.9",
			"/usr/local/bin/python3.13",
			"/usr/local/bin/python3",
		)
		// Priority 2: User-local environment (~/.local/bin)
		if homeDir != "" {
			userLocal := filepath.Join(homeDir, ".local", "bin")
			candidates = append(candidates,
				filepath.Join(userLocal, "python3.12"),
				filepath.Join(userLocal, "python3.11"),
				filepath.Join(userLocal, "python3.10"),
				filepath.Join(userLocal, "python3.9"),
				filepath.Join(userLocal, "python3.13"),
				filepath.Join(userLocal, "python3"),
			)
		}
		// Priority 3: System PATH lookups
		candidates = append(candidates,
			"python3.12",
			"python3.11",
			"python3.10",
			"python3.9",
			"python3.13",
			"python3",
		)
		// Priority 4: Apple system python as last fallback
		candidates = append(candidates, "/usr/bin/python3")
	} else if r.goos == "linux" {
		candidates = append(candidates,
			"/usr/bin/python3.12",
			"/usr/bin/python3.11",
			"/usr/bin/python3.10",
			"/usr/bin/python3.9",
			"/usr/bin/python3.13",
			"/usr/bin/python3",
			"/usr/local/bin/python3.12",
			"/usr/local/bin/python3.11",
			"/usr/local/bin/python3",
			"python3.12",
			"python3.11",
			"python3.10",
			"python3.9",
			"python3",
		)
	} else {
		candidates = append(candidates,
			"python3.12",
			"python3.11",
			"python3.10",
			"python3.9",
			"python3",
			"python",
		)
	}

	type evaluatedCandidate struct {
		path    string
		version string
		score   int
	}

	var valid []evaluatedCandidate
	seen := make(map[string]bool)

	for _, cand := range candidates {
		var resolved string
		if filepath.IsAbs(cand) {
			if info, err := r.stat(cand); err == nil && !info.IsDir() {
				resolved = cand
			}
		} else {
			if p, err := r.lookPath(cand); err == nil {
				resolved = p
			}
		}
		if resolved == "" || seen[resolved] {
			continue
		}
		seen[resolved] = true

		out, err := r.execCmd(ctx, resolved, "-c", "import sys; print(f'{sys.version_info[0]}.{sys.version_info[1]}.{sys.version_info[2]}')")
		if err != nil {
			continue
		}
		ver := strings.TrimSpace(string(out))
		if !r.isVersionCompatible(ver) {
			continue
		}

		// Calculate candidate quality score
		score := 0
		parts := strings.Split(ver, ".")
		minor := 0
		if len(parts) >= 2 {
			minor, _ = strconv.Atoi(parts[1])
		}

		switch minor {
		case 11:
			score += 100
		case 12:
			score += 95
		case 10:
			score += 90
		case 9:
			score += 85
		case 13:
			score += 80
		default:
			score += 70
		}

		// Location scoring: heavily prefer Homebrew/local binaries, penalize /usr/bin/python3
		if resolved == "/usr/bin/python3" {
			score -= 30
		} else if strings.HasPrefix(resolved, "/opt/homebrew/") || strings.HasPrefix(resolved, "/usr/local/") {
			score += 15
		}

		valid = append(valid, evaluatedCandidate{path: resolved, version: ver, score: score})
	}

	if len(valid) == 0 {
		return "", ""
	}

	// Select candidate with highest score
	best := valid[0]
	for _, c := range valid[1:] {
		if c.score > best.score {
			best = c
		}
	}

	return best.path, best.version
}

// VisionRuntimeProvisioner handles the safe, user-initiated setup of the local
// venv and dependencies for Full Edge mode.
type VisionRuntimeProvisioner struct {
	resolver  *VisionRuntimeResolver
	timeAfter func(d time.Duration) <-chan time.Time
}

// NewVisionRuntimeProvisioner builds a provisioner from a resolver.
func NewVisionRuntimeProvisioner(resolver *VisionRuntimeResolver) *VisionRuntimeProvisioner {
	return &VisionRuntimeProvisioner{
		resolver:  resolver,
		timeAfter: time.After,
	}
}

// StageWorkerAssets copies embedded worker.py, backend.py, requirements.txt to destDir.
func StageWorkerAssets(destDir string) error {
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return fmt.Errorf("create worker directory %s: %w", destDir, err)
	}

	entries, err := embeddedWorkerFS.ReadDir("assets/vision-worker")
	if err != nil {
		return fmt.Errorf("read embedded worker assets: %w", err)
	}

	for _, entry := range entries {
		data, err := embeddedWorkerFS.ReadFile("assets/vision-worker/" + entry.Name())
		if err != nil {
			return fmt.Errorf("read embedded asset %s: %w", entry.Name(), err)
		}
		targetPath := filepath.Join(destDir, entry.Name())
		if err := os.WriteFile(targetPath, data, 0o644); err != nil {
			return fmt.Errorf("write staged file %s: %w", targetPath, err)
		}
	}
	return nil
}

// isVenvHealthy checks if the existing venv can run python and pip with compatible versions.
func (p *VisionRuntimeProvisioner) isVenvHealthy(ctx context.Context, venvPy string) bool {
	if _, err := p.resolver.stat(venvPy); err != nil {
		return false
	}
	out, err := p.resolver.execCmd(ctx, venvPy, "-c", "import sys; print(f'{sys.version_info[0]}.{sys.version_info[1]}.{sys.version_info[2]}')")
	if err != nil {
		return false
	}
	ver := strings.TrimSpace(string(out))
	if !p.resolver.isVersionCompatible(ver) {
		return false
	}
	if _, err := p.resolver.execCmd(ctx, venvPy, "-m", "pip", "--version"); err != nil {
		return false
	}
	return true
}

// isTransientPipError detects temporary PyPI network and timeout errors.
func isTransientPipError(output string) bool {
	lower := strings.ToLower(output)

	// Permanent failures must never be retried
	permanent := []string{
		"no matching distribution",
		"could not find a version that satisfies the requirement",
		"incompatible with this python",
		"requires-python",
		"syntaxerror",
		"permission denied",
		"invalid requirement",
	}
	for _, p := range permanent {
		if strings.Contains(lower, p) {
			return false
		}
	}

	transient := []string{
		"readtimeouterror",
		"read timed out",
		"connecttimeout",
		"connectionreset",
		"connection reset by peer",
		"temporary failure",
		"timed out",
		"503",
		"502",
		"504",
		"500 internal server error",
		"network is unreachable",
		"tls handshake timeout",
		"broken pipe",
		"ssl error",
		"connection error",
	}
	for _, t := range transient {
		if strings.Contains(lower, t) {
			return true
		}
	}
	return false
}

func truncateDetails(s string, maxLen int) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxLen {
		return s
	}
	return s[len(s)-maxLen:]
}

// Provision sets up the venv, updates pip, installs requirements, verifies imports, and persists config.
func (p *VisionRuntimeProvisioner) Provision(ctx context.Context) (*VisionRuntimeStatus, error) {
	r := p.resolver

	// 1. Locate base Python
	basePy, _ := r.findBasePython(ctx)
	if basePy == "" {
		return nil, &SafeError{
			Code:        "PYTHON_NOT_FOUND",
			SafeMessage: "No compatible Python 3 (>= 3.8, <= 3.13) interpreter was found on the host. Please install Python 3.11 or 3.12.",
			Recoverable: true,
		}
	}

	// 2. Ensure directories
	workerDir := filepath.Join(r.dataDir, "vision-worker")
	modelsDir := filepath.Join(r.dataDir, config.DefaultEdgeYOLOModelsDirName)
	venvDir := filepath.Join(r.dataDir, "vision-runtime")

	if err := os.MkdirAll(modelsDir, 0o755); err != nil {
		return nil, &SafeError{
			Code:        "PROVISION_DIR_ERROR",
			SafeMessage: fmt.Sprintf("Failed to create models directory: %v", err),
			Recoverable: true,
		}
	}

	// 3. Stage embedded worker scripts
	if err := StageWorkerAssets(workerDir); err != nil {
		return nil, &SafeError{
			Code:        "PROVISION_STAGE_ERROR",
			SafeMessage: fmt.Sprintf("Failed to stage worker scripts: %v", err),
			Recoverable: true,
		}
	}

	// 4. Create or heal virtual environment
	venvPy := r.venvPythonPath()
	if !p.isVenvHealthy(ctx, venvPy) {
		_ = os.RemoveAll(venvDir)
		if _, err := r.execCmd(ctx, basePy, "-m", "venv", venvDir); err != nil {
			return nil, &SafeError{
				Code:        "VENV_CREATE_FAILED",
				SafeMessage: fmt.Sprintf("Failed to create Python virtual environment with %s: %v", basePy, err),
				Recoverable: true,
				Details:     truncateDetails(err.Error(), 300),
			}
		}
	}

	// 5. Upgrade pip toolchain in venv before installing requirements
	_, _ = r.execCmd(ctx, venvPy, "-m", "pip", "install", "--upgrade", "pip", "setuptools", "wheel")
	if _, err := r.execCmd(ctx, venvPy, "-m", "pip", "--version"); err != nil {
		return nil, &SafeError{
			Code:        "PIP_TOOLCHAIN_FAILED",
			SafeMessage: "Virtual environment pip toolchain is unusable.",
			Recoverable: true,
			Details:     truncateDetails(err.Error(), 300),
		}
	}

	// 6. Install dependencies into the venv with retry and generous timeouts
	reqFile := filepath.Join(workerDir, "requirements.txt")
	const maxAttempts = 3
	backoffs := []time.Duration{5 * time.Second, 15 * time.Second, 30 * time.Second}
	var lastOutput string
	var installErr error

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		args := []string{
			"-m", "pip", "install",
			"--prefer-binary",
			"--retries", "8",
			"--timeout", "120",
			"-r", reqFile,
		}
		out, err := r.execCmd(ctx, venvPy, args...)
		if err == nil {
			installErr = nil
			break
		}

		installErr = err
		lastOutput = string(out)
		combinedErrOutput := lastOutput
		if combinedErrOutput == "" && err != nil {
			combinedErrOutput = err.Error()
		} else if err != nil {
			combinedErrOutput = combinedErrOutput + "\n" + err.Error()
		}

		if !isTransientPipError(combinedErrOutput) || attempt == maxAttempts {
			break
		}

		backoff := backoffs[attempt-1]
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-p.timeAfter(backoff):
		}
	}

	if installErr != nil {
		combinedFinalOutput := lastOutput
		if combinedFinalOutput == "" && installErr != nil {
			combinedFinalOutput = installErr.Error()
		} else if installErr != nil {
			combinedFinalOutput = combinedFinalOutput + "\n" + installErr.Error()
		}

		if isTransientPipError(combinedFinalOutput) {
			return nil, &SafeError{
				Code:        "PIP_TIMEOUT",
				SafeMessage: "No se pudo completar la descarga de las dependencias. La conexión con PyPI agotó el tiempo de espera. Podés reintentar sin perder lo ya descargado.",
				Recoverable: true,
				Details:     truncateDetails(combinedFinalOutput, 300),
			}
		}
		return nil, &SafeError{
			Code:        "PIP_INSTALL_FAILED",
			SafeMessage: "Error al instalar las dependencias de visión en el entorno virtual.",
			Recoverable: true,
			Details:     truncateDetails(combinedFinalOutput, 300),
		}
	}

	// 7. Probe the venv environment
	probe, err := r.probePython(ctx, venvPy)
	if err != nil || len(probe.Missing) > 0 {
		var missingStr string
		if probe != nil {
			missingStr = strings.Join(probe.Missing, ", ")
		}
		return nil, &SafeError{
			Code:        "VERIFICATION_FAILED",
			SafeMessage: fmt.Sprintf("Python dependencies verification failed. Missing: %s", missingStr),
			Recoverable: true,
		}
	}

	// 8. Persist configuration
	stagedWorker := filepath.Join(workerDir, "worker.py")
	changes := map[string]string{
		"GEOCAM_EDGE_YOLO_WORKER_CMD":  venvPy,
		"GEOCAM_EDGE_YOLO_WORKER_ARGS": stagedWorker,
		"GEOCAM_EDGE_YOLO_MODELS_DIR":  modelsDir,
		"GEOCAM_EDGE_YOLO_DEVICE":      probe.Device,
	}

	if err := config.WritePersistentValues(changes); err != nil {
		return nil, &SafeError{
			Code:        "CONFIG_PERSIST_FAILED",
			SafeMessage: "Failed to persist Full Edge configuration settings.",
			Recoverable: true,
			Details:     err.Error(),
		}
	}

	// 9. Return fresh status
	return r.Resolve(ctx)
}
