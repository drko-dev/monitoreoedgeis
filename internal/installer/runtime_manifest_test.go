package installer

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// helper to calculate sha256 of bytes
func sha256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// createValidSyntheticManifest returns a fully populated valid RuntimeManifest struct
func createValidSyntheticManifest() RuntimeManifest {
	return RuntimeManifest{
		SchemaVersion:  CurrentSchemaVersion,
		RuntimeVersion: "1.0.0",
		Platform:       PlatformDarwin,
		Architecture:   ArchArm64,
		Python: PythonSpec{
			Version:            "3.11.9",
			ABI:                "cp311",
			RelativeBinaryPath: "python/bin/python3",
			SHA256:             "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			SizeBytes:          1024,
		},
		Worker: WorkerSpec{
			Version:    "1.1.0",
			Entrypoint: "worker/worker.py",
			SHA256:     "123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef0",
			SizeBytes:  512,
		},
		Binaries: []BinarySpec{
			{
				Name:         "ffmpeg",
				RelativePath: "bin/ffmpeg",
				Version:      "7.1.0",
				SHA256:       "23456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef01",
				SizeBytes:    2048,
			},
		},
		Dependencies: []DependencySpec{
			{
				Name:         "torch",
				Version:      "2.4.0",
				Filename:     "torch-2.4.0-cp311-none-macosx_11_0_arm64.whl",
				RelativePath: "wheelhouse/torch-2.4.0-cp311-none-macosx_11_0_arm64.whl",
				SHA256:       "3456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef012",
				SizeBytes:    4096,
			},
		},
		Compatibility: CompatibilitySpec{
			EdgeMinVersion:       "1.1.6",
			ModelsPackMinVersion: "1.0.0",
		},
	}
}

func TestRuntimeManifest_EmbeddedManifest(t *testing.T) {
	manifest, err := LoadEmbeddedRuntimeManifest()
	if err != nil {
		t.Fatalf("LoadEmbeddedRuntimeManifest failed: %v", err)
	}
	if manifest.SchemaVersion != CurrentSchemaVersion {
		t.Errorf("got SchemaVersion %s, want %s", manifest.SchemaVersion, CurrentSchemaVersion)
	}
	if err := manifest.ValidateSchema(); err != nil {
		t.Errorf("embedded manifest failed schema validation: %v", err)
	}
	if err := manifest.ValidateCompatibility(PlatformDarwin, ArchArm64); err != nil {
		t.Errorf("embedded manifest compatibility check failed: %v", err)
	}
}

// 1. Manifest válido (synthetic fixture)
func TestRuntimeManifest_Case1_ValidSyntheticManifest(t *testing.T) {
	m := createValidSyntheticManifest()
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("failed to marshal valid manifest: %v", err)
	}

	parsed, err := ParseRuntimeManifest(data)
	if err != nil {
		t.Fatalf("ParseRuntimeManifest failed for valid manifest: %v", err)
	}

	if parsed.RuntimeVersion != "1.0.0" {
		t.Errorf("expected runtime_version 1.0.0, got %s", parsed.RuntimeVersion)
	}
	if err := parsed.ValidateCompatibility(PlatformDarwin, ArchArm64); err != nil {
		t.Errorf("ValidateCompatibility failed: %v", err)
	}
}

// 2. JSON corrupto / malformado
func TestRuntimeManifest_Case2_CorruptMalformedJSON(t *testing.T) {
	tests := []struct {
		name string
		data []byte
	}{
		{"empty payload", []byte("")},
		{"malformed json syntax", []byte(`{ "schema_version": "1.0.0", incomplete`)},
		{"wrong type for field", []byte(`{ "schema_version": 123 }`)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseRuntimeManifest(tt.data)
			if err == nil {
				t.Errorf("expected error for malformed json %s, got nil", tt.name)
			}
		})
	}
}

// 3. Schema_version desconocido / no soportado
func TestRuntimeManifest_Case3_UnsupportedSchemaVersion(t *testing.T) {
	versions := []string{"0.9.0", "2.0.0", "v1.0", "", "1.0"}
	for _, v := range versions {
		t.Run("version_"+v, func(t *testing.T) {
			m := createValidSyntheticManifest()
			m.SchemaVersion = v
			data, err := json.Marshal(m)
			if err != nil {
				t.Fatalf("marshal error: %v", err)
			}
			_, err = ParseRuntimeManifest(data)
			if err == nil {
				t.Fatalf("expected error for unsupported schema version %q, got nil", v)
			}
			if !errors.Is(err, ErrManifestSchemaInvalid) {
				var safeErr *SafeError
				if !errors.As(err, &safeErr) || safeErr.Code != "UNSUPPORTED_SCHEMA_VERSION" {
					t.Errorf("expected UNSUPPORTED_SCHEMA_VERSION error, got: %v", err)
				}
			}
		})
	}
}

// 4. Campo obligatorio faltante (missing required fields)
func TestRuntimeManifest_Case4_MissingRequiredFields(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(*RuntimeManifest)
		expectErr string
	}{
		{
			name: "missing runtime_version",
			mutate: func(m *RuntimeManifest) {
				m.RuntimeVersion = ""
			},
			expectErr: "INVALID_RUNTIME_VERSION",
		},
		{
			name: "missing platform",
			mutate: func(m *RuntimeManifest) {
				m.Platform = ""
			},
			expectErr: "INVALID_PLATFORM",
		},
		{
			name: "missing architecture",
			mutate: func(m *RuntimeManifest) {
				m.Architecture = ""
			},
			expectErr: "INVALID_ARCHITECTURE",
		},
		{
			name: "missing python version",
			mutate: func(m *RuntimeManifest) {
				m.Python.Version = ""
			},
			expectErr: "INVALID_PYTHON_VERSION",
		},
		{
			name: "missing python abi",
			mutate: func(m *RuntimeManifest) {
				m.Python.ABI = ""
			},
			expectErr: "INVALID_PYTHON_ABI",
		},
		{
			name: "missing worker version",
			mutate: func(m *RuntimeManifest) {
				m.Worker.Version = ""
			},
			expectErr: "INVALID_WORKER_VERSION",
		},
		{
			name: "missing binary name",
			mutate: func(m *RuntimeManifest) {
				m.Binaries[0].Name = ""
			},
			expectErr: "INVALID_BINARY_NAME",
		},
		{
			name: "missing dependency name",
			mutate: func(m *RuntimeManifest) {
				m.Dependencies[0].Name = ""
			},
			expectErr: "INVALID_DEP_NAME",
		},
		{
			name: "missing dependency filename",
			mutate: func(m *RuntimeManifest) {
				m.Dependencies[0].Filename = ""
			},
			expectErr: "INVALID_DEP_FILENAME",
		},
		{
			name: "missing edge_min_version",
			mutate: func(m *RuntimeManifest) {
				m.Compatibility.EdgeMinVersion = ""
			},
			expectErr: "INVALID_COMPATIBILITY",
		},
		{
			name: "missing models_pack_min_version",
			mutate: func(m *RuntimeManifest) {
				m.Compatibility.ModelsPackMinVersion = ""
			},
			expectErr: "INVALID_COMPATIBILITY",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := createValidSyntheticManifest()
			tt.mutate(&m)
			err := m.ValidateSchema()
			if err == nil {
				t.Fatalf("expected error for %s, got nil", tt.name)
			}
			var safeErr *SafeError
			if errors.As(err, &safeErr) {
				if safeErr.Code != tt.expectErr {
					t.Errorf("got error code %s, want %s", safeErr.Code, tt.expectErr)
				}
			}
		})
	}
}

// 5. Hash SHA-256 inválido (non-64 hex char)
func TestRuntimeManifest_Case5_InvalidSHA256(t *testing.T) {
	badHashes := []string{
		"tooshort",
		"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcde",   // 63 chars
		"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdefg", // 65 chars (contains 'g')
		"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdeZ",  // valid length but non-hex 'Z'
		"",
	}

	for _, bh := range badHashes {
		t.Run("hash_"+bh, func(t *testing.T) {
			m := createValidSyntheticManifest()
			m.Python.SHA256 = bh
			if err := m.ValidateSchema(); err == nil {
				t.Errorf("expected error for python SHA256 %q, got nil", bh)
			}

			m = createValidSyntheticManifest()
			m.Worker.SHA256 = bh
			if err := m.ValidateSchema(); err == nil {
				t.Errorf("expected error for worker SHA256 %q, got nil", bh)
			}

			m = createValidSyntheticManifest()
			m.Binaries[0].SHA256 = bh
			if err := m.ValidateSchema(); err == nil {
				t.Errorf("expected error for binary SHA256 %q, got nil", bh)
			}

			m = createValidSyntheticManifest()
			m.Dependencies[0].SHA256 = bh
			if err := m.ValidateSchema(); err == nil {
				t.Errorf("expected error for dependency SHA256 %q, got nil", bh)
			}
		})
	}
}

// 6. Tamaño incorrecto (size_bytes <= 0 en spec, o mismatch físico)
func TestRuntimeManifest_Case6_InvalidSizes(t *testing.T) {
	t.Run("spec size_bytes <= 0", func(t *testing.T) {
		m := createValidSyntheticManifest()
		m.Python.SizeBytes = 0
		if err := m.ValidateSchema(); err == nil {
			t.Error("expected error for Python.SizeBytes <= 0")
		}

		m = createValidSyntheticManifest()
		m.Worker.SizeBytes = -10
		if err := m.ValidateSchema(); err == nil {
			t.Error("expected error for Worker.SizeBytes <= 0")
		}

		m = createValidSyntheticManifest()
		m.Binaries[0].SizeBytes = 0
		if err := m.ValidateSchema(); err == nil {
			t.Error("expected error for Binary.SizeBytes <= 0")
		}

		m = createValidSyntheticManifest()
		m.Dependencies[0].SizeBytes = -1
		if err := m.ValidateSchema(); err == nil {
			t.Error("expected error for Dependency.SizeBytes <= 0")
		}
	})

	t.Run("physical size mismatch", func(t *testing.T) {
		tmpDir := t.TempDir()
		m := setupPhysicalFiles(t, tmpDir)

		// Alter expected size in manifest to cause mismatch
		m.Python.SizeBytes = 999999
		report, err := m.ValidateArtifacts(tmpDir)
		if err == nil {
			t.Fatal("expected error for physical size mismatch, got nil")
		}
		if !errors.Is(err, ErrArtifactCorrupted) {
			var safeErr *SafeError
			if !errors.As(err, &safeErr) || safeErr.Code != "ARTIFACT_SIZE_MISMATCH" {
				t.Errorf("expected ARTIFACT_SIZE_MISMATCH, got: %v", err)
			}
		}
		if report == nil || report.ValidArtifacts != 0 {
			t.Errorf("unexpected report state: %+v", report)
		}
	})
}

// Helper to set up physical directory matching a manifest
func setupPhysicalFiles(t *testing.T, root string) RuntimeManifest {
	pyContent := []byte("#!/usr/bin/env python3\nprint('python')\n")
	workerContent := []byte("# worker script\nprint('worker')\n")
	binContent := []byte("binary-payload-data")
	depContent := []byte("wheel-archive-content-simulated")

	pyRel := "python/bin/python3"
	workerRel := "worker/worker.py"
	binRel := "bin/ffmpeg"
	depRel := "wheelhouse/torch-2.4.0-cp311-none-macosx_11_0_arm64.whl"

	writeFile(t, filepath.Join(root, pyRel), pyContent)
	writeFile(t, filepath.Join(root, workerRel), workerContent)
	writeFile(t, filepath.Join(root, binRel), binContent)
	writeFile(t, filepath.Join(root, depRel), depContent)

	m := createValidSyntheticManifest()
	m.Python.RelativeBinaryPath = pyRel
	m.Python.SHA256 = sha256Hex(pyContent)
	m.Python.SizeBytes = int64(len(pyContent))

	m.Worker.Entrypoint = workerRel
	m.Worker.SHA256 = sha256Hex(workerContent)
	m.Worker.SizeBytes = int64(len(workerContent))

	m.Binaries[0].RelativePath = binRel
	m.Binaries[0].SHA256 = sha256Hex(binContent)
	m.Binaries[0].SizeBytes = int64(len(binContent))

	m.Dependencies[0].RelativePath = depRel
	m.Dependencies[0].SHA256 = sha256Hex(depContent)
	m.Dependencies[0].SizeBytes = int64(len(depContent))

	return m
}

func writeFile(t *testing.T, fullPath string, data []byte) {
	if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
		t.Fatalf("failed to create dir: %v", err)
	}
	if err := os.WriteFile(fullPath, data, 0644); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}
}

// 7. Archivo físico ausente (ErrArtifactMissing)
func TestRuntimeManifest_Case7_MissingPhysicalArtifact(t *testing.T) {
	tmpDir := t.TempDir()
	m := setupPhysicalFiles(t, tmpDir)

	// Remove one of the required physical files
	os.Remove(filepath.Join(tmpDir, m.Worker.Entrypoint))

	report, err := m.ValidateArtifacts(tmpDir)
	if err == nil {
		t.Fatal("expected error for missing physical artifact, got nil")
	}
	if !errors.Is(err, ErrArtifactMissing) {
		var safeErr *SafeError
		if !errors.As(err, &safeErr) || safeErr.Code != "ARTIFACT_MISSING" {
			t.Errorf("expected ARTIFACT_MISSING, got: %v", err)
		}
	}
	if report == nil {
		t.Fatal("expected non-nil report even on failure")
	}
}

// 8. Archivo truncado (ErrArtifactCorrupted)
func TestRuntimeManifest_Case8_TruncatedFile(t *testing.T) {
	tmpDir := t.TempDir()
	m := setupPhysicalFiles(t, tmpDir)

	// Truncate binary
	binFull := filepath.Join(tmpDir, m.Binaries[0].RelativePath)
	if err := os.WriteFile(binFull, []byte("short"), 0644); err != nil {
		t.Fatalf("failed to truncate: %v", err)
	}

	report, err := m.ValidateArtifacts(tmpDir)
	if err == nil {
		t.Fatal("expected error for truncated physical artifact, got nil")
	}
	if !errors.Is(err, ErrArtifactCorrupted) {
		var safeErr *SafeError
		if !errors.As(err, &safeErr) || safeErr.Code != "ARTIFACT_SIZE_MISMATCH" {
			t.Errorf("expected ARTIFACT_SIZE_MISMATCH, got: %v", err)
		}
	}
	if report == nil {
		t.Fatal("expected non-nil report")
	}
}

// 9. Archivo modificado / hash mismatch (ErrArtifactCorrupted)
func TestRuntimeManifest_Case9_ModifiedFileHashMismatch(t *testing.T) {
	tmpDir := t.TempDir()
	m := setupPhysicalFiles(t, tmpDir)

	// Overwrite dependency with same length but different bytes
	depFull := filepath.Join(tmpDir, m.Dependencies[0].RelativePath)
	sameLenTampered := make([]byte, m.Dependencies[0].SizeBytes)
	for i := range sameLenTampered {
		sameLenTampered[i] = 'X'
	}
	if err := os.WriteFile(depFull, sameLenTampered, 0644); err != nil {
		t.Fatalf("failed to write tampered file: %v", err)
	}

	report, err := m.ValidateArtifacts(tmpDir)
	if err == nil {
		t.Fatal("expected error for tampered physical artifact, got nil")
	}
	if !errors.Is(err, ErrArtifactCorrupted) {
		var safeErr *SafeError
		if !errors.As(err, &safeErr) || safeErr.Code != "ARTIFACT_HASH_MISMATCH" {
			t.Errorf("expected ARTIFACT_HASH_MISMATCH, got: %v", err)
		}
	}
	if report == nil {
		t.Fatal("expected non-nil report")
	}
}

// 10. Arquitectura incompatible (arm64 vs amd64)
func TestRuntimeManifest_Case10_IncompatibleArchitecture(t *testing.T) {
	m := createValidSyntheticManifest()
	m.Architecture = ArchArm64

	err := m.ValidateCompatibility(PlatformDarwin, ArchAmd64)
	if err == nil {
		t.Fatal("expected compatibility error for architecture mismatch, got nil")
	}
	if !errors.Is(err, ErrManifestIncompatible) {
		var safeErr *SafeError
		if !errors.As(err, &safeErr) || safeErr.Code != "INCOMPATIBLE_ARCHITECTURE" {
			t.Errorf("expected INCOMPATIBLE_ARCHITECTURE, got: %v", err)
		}
	}
}

// 11. ABI / plataforma incompatible (darwin vs linux)
func TestRuntimeManifest_Case11_IncompatiblePlatform(t *testing.T) {
	m := createValidSyntheticManifest()
	m.Platform = PlatformDarwin

	err := m.ValidateCompatibility(PlatformLinux, ArchArm64)
	if err == nil {
		t.Fatal("expected compatibility error for platform mismatch, got nil")
	}
	if !errors.Is(err, ErrManifestIncompatible) {
		var safeErr *SafeError
		if !errors.As(err, &safeErr) || safeErr.Code != "INCOMPATIBLE_PLATFORM" {
			t.Errorf("expected INCOMPATIBLE_PLATFORM, got: %v", err)
		}
	}
}

// 12. Versiones malformadas o rangos abiertos (ultralytics>=8.3, etc.)
func TestRuntimeManifest_Case12_OpenVersionRangesRejected(t *testing.T) {
	forbiddenVersions := []string{
		">=8.3.0",
		"<=2.0.0",
		">1.0",
		"<3.0",
		"~1.2.3",
		"^1.0.0",
		"==1.1.0",
		"8.3.*",
	}

	for _, fv := range forbiddenVersions {
		t.Run("version_"+fv, func(t *testing.T) {
			m := createValidSyntheticManifest()
			m.Dependencies[0].Version = fv
			err := m.ValidateSchema()
			if err == nil {
				t.Fatalf("expected error for loose version range %q, got nil", fv)
			}
			var safeErr *SafeError
			if !errors.As(err, &safeErr) || safeErr.Code != "INVALID_VERSION_PIN" {
				t.Errorf("expected INVALID_VERSION_PIN, got: %v", err)
			}
		})
	}
}

// 13. Rutas absolutas (/bin/python rechazado)
func TestRuntimeManifest_Case13_AbsolutePathsRejected(t *testing.T) {
	absPaths := []string{
		"/usr/bin/python3",
		"/bin/sh",
		"/etc/passwd",
		"\\Windows\\System32\\cmd.exe",
	}

	for _, ap := range absPaths {
		t.Run("abs_"+ap, func(t *testing.T) {
			m := createValidSyntheticManifest()
			m.Python.RelativeBinaryPath = ap
			err := m.ValidateSchema()
			if err == nil {
				t.Fatalf("expected error for absolute path %q, got nil", ap)
			}
			if !errors.Is(err, ErrArtifactSecurity) {
				t.Errorf("expected ErrArtifactSecurity, got %v", err)
			}
		})
	}
}

// 14. Path traversal (../../etc/passwd rechazado)
func TestRuntimeManifest_Case14_PathTraversalRejected(t *testing.T) {
	traversals := []string{
		"../escape",
		"../../etc/passwd",
		"foo/../../bar",
		"./../bad",
		"dir/subdir/../../../../secret",
	}

	for _, pt := range traversals {
		t.Run("traversal_"+pt, func(t *testing.T) {
			m := createValidSyntheticManifest()
			m.Worker.Entrypoint = pt
			err := m.ValidateSchema()
			if err == nil {
				t.Fatalf("expected error for traversal path %q, got nil", pt)
			}
			if !errors.Is(err, ErrArtifactSecurity) {
				t.Errorf("expected ErrArtifactSecurity, got %v", err)
			}
		})
	}
}

// 15. Duplicados (binary names, filenames, relative paths)
func TestRuntimeManifest_Case15_DuplicatesRejected(t *testing.T) {
	t.Run("duplicate binary name", func(t *testing.T) {
		m := createValidSyntheticManifest()
		m.Binaries = append(m.Binaries, BinarySpec{
			Name:         m.Binaries[0].Name, // duplicate name
			RelativePath: "bin/other",
			Version:      "1.0.0",
			SHA256:       m.Binaries[0].SHA256,
			SizeBytes:    100,
		})
		err := m.ValidateSchema()
		if err == nil {
			t.Fatal("expected error for duplicate binary name, got nil")
		}
	})

	t.Run("duplicate binary path", func(t *testing.T) {
		m := createValidSyntheticManifest()
		m.Binaries = append(m.Binaries, BinarySpec{
			Name:         "other-binary",
			RelativePath: m.Binaries[0].RelativePath, // duplicate path
			Version:      "1.0.0",
			SHA256:       m.Binaries[0].SHA256,
			SizeBytes:    100,
		})
		err := m.ValidateSchema()
		if err == nil {
			t.Fatal("expected error for duplicate binary relative path, got nil")
		}
	})

	t.Run("duplicate dependency name", func(t *testing.T) {
		m := createValidSyntheticManifest()
		m.Dependencies = append(m.Dependencies, DependencySpec{
			Name:         m.Dependencies[0].Name, // duplicate name
			Version:      "2.5.0",
			Filename:     "different.whl",
			RelativePath: "wheelhouse/different.whl",
			SHA256:       m.Dependencies[0].SHA256,
			SizeBytes:    200,
		})
		err := m.ValidateSchema()
		if err == nil {
			t.Fatal("expected error for duplicate dependency name, got nil")
		}
	})

	t.Run("duplicate dependency filename", func(t *testing.T) {
		m := createValidSyntheticManifest()
		m.Dependencies = append(m.Dependencies, DependencySpec{
			Name:         "other-dep",
			Version:      "2.5.0",
			Filename:     m.Dependencies[0].Filename, // duplicate filename
			RelativePath: "wheelhouse/other.whl",
			SHA256:       m.Dependencies[0].SHA256,
			SizeBytes:    200,
		})
		err := m.ValidateSchema()
		if err == nil {
			t.Fatal("expected error for duplicate dependency filename, got nil")
		}
	})

	t.Run("duplicate dependency path", func(t *testing.T) {
		m := createValidSyntheticManifest()
		m.Dependencies = append(m.Dependencies, DependencySpec{
			Name:         "other-dep",
			Version:      "2.5.0",
			Filename:     "other.whl",
			RelativePath: m.Dependencies[0].RelativePath, // duplicate relative path
			SHA256:       m.Dependencies[0].SHA256,
			SizeBytes:    200,
		})
		err := m.ValidateSchema()
		if err == nil {
			t.Fatal("expected error for duplicate dependency relative path, got nil")
		}
	})
}

// 16. Symlink que sale del pack root (ErrArtifactSecurity)
func TestRuntimeManifest_Case16_EscapingSymlinkRejected(t *testing.T) {
	tmpDir := t.TempDir()
	outsideDir := t.TempDir()
	m := setupPhysicalFiles(t, tmpDir)

	// Create an external secret file
	secretPath := filepath.Join(outsideDir, "secret.txt")
	if err := os.WriteFile(secretPath, []byte("confidential"), 0644); err != nil {
		t.Fatalf("failed to write outside file: %v", err)
	}

	// Replace the worker file with a symlink pointing outside
	workerPath := filepath.Join(tmpDir, m.Worker.Entrypoint)
	if err := os.Remove(workerPath); err != nil {
		t.Fatalf("failed to remove worker: %v", err)
	}
	if err := os.Symlink(secretPath, workerPath); err != nil {
		t.Fatalf("failed to create symlink: %v", err)
	}

	report, err := m.ValidateArtifacts(tmpDir)
	if err == nil {
		t.Fatal("expected error for escaping symlink, got nil")
	}
	if !errors.Is(err, ErrArtifactSecurity) {
		var safeErr *SafeError
		if !errors.As(err, &safeErr) || safeErr.Code != "SECURITY_ESCAPING_SYMLINK" {
			t.Errorf("expected SECURITY_ESCAPING_SYMLINK, got: %v", err)
		}
	}
	if report == nil {
		t.Fatal("expected report")
	}

	// Also test broken symlink
	brokenTarget := filepath.Join(tmpDir, "non_existent_target")
	if err := os.Remove(workerPath); err != nil {
		t.Fatalf("failed to remove worker: %v", err)
	}
	if err := os.Symlink(brokenTarget, workerPath); err != nil {
		t.Fatalf("failed to create symlink: %v", err)
	}

	_, err = m.ValidateArtifacts(tmpDir)
	if err == nil {
		t.Fatal("expected error for broken symlink, got nil")
	}
	if !errors.Is(err, ErrArtifactSecurity) {
		var safeErr *SafeError
		if !errors.As(err, &safeErr) || safeErr.Code != "SECURITY_BROKEN_SYMLINK" {
			t.Errorf("expected SECURITY_BROKEN_SYMLINK, got: %v", err)
		}
	}
}

// 17. Archivo grande validado mediante streaming (hashFileSHA256)
func TestRuntimeManifest_Case17_LargeFileStreamingValidation(t *testing.T) {
	tmpDir := t.TempDir()
	largeFilePath := filepath.Join(tmpDir, "large_file.bin")

	// Generate a 4 MB file with patterned data
	chunkSize := 64 * 1024
	totalChunks := 64 // 4 MB
	chunk := make([]byte, chunkSize)
	for i := range chunk {
		chunk[i] = byte(i % 256)
	}

	f, err := os.Create(largeFilePath)
	if err != nil {
		t.Fatalf("failed to create file: %v", err)
	}
	hasher := sha256.New()
	for i := 0; i < totalChunks; i++ {
		if _, err := f.Write(chunk); err != nil {
			f.Close()
			t.Fatalf("failed to write chunk: %v", err)
		}
		hasher.Write(chunk)
	}
	f.Close()

	expectedHash := hex.EncodeToString(hasher.Sum(nil))

	computedHash, err := hashFileSHA256(largeFilePath)
	if err != nil {
		t.Fatalf("hashFileSHA256 failed: %v", err)
	}

	if computedHash != expectedHash {
		t.Errorf("hash mismatch: got %s, want %s", computedHash, expectedHash)
	}
}

// 18. Manifest con entradas en distinto orden
func TestRuntimeManifest_Case18_ManifestDifferentOrder(t *testing.T) {
	// Reorder JSON keys and array elements
	reorderedJSON := `{
  "compatibility": {
    "models_pack_min_version": "1.0.0",
    "edge_min_version": "1.1.6"
  },
  "dependencies": [
    {
      "size_bytes": 4096,
      "sha256": "3456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef012",
      "relative_path": "wheelhouse/torch-2.4.0-cp311-none-macosx_11_0_arm64.whl",
      "filename": "torch-2.4.0-cp311-none-macosx_11_0_arm64.whl",
      "version": "2.4.0",
      "name": "torch"
    }
  ],
  "binaries": [
    {
      "size_bytes": 2048,
      "sha256": "23456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef01",
      "version": "7.1.0",
      "relative_path": "bin/ffmpeg",
      "name": "ffmpeg"
    }
  ],
  "worker": {
    "size_bytes": 512,
    "sha256": "123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef0",
    "entrypoint": "worker/worker.py",
    "version": "1.1.0"
  },
  "python": {
    "size_bytes": 1024,
    "sha256": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
    "relative_binary_path": "python/bin/python3",
    "abi": "cp311",
    "version": "3.11.9"
  },
  "architecture": "arm64",
  "platform": "darwin",
  "runtime_version": "1.0.0",
  "schema_version": "1.0.0"
}`

	m, err := ParseRuntimeManifest([]byte(reorderedJSON))
	if err != nil {
		t.Fatalf("ParseRuntimeManifest failed for reordered JSON: %v", err)
	}

	if err := m.ValidateSchema(); err != nil {
		t.Fatalf("ValidateSchema failed for reordered manifest: %v", err)
	}
	if err := m.ValidateCompatibility(PlatformDarwin, ArchArm64); err != nil {
		t.Fatalf("ValidateCompatibility failed: %v", err)
	}
	if m.Python.Version != "3.11.9" || m.Worker.Version != "1.1.0" {
		t.Errorf("fields not parsed correctly: %+v", m)
	}
}

// Test progress callback and full physical verification success
func TestRuntimeManifest_ValidateArtifactsWithProgress_Success(t *testing.T) {
	tmpDir := t.TempDir()
	m := setupPhysicalFiles(t, tmpDir)

	var progressCalls []string
	report, err := m.ValidateArtifactsWithProgress(tmpDir, func(current, total int, path string) {
		progressCalls = append(progressCalls, fmt.Sprintf("%d/%d: %s", current, total, path))
	})

	if err != nil {
		t.Fatalf("ValidateArtifactsWithProgress failed: %v", err)
	}

	if report.TotalArtifacts != 4 {
		t.Errorf("got TotalArtifacts %d, want 4", report.TotalArtifacts)
	}
	if report.ValidArtifacts != 4 {
		t.Errorf("got ValidArtifacts %d, want 4", report.ValidArtifacts)
	}
	if len(progressCalls) != 4 {
		t.Errorf("expected 4 progress notifications, got %d", len(progressCalls))
	}
}

func TestRuntimeManifest_LoadRuntimeManifest(t *testing.T) {
	tmpDir := t.TempDir()
	m := createValidSyntheticManifest()
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}

	validPath := filepath.Join(tmpDir, "runtime-manifest.json")
	if err := os.WriteFile(validPath, data, 0644); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadRuntimeManifest(validPath)
	if err != nil {
		t.Fatalf("LoadRuntimeManifest failed: %v", err)
	}
	if loaded.RuntimeVersion != m.RuntimeVersion {
		t.Errorf("got %s, want %s", loaded.RuntimeVersion, m.RuntimeVersion)
	}

	// Non-existent path
	_, err = LoadRuntimeManifest(filepath.Join(tmpDir, "missing.json"))
	if err == nil {
		t.Fatal("expected error for non-existent file, got nil")
	}
	if !errors.Is(err, ErrManifestNotFound) {
		var safeErr *SafeError
		if !errors.As(err, &safeErr) || safeErr.Code != "MANIFEST_NOT_FOUND" {
			t.Errorf("expected MANIFEST_NOT_FOUND, got: %v", err)
		}
	}
}
