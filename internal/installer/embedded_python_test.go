package installer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Helper to create a synthetic Mach-O arm64 binary header with given size.
func createSyntheticMachOArm64(size int64) []byte {
	header := []byte{
		0xcf, 0xfa, 0xed, 0xfe, // magic: 0xfeedfacf (MH_MAGIC_64 little endian)
		0x0c, 0x00, 0x00, 0x01, // cputype: 0x0100000c (CpuArm64 little endian)
		0x00, 0x00, 0x00, 0x00, // cpusubtype: 0
		0x02, 0x00, 0x00, 0x00, // filetype: 2 (MH_EXECUTE)
		0x00, 0x00, 0x00, 0x00, // ncmds: 0
		0x00, 0x00, 0x00, 0x00, // sizeofcmds: 0
		0x00, 0x00, 0x00, 0x00, // flags: 0
		0x00, 0x00, 0x00, 0x00, // reserved: 0
	}
	if int64(len(header)) >= size {
		return header[:size]
	}
	buf := make([]byte, size)
	copy(buf, header)
	return buf
}

// Helper to create a synthetic Mach-O x86_64 binary header with given size.
func createSyntheticMachOX86_64(size int64) []byte {
	header := []byte{
		0xcf, 0xfa, 0xed, 0xfe, // magic: 0xfeedfacf (MH_MAGIC_64 little endian)
		0x07, 0x00, 0x00, 0x01, // cputype: 0x01000007 (CpuX86_64 little endian)
		0x03, 0x00, 0x00, 0x00, // cpusubtype: CPU_SUBTYPE_X86_64_ALL (3)
		0x02, 0x00, 0x00, 0x00, // filetype: 2 (MH_EXECUTE)
		0x00, 0x00, 0x00, 0x00, // ncmds: 0
		0x00, 0x00, 0x00, 0x00, // sizeofcmds: 0
		0x00, 0x00, 0x00, 0x00, // flags: 0
		0x00, 0x00, 0x00, 0x00, // reserved: 0
	}
	if int64(len(header)) >= size {
		return header[:size]
	}
	buf := make([]byte, size)
	copy(buf, header)
	return buf
}

// Helper to create a synthetic Mach-O arm64 dylib header with given size.
func createSyntheticMachODylibArm64(size int64) []byte {
	header := []byte{
		0xcf, 0xfa, 0xed, 0xfe, // magic: 0xfeedfacf (MH_MAGIC_64 little endian)
		0x0c, 0x00, 0x00, 0x01, // cputype: 0x0100000c (CpuArm64 little endian)
		0x00, 0x00, 0x00, 0x00, // cpusubtype: 0
		0x06, 0x00, 0x00, 0x00, // filetype: 6 (MH_DYLIB)
		0x00, 0x00, 0x00, 0x00, // ncmds: 0
		0x00, 0x00, 0x00, 0x00, // sizeofcmds: 0
		0x00, 0x00, 0x00, 0x00, // flags: 0
		0x00, 0x00, 0x00, 0x00, // reserved: 0
	}
	if int64(len(header)) >= size {
		return header[:size]
	}
	buf := make([]byte, size)
	copy(buf, header)
	return buf
}

// setupSyntheticPythonPack initializes a temporary directory with a synthetic Python runtime pack.
func setupSyntheticPythonPack(t *testing.T, binContent []byte, binPerm os.FileMode, createStdlib bool) (string, *RuntimeManifest) {
	t.Helper()
	tmpDir := t.TempDir()

	binDir := filepath.Join(tmpDir, "python", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("failed to create bin dir: %v", err)
	}

	binPath := filepath.Join(binDir, "python3")
	if binContent != nil {
		if err := os.WriteFile(binPath, binContent, binPerm); err != nil {
			t.Fatalf("failed to write python binary: %v", err)
		}
	}

	if createStdlib {
		libDir := filepath.Join(tmpDir, "python", "lib")
		if err := os.MkdirAll(libDir, 0o755); err != nil {
			t.Fatalf("failed to create lib dir: %v", err)
		}
		dylibContent := createSyntheticMachODylibArm64(128)
		if err := os.WriteFile(filepath.Join(libDir, "libpython3.11.dylib"), dylibContent, 0o644); err != nil {
			t.Fatalf("failed to write dylib: %v", err)
		}

		stdlibDir := filepath.Join(libDir, "python3.11")
		if err := os.MkdirAll(stdlibDir, 0o755); err != nil {
			t.Fatalf("failed to create stdlib dir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(stdlibDir, "os.py"), []byte("# synthetic os module\n"), 0o644); err != nil {
			t.Fatalf("failed to write stdlib file: %v", err)
		}
		if err := os.MkdirAll(filepath.Join(stdlibDir, "lib-dynload"), 0o755); err != nil {
			t.Fatalf("failed to create lib-dynload dir: %v", err)
		}
	}

	var size int64
	var hash string
	if binContent != nil {
		size = int64(len(binContent))
		hash = sha256Hex(binContent)
	} else {
		size = 1024
		hash = strings.Repeat("a", 64)
	}

	manifest := &RuntimeManifest{
		SchemaVersion:  CurrentSchemaVersion,
		RuntimeVersion: "1.0.0",
		Platform:       PlatformDarwin,
		Architecture:   ArchArm64,
		Python: PythonSpec{
			Version:            "3.11.9",
			ABI:                "cp311",
			RelativeBinaryPath: "python/bin/python3",
			SHA256:             hash,
			SizeBytes:          size,
		},
		Worker: WorkerSpec{
			Version:    "1.1.0",
			Entrypoint: "worker/worker.py",
			SHA256:     strings.Repeat("b", 64),
			SizeBytes:  100,
		},
		Binaries:     []BinarySpec{},
		Dependencies: []DependencySpec{},
		Compatibility: CompatibilitySpec{
			EdgeMinVersion:       "1.1.6",
			ModelsPackMinVersion: "1.0.0",
		},
	}

	return tmpDir, manifest
}

// 1. Python embebido válido
func TestEmbeddedPython_Case1_ValidEmbeddedPython(t *testing.T) {
	content := createSyntheticMachOArm64(128)
	tmpDir, manifest := setupSyntheticPythonPack(t, content, 0o755, true)

	mgr, err := NewEmbeddedPythonManager(tmpDir, manifest)
	if err != nil {
		t.Fatalf("NewEmbeddedPythonManager failed: %v", err)
	}

	if err := mgr.Validate(); err != nil {
		t.Fatalf("expected Validate() to pass, got: %v", err)
	}

	if mgr.BinaryPath() != filepath.Join(tmpDir, "python", "bin", "python3") {
		t.Errorf("unexpected BinaryPath: got %s", mgr.BinaryPath())
	}
}

// 2. Python ausente
func TestEmbeddedPython_Case2_MissingPython(t *testing.T) {
	tmpDir, manifest := setupSyntheticPythonPack(t, nil, 0o755, true) // nil content -> binary not created

	mgr, err := NewEmbeddedPythonManager(tmpDir, manifest)
	if err != nil {
		t.Fatalf("NewEmbeddedPythonManager failed: %v", err)
	}

	err = mgr.Validate()
	if err == nil {
		t.Fatalf("expected error for missing python, got nil")
	}

	var safeErr *SafeError
	if errors.As(err, &safeErr) {
		if safeErr.Code != ErrCodePythonNotFound {
			t.Errorf("expected error code %s, got %s", ErrCodePythonNotFound, safeErr.Code)
		}
	} else {
		t.Errorf("expected SafeError, got %T: %v", err, err)
	}
}

// 3. Hash incorrecto
func TestEmbeddedPython_Case3_HashMismatch(t *testing.T) {
	content := createSyntheticMachOArm64(128)
	tmpDir, manifest := setupSyntheticPythonPack(t, content, 0o755, true)
	manifest.Python.SHA256 = strings.Repeat("f", 64) // Corrupt expected hash

	mgr, err := NewEmbeddedPythonManager(tmpDir, manifest)
	if err != nil {
		t.Fatalf("NewEmbeddedPythonManager failed: %v", err)
	}

	err = mgr.Validate()
	if err == nil {
		t.Fatalf("expected error for hash mismatch, got nil")
	}

	var safeErr *SafeError
	if errors.As(err, &safeErr) {
		if safeErr.Code != ErrCodePythonHashMismatch {
			t.Errorf("expected error code %s, got %s", ErrCodePythonHashMismatch, safeErr.Code)
		}
	} else {
		t.Errorf("expected SafeError, got %T", err)
	}
}

// 4. Tamaño incorrecto
func TestEmbeddedPython_Case4_SizeMismatch(t *testing.T) {
	content := createSyntheticMachOArm64(128)
	tmpDir, manifest := setupSyntheticPythonPack(t, content, 0o755, true)
	manifest.Python.SizeBytes = 256 // Corrupt expected size

	mgr, err := NewEmbeddedPythonManager(tmpDir, manifest)
	if err != nil {
		t.Fatalf("NewEmbeddedPythonManager failed: %v", err)
	}

	err = mgr.Validate()
	if err == nil {
		t.Fatalf("expected error for size mismatch, got nil")
	}

	var safeErr *SafeError
	if errors.As(err, &safeErr) {
		if safeErr.Code != ErrCodePythonSizeMismatch {
			t.Errorf("expected error code %s, got %s", ErrCodePythonSizeMismatch, safeErr.Code)
		}
	} else {
		t.Errorf("expected SafeError, got %T", err)
	}
}

// 5. Ejecutable sin permisos
func TestEmbeddedPython_Case5_NotExecutable(t *testing.T) {
	content := createSyntheticMachOArm64(128)
	tmpDir, manifest := setupSyntheticPythonPack(t, content, 0o644, true) // No execute bit

	mgr, err := NewEmbeddedPythonManager(tmpDir, manifest)
	if err != nil {
		t.Fatalf("NewEmbeddedPythonManager failed: %v", err)
	}

	err = mgr.Validate()
	if err == nil {
		t.Fatalf("expected error for non-executable python, got nil")
	}

	var safeErr *SafeError
	if errors.As(err, &safeErr) {
		if safeErr.Code != ErrCodePythonNotExecutable {
			t.Errorf("expected error code %s, got %s", ErrCodePythonNotExecutable, safeErr.Code)
		}
	} else {
		t.Errorf("expected SafeError, got %T", err)
	}
}

// 6. Arquitectura incorrecta (Mach-O x86_64 binary)
func TestEmbeddedPython_Case6_WrongArchitecture(t *testing.T) {
	content := createSyntheticMachOX86_64(128) // x86_64 instead of arm64
	tmpDir, manifest := setupSyntheticPythonPack(t, content, 0o755, true)

	mgr, err := NewEmbeddedPythonManager(tmpDir, manifest)
	if err != nil {
		t.Fatalf("NewEmbeddedPythonManager failed: %v", err)
	}

	err = mgr.Validate()
	if err == nil {
		t.Fatalf("expected error for wrong architecture, got nil")
	}

	var safeErr *SafeError
	if errors.As(err, &safeErr) {
		if safeErr.Code != ErrCodePythonArchIncompatible {
			t.Errorf("expected error code %s, got %s", ErrCodePythonArchIncompatible, safeErr.Code)
		}
	} else {
		t.Errorf("expected SafeError, got %T", err)
	}
}

// 7. Versión incorrecta
func TestEmbeddedPython_Case7_WrongVersion(t *testing.T) {
	content := createSyntheticMachOArm64(128)
	tmpDir, manifest := setupSyntheticPythonPack(t, content, 0o755, true)

	mgr, err := NewEmbeddedPythonManager(tmpDir, manifest)
	if err != nil {
		t.Fatalf("NewEmbeddedPythonManager failed: %v", err)
	}

	// Mock command executor returning Python 3.14.0
	mgr.SetExecutor(func(ctx context.Context, binary string, env []string, args ...string) ([]byte, error) {
		return []byte(`{"version": "3.14.0", "major": 3, "minor": 14, "micro": 0, "platform": "darwin", "executable": "/mock/bin/python3", "prefix": "/mock"}`), nil
	})

	_, err = mgr.ProbeInterpreter(context.Background())
	if err == nil {
		t.Fatalf("expected error for wrong Python version, got nil")
	}

	var safeErr *SafeError
	if errors.As(err, &safeErr) {
		if safeErr.Code != ErrCodePythonVersionIncompatible {
			t.Errorf("expected error code %s, got %s", ErrCodePythonVersionIncompatible, safeErr.Code)
		}
	} else {
		t.Errorf("expected SafeError, got %T", err)
	}
}

// 8. ABI incompatible
func TestEmbeddedPython_Case8_IncompatibleABI(t *testing.T) {
	content := createSyntheticMachOArm64(128)
	tmpDir, manifest := setupSyntheticPythonPack(t, content, 0o755, true)
	manifest.Python.ABI = "cp312" // Incompatible ABI for 3.11

	mgr, err := NewEmbeddedPythonManager(tmpDir, manifest)
	if err != nil {
		t.Fatalf("NewEmbeddedPythonManager failed: %v", err)
	}

	err = mgr.Validate()
	if err == nil {
		t.Fatalf("expected error for incompatible ABI, got nil")
	}

	var safeErr *SafeError
	if errors.As(err, &safeErr) {
		if safeErr.Code != ErrCodePythonABIIncompatible {
			t.Errorf("expected error code %s, got %s", ErrCodePythonABIIncompatible, safeErr.Code)
		}
	} else {
		t.Errorf("expected SafeError, got %T", err)
	}
}

// 9. Distribución incompleta (stdlib missing)
func TestEmbeddedPython_Case9_IncompleteDistribution(t *testing.T) {
	content := createSyntheticMachOArm64(128)
	tmpDir, manifest := setupSyntheticPythonPack(t, content, 0o755, false) // false -> stdlib not created

	mgr, err := NewEmbeddedPythonManager(tmpDir, manifest)
	if err != nil {
		t.Fatalf("NewEmbeddedPythonManager failed: %v", err)
	}

	err = mgr.Validate()
	if err == nil {
		t.Fatalf("expected error for missing stdlib, got nil")
	}

	var safeErr *SafeError
	if errors.As(err, &safeErr) {
		if safeErr.Code != ErrCodePythonStdlibIncomplete {
			t.Errorf("expected error code %s, got %s", ErrCodePythonStdlibIncomplete, safeErr.Code)
		}
	} else {
		t.Errorf("expected SafeError, got %T", err)
	}
}

// 10. Sin Python del sistema en PATH
func TestEmbeddedPython_Case10_NoSystemPythonInPath(t *testing.T) {
	// Base environment with NO python in PATH
	dirtyEnv := []string{
		"PATH=/custom/bin:/another/bin",
		"USER=testuser",
	}

	cleanEnv := IsolateEnv(dirtyEnv)

	foundPath := false
	for _, env := range cleanEnv {
		if strings.HasPrefix(env, "PATH=") {
			foundPath = true
			if !strings.Contains(env, "/custom/bin") {
				t.Errorf("custom non-package-manager path dropped: %s", env)
			}
		}
		if strings.HasPrefix(env, "PYTHON") && !strings.HasPrefix(env, "PYTHONNOUSERSITE=") {
			t.Errorf("unwanted python variable retained: %s", env)
		}
	}
	if !foundPath {
		t.Errorf("PATH was not retained")
	}
}

// 11. Homebrew ausente en PATH
func TestEmbeddedPython_Case11_HomebrewAbsentFromPath(t *testing.T) {
	dirtyEnv := []string{
		"PATH=/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/opt/homebrew/sbin",
		"HOMEBREW_PREFIX=/opt/homebrew",
	}

	cleanEnv := IsolateEnv(dirtyEnv)

	for _, env := range cleanEnv {
		if strings.HasPrefix(env, "PATH=") {
			if strings.Contains(env, "/opt/homebrew") || strings.Contains(env, "/usr/local/bin") {
				t.Errorf("Homebrew path segments not scrubbed: %s", env)
			}
			if !strings.Contains(env, "/usr/bin") || !strings.Contains(env, "/bin") {
				t.Errorf("standard system paths missing: %s", env)
			}
		}
	}
}

// 12. Python host incompatible
func TestEmbeddedPython_Case12_IncompatibleHostPythonIgnored(t *testing.T) {
	content := createSyntheticMachOArm64(128)
	tmpDir, manifest := setupSyntheticPythonPack(t, content, 0o755, true)

	mgr, err := NewEmbeddedPythonManager(tmpDir, manifest)
	if err != nil {
		t.Fatalf("NewEmbeddedPythonManager failed: %v", err)
	}

	// Verify manager targets embedded binary, not any system binary
	bin := mgr.BinaryPath()
	if !strings.HasPrefix(bin, tmpDir) {
		t.Errorf("BinaryPath does not point to embedded runtime: %s", bin)
	}

	// Test probe with isolated 3.11 response
	mgr.SetExecutor(func(ctx context.Context, binary string, env []string, args ...string) ([]byte, error) {
		return []byte(`{"version": "3.11.9", "major": 3, "minor": 11, "micro": 9, "platform": "darwin", "executable": "` + bin + `", "prefix": "` + tmpDir + `/python"}`), nil
	})

	info, err := mgr.ProbeInterpreter(context.Background())
	if err != nil {
		t.Fatalf("ProbeInterpreter failed: %v", err)
	}
	if info.Version != "3.11.9" {
		t.Errorf("expected 3.11.9, got %s", info.Version)
	}
}

// 13. PYTHONHOME/PYTHONPATH contaminados
func TestEmbeddedPython_Case13_ContaminatedEnvScrubbed(t *testing.T) {
	dirtyEnv := []string{
		"PYTHONHOME=/toxic/pythonhome",
		"PYTHONPATH=/toxic/pythonpath:/evil/site-packages",
		"PYTHONSTARTUP=/toxic/startup.py",
		"PYTHONUSERBASE=/toxic/userbase",
		"PYTHONEXECUTABLE=/toxic/bin/python",
		"VIRTUAL_ENV=/toxic/venv",
		"__PYVENV_LAUNCHER__=/toxic/launcher",
		"USER=geocam",
		"PATH=/usr/bin:/bin",
	}

	clean := IsolateEnv(dirtyEnv)

	for _, e := range clean {
		upper := strings.ToUpper(e)
		if strings.HasPrefix(upper, "PYTHONHOME=") ||
			strings.HasPrefix(upper, "PYTHONPATH=") ||
			strings.HasPrefix(upper, "PYTHONSTARTUP=") ||
			strings.HasPrefix(upper, "PYTHONUSERBASE=") ||
			strings.HasPrefix(upper, "PYTHONEXECUTABLE=") ||
			strings.HasPrefix(upper, "VIRTUAL_ENV=") ||
			strings.HasPrefix(upper, "__PYVENV_LAUNCHER__=") {
			t.Errorf("contaminated variable was not scrubbed: %s", e)
		}
	}

	// Check PYTHONNOUSERSITE is set
	hasNoUserSite := false
	for _, e := range clean {
		if e == "PYTHONNOUSERSITE=1" {
			hasNoUserSite = true
			break
		}
	}
	if !hasNoUserSite {
		t.Errorf("PYTHONNOUSERSITE=1 flag missing from isolated env")
	}
}

// 14. Symlink externo
func TestEmbeddedPython_Case14_ExternalSymlinkRejected(t *testing.T) {
	tmpDir := t.TempDir()
	outsideDir := t.TempDir()

	externalBin := filepath.Join(outsideDir, "external_python3")
	content := createSyntheticMachOArm64(128)
	if err := os.WriteFile(externalBin, content, 0o755); err != nil {
		t.Fatalf("failed to write external bin: %v", err)
	}

	binDir := filepath.Join(tmpDir, "python", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("failed to create bin dir: %v", err)
	}

	symlinkPath := filepath.Join(binDir, "python3")
	if err := os.Symlink(externalBin, symlinkPath); err != nil {
		t.Fatalf("failed to create symlink: %v", err)
	}

	// Create stdlib inside pack
	stdlibDir := filepath.Join(tmpDir, "python", "lib", "python3.11")
	_ = os.MkdirAll(stdlibDir, 0o755)
	_ = os.WriteFile(filepath.Join(stdlibDir, "os.py"), []byte("# test"), 0o644)

	manifest := &RuntimeManifest{
		SchemaVersion:  CurrentSchemaVersion,
		RuntimeVersion: "1.0.0",
		Platform:       PlatformDarwin,
		Architecture:   ArchArm64,
		Python: PythonSpec{
			Version:            "3.11.9",
			ABI:                "cp311",
			RelativeBinaryPath: "python/bin/python3",
			SHA256:             sha256Hex(content),
			SizeBytes:          int64(len(content)),
		},
		Worker: WorkerSpec{
			Version:    "1.1.0",
			Entrypoint: "worker/worker.py",
			SHA256:     strings.Repeat("a", 64),
			SizeBytes:  10,
		},
		Binaries:     []BinarySpec{},
		Dependencies: []DependencySpec{},
		Compatibility: CompatibilitySpec{
			EdgeMinVersion:       "1.1.6",
			ModelsPackMinVersion: "1.0.0",
		},
	}

	mgr, err := NewEmbeddedPythonManager(tmpDir, manifest)
	if err != nil {
		t.Fatalf("NewEmbeddedPythonManager failed: %v", err)
	}

	err = mgr.Validate()
	if err == nil {
		t.Fatalf("expected error for escaping symlink, got nil")
	}

	var safeErr *SafeError
	if errors.As(err, &safeErr) {
		if safeErr.Code != ErrCodePythonSecurityEscape {
			t.Errorf("expected error code %s, got %s", ErrCodePythonSecurityEscape, safeErr.Code)
		}
	} else {
		t.Errorf("expected SafeError, got %T: %v", err, err)
	}
}

// 15. Ejecución con timeout
func TestEmbeddedPython_Case15_ExecutionTimeout(t *testing.T) {
	content := createSyntheticMachOArm64(128)
	tmpDir, manifest := setupSyntheticPythonPack(t, content, 0o755, true)

	mgr, err := NewEmbeddedPythonManager(tmpDir, manifest)
	if err != nil {
		t.Fatalf("NewEmbeddedPythonManager failed: %v", err)
	}

	// Mock executor that simulates deadline exceeded
	mgr.SetExecutor(func(ctx context.Context, binary string, env []string, args ...string) ([]byte, error) {
		return nil, context.DeadlineExceeded
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	_, err = mgr.Execute(ctx, "-c", "import time; time.sleep(10)")
	if err == nil {
		t.Fatalf("expected error for timeout, got nil")
	}

	var safeErr *SafeError
	if errors.As(err, &safeErr) {
		if safeErr.Code != ErrCodePythonExecTimeout {
			t.Errorf("expected error code %s, got %s", ErrCodePythonExecTimeout, safeErr.Code)
		}
	} else {
		t.Errorf("expected SafeError, got %T", err)
	}
}

// 16. Ejecución de biblioteca estándar
func TestEmbeddedPython_Case16_StandardLibraryExecution(t *testing.T) {
	content := createSyntheticMachOArm64(128)
	tmpDir, manifest := setupSyntheticPythonPack(t, content, 0o755, true)

	mgr, err := NewEmbeddedPythonManager(tmpDir, manifest)
	if err != nil {
		t.Fatalf("NewEmbeddedPythonManager failed: %v", err)
	}

	// Mock successful standard library execution
	mgr.SetExecutor(func(ctx context.Context, binary string, env []string, args ...string) ([]byte, error) {
		return []byte("STDLIB_VERIFIED\n"), nil
	})

	if err := mgr.VerifyStandardLibrary(context.Background()); err != nil {
		t.Fatalf("expected VerifyStandardLibrary to pass, got: %v", err)
	}
}

// 17. Preparación con red bloqueada
func TestEmbeddedPython_Case17_AirgappedPreparation(t *testing.T) {
	content := createSyntheticMachOArm64(128)
	tmpDir, manifest := setupSyntheticPythonPack(t, content, 0o755, true)

	mgr, err := NewEmbeddedPythonManager(tmpDir, manifest)
	if err != nil {
		t.Fatalf("NewEmbeddedPythonManager failed: %v", err)
	}

	// Validation must complete without opening sockets or network
	if err := mgr.Validate(); err != nil {
		t.Fatalf("expected offline Validate() to pass, got: %v", err)
	}
}

// 18. Pack ubicado en una ruta diferente (relocatabilidad)
func TestEmbeddedPython_Case18_CustomPackPath(t *testing.T) {
	content := createSyntheticMachOArm64(128)
	tmpDir, manifest := setupSyntheticPythonPack(t, content, 0o755, true)

	// Move to alternative directory
	newDir := filepath.Join(t.TempDir(), "relocated-runtime")
	if err := os.Rename(tmpDir, newDir); err != nil {
		t.Fatalf("failed to relocate directory: %v", err)
	}

	mgr, err := NewEmbeddedPythonManager(newDir, manifest)
	if err != nil {
		t.Fatalf("NewEmbeddedPythonManager failed: %v", err)
	}

	if err := mgr.Validate(); err != nil {
		t.Fatalf("expected Validate() on relocated pack to pass, got: %v", err)
	}
}

// 19. Rutas con espacios
func TestEmbeddedPython_Case19_PathWithSpaces(t *testing.T) {
	parent := t.TempDir()
	spaceDir := filepath.Join(parent, "path with spaces in name")
	if err := os.MkdirAll(spaceDir, 0o755); err != nil {
		t.Fatalf("failed to create dir with spaces: %v", err)
	}

	content := createSyntheticMachOArm64(128)
	binDir := filepath.Join(spaceDir, "python", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("failed to create bin dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "python3"), content, 0o755); err != nil {
		t.Fatalf("failed to write bin: %v", err)
	}

	libDir := filepath.Join(spaceDir, "python", "lib")
	_ = os.MkdirAll(libDir, 0o755)
	_ = os.WriteFile(filepath.Join(libDir, "libpython3.11.dylib"), createSyntheticMachODylibArm64(128), 0o644)
	stdlibDir := filepath.Join(libDir, "python3.11")
	_ = os.MkdirAll(stdlibDir, 0o755)
	_ = os.WriteFile(filepath.Join(stdlibDir, "os.py"), []byte("# test"), 0o644)
	_ = os.MkdirAll(filepath.Join(stdlibDir, "lib-dynload"), 0o755)

	manifest := &RuntimeManifest{
		SchemaVersion:  CurrentSchemaVersion,
		RuntimeVersion: "1.0.0",
		Platform:       PlatformDarwin,
		Architecture:   ArchArm64,
		Python: PythonSpec{
			Version:            "3.11.9",
			ABI:                "cp311",
			RelativeBinaryPath: "python/bin/python3",
			SHA256:             sha256Hex(content),
			SizeBytes:          int64(len(content)),
		},
		Worker: WorkerSpec{
			Version:    "1.1.0",
			Entrypoint: "worker/worker.py",
			SHA256:     strings.Repeat("a", 64),
			SizeBytes:  10,
		},
		Binaries:     []BinarySpec{},
		Dependencies: []DependencySpec{},
		Compatibility: CompatibilitySpec{
			EdgeMinVersion:       "1.1.6",
			ModelsPackMinVersion: "1.0.0",
		},
	}

	mgr, err := NewEmbeddedPythonManager(spaceDir, manifest)
	if err != nil {
		t.Fatalf("NewEmbeddedPythonManager failed: %v", err)
	}

	if err := mgr.Validate(); err != nil {
		t.Fatalf("expected Validate() on path with spaces to pass, got: %v", err)
	}
}

// 20. Validación repetida e idempotente
func TestEmbeddedPython_Case20_IdempotentValidation(t *testing.T) {
	content := createSyntheticMachOArm64(128)
	tmpDir, manifest := setupSyntheticPythonPack(t, content, 0o755, true)

	mgr, err := NewEmbeddedPythonManager(tmpDir, manifest)
	if err != nil {
		t.Fatalf("NewEmbeddedPythonManager failed: %v", err)
	}

	for i := 0; i < 5; i++ {
		if err := mgr.Validate(); err != nil {
			t.Fatalf("Validate iteration %d failed: %v", i, err)
		}
	}
}

// Integration Test: Real Python Standalone Execution (runs if physical interpreter is present)
func TestEmbeddedPython_RealPythonExecution(t *testing.T) {
	// Check if scratch/python or runtime/python exists
	scratchPath := "/Users/gustavomarcon/.gemini/antigravity-cli/brain/115ba03b-08bb-4cfb-8d2d-8c8187d36286/scratch"
	realBin := filepath.Join(scratchPath, "python", "bin", "python3.11")
	if _, err := os.Stat(realBin); os.IsNotExist(err) {
		t.Skip("skipping real Python test: scratch python binary not present")
	}

	manifest, err := LoadEmbeddedRuntimeManifest()
	if err != nil {
		t.Fatalf("LoadEmbeddedRuntimeManifest failed: %v", err)
	}

	mgr, err := NewEmbeddedPythonManager(scratchPath, manifest)
	if err != nil {
		t.Fatalf("NewEmbeddedPythonManager failed: %v", err)
	}

	// Physical validation
	if err := mgr.Validate(); err != nil {
		t.Fatalf("real python physical Validate() failed: %v", err)
	}

	// Real isolated probe
	info, err := mgr.ProbeInterpreter(context.Background())
	if err != nil {
		t.Fatalf("real python ProbeInterpreter() failed: %v", err)
	}
	if info.Major != 3 || info.Minor != 11 {
		t.Errorf("unexpected real python version: %s (major: %d, minor: %d)", info.Version, info.Major, info.Minor)
	}
	if info.Platform != "darwin" {
		t.Errorf("unexpected platform: %s", info.Platform)
	}

	// Real standard library execution
	if err := mgr.VerifyStandardLibrary(context.Background()); err != nil {
		t.Fatalf("real python VerifyStandardLibrary() failed: %v", err)
	}

	// Real execution test
	res, err := mgr.Execute(context.Background(), "-I", "-c", "import sys; sys.stdout.write('HELLO_GEO_CAM')")
	if err != nil {
		t.Fatalf("real python Execute() failed: %v", err)
	}
	if res.Stdout != "HELLO_GEO_CAM" {
		t.Errorf("unexpected stdout: %q", res.Stdout)
	}
}
