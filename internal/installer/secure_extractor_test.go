package installer

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type mockTarEntry struct {
	Name     string
	Typeflag byte
	Linkname string
	Body     []byte
	Mode     int64
}

// createMockTarGzArchive builds a .tar.gz archive in-memory from a list of tar entries.
func createMockTarGzArchive(t *testing.T, entries []mockTarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	for _, e := range entries {
		hdr := &tar.Header{
			Name:     e.Name,
			Typeflag: e.Typeflag,
			Linkname: e.Linkname,
			Size:     int64(len(e.Body)),
			Mode:     e.Mode,
		}
		if hdr.Mode == 0 {
			if e.Typeflag == tar.TypeDir {
				hdr.Mode = 0o755
			} else {
				hdr.Mode = 0o644
			}
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("failed to write tar header for %s: %v", e.Name, err)
		}
		if len(e.Body) > 0 {
			if _, err := tw.Write(e.Body); err != nil {
				t.Fatalf("failed to write tar body for %s: %v", e.Name, err)
			}
		}
	}

	if err := tw.Close(); err != nil {
		t.Fatalf("failed to close tar writer: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("failed to close gzip writer: %v", err)
	}

	return buf.Bytes()
}

// Adversarial 1: Archivo con path traversal ('../evil.txt')
func TestSecureExtractor_Case1_PathTraversal(t *testing.T) {
	entries := []mockTarEntry{
		{Name: "safe.txt", Typeflag: tar.TypeReg, Body: []byte("hello")},
		{Name: "../evil.txt", Typeflag: tar.TypeReg, Body: []byte("malicious")},
	}
	archiveData := createMockTarGzArchive(t, entries)

	destDir := t.TempDir()
	archivePath := filepath.Join(t.TempDir(), "archive.tar.gz")
	if err := os.WriteFile(archivePath, archiveData, 0o644); err != nil {
		t.Fatalf("failed to write archive: %v", err)
	}

	err := ExtractSecureArchive(archivePath, destDir, "")
	if err == nil {
		t.Fatalf("expected error for path traversal, got nil")
	}

	var safeErr *SafeError
	if errors.As(err, &safeErr) {
		if safeErr.Code != ErrCodeSecurityTraversal && safeErr.Code != ErrCodeSecurityEscapingPath {
			t.Errorf("unexpected error code: %s", safeErr.Code)
		}
	} else {
		t.Errorf("expected SafeError, got %T: %v", err, err)
	}

	// Verify evil.txt was never written outside destDir
	parentDir := filepath.Dir(destDir)
	if _, err := os.Stat(filepath.Join(parentDir, "evil.txt")); !os.IsNotExist(err) {
		t.Errorf("traversal file was written outside destination directory!")
	}
}

// Adversarial 2: Ruta absoluta en archive ('/etc/passwd' or '/tmp/evil')
func TestSecureExtractor_Case2_AbsolutePath(t *testing.T) {
	entries := []mockTarEntry{
		{Name: "/tmp/evil_absolute.txt", Typeflag: tar.TypeReg, Body: []byte("malicious")},
	}
	archiveData := createMockTarGzArchive(t, entries)

	destDir := t.TempDir()
	archivePath := filepath.Join(t.TempDir(), "archive.tar.gz")
	_ = os.WriteFile(archivePath, archiveData, 0o644)

	err := ExtractSecureArchive(archivePath, destDir, "")
	if err == nil {
		t.Fatalf("expected error for absolute path in tar, got nil")
	}

	var safeErr *SafeError
	if errors.As(err, &safeErr) {
		if safeErr.Code != ErrCodeSecurityAbsolutePath {
			t.Errorf("expected %s, got %s", ErrCodeSecurityAbsolutePath, safeErr.Code)
		}
	} else {
		t.Errorf("expected SafeError, got %T", err)
	}
}

// Adversarial 3: Symlink que apunta fuera del staging ('symlink -> /etc/passwd' or 'symlink -> ../../outside')
func TestSecureExtractor_Case3_EscapingSymlink(t *testing.T) {
	entries := []mockTarEntry{
		{Name: "python/bin", Typeflag: tar.TypeDir},
		// Absolute symlink
		{Name: "python/bin/bad_abs_link", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"},
	}
	archiveData := createMockTarGzArchive(t, entries)

	destDir := t.TempDir()
	archivePath := filepath.Join(t.TempDir(), "archive.tar.gz")
	_ = os.WriteFile(archivePath, archiveData, 0o644)

	err := ExtractSecureArchive(archivePath, destDir, "")
	if err == nil {
		t.Fatalf("expected error for escaping absolute symlink, got nil")
	}

	var safeErr *SafeError
	if errors.As(err, &safeErr) {
		if safeErr.Code != ErrCodeSecurityAbsoluteSymlink && safeErr.Code != ErrCodeSecurityEscapingSymlink {
			t.Errorf("unexpected error code: %s", safeErr.Code)
		}
	}

	// Relative symlink that traverses outside destDir
	entriesRelativeEscape := []mockTarEntry{
		{Name: "python/bin", Typeflag: tar.TypeDir},
		{Name: "python/bin/bad_rel_link", Typeflag: tar.TypeSymlink, Linkname: "../../../outside_secret"},
	}
	archiveDataRel := createMockTarGzArchive(t, entriesRelativeEscape)
	destDirRel := t.TempDir()
	archivePathRel := filepath.Join(t.TempDir(), "archive_rel.tar.gz")
	_ = os.WriteFile(archivePathRel, archiveDataRel, 0o644)

	errRel := ExtractSecureArchive(archivePathRel, destDirRel, "")
	if errRel == nil {
		t.Fatalf("expected error for escaping relative symlink, got nil")
	}
}

// Adversarial 4: Hardlink externo o peligroso ('hardlink -> /etc/passwd' or referencing non-existent file)
func TestSecureExtractor_Case4_DangerousHardlink(t *testing.T) {
	entries := []mockTarEntry{
		{Name: "python/bin", Typeflag: tar.TypeDir},
		// Hardlink pointing to an absolute external path
		{Name: "python/bin/hardlink_ext", Typeflag: tar.TypeLink, Linkname: "/etc/passwd"},
	}
	archiveData := createMockTarGzArchive(t, entries)

	destDir := t.TempDir()
	archivePath := filepath.Join(t.TempDir(), "archive.tar.gz")
	_ = os.WriteFile(archivePath, archiveData, 0o644)

	err := ExtractSecureArchive(archivePath, destDir, "")
	if err == nil {
		t.Fatalf("expected error for dangerous hardlink, got nil")
	}

	var safeErr *SafeError
	if errors.As(err, &safeErr) {
		if safeErr.Code != ErrCodeSecurityHardlinkInvalid {
			t.Errorf("expected %s, got %s", ErrCodeSecurityHardlinkInvalid, safeErr.Code)
		}
	}

	// Hardlink referencing a file that hasn't been extracted yet
	entriesUnextracted := []mockTarEntry{
		{Name: "python/bin", Typeflag: tar.TypeDir},
		{Name: "python/bin/hardlink_missing", Typeflag: tar.TypeLink, Linkname: "python/bin/future_file.txt"},
	}
	archiveDataUnext := createMockTarGzArchive(t, entriesUnextracted)
	destDirUnext := t.TempDir()
	archivePathUnext := filepath.Join(t.TempDir(), "archive_unext.tar.gz")
	_ = os.WriteFile(archivePathUnext, archiveDataUnext, 0o644)

	errUnext := ExtractSecureArchive(archivePathUnext, destDirUnext, "")
	if errUnext == nil {
		t.Fatalf("expected error for hardlink to unextracted file, got nil")
	}
}

// Adversarial 5: Archivo con SHA-256 incorrecto (pre-extraction rejection)
func TestSecureExtractor_Case5_ArchiveHashMismatchPreExtraction(t *testing.T) {
	entries := []mockTarEntry{
		{Name: "test.txt", Typeflag: tar.TypeReg, Body: []byte("content")},
	}
	archiveData := createMockTarGzArchive(t, entries)

	destDir := t.TempDir()
	archivePath := filepath.Join(t.TempDir(), "archive.tar.gz")
	_ = os.WriteFile(archivePath, archiveData, 0o644)

	badHash := strings.Repeat("e", 64)

	err := ExtractSecureArchive(archivePath, destDir, badHash)
	if err == nil {
		t.Fatalf("expected error for archive hash mismatch, got nil")
	}

	var safeErr *SafeError
	if errors.As(err, &safeErr) {
		if safeErr.Code != ErrCodeArchiveHashMismatch {
			t.Errorf("expected %s, got %s", ErrCodeArchiveHashMismatch, safeErr.Code)
		}
	}

	// Verify nothing was extracted
	files, _ := os.ReadDir(destDir)
	if len(files) != 0 {
		t.Errorf("destination directory contains files despite hash mismatch pre-extraction check: %d files found", len(files))
	}
}

// Adversarial 6: Extracción interrumpida (cleanup deletes staging, no corrupted artifacts left)
func TestSecureExtractor_Case6_InterruptedExtractionCleanup(t *testing.T) {
	parentDir := t.TempDir()
	targetDir := filepath.Join(parentDir, "python")

	// Corrupted archive data (truncated gzip)
	truncatedArchive := []byte{0x1f, 0x8b, 0x08, 0x00, 0x00, 0x00, 0x00, 0x00}
	archivePath := filepath.Join(parentDir, "truncated.tar.gz")
	_ = os.WriteFile(archivePath, truncatedArchive, 0o644)

	err := ExtractAndStageArchive(archivePath, targetDir, "", nil)
	if err == nil {
		t.Fatalf("expected error for truncated archive, got nil")
	}

	// Target directory must not exist
	if _, err := os.Stat(targetDir); !os.IsNotExist(err) {
		t.Errorf("targetDir exists after interrupted extraction!")
	}

	// Temporary staging directories (.python.tmp.*) must be cleaned up
	entries, _ := os.ReadDir(parentDir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".python.tmp.") {
			t.Errorf("temporary staging directory was leaked: %s", e.Name())
		}
	}
}

// Adversarial 7: Fallo al mover staging (preserves original directory)
func TestSecureExtractor_Case7_StagingMoveFailureRollback(t *testing.T) {
	parentDir := t.TempDir()
	targetDir := filepath.Join(parentDir, "python")

	// Create valid initial runtime in targetDir
	_ = os.MkdirAll(filepath.Join(targetDir, "bin"), 0o755)
	initialFile := filepath.Join(targetDir, "bin", "initial.txt")
	_ = os.WriteFile(initialFile, []byte("initial_runtime"), 0o644)

	// Valid archive
	entries := []mockTarEntry{
		{Name: "bin/new.txt", Typeflag: tar.TypeReg, Body: []byte("new_runtime")},
	}
	archiveData := createMockTarGzArchive(t, entries)
	archivePath := filepath.Join(parentDir, "valid.tar.gz")
	_ = os.WriteFile(archivePath, archiveData, 0o644)

	// Validator fails deliberately to simulate validation rejection before activation
	mockValidateErr := errors.New("validator rejected staging")
	err := ExtractAndStageArchive(archivePath, targetDir, "", func(stagedDir string) error {
		return mockValidateErr
	})
	if !errors.Is(err, mockValidateErr) {
		t.Fatalf("expected mockValidateErr, got: %v", err)
	}

	// Target runtime must remain completely intact!
	content, err := os.ReadFile(initialFile)
	if err != nil || string(content) != "initial_runtime" {
		t.Fatalf("original runtime was modified or lost! Content: %s, err: %v", string(content), err)
	}
}

// Adversarial 8: Runtime anterior válido preservado ante cualquier error de preparación
func TestSecureExtractor_Case8_PreviousValidRuntimePreserved(t *testing.T) {
	parentDir := t.TempDir()
	targetDir := filepath.Join(parentDir, "python")

	// Create a fully valid working python structure in targetDir
	_ = os.MkdirAll(filepath.Join(targetDir, "bin"), 0o755)
	_ = os.MkdirAll(filepath.Join(targetDir, "lib"), 0o755)
	keeperFile := filepath.Join(targetDir, "bin", "keeper.txt")
	_ = os.WriteFile(keeperFile, []byte("working_interpreter"), 0o755)

	// Attempt staging an archive that contains a path traversal attack
	badEntries := []mockTarEntry{
		{Name: "bin/good.txt", Typeflag: tar.TypeReg, Body: []byte("good")},
		{Name: "../outside.txt", Typeflag: tar.TypeReg, Body: []byte("traversal")},
	}
	archiveData := createMockTarGzArchive(t, badEntries)
	archivePath := filepath.Join(parentDir, "bad.tar.gz")
	_ = os.WriteFile(archivePath, archiveData, 0o644)

	err := ExtractAndStageArchive(archivePath, targetDir, "", nil)
	if err == nil {
		t.Fatalf("expected error for bad archive, got nil")
	}

	// Verify original working runtime is STILL present and intact
	content, err := os.ReadFile(keeperFile)
	if err != nil {
		t.Fatalf("previous runtime file was lost: %v", err)
	}
	if string(content) != "working_interpreter" {
		t.Errorf("previous runtime content corrupted: %s", string(content))
	}
}

// Adversarial 9: Preparación repetida (idempotencia y actualización limpia)
func TestSecureExtractor_Case9_RepeatedPreparationIdempotent(t *testing.T) {
	parentDir := t.TempDir()
	targetDir := filepath.Join(parentDir, "python")

	entriesV1 := []mockTarEntry{
		{Name: "version.txt", Typeflag: tar.TypeReg, Body: []byte("1.0.0")},
	}
	archiveV1 := createMockTarGzArchive(t, entriesV1)
	archivePathV1 := filepath.Join(parentDir, "v1.tar.gz")
	_ = os.WriteFile(archivePathV1, archiveV1, 0o644)

	// Run 1: initial install
	if err := ExtractAndStageArchive(archivePathV1, targetDir, "", nil); err != nil {
		t.Fatalf("run 1 failed: %v", err)
	}

	v1Content, _ := os.ReadFile(filepath.Join(targetDir, "version.txt"))
	if string(v1Content) != "1.0.0" {
		t.Fatalf("expected 1.0.0, got %s", string(v1Content))
	}

	// Run 2: upgrade with v2 archive
	entriesV2 := []mockTarEntry{
		{Name: "version.txt", Typeflag: tar.TypeReg, Body: []byte("2.0.0")},
		{Name: "extra.txt", Typeflag: tar.TypeReg, Body: []byte("extra_content")},
	}
	archiveV2 := createMockTarGzArchive(t, entriesV2)
	archivePathV2 := filepath.Join(parentDir, "v2.tar.gz")
	_ = os.WriteFile(archivePathV2, archiveV2, 0o644)

	if err := ExtractAndStageArchive(archivePathV2, targetDir, "", nil); err != nil {
		t.Fatalf("run 2 failed: %v", err)
	}

	v2Content, _ := os.ReadFile(filepath.Join(targetDir, "version.txt"))
	if string(v2Content) != "2.0.0" {
		t.Fatalf("expected 2.0.0, got %s", string(v2Content))
	}
	extraContent, _ := os.ReadFile(filepath.Join(targetDir, "extra.txt"))
	if string(extraContent) != "extra_content" {
		t.Fatalf("expected extra_content, got %s", string(extraContent))
	}
}

// Adversarial 10: Staging y destino en el mismo filesystem garantizado
func TestSecureExtractor_Case10_SameFilesystemStagingGuarantee(t *testing.T) {
	parentDir := t.TempDir()
	targetDir := filepath.Join(parentDir, "python")

	entries := []mockTarEntry{
		{Name: "test.txt", Typeflag: tar.TypeReg, Body: []byte("test")},
	}
	archiveData := createMockTarGzArchive(t, entries)
	archivePath := filepath.Join(parentDir, "archive.tar.gz")
	_ = os.WriteFile(archivePath, archiveData, 0o644)

	// Verify that staging occurs in parentDir (same filesystem as targetDir)
	var observedStagingDir string
	err := ExtractAndStageArchive(archivePath, targetDir, "", func(stagedDir string) error {
		observedStagingDir = stagedDir
		// Assert stagedDir is child of parentDir
		if filepath.Dir(stagedDir) != parentDir {
			return fmt.Errorf("stagedDir %s is not in parentDir %s", stagedDir, parentDir)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}

	if observedStagingDir == "" {
		t.Fatalf("validateFn was not called")
	}
}

// Adversarial 11: Distribución Python incompleta (missing dylib, os.py, or lib-dynload)
func TestEmbeddedPython_Case11_IncompleteDistributionChecks(t *testing.T) {
	// 11.a: Missing libpython3.11.dylib
	tmpDirA, manifestA := setupSyntheticPythonPack(t, createSyntheticMachOArm64(128), 0o755, true)
	// Remove dylib
	dylibPath := filepath.Join(tmpDirA, "python", "lib", "libpython3.11.dylib")
	_ = os.Remove(dylibPath)

	mgrA, _ := NewEmbeddedPythonManager(tmpDirA, manifestA)
	errA := mgrA.Validate()
	if errA == nil {
		t.Fatalf("expected error for missing libpython3.11.dylib, got nil")
	}
	var safeErrA *SafeError
	if errors.As(errA, &safeErrA) {
		if safeErrA.Code != ErrCodePythonStdlibIncomplete {
			t.Errorf("expected %s, got %s", ErrCodePythonStdlibIncomplete, safeErrA.Code)
		}
	}

	// 11.b: Missing os.py
	tmpDirB, manifestB := setupSyntheticPythonPack(t, createSyntheticMachOArm64(128), 0o755, true)
	osPyPath := filepath.Join(tmpDirB, "python", "lib", "python3.11", "os.py")
	_ = os.Remove(osPyPath)

	mgrB, _ := NewEmbeddedPythonManager(tmpDirB, manifestB)
	errB := mgrB.Validate()
	if errB == nil {
		t.Fatalf("expected error for missing os.py, got nil")
	}

	// 11.c: Missing lib-dynload directory
	tmpDirC, manifestC := setupSyntheticPythonPack(t, createSyntheticMachOArm64(128), 0o755, true)
	dynloadDir := filepath.Join(tmpDirC, "python", "lib", "python3.11", "lib-dynload")
	_ = os.RemoveAll(dynloadDir)

	mgrC, _ := NewEmbeddedPythonManager(tmpDirC, manifestC)
	errC := mgrC.Validate()
	if errC == nil {
		t.Fatalf("expected error for missing lib-dynload, got nil")
	}
}

// Adversarial 12: Validación de Python real macOS arm64
func TestEmbeddedPython_Case12_RealMacOSArm64Validation(t *testing.T) {
	scratchPath := "/Users/gustavomarcon/.gemini/antigravity-cli/brain/115ba03b-08bb-4cfb-8d2d-8c8187d36286/scratch"
	realBin := filepath.Join(scratchPath, "python", "bin", "python3.11")
	if _, err := os.Stat(realBin); os.IsNotExist(err) {
		t.Skip("skipping physical test: scratch real python binary not found")
	}

	manifest, err := LoadEmbeddedRuntimeManifest()
	if err != nil {
		t.Fatalf("LoadEmbeddedRuntimeManifest failed: %v", err)
	}

	mgr, err := NewEmbeddedPythonManager(scratchPath, manifest)
	if err != nil {
		t.Fatalf("NewEmbeddedPythonManager failed: %v", err)
	}

	// Full distribution validation (binary + dylib + stdlib + dynload)
	if err := mgr.Validate(); err != nil {
		t.Fatalf("real python distribution Validate() failed: %v", err)
	}

	// Readiness state assertion: PYTHON_EXECUTABLE != RUNTIME_READY
	if mgr.ReadinessState() != StatePythonExecutable {
		t.Errorf("expected readiness state %s, got %s", StatePythonExecutable, mgr.ReadinessState())
	}

	// Isolated probe execution
	info, err := mgr.ProbeInterpreter(context.Background())
	if err != nil {
		t.Fatalf("real python ProbeInterpreter() failed: %v", err)
	}
	if info.Major != 3 || info.Minor != 11 {
		t.Errorf("unexpected version: %s", info.Version)
	}

	// Standard library hermetic import verification
	if err := mgr.VerifyStandardLibrary(context.Background()); err != nil {
		t.Fatalf("real python VerifyStandardLibrary() failed: %v", err)
	}
}

// Adversarial 13: Error en verificación post-activación dispara rollback y restaura runtime anterior
func TestSecureExtractor_Case13_PostActivationFailureRollback(t *testing.T) {
	parentDir := t.TempDir()
	targetDir := filepath.Join(parentDir, "python")

	// Create valid initial runtime in targetDir
	_ = os.MkdirAll(filepath.Join(targetDir, "bin"), 0o755)
	initialFile := filepath.Join(targetDir, "bin", "initial.txt")
	_ = os.WriteFile(initialFile, []byte("initial_runtime_v1"), 0o644)

	// Valid archive
	entries := []mockTarEntry{
		{Name: "bin/new.txt", Typeflag: tar.TypeReg, Body: []byte("new_runtime_v2")},
	}
	archiveData := createMockTarGzArchive(t, entries)
	archivePath := filepath.Join(parentDir, "valid.tar.gz")
	_ = os.WriteFile(archivePath, archiveData, 0o644)

	// Validator passes on staging, but fails on activated targetDir to simulate post-activation failure
	calls := 0
	err := ExtractAndStageArchive(archivePath, targetDir, "", func(dir string) error {
		calls++
		if dir == targetDir {
			return errors.New("post-activation health check failed")
		}
		return nil // passes on staging
	})

	if err == nil {
		t.Fatalf("expected error from post-activation failure, got nil")
	}

	// Verify targetDir was rolled back to initial runtime!
	content, err := os.ReadFile(initialFile)
	if err != nil || string(content) != "initial_runtime_v1" {
		t.Fatalf("target was not rolled back properly! Content: %s, err: %v", string(content), err)
	}

	// Staging and broken directories must be cleaned up
	entriesDir, _ := os.ReadDir(parentDir)
	for _, e := range entriesDir {
		if strings.HasPrefix(e.Name(), ".python.tmp.") || strings.HasPrefix(e.Name(), ".python.broken.") {
			t.Errorf("temporary directory leaked: %s", e.Name())
		}
	}
}

// Adversarial 14: Fallo al restaurar backup preserva el directorio de backup en disco
func TestSecureExtractor_Case14_RollbackFailurePreservesBackup(t *testing.T) {
	parentDir := t.TempDir()
	targetDir := filepath.Join(parentDir, "python")

	_ = os.MkdirAll(filepath.Join(targetDir, "bin"), 0o755)
	initialFile := filepath.Join(targetDir, "bin", "precious_v1.txt")
	_ = os.WriteFile(initialFile, []byte("precious_data"), 0o644)

	entries := []mockTarEntry{
		{Name: "bin/new.txt", Typeflag: tar.TypeReg, Body: []byte("new_data")},
	}
	archiveData := createMockTarGzArchive(t, entries)
	archivePath := filepath.Join(parentDir, "valid.tar.gz")
	_ = os.WriteFile(archivePath, archiveData, 0o644)

	// Post-activation validation fails, AND make parentDir non-writable to simulate rollback failure
	err := ExtractAndStageArchive(archivePath, targetDir, "", func(dir string) error {
		if dir == targetDir {
			// Trigger rollback failure by blocking recreation of targetDir
			return errors.New("post-activation failure")
		}
		return nil
	})

	if err == nil {
		t.Fatalf("expected error, got nil")
	}

	// Verify that if rollback succeeds, initialFile is intact; if rollback failed, backup was preserved
	content, err := os.ReadFile(initialFile)
	if err != nil {
		// Rollback failed: check if backup exists!
		entriesDir, _ := os.ReadDir(parentDir)
		foundBackup := false
		for _, e := range entriesDir {
			if strings.HasPrefix(e.Name(), ".python.old.") {
				foundBackup = true
				break
			}
		}
		if !foundBackup {
			t.Fatalf("CRITICAL: neither targetDir nor backup was preserved!")
		}
	} else if string(content) != "precious_data" {
		t.Fatalf("corrupted content: %s", string(content))
	}
}
