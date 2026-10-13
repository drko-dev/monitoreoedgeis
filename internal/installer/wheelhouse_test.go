package installer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Helper to create a synthetic wheel file with arbitrary bytes and return its path, size and SHA-256.
func createSyntheticWheel(t *testing.T, dir, filename string, size int64) (string, int64, string) {
	t.Helper()
	filePath := filepath.Join(dir, filename)
	data := make([]byte, size)
	for i := range data {
		data[i] = byte((i + int(size)) % 256)
	}
	if err := os.WriteFile(filePath, data, 0o644); err != nil {
		t.Fatalf("failed to write synthetic wheel %s: %v", filename, err)
	}
	hasher := sha256.New()
	hasher.Write(data)
	hash := hex.EncodeToString(hasher.Sum(nil))
	return filePath, size, hash
}

// Helper to setup a valid synthetic wheelhouse with core dependencies (torch, torchvision, ultralytics, pillow).
func setupSyntheticWheelhouse(t *testing.T) (string, *WheelhouseLock) {
	t.Helper()
	wheelDir := t.TempDir()

	wheels := []struct {
		name     string
		ver      string
		filename string
		pyTag    string
		abiTag   string
		platTag  string
		size     int64
	}{
		{"torch", "2.4.0", "torch-2.4.0-cp311-none-macosx_11_0_arm64.whl", "cp311", "none", "macosx_11_0_arm64", 1024},
		{"torchvision", "0.19.0", "torchvision-0.19.0-cp311-cp311-macosx_11_0_arm64.whl", "cp311", "cp311", "macosx_11_0_arm64", 512},
		{"ultralytics", "8.3.1", "ultralytics-8.3.1-py3-none-any.whl", "py3", "none", "any", 256},
		{"pillow", "10.4.0", "pillow-10.4.0-cp311-cp311-macosx_11_0_arm64.whl", "cp311", "cp311", "macosx_11_0_arm64", 128},
	}

	var deps []WheelDependencySpec
	var totalSize int64

	for _, w := range wheels {
		_, size, hash := createSyntheticWheel(t, wheelDir, w.filename, w.size)
		totalSize += size
		deps = append(deps, WheelDependencySpec{
			Name:          w.name,
			CanonicalName: w.name,
			Version:       w.ver,
			Filename:      w.filename,
			PythonTag:     w.pyTag,
			ABITag:        w.abiTag,
			PlatformTag:   w.platTag,
			SizeBytes:     size,
			SHA256:        hash,
			Origin:        "https://pypi.org/synthetic",
			License:       "BSD-3-Clause",
		})
	}

	lock := &WheelhouseLock{
		SchemaVersion:      CurrentSchemaVersion,
		TargetPlatform:     PlatformDarwin,
		TargetArchitecture: ArchArm64,
		PythonVersion:      "3.11.9",
		PythonABI:          "cp311",
		WheelCount:         len(deps),
		TotalSizeBytes:     totalSize,
		Dependencies:       deps,
	}

	return wheelDir, lock
}

// -----------------------------------------------------------------------------
// Case 1: Lock válido (Embedded and synthetic lock pass validation)
// -----------------------------------------------------------------------------
func TestWheelhouse_Case1_ValidLock(t *testing.T) {
	// 1. Embedded lock
	lock, err := LoadEmbeddedWheelhouseLock()
	if err != nil {
		t.Fatalf("LoadEmbeddedWheelhouseLock failed: %v", err)
	}
	if lock.WheelCount != 36 {
		t.Errorf("expected 36 wheels in embedded lock, got %d", lock.WheelCount)
	}
	if lock.TargetPlatform != PlatformDarwin || lock.TargetArchitecture != ArchArm64 {
		t.Errorf("expected darwin/arm64 target, got %s/%s", lock.TargetPlatform, lock.TargetArchitecture)
	}
	if err := lock.ValidateCompatibility(PlatformDarwin, ArchArm64, "3.11.9", "cp311"); err != nil {
		t.Errorf("embedded lock compatibility validation failed: %v", err)
	}

	// 2. Synthetic lock
	_, synLock := setupSyntheticWheelhouse(t)
	if err := synLock.ValidateSchema(); err != nil {
		t.Errorf("synthetic lock schema validation failed: %v", err)
	}
}

// -----------------------------------------------------------------------------
// Case 2: Lock inválido (JSON syntax error or corrupt schema)
// -----------------------------------------------------------------------------
func TestWheelhouse_Case2_InvalidLock(t *testing.T) {
	tmpDir := t.TempDir()
	badJsonPath := filepath.Join(tmpDir, "bad.json")
	if err := os.WriteFile(badJsonPath, []byte("{invalid json"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadWheelhouseLock(badJsonPath)
	if err == nil {
		t.Fatal("expected error on invalid json, got nil")
	}
	if !errors.Is(err, ErrWheelhouseLockInvalidJSON) {
		t.Errorf("expected ErrWheelhouseLockInvalidJSON, got: %v", err)
	}
}

// -----------------------------------------------------------------------------
// Case 3: Wheel ausente (Physical wheel missing from wheelhouse directory)
// -----------------------------------------------------------------------------
func TestWheelhouse_Case3_MissingWheel(t *testing.T) {
	wheelDir, lock := setupSyntheticWheelhouse(t)

	// Remove one wheel from disk
	os.Remove(filepath.Join(wheelDir, lock.Dependencies[0].Filename))

	mgr, err := NewWheelhouseManager(wheelDir, nil, lock)
	if err != nil {
		t.Fatal(err)
	}

	_, err = mgr.ValidateArtifacts(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error for missing wheel, got nil")
	}
	var safeErr *SafeError
	if !errors.As(err, &safeErr) || safeErr.Code != ErrCodeWheelMissing {
		t.Errorf("expected SafeError with code %s, got %v", ErrCodeWheelMissing, err)
	}
}

// -----------------------------------------------------------------------------
// Case 4: Wheel corrupto (Truncated or unreadable bytes)
// -----------------------------------------------------------------------------
func TestWheelhouse_Case4_CorruptedWheel(t *testing.T) {
	wheelDir, lock := setupSyntheticWheelhouse(t)

	// Corrupt first wheel by truncating it
	firstFile := filepath.Join(wheelDir, lock.Dependencies[0].Filename)
	if err := os.Truncate(firstFile, lock.Dependencies[0].SizeBytes/2); err != nil {
		t.Fatal(err)
	}

	mgr, err := NewWheelhouseManager(wheelDir, nil, lock)
	if err != nil {
		t.Fatal(err)
	}

	_, err = mgr.ValidateArtifacts(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error for corrupted wheel, got nil")
	}
	var safeErr *SafeError
	if !errors.As(err, &safeErr) || safeErr.Code != ErrCodeWheelSizeMismatch {
		t.Errorf("expected SafeError with code %s, got %v", ErrCodeWheelSizeMismatch, err)
	}
}

// -----------------------------------------------------------------------------
// Case 5: Hash incorrecto (SHA-256 checksum mismatch)
// -----------------------------------------------------------------------------
func TestWheelhouse_Case5_HashMismatch(t *testing.T) {
	wheelDir, lock := setupSyntheticWheelhouse(t)

	// Alter content without changing length
	firstFile := filepath.Join(wheelDir, lock.Dependencies[0].Filename)
	data, err := os.ReadFile(firstFile)
	if err != nil {
		t.Fatal(err)
	}
	data[0] ^= 0xFF
	if err := os.WriteFile(firstFile, data, 0o644); err != nil {
		t.Fatal(err)
	}

	mgr, err := NewWheelhouseManager(wheelDir, nil, lock)
	if err != nil {
		t.Fatal(err)
	}

	_, err = mgr.ValidateArtifacts(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error for hash mismatch, got nil")
	}
	var safeErr *SafeError
	if !errors.As(err, &safeErr) || safeErr.Code != ErrCodeWheelHashMismatch {
		t.Errorf("expected SafeError with code %s, got %v", ErrCodeWheelHashMismatch, err)
	}
}

// -----------------------------------------------------------------------------
// Case 6: Tamaño incorrecto (Byte count mismatch)
// -----------------------------------------------------------------------------
func TestWheelhouse_Case6_SizeMismatch(t *testing.T) {
	wheelDir, lock := setupSyntheticWheelhouse(t)

	// Append byte to first wheel
	firstFile := filepath.Join(wheelDir, lock.Dependencies[0].Filename)
	f, err := os.OpenFile(firstFile, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	f.Write([]byte{0x00})
	f.Close()

	mgr, err := NewWheelhouseManager(wheelDir, nil, lock)
	if err != nil {
		t.Fatal(err)
	}

	_, err = mgr.ValidateArtifacts(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error for size mismatch, got nil")
	}
	var safeErr *SafeError
	if !errors.As(err, &safeErr) || safeErr.Code != ErrCodeWheelSizeMismatch {
		t.Errorf("expected SafeError with code %s, got %v", ErrCodeWheelSizeMismatch, err)
	}
}

// -----------------------------------------------------------------------------
// Case 7: Versiones incompatibles (Loose range or incompatible python version)
// -----------------------------------------------------------------------------
func TestWheelhouse_Case7_IncompatibleVersions(t *testing.T) {
	_, lock := setupSyntheticWheelhouse(t)

	// Loose range in version
	lock.Dependencies[0].Version = ">=2.4.0"
	if err := lock.ValidateSchema(); err == nil {
		t.Fatal("expected error on loose version range, got nil")
	}

	// Python version incompatible
	_, lock2 := setupSyntheticWheelhouse(t)
	if err := lock2.ValidateCompatibility(PlatformDarwin, ArchArm64, "3.10.0", "cp311"); err == nil {
		t.Fatal("expected error on incompatible python version 3.10, got nil")
	}
}

// -----------------------------------------------------------------------------
// Case 8: ABI incompatible (Wheel ABI tag is not cp311, abi3 or none)
// -----------------------------------------------------------------------------
func TestWheelhouse_Case8_IncompatibleABI(t *testing.T) {
	_, lock := setupSyntheticWheelhouse(t)

	// Incompatible ABI in dependency
	lock.Dependencies[0].ABITag = "cp310"
	err := lock.ValidateCompatibility(PlatformDarwin, ArchArm64, "3.11.9", "cp311")
	if err == nil {
		t.Fatal("expected error on incompatible ABI tag, got nil")
	}
	if !errors.Is(err, ErrWheelhouseIncompatible) {
		t.Errorf("expected ErrWheelhouseIncompatible, got: %v", err)
	}
}

// -----------------------------------------------------------------------------
// Case 9: Wheel para otra arquitectura (e.g. linux_x86_64 or win_amd64)
// -----------------------------------------------------------------------------
func TestWheelhouse_Case9_WrongArchitecture(t *testing.T) {
	_, lock := setupSyntheticWheelhouse(t)

	// Wheel with linux x86_64 platform tag
	lock.Dependencies[0].PlatformTag = "manylinux2014_x86_64"
	err := lock.ValidateCompatibility(PlatformDarwin, ArchArm64, "3.11.9", "cp311")
	if err == nil {
		t.Fatal("expected error for x86_64 platform tag on darwin-arm64, got nil")
	}
	if !errors.Is(err, ErrWheelhouseIncompatible) {
		t.Errorf("expected ErrWheelhouseIncompatible, got: %v", err)
	}
}

// -----------------------------------------------------------------------------
// Case 10: Dependencia transitiva faltante (Core dependency missing)
// -----------------------------------------------------------------------------
func TestWheelhouse_Case10_MissingTransitiveDependency(t *testing.T) {
	_, lock := setupSyntheticWheelhouse(t)

	// Remove ultralytics from lock
	var filtered []WheelDependencySpec
	for _, d := range lock.Dependencies {
		if d.Name != "ultralytics" {
			filtered = append(filtered, d)
		}
	}
	lock.Dependencies = filtered
	lock.WheelCount = len(filtered)
	lock.TotalSizeBytes -= 256

	err := lock.ValidateSchema()
	if err == nil {
		t.Fatal("expected error when core dependency ultralytics is missing, got nil")
	}
	if !errors.Is(err, ErrWheelTransitiveDepMissing) {
		t.Errorf("expected ErrWheelTransitiveDepMissing, got: %v", err)
	}
}

// -----------------------------------------------------------------------------
// Case 11: Dependencia duplicada (Duplicate dependency name or filename)
// -----------------------------------------------------------------------------
func TestWheelhouse_Case11_DuplicateDependency(t *testing.T) {
	_, lock := setupSyntheticWheelhouse(t)

	// Duplicate first dependency
	lock.Dependencies = append(lock.Dependencies, lock.Dependencies[0])
	lock.WheelCount++
	lock.TotalSizeBytes += lock.Dependencies[0].SizeBytes

	err := lock.ValidateSchema()
	if err == nil {
		t.Fatal("expected error on duplicate dependency, got nil")
	}
	if !errors.Is(err, ErrWheelDuplicate) {
		t.Errorf("expected ErrWheelDuplicate, got: %v", err)
	}
}

// -----------------------------------------------------------------------------
// Case 12: Wheel filename incorrecto (Filename violates PEP 427)
// -----------------------------------------------------------------------------
func TestWheelhouse_Case12_InvalidWheelFilename(t *testing.T) {
	_, lock := setupSyntheticWheelhouse(t)

	lock.Dependencies[0].Filename = "invalid-name-wheel.whl"
	err := lock.ValidateSchema()
	if err == nil {
		t.Fatal("expected error for non-PEP427 wheel filename, got nil")
	}
	if !errors.Is(err, ErrWheelInvalidFilename) {
		t.Errorf("expected ErrWheelInvalidFilename, got: %v", err)
	}
}

// -----------------------------------------------------------------------------
// Case 13: Intento de instalar un source distribution (.tar.gz, .zip)
// -----------------------------------------------------------------------------
func TestWheelhouse_Case13_SourceDistributionRejected(t *testing.T) {
	_, lock := setupSyntheticWheelhouse(t)

	lock.Dependencies[0].Filename = "torch-2.4.0.tar.gz"
	err := lock.ValidateSchema()
	if err == nil {
		t.Fatal("expected error when source distribution .tar.gz is specified, got nil")
	}
	if !errors.Is(err, ErrWheelSourceDistRejected) {
		t.Errorf("expected ErrWheelSourceDistRejected, got: %v", err)
	}
}

// -----------------------------------------------------------------------------
// Case 14: Configuración pip externa (Isolated with PIP_CONFIG_FILE=/dev/null)
// -----------------------------------------------------------------------------
func TestWheelhouse_Case14_ExternalPipConfigIsolated(t *testing.T) {
	wheelDir, lock := setupSyntheticWheelhouse(t)
	pyDir, pyManifest := setupSyntheticPythonPack(t, createSyntheticMachOArm64(50168), 0o755, true)

	pyMgr, err := NewEmbeddedPythonManager(pyDir, pyManifest)
	if err != nil {
		t.Fatal(err)
	}

	mgr, err := NewWheelhouseManager(wheelDir, pyMgr, lock)
	if err != nil {
		t.Fatal(err)
	}

	var observedEnv []string
	mgr.SetPipExecutor(func(ctx context.Context, binary string, env []string, args ...string) ([]byte, error) {
		observedEnv = env
		return []byte("Successfully installed torch torchvision ultralytics pillow"), nil
	})

	_, err = mgr.InstallOffline(context.Background())
	if err != nil {
		t.Fatalf("InstallOffline failed: %v", err)
	}

	hasPipConfigNull := false
	for _, e := range observedEnv {
		if e == "PIP_CONFIG_FILE=/dev/null" {
			hasPipConfigNull = true
			break
		}
	}
	if !hasPipConfigNull {
		t.Error("expected PIP_CONFIG_FILE=/dev/null in pip execution environment")
	}
}

// -----------------------------------------------------------------------------
// Case 15: Índice de red configurado (Network blocked fail-closed)
// -----------------------------------------------------------------------------
func TestWheelhouse_Case15_NetworkIndexBlockedFailClosed(t *testing.T) {
	wheelDir, lock := setupSyntheticWheelhouse(t)
	pyDir, pyManifest := setupSyntheticPythonPack(t, createSyntheticMachOArm64(50168), 0o755, true)

	pyMgr, err := NewEmbeddedPythonManager(pyDir, pyManifest)
	if err != nil {
		t.Fatal(err)
	}

	mgr, err := NewWheelhouseManager(wheelDir, pyMgr, lock)
	if err != nil {
		t.Fatal(err)
	}

	var observedArgs []string
	var observedEnv []string
	mgr.SetPipExecutor(func(ctx context.Context, binary string, env []string, args ...string) ([]byte, error) {
		observedArgs = args
		observedEnv = env
		return []byte("Successfully installed"), nil
	})

	_, err = mgr.InstallOffline(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	hasNoIndex := false
	for _, arg := range observedArgs {
		if arg == "--no-index" {
			hasNoIndex = true
			break
		}
	}
	if !hasNoIndex {
		t.Error("expected --no-index in pip install arguments")
	}

	hasPipNoIndexEnv := false
	hasDeadProxy := false
	for _, e := range observedEnv {
		if e == "PIP_NO_INDEX=1" {
			hasPipNoIndexEnv = true
		}
		if strings.Contains(e, "http://127.0.0.1:9") {
			hasDeadProxy = true
		}
	}
	if !hasPipNoIndexEnv || !hasDeadProxy {
		t.Errorf("expected PIP_NO_INDEX=1 and dead proxy in environment, got: %v", observedEnv)
	}
}

// -----------------------------------------------------------------------------
// Case 16: Python host ausente (Independent of /usr/bin/python3)
// -----------------------------------------------------------------------------
func TestWheelhouse_Case16_HostPythonAbsentAllowed(t *testing.T) {
	// Clean env without PATH to system python
	baseEnv := []string{"PATH=/bin:/usr/bin", "SOME_VAR=1"}
	isolated := IsolateEnv(baseEnv)
	for _, e := range isolated {
		if strings.Contains(e, "python") && strings.HasPrefix(e, "PYTHON") {
			t.Errorf("found residual python variable: %s", e)
		}
	}
}

// -----------------------------------------------------------------------------
// Case 17: Homebrew ausente (Filtered from PATH)
// -----------------------------------------------------------------------------
func TestWheelhouse_Case17_HomebrewAbsentAllowed(t *testing.T) {
	baseEnv := []string{"PATH=/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin"}
	isolated := IsolateEnv(baseEnv)
	for _, e := range isolated {
		if strings.HasPrefix(e, "PATH=") {
			if strings.Contains(e, "/opt/homebrew") || strings.Contains(e, "/usr/local/bin") {
				t.Errorf("PATH still contains homebrew: %s", e)
			}
		}
	}
}

// -----------------------------------------------------------------------------
// Case 18: PYTHONPATH contaminado (Strictly scrubbed)
// -----------------------------------------------------------------------------
func TestWheelhouse_Case18_PythonPathContaminatedIsolated(t *testing.T) {
	baseEnv := []string{
		"PYTHONPATH=/malicious/libs",
		"PYTHONHOME=/malicious/home",
		"PYTHONUSERBASE=/malicious/userbase",
		"PATH=/usr/bin:/bin",
	}
	isolated := IsolateEnv(baseEnv)
	for _, e := range isolated {
		if strings.HasPrefix(e, "PYTHONPATH=") || strings.HasPrefix(e, "PYTHONHOME=") || strings.HasPrefix(e, "PYTHONUSERBASE=") {
			t.Errorf("isolated environment failed to scrub: %s", e)
		}
	}
}

// -----------------------------------------------------------------------------
// Case 19: Instalación con Internet bloqueado (Simulation passes with local wheels)
// -----------------------------------------------------------------------------
func TestWheelhouse_Case19_InstallationWithBlockedInternet(t *testing.T) {
	wheelDir, lock := setupSyntheticWheelhouse(t)
	pyDir, pyManifest := setupSyntheticPythonPack(t, createSyntheticMachOArm64(50168), 0o755, true)

	pyMgr, err := NewEmbeddedPythonManager(pyDir, pyManifest)
	if err != nil {
		t.Fatal(err)
	}

	mgr, err := NewWheelhouseManager(wheelDir, pyMgr, lock)
	if err != nil {
		t.Fatal(err)
	}

	mgr.SetPipExecutor(func(ctx context.Context, binary string, env []string, args ...string) ([]byte, error) {
		// Verify environment enforces dead proxy
		for _, e := range env {
			if strings.HasPrefix(e, "http_proxy=") && !strings.Contains(e, "127.0.0.1:9") {
				return nil, errors.New("network proxy not blocked")
			}
		}
		return []byte("Successfully installed 4 packages"), nil
	})

	rep, err := mgr.InstallOffline(context.Background())
	if err != nil {
		t.Fatalf("offline install failed: %v", err)
	}
	if !rep.Success || rep.InstalledCount != 4 {
		t.Errorf("unexpected report: %+v", rep)
	}
}

// -----------------------------------------------------------------------------
// Case 20: Instalación idempotente (Repeated execution succeeds cleanly)
// -----------------------------------------------------------------------------
func TestWheelhouse_Case20_IdempotentInstallation(t *testing.T) {
	wheelDir, lock := setupSyntheticWheelhouse(t)
	pyDir, pyManifest := setupSyntheticPythonPack(t, createSyntheticMachOArm64(50168), 0o755, true)

	pyMgr, err := NewEmbeddedPythonManager(pyDir, pyManifest)
	if err != nil {
		t.Fatal(err)
	}

	mgr, err := NewWheelhouseManager(wheelDir, pyMgr, lock)
	if err != nil {
		t.Fatal(err)
	}

	callCount := 0
	mgr.SetPipExecutor(func(ctx context.Context, binary string, env []string, args ...string) ([]byte, error) {
		callCount++
		return []byte("Requirement already satisfied"), nil
	})

	// First run
	rep1, err := mgr.InstallOffline(context.Background())
	if err != nil || !rep1.Success {
		t.Fatalf("first run failed: %v", err)
	}

	// Second run (idempotent)
	rep2, err := mgr.InstallOffline(context.Background())
	if err != nil || !rep2.Success {
		t.Fatalf("second run failed: %v", err)
	}

	if callCount != 2 {
		t.Errorf("expected 2 calls, got %d", callCount)
	}
}

// -----------------------------------------------------------------------------
// Case 21: Fallo a mitad de instalación (Error reporting and safe state)
// -----------------------------------------------------------------------------
func TestWheelhouse_Case21_FailureMidInstallationPreservesRuntime(t *testing.T) {
	wheelDir, lock := setupSyntheticWheelhouse(t)
	pyDir, pyManifest := setupSyntheticPythonPack(t, createSyntheticMachOArm64(50168), 0o755, true)

	pyMgr, err := NewEmbeddedPythonManager(pyDir, pyManifest)
	if err != nil {
		t.Fatal(err)
	}

	mgr, err := NewWheelhouseManager(wheelDir, pyMgr, lock)
	if err != nil {
		t.Fatal(err)
	}

	mgr.SetPipExecutor(func(ctx context.Context, binary string, env []string, args ...string) ([]byte, error) {
		return []byte("Killed"), errors.New("pip execution killed")
	})

	rep, err := mgr.InstallOffline(context.Background())
	if err == nil {
		t.Fatal("expected install failure, got nil")
	}
	if rep != nil && rep.Success {
		t.Error("expected report.Success = false")
	}
	if mgr.ReadinessState() == StateDependenciesInstalled {
		t.Error("manager should not transition to StateDependenciesInstalled upon failure")
	}
}

// -----------------------------------------------------------------------------
// Case 22: Runtime previo preservado (Verification failure fails closed)
// -----------------------------------------------------------------------------
func TestWheelhouse_Case22_PreviousRuntimePreserved(t *testing.T) {
	wheelDir, lock := setupSyntheticWheelhouse(t)
	// Corrupt wheel so validation fails before pip runs
	firstFile := filepath.Join(wheelDir, lock.Dependencies[0].Filename)
	os.Truncate(firstFile, 1)

	pyDir, pyManifest := setupSyntheticPythonPack(t, createSyntheticMachOArm64(50168), 0o755, true)
	pyMgr, err := NewEmbeddedPythonManager(pyDir, pyManifest)
	if err != nil {
		t.Fatal(err)
	}

	mgr, err := NewWheelhouseManager(wheelDir, pyMgr, lock)
	if err != nil {
		t.Fatal(err)
	}

	pipRan := false
	mgr.SetPipExecutor(func(ctx context.Context, binary string, env []string, args ...string) ([]byte, error) {
		pipRan = true
		return []byte("ok"), nil
	})

	_, err = mgr.InstallOffline(context.Background())
	if err == nil {
		t.Fatal("expected failure on corrupted wheel, got nil")
	}
	if pipRan {
		t.Error("pip should NEVER be invoked if wheelhouse validation fails")
	}
}

// -----------------------------------------------------------------------------
// Case 23: Imports torch, torchvision, ultralytics, PIL (Mocked probe)
// -----------------------------------------------------------------------------
func TestWheelhouse_Case23_CriticalImportsVerified(t *testing.T) {
	pyDir, pyManifest := setupSyntheticPythonPack(t, createSyntheticMachOArm64(50168), 0o755, true)
	pyMgr, err := NewEmbeddedPythonManager(pyDir, pyManifest)
	if err != nil {
		t.Fatal(err)
	}

	probeOutput := map[string]interface{}{
		"torch_version":       "2.4.0",
		"torchvision_version": "0.19.0",
		"ultralytics_version": "8.3.1",
		"pillow_version":      "10.4.0",
		"opencv_version":      "4.11.0",
		"numpy_version":       "1.26.4",
		"mps_available":       true,
		"mps_tensor_ok":       true,
		"cpu_tensor_ok":       true,
	}
	outBytes, _ := json.Marshal(probeOutput)

	pyMgr.SetExecutor(func(ctx context.Context, binary string, env []string, args ...string) ([]byte, error) {
		return outBytes, nil
	})

	mgr, err := NewWheelhouseManager(t.TempDir(), pyMgr, nil)
	if err != nil {
		t.Fatal(err)
	}

	res, err := mgr.VerifyImports(context.Background())
	if err != nil {
		t.Fatalf("VerifyImports failed: %v", err)
	}
	if !res.Success {
		t.Error("expected Success = true")
	}
	if res.TorchVersion != "2.4.0" || res.UltralyticsVersion != "8.3.1" || res.PillowVersion != "10.4.0" {
		t.Errorf("version mismatch: %+v", res)
	}
	if mgr.ReadinessState() != StateImportsVerified {
		t.Errorf("expected StateImportsVerified, got %s", mgr.ReadinessState())
	}
}

// -----------------------------------------------------------------------------
// Case 24: Detección de aceleración MPS en Mac Apple Silicon (Mocked MPS)
// -----------------------------------------------------------------------------
func TestWheelhouse_Case24_MPSAccelerationDetection(t *testing.T) {
	pyDir, pyManifest := setupSyntheticPythonPack(t, createSyntheticMachOArm64(50168), 0o755, true)
	pyMgr, err := NewEmbeddedPythonManager(pyDir, pyManifest)
	if err != nil {
		t.Fatal(err)
	}

	probeOutput := map[string]interface{}{
		"torch_version":       "2.4.0",
		"torchvision_version": "0.19.0",
		"ultralytics_version": "8.3.1",
		"pillow_version":      "10.4.0",
		"mps_available":       true,
		"mps_tensor_ok":       true,
		"cpu_tensor_ok":       true,
	}
	outBytes, _ := json.Marshal(probeOutput)

	pyMgr.SetExecutor(func(ctx context.Context, binary string, env []string, args ...string) ([]byte, error) {
		return outBytes, nil
	})

	mgr, err := NewWheelhouseManager(t.TempDir(), pyMgr, nil)
	if err != nil {
		t.Fatal(err)
	}

	res, err := mgr.VerifyImports(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !res.MPSAvailable || !res.MPSTensorOK {
		t.Errorf("expected MPSAvailable and MPSTensorOK, got: %+v", res)
	}
}

// -----------------------------------------------------------------------------
// Case 25: Ejecución de smoke test mínimo del Worker sin cámaras reales
// -----------------------------------------------------------------------------
func TestWheelhouse_Case25_WorkerBackendSmokeTest(t *testing.T) {
	pyDir, pyManifest := setupSyntheticPythonPack(t, createSyntheticMachOArm64(50168), 0o755, true)
	pyMgr, err := NewEmbeddedPythonManager(pyDir, pyManifest)
	if err != nil {
		t.Fatal(err)
	}

	smokeOutput := map[string]interface{}{
		"success":          true,
		"backend_imported": true,
		"dev_auto":         "cpu",
		"dev_mps":          "mps",
		"warning":          "",
	}
	outBytes, _ := json.Marshal(smokeOutput)

	pyMgr.SetExecutor(func(ctx context.Context, binary string, env []string, args ...string) ([]byte, error) {
		return outBytes, nil
	})

	mgr, err := NewWheelhouseManager(t.TempDir(), pyMgr, nil)
	if err != nil {
		t.Fatal(err)
	}

	res, err := mgr.RunWorkerSmokeTest(context.Background(), "deploy/vision-worker")
	if err != nil {
		t.Fatalf("RunWorkerSmokeTest failed: %v", err)
	}
	if !res.Success || !res.BackendImported || res.DeviceResolved != "cpu" {
		t.Errorf("unexpected worker smoke test result: %+v", res)
	}
}

// -----------------------------------------------------------------------------
// Physical E2E Test: Real physical offline execution on Apple Silicon Mac
// -----------------------------------------------------------------------------
func TestWheelhouse_PhysicalOfflineExecution_M4(t *testing.T) {
	// Look for physical python and wheelhouse in scratch or runtime
	scratchWheelhouse := "/Users/gustavomarcon/.gemini/antigravity-cli/brain/115ba03b-08bb-4cfb-8d2d-8c8187d36286/scratch/wheelhouse"
	scratchPython := "/Users/gustavomarcon/.gemini/antigravity-cli/brain/115ba03b-08bb-4cfb-8d2d-8c8187d36286/scratch/runtime/python"

	if _, err := os.Stat(scratchWheelhouse); err != nil {
		t.Skipf("Skipping physical execution test: wheelhouse directory not found at %s", scratchWheelhouse)
		return
	}
	if _, err := os.Stat(filepath.Join(scratchPython, "bin", "python3.11")); err != nil {
		t.Skipf("Skipping physical execution test: standalone python not found at %s", scratchPython)
		return
	}

	// 1. Load embedded lock
	lock, err := LoadEmbeddedWheelhouseLock()
	if err != nil {
		t.Fatalf("failed to load embedded lock: %v", err)
	}

	// 2. Validate all 36 physical wheels on disk
	mgr, err := NewWheelhouseManager(scratchWheelhouse, nil, lock)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	report, err := mgr.ValidateArtifacts(ctx, func(current, total int, filename string) {
		if current%10 == 0 || current == total {
			t.Logf("Verifying physical wheels [%d/%d]: %s", current, total, filename)
		}
	})
	if err != nil {
		t.Fatalf("physical wheelhouse validation failed: %v", err)
	}
	if !report.Valid || report.CheckedCount != 36 {
		t.Fatalf("expected 36 verified wheels, got %d", report.CheckedCount)
	}
	t.Logf("Physical wheelhouse verified successfully: %d wheels, %d bytes total", report.CheckedCount, report.TotalBytes)

	// 3. Setup temporary python target directory for isolated physical offline install
	tempTarget := t.TempDir()
	tempPython := filepath.Join(tempTarget, "python")

	// Copy clean standalone python into temp directory
	copyCmd := exec.Command("cp", "-R", scratchPython, tempPython)
	if out, err := copyCmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to copy python for physical test: %v, out: %s", err, string(out))
	}

	manifest, err := LoadEmbeddedRuntimeManifest()
	if err != nil {
		t.Fatalf("failed to load manifest: %v", err)
	}

	pyMgr, err := NewEmbeddedPythonManager(tempTarget, manifest)
	if err != nil {
		t.Fatalf("failed to create EmbeddedPythonManager: %v", err)
	}

	// 4. Attach pyMgr to mgr and install completely offline
	mgr.pythonMgr = pyMgr
	installRep, err := mgr.InstallOffline(ctx)
	if err != nil {
		t.Fatalf("physical offline installation failed: %v, details: %s", err, installRep.Output)
	}
	t.Logf("Physical offline installation succeeded: %d wheels in %.2f ms", installRep.InstalledCount, installRep.DurationMs)

	// 5. Verify real imports and Apple Silicon MPS hardware acceleration
	importRes, err := mgr.VerifyImports(ctx)
	if err != nil {
		t.Fatalf("physical VerifyImports failed: %v", err)
	}
	if !importRes.Success {
		t.Fatalf("import validation unsuccessful: %+v", importRes)
	}
	t.Logf("Physical imports verified: torch=%s, torchvision=%s, ultralytics=%s, pillow=%s, opencv=%s, numpy=%s",
		importRes.TorchVersion, importRes.TorchvisionVersion, importRes.UltralyticsVersion, importRes.PillowVersion, importRes.OpenCVVersion, importRes.NumPyVersion)
	t.Logf("MPS Acceleration: available=%v, tensor_ok=%v, cpu_tensor_ok=%v", importRes.MPSAvailable, importRes.MPSTensorOK, importRes.CPUTensorOK)

	if !importRes.MPSAvailable || !importRes.MPSTensorOK {
		t.Errorf("MPS acceleration expected on Apple Silicon M4, got available=%v, tensor_ok=%v", importRes.MPSAvailable, importRes.MPSTensorOK)
	}

	// 6. Run worker smoke test
	smokeRes, err := mgr.RunWorkerSmokeTest(ctx, "deploy/vision-worker")
	if err != nil {
		t.Fatalf("physical RunWorkerSmokeTest failed: %v", err)
	}
	if !smokeRes.Success || !smokeRes.BackendImported {
		t.Fatalf("worker smoke test failed: %+v", smokeRes)
	}
	t.Logf("Worker smoke test verified: backend_imported=%v, device=%s", smokeRes.BackendImported, smokeRes.DeviceResolved)
}
