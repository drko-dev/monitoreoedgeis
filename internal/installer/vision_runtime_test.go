package installer

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/config"
)

func mockCommandExecutor(responses map[string]struct {
	out []byte
	err error
}) CommandExecutor {
	return func(ctx context.Context, name string, args ...string) ([]byte, error) {
		cmdStr := name + " " + strings.Join(args, " ")
		for pattern, resp := range responses {
			if strings.Contains(cmdStr, pattern) {
				return resp.out, resp.err
			}
		}
		return nil, errors.New("unhandled mock command: " + cmdStr)
	}
}

func TestStageWorkerAssets(t *testing.T) {
	dest := t.TempDir()
	if err := StageWorkerAssets(dest); err != nil {
		t.Fatalf("StageWorkerAssets failed: %v", err)
	}

	for _, name := range []string{"worker.py", "backend.py", "requirements.txt"} {
		path := filepath.Join(dest, name)
		info, err := os.Stat(path)
		if err != nil {
			t.Errorf("expected staged file %s: %v", name, err)
		} else if info.Size() == 0 {
			t.Errorf("file %s is empty", name)
		}
	}
}

func TestVisionRuntimeResolverNoPython(t *testing.T) {
	dir := t.TempDir()
	res := NewVisionRuntimeResolver(dir)
	res.lookPath = func(file string) (string, error) {
		return "", errors.New("not found")
	}
	res.stat = func(name string) (os.FileInfo, error) {
		return nil, os.ErrNotExist
	}
	res.execCmd = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return nil, errors.New("command failed")
	}

	status, err := res.Resolve(context.Background())
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}
	if status.PythonFound {
		t.Errorf("expected PythonFound=false, got true")
	}
	if status.CanProvision {
		t.Errorf("expected CanProvision=false, got true")
	}
	if status.Ready {
		t.Errorf("expected Ready=false, got true")
	}
	if len(status.MissingModels) != 2 {
		t.Errorf("expected 2 missing models, got %d", len(status.MissingModels))
	}
}

func TestVisionRuntimeResolverIncompatiblePython(t *testing.T) {
	dir := t.TempDir()
	res := NewVisionRuntimeResolver(dir)
	res.lookPath = func(file string) (string, error) {
		if file == "python3" {
			return "/usr/bin/python3", nil
		}
		return "", errors.New("not found")
	}
	res.stat = func(name string) (os.FileInfo, error) {
		if name == "/usr/bin/python3" {
			return mockFileInfo{name: "python3", isDir: false}, nil
		}
		return nil, os.ErrNotExist
	}
	res.execCmd = mockCommandExecutor(map[string]struct {
		out []byte
		err error
	}{
		"sys.version_info": {out: []byte("2.7.18\n"), err: nil},
	})

	status, err := res.Resolve(context.Background())
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}
	if status.CanProvision {
		t.Errorf("expected CanProvision=false for Python 2.7, got true")
	}
	if status.Ready {
		t.Errorf("expected Ready=false, got true")
	}
}

func TestVisionRuntimeResolverCompatiblePythonNeedsProvisioning(t *testing.T) {
	dir := t.TempDir()
	res := NewVisionRuntimeResolver(dir)
	res.lookPath = func(file string) (string, error) {
		if file == "python3" {
			return "/usr/bin/python3", nil
		}
		return "", errors.New("not found")
	}
	res.stat = func(name string) (os.FileInfo, error) {
		if name == "/usr/bin/python3" {
			return mockFileInfo{name: "python3", isDir: false}, nil
		}
		return nil, os.ErrNotExist
	}
	res.execCmd = mockCommandExecutor(map[string]struct {
		out []byte
		err error
	}{
		"sys.version_info": {out: []byte("3.11.4\n"), err: nil},
	})

	status, err := res.Resolve(context.Background())
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}
	if !status.PythonFound {
		t.Errorf("expected PythonFound=true")
	}
	if !status.CanProvision {
		t.Errorf("expected CanProvision=true for Python 3.11")
	}
	if status.PackagesReady {
		t.Errorf("expected PackagesReady=false without venv")
	}
	if status.Ready {
		t.Errorf("expected Ready=false")
	}
}

func TestVisionRuntimeResolverFullReadinessMPS(t *testing.T) {
	dir := t.TempDir()
	// Create models
	modelsDir := filepath.Join(dir, config.DefaultEdgeYOLOModelsDirName)
	if err := os.MkdirAll(modelsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{config.DefaultEdgeYOLOPersonModel, config.DefaultEdgeYOLOVehicleModel} {
		if err := os.WriteFile(filepath.Join(modelsDir, m), []byte("model"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Create worker script
	workerDir := filepath.Join(dir, "vision-worker")
	if err := os.MkdirAll(workerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workerDir, "worker.py"), []byte("#!/usr/bin/env python3"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Create fake venv python
	venvBin := filepath.Join(dir, "vision-runtime", "bin")
	if err := os.MkdirAll(venvBin, 0o755); err != nil {
		t.Fatal(err)
	}
	venvPy := filepath.Join(venvBin, "python")
	if err := os.WriteFile(venvPy, []byte("#!/bin/sh"), 0o755); err != nil {
		t.Fatal(err)
	}

	res := NewVisionRuntimeResolver(dir)
	probeResp, _ := json.Marshal(map[string]interface{}{
		"missing":     []string{},
		"versions":    map[string]string{"torch": "2.4.0", "ultralytics": "8.3.1", "PIL": "10.4.0"},
		"device":      "mps",
		"device_name": "Apple Silicon Metal (MPS)",
		"python":      "3.11.4",
	})

	res.execCmd = mockCommandExecutor(map[string]struct {
		out []byte
		err error
	}{
		"probe": {out: append(probeResp, '\n'), err: nil},
		"-c":    {out: append(probeResp, '\n'), err: nil},
	})

	status, err := res.Resolve(context.Background())
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}
	if !status.PackagesReady {
		t.Errorf("expected PackagesReady=true")
	}
	if !status.PersonModelFound || !status.VehicleModelFound {
		t.Errorf("expected models found")
	}
	if status.Device != "mps" {
		t.Errorf("expected device=mps, got %s", status.Device)
	}
	if !status.Ready {
		t.Errorf("expected Ready=true, got false (msg: %s)", status.Message)
	}
}

func TestVisionRuntimeResolverMissingOneModel(t *testing.T) {
	dir := t.TempDir()
	// Create only person model, omit vehicle model
	modelsDir := filepath.Join(dir, config.DefaultEdgeYOLOModelsDirName)
	if err := os.MkdirAll(modelsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(modelsDir, config.DefaultEdgeYOLOPersonModel), []byte("model"), 0o644); err != nil {
		t.Fatal(err)
	}

	workerDir := filepath.Join(dir, "vision-worker")
	if err := os.MkdirAll(workerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workerDir, "worker.py"), []byte("#!/usr/bin/env python3"), 0o755); err != nil {
		t.Fatal(err)
	}

	venvBin := filepath.Join(dir, "vision-runtime", "bin")
	if err := os.MkdirAll(venvBin, 0o755); err != nil {
		t.Fatal(err)
	}
	venvPy := filepath.Join(venvBin, "python")
	if err := os.WriteFile(venvPy, []byte("#!/bin/sh"), 0o755); err != nil {
		t.Fatal(err)
	}

	res := NewVisionRuntimeResolver(dir)
	probeResp, _ := json.Marshal(map[string]interface{}{
		"missing":     []string{},
		"versions":    map[string]string{"torch": "2.4.0", "ultralytics": "8.3.1", "PIL": "10.4.0"},
		"device":      "cpu",
		"device_name": "CPU",
		"python":      "3.11.4",
	})
	res.execCmd = mockCommandExecutor(map[string]struct {
		out []byte
		err error
	}{
		"-c": {out: append(probeResp, '\n'), err: nil},
	})

	status, err := res.Resolve(context.Background())
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}
	if status.Ready {
		t.Errorf("expected Ready=false when vehicle model is missing")
	}
	if len(status.MissingModels) != 1 || status.MissingModels[0] != config.DefaultEdgeYOLOVehicleModel {
		t.Errorf("expected vehicle model in MissingModels, got %v", status.MissingModels)
	}
}

func TestVisionRuntimeProvisionerSuccess(t *testing.T) {
	dataDir := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "edge.env")
	if err := os.WriteFile(configPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.ConfigFileEnv, configPath)

	res := NewVisionRuntimeResolver(dataDir)
	res.lookPath = func(file string) (string, error) {
		if file == "python3" {
			return "/usr/bin/python3", nil
		}
		return "", errors.New("not found")
	}

	probeResp, _ := json.Marshal(map[string]interface{}{
		"missing":     []string{},
		"versions":    map[string]string{"torch": "2.4.0", "ultralytics": "8.3.1", "PIL": "10.4.0"},
		"device":      "mps",
		"device_name": "Apple Silicon Metal (MPS)",
		"python":      "3.11.4",
	})

	res.execCmd = mockCommandExecutor(map[string]struct {
		out []byte
		err error
	}{
		"sys.version_info": {out: []byte("3.11.4\n"), err: nil},
		"-m venv":          {out: []byte(""), err: nil},
		"pip --version":    {out: []byte("pip 24.0 from ...\n"), err: nil},
		"pip install":      {out: []byte("Successfully installed"), err: nil},
		"-c":               {out: append(probeResp, '\n'), err: nil},
	})

	provisioner := NewVisionRuntimeProvisioner(res)
	status, err := provisioner.Provision(context.Background())
	if err != nil {
		t.Fatalf("Provision failed: %v", err)
	}

	// Verify staged assets
	for _, f := range []string{"worker.py", "backend.py", "requirements.txt"} {
		if _, err := os.Stat(filepath.Join(dataDir, "vision-worker", f)); err != nil {
			t.Errorf("expected staged %s: %v", f, err)
		}
	}

	// Verify persistent settings
	workerCmd, hasCmd, _ := config.PersistentFileValue("GEOCAM_EDGE_YOLO_WORKER_CMD")
	if !hasCmd || workerCmd == "" {
		t.Errorf("expected GEOCAM_EDGE_YOLO_WORKER_CMD persisted, got %q", workerCmd)
	}
	workerArgs, hasArgs, _ := config.PersistentFileValue("GEOCAM_EDGE_YOLO_WORKER_ARGS")
	if !hasArgs || !strings.Contains(workerArgs, "worker.py") {
		t.Errorf("expected GEOCAM_EDGE_YOLO_WORKER_ARGS persisted, got %q", workerArgs)
	}
	device, _, _ := config.PersistentFileValue("GEOCAM_EDGE_YOLO_DEVICE")
	if device != "mps" {
		t.Errorf("expected GEOCAM_EDGE_YOLO_DEVICE=mps, got %q", device)
	}

	// Models were not created in this test, so PackagesReady is true but Ready is false with missing models
	if !status.PackagesReady {
		t.Errorf("expected PackagesReady=true")
	}
	if status.Ready {
		t.Errorf("expected Ready=false because models are not placed yet")
	}
}

func TestVisionRuntimeProvisionerFailsWithoutBasePython(t *testing.T) {
	dataDir := t.TempDir()
	res := NewVisionRuntimeResolver(dataDir)
	res.lookPath = func(file string) (string, error) {
		return "", errors.New("not found")
	}
	res.stat = func(name string) (os.FileInfo, error) {
		return nil, os.ErrNotExist
	}

	provisioner := NewVisionRuntimeProvisioner(res)
	_, err := provisioner.Provision(context.Background())
	if err == nil {
		t.Fatal("expected Provision to fail without base Python")
	}
	var safeErr *SafeError
	if !errors.As(err, &safeErr) || safeErr.Code != "PYTHON_NOT_FOUND" {
		t.Errorf("expected PYTHON_NOT_FOUND SafeError, got %v", err)
	}
}

func TestDynamicReEvaluationAfterPlacingModels(t *testing.T) {
	svc := newIsolatedService(t)
	modelsDir := filepath.Join(svc.DataDir, config.DefaultEdgeYOLOModelsDirName)

	// Step 1: Full Edge is UNAVAILABLE before anything is configured
	opts, err := svc.GetProcessingModeOptions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var feOpt ProcessingModeOption
	for _, opt := range opts {
		if opt.Mode == ProcessingModeFullEdge {
			feOpt = opt
			break
		}
	}
	if feOpt.Capability != CapabilityUnavailable {
		t.Errorf("expected Full Edge UNAVAILABLE initially, got %s", feOpt.Capability)
	}

	// Step 2: Configure worker cmd and args (simulating provisioned runtime)
	if err := config.WritePersistentValues(map[string]string{
		"GEOCAM_EDGE_YOLO_WORKER_CMD":  "echo",
		"GEOCAM_EDGE_YOLO_MODELS_DIR":  modelsDir,
		"GEOCAM_EDGE_YOLO_WORKER_ARGS": filepath.Join(svc.DataDir, "vision-worker", "worker.py"),
	}); err != nil {
		t.Fatal(err)
	}

	// Still UNAVAILABLE because models do not exist yet
	opts, _ = svc.GetProcessingModeOptions(context.Background())
	for _, opt := range opts {
		if opt.Mode == ProcessingModeFullEdge {
			if opt.Capability != CapabilityUnavailable {
				t.Errorf("expected Full Edge UNAVAILABLE without models, got %s", opt.Capability)
			}
			break
		}
	}

	// Step 3: Place models in modelsDir (operator downloads/copies models)
	if err := os.MkdirAll(modelsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{config.DefaultEdgeYOLOPersonModel, config.DefaultEdgeYOLOVehicleModel} {
		if err := os.WriteFile(filepath.Join(modelsDir, m), []byte("weights"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Step 4: Dynamic re-evaluation - Full Edge becomes SUPPORTED!
	opts, _ = svc.GetProcessingModeOptions(context.Background())
	for _, opt := range opts {
		if opt.Mode == ProcessingModeFullEdge {
			if opt.Capability != CapabilitySupported {
				t.Errorf("expected Full Edge SUPPORTED after placing models, got %s (blockers: %v)", opt.Capability, opt.Blockers)
			}
			break
		}
	}
}

func TestPythonCandidateScoringPrioritizesHomebrewOverSystem(t *testing.T) {
	dataDir := t.TempDir()
	res := NewVisionRuntimeResolver(dataDir)
	res.goos = "darwin"
	res.userHomeDir = func() (string, error) { return "/Users/fakeuser", nil }

	res.stat = func(name string) (os.FileInfo, error) {
		// Both Homebrew python and /usr/bin/python3 exist
		if name == "/opt/homebrew/bin/python3.11" || name == "/usr/bin/python3" {
			return mockFileInfo{name: filepath.Base(name), isDir: false}, nil
		}
		return nil, os.ErrNotExist
	}

	res.execCmd = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "/opt/homebrew/bin/python3.11" {
			return []byte("3.11.8\n"), nil
		}
		if name == "/usr/bin/python3" {
			return []byte("3.9.6\n"), nil
		}
		return nil, errors.New("not found")
	}

	bestPy, ver := res.findBasePython(context.Background())
	if bestPy != "/opt/homebrew/bin/python3.11" {
		t.Errorf("expected Homebrew python /opt/homebrew/bin/python3.11, got %q (ver: %s)", bestPy, ver)
	}
	if ver != "3.11.8" {
		t.Errorf("expected version 3.11.8, got %s", ver)
	}
}

func TestPythonCandidateIncompatibleRejected(t *testing.T) {
	dataDir := t.TempDir()
	res := NewVisionRuntimeResolver(dataDir)
	res.goos = "darwin"
	res.stat = func(name string) (os.FileInfo, error) {
		if name == "/opt/homebrew/bin/python3" {
			return mockFileInfo{name: "python3", isDir: false}, nil
		}
		return nil, os.ErrNotExist
	}
	res.execCmd = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		// Python 3.14 is incompatible (torch wheels do not exist)
		return []byte("3.14.0\n"), nil
	}

	bestPy, _ := res.findBasePython(context.Background())
	if bestPy != "" {
		t.Errorf("expected no base python found for incompatible 3.14, got %q", bestPy)
	}
}

func TestVisionRuntimeProvisionerTransientPipTimeoutRetry(t *testing.T) {
	dataDir := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "edge.env")
	if err := os.WriteFile(configPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.ConfigFileEnv, configPath)

	res := NewVisionRuntimeResolver(dataDir)
	res.lookPath = func(file string) (string, error) {
		return "/opt/homebrew/bin/python3.11", nil
	}

	attempts := 0
	res.execCmd = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		cmdStr := name + " " + strings.Join(args, " ")
		if strings.Contains(cmdStr, "sys.version_info") {
			return []byte("3.11.8\n"), nil
		}
		if strings.Contains(cmdStr, "-m venv") {
			return []byte(""), nil
		}
		if strings.Contains(cmdStr, "pip --version") {
			return []byte("pip 24.0\n"), nil
		}
		if strings.Contains(cmdStr, "pip install --upgrade") {
			return []byte("pip upgraded\n"), nil
		}
		if strings.Contains(cmdStr, "requirements.txt") {
			attempts++
			return nil, errors.New("pip._vendor.urllib3.exceptions.ReadTimeoutError: HTTPSConnectionPool(host='files.pythonhosted.org', port=443): Read timed out.")
		}
		return nil, errors.New("unhandled mock")
	}

	provisioner := NewVisionRuntimeProvisioner(res)
	provisioner.timeAfter = func(d time.Duration) <-chan time.Time {
		ch := make(chan time.Time, 1)
		ch <- time.Now()
		return ch
	}
	_, err := provisioner.Provision(context.Background())
	if err == nil {
		t.Fatal("expected Provision to fail after pip retries exhausted")
	}
	var safeErr *SafeError
	if !errors.As(err, &safeErr) {
		t.Fatalf("expected SafeError, got %v", err)
	}
	if safeErr.Code != "PIP_TIMEOUT" {
		t.Errorf("expected PIP_TIMEOUT code, got %s", safeErr.Code)
	}
	if attempts != 3 {
		t.Errorf("expected exactly 3 attempts for transient pip error, got %d", attempts)
	}
}

func TestImportModelValidationAndAtomicPlacement(t *testing.T) {
	dataDir := t.TempDir()
	res := NewVisionRuntimeResolver(dataDir)

	// Disallowed model name
	_, err := res.ImportModel(context.Background(), "malicious.pt", "/tmp/fake.pt")
	if err == nil {
		t.Fatal("expected error for disallowed model name")
	}
	var safeErr *SafeError
	if !errors.As(err, &safeErr) || safeErr.Code != "INVALID_MODEL_NAME" {
		t.Errorf("expected INVALID_MODEL_NAME error, got %v", err)
	}

	// Missing source file
	_, err = res.ImportModel(context.Background(), config.DefaultEdgeYOLOPersonModel, "/tmp/non-existent-file.pt")
	if err == nil {
		t.Fatal("expected error for non-existent source file")
	}
	if !errors.As(err, &safeErr) || safeErr.Code != "FILE_NOT_FOUND" {
		t.Errorf("expected FILE_NOT_FOUND error, got %v", err)
	}

	// Empty source file
	emptyFile := filepath.Join(t.TempDir(), "empty.pt")
	if err := os.WriteFile(emptyFile, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = res.ImportModel(context.Background(), config.DefaultEdgeYOLOPersonModel, emptyFile)
	if err == nil {
		t.Fatal("expected error for empty file")
	}
	if !errors.As(err, &safeErr) || safeErr.Code != "EMPTY_FILE" {
		t.Errorf("expected EMPTY_FILE error, got %v", err)
	}

	// Valid model import
	validFile := filepath.Join(t.TempDir(), "valid.pt")
	if err := os.WriteFile(validFile, []byte("pytorch_weights_data"), 0o644); err != nil {
		t.Fatal(err)
	}
	status, err := res.ImportModel(context.Background(), config.DefaultEdgeYOLOPersonModel, validFile)
	if err != nil {
		t.Fatalf("expected successful import, got %v", err)
	}
	if !status.PersonModelFound {
		t.Errorf("expected PersonModelFound=true after import")
	}
	destPath := filepath.Join(dataDir, config.DefaultEdgeYOLOModelsDirName, config.DefaultEdgeYOLOPersonModel)
	content, err := os.ReadFile(destPath)
	if err != nil {
		t.Fatalf("failed to read copied model at %s: %v", destPath, err)
	}
	if string(content) != "pytorch_weights_data" {
		t.Errorf("unexpected content in copied model: %s", string(content))
	}
}
