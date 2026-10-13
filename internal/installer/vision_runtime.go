package installer

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/drko-dev/monitoreoedgeis/internal/config"
	"github.com/drko-dev/monitoreoedgeis/internal/fulledge"
)

//go:embed assets/vision-worker/*
var embeddedWorkerFS embed.FS

// VisionRuntimeStatus reflects the inspection and readiness state of the local
// Full Edge vision runtime and its required detection models.
type VisionRuntimeStatus struct {
	PythonFound        bool     `json:"python_found"`
	PythonPath         string   `json:"python_path"`
	PythonVersion      string   `json:"python_version"`
	VenvFound          bool     `json:"venv_found"`
	VenvPath           string   `json:"venv_path"`
	WorkerScriptPath   string   `json:"worker_script_path"`
	PackagesReady      bool     `json:"packages_ready"`
	TorchAvailable     bool     `json:"torch_available"`
	TorchVersion       string   `json:"torch_version,omitempty"`
	UltralyticsReady   bool     `json:"ultralytics_ready"`
	UltralyticsVersion string   `json:"ultralytics_version,omitempty"`
	PillowReady        bool     `json:"pillow_ready"`
	Device             string   `json:"device"`
	DeviceName         string   `json:"device_name"`
	ModelsDir          string   `json:"models_dir"`
	PersonModelFound   bool     `json:"person_model_found"`
	VehicleModelFound  bool     `json:"vehicle_model_found"`
	MissingModels      []string `json:"missing_models,omitempty"`
	Ready              bool     `json:"ready"`
	CanProvision       bool     `json:"can_provision"`
	Message            string   `json:"message,omitempty"`
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
	getConfiguredCmd  func() (string, bool, error)
	getConfiguredArgs func() (string, bool, error)
	getConfiguredDir  func() (string, bool, error)
	getConfiguredDev  func() (string, bool, error)
}

// NewVisionRuntimeResolver constructs a resolver for dataDir using OS primitives.
func NewVisionRuntimeResolver(dataDir string) *VisionRuntimeResolver {
	return &VisionRuntimeResolver{
		dataDir:  dataDir,
		lookPath: exec.LookPath,
		stat:     os.Stat,
		execCmd:  DefaultCommandExecutor,
		goos:     runtime.GOOS,
		goarch:   runtime.GOARCH,
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

	personModel := config.DefaultEdgeYOLOPersonModel
	vehicleModel := config.DefaultEdgeYOLOVehicleModel

	if _, err := r.stat(filepath.Join(modelsDir, personModel)); err == nil {
		status.PersonModelFound = true
	} else {
		status.MissingModels = append(status.MissingModels, personModel)
	}

	if _, err := r.stat(filepath.Join(modelsDir, vehicleModel)); err == nil {
		status.VehicleModelFound = true
	} else {
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
	// Ready requires:
	// - worker script present (or custom command resolvable)
	// - packages ready
	// - both models found
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
	if status.Ready {
		status.Message = fmt.Sprintf("Listo para inferencia local (%s)", status.DeviceName)
	} else if len(msgs) > 0 {
		status.Message = strings.Join(msgs, " ")
	}

	return status, nil
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
	if err != nil || minor < 8 {
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
	candidates := []string{"python3.11", "python3.12", "python3.10", "python3.9", "python3", "python"}
	if r.goos == "darwin" {
		candidates = append(candidates,
			"/opt/homebrew/bin/python3.11",
			"/opt/homebrew/bin/python3.12",
			"/opt/homebrew/bin/python3.10",
			"/opt/homebrew/bin/python3.9",
			"/opt/homebrew/bin/python3",
			"/usr/local/bin/python3.11",
			"/usr/local/bin/python3.12",
			"/usr/local/bin/python3",
			"/usr/bin/python3",
		)
	} else if r.goos == "linux" {
		candidates = append(candidates,
			"/usr/bin/python3.11",
			"/usr/bin/python3.12",
			"/usr/bin/python3.10",
			"/usr/bin/python3.9",
			"/usr/bin/python3",
			"/usr/local/bin/python3",
		)
	}

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
		if resolved == "" {
			continue
		}

		out, err := r.execCmd(ctx, resolved, "-c", "import sys; print(f'{sys.version_info[0]}.{sys.version_info[1]}.{sys.version_info[2]}')")
		if err != nil {
			continue
		}
		ver := strings.TrimSpace(string(out))
		if r.isVersionCompatible(ver) {
			return resolved, ver
		}
	}

	return "", ""
}

// VisionRuntimeProvisioner handles the safe, user-initiated setup of the local
// venv and dependencies for Full Edge mode.
type VisionRuntimeProvisioner struct {
	resolver *VisionRuntimeResolver
}

// NewVisionRuntimeProvisioner builds a provisioner from a resolver.
func NewVisionRuntimeProvisioner(resolver *VisionRuntimeResolver) *VisionRuntimeProvisioner {
	return &VisionRuntimeProvisioner{resolver: resolver}
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

// Provision sets up the venv, installs requirements, verifies imports, and persists config.
func (p *VisionRuntimeProvisioner) Provision(ctx context.Context) (*VisionRuntimeStatus, error) {
	r := p.resolver

	// 1. Locate base Python
	basePy, _ := r.findBasePython(ctx)
	if basePy == "" {
		return nil, &SafeError{
			Code:        "PYTHON_NOT_FOUND",
			SafeMessage: "No compatible Python 3 (>= 3.8) interpreter was found on the host. Please install Python 3.9 - 3.12.",
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

	// 4. Create virtual environment if not already present
	venvPy := r.venvPythonPath()
	if _, err := r.stat(venvPy); err != nil {
		if _, err := r.execCmd(ctx, basePy, "-m", "venv", venvDir); err != nil {
			return nil, &SafeError{
				Code:        "VENV_CREATE_FAILED",
				SafeMessage: fmt.Sprintf("Failed to create Python virtual environment with %s: %v", basePy, err),
				Recoverable: true,
				Details:     err.Error(),
			}
		}
	}

	// 5. Install dependencies into the venv safely via pip
	reqFile := filepath.Join(workerDir, "requirements.txt")
	if _, err := r.execCmd(ctx, venvPy, "-m", "pip", "install", "-r", reqFile); err != nil {
		return nil, &SafeError{
			Code:        "PIP_INSTALL_FAILED",
			SafeMessage: "Failed to install required Python packages (ultralytics, pillow).",
			Recoverable: true,
			Details:     err.Error(),
		}
	}

	// 6. Probe the venv environment
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

	// 7. Persist configuration
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

	// 8. Return fresh status
	return r.Resolve(ctx)
}
