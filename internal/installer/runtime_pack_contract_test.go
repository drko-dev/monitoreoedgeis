package installer

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drko-dev/monitoreoedgeis/internal/workerbootstrap"
)

func syntheticMediaPack(t *testing.T) (string, *RuntimeManifest) {
	t.Helper()
	root := t.TempDir()
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	root = canonicalRoot
	manifest := createValidSyntheticManifest()
	manifest.ManifestType = ManifestTypeRelease
	manifest.Dependencies = nil
	manifest.Binaries = []BinarySpec{
		{Name: "ffmpeg", RelativePath: "media/ffmpeg", Version: "7.1.0"},
		{Name: "ffprobe", RelativePath: "media/ffprobe", Version: "7.1.0"},
	}
	artifacts := []struct {
		path string
		data []byte
		mode os.FileMode
	}{
		{manifest.Python.RelativeBinaryPath, []byte("synthetic-python"), 0o755},
		{manifest.Worker.Entrypoint, []byte("synthetic-worker"), 0o644},
		{filepath.ToSlash(filepath.Join(filepath.Dir(manifest.Worker.Entrypoint), "backend.py")), []byte("synthetic-backend"), 0o644},
		{manifest.Binaries[0].RelativePath, []byte("synthetic-ffmpeg"), 0o755},
		{manifest.Binaries[1].RelativePath, []byte("synthetic-ffprobe"), 0o755},
	}
	for _, artifact := range artifacts {
		path := filepath.Join(root, artifact.path)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, artifact.data, artifact.mode); err != nil {
			t.Fatal(err)
		}
	}
	manifest.Python.SHA256 = sha256Hex(artifacts[0].data)
	manifest.Python.SizeBytes = int64(len(artifacts[0].data))
	manifest.Worker.SHA256 = sha256Hex(artifacts[1].data)
	manifest.Worker.SizeBytes = int64(len(artifacts[1].data))
	manifest.Worker.BackendRelativePath = artifacts[2].path
	manifest.Worker.BackendSHA256 = sha256Hex(artifacts[2].data)
	manifest.Worker.BackendSizeBytes = int64(len(artifacts[2].data))
	for i := range manifest.Binaries {
		manifest.Binaries[i].SHA256 = sha256Hex(artifacts[i+3].data)
		manifest.Binaries[i].SizeBytes = int64(len(artifacts[i+3].data))
	}
	return root, &manifest
}

func TestResolveRuntimePackMedia(t *testing.T) {
	t.Run("resolves only verified bundled media without host lookup", func(t *testing.T) {
		root, manifest := syntheticMediaPack(t)
		hostBin := t.TempDir()
		if err := os.WriteFile(filepath.Join(hostBin, "ffmpeg"), []byte("host ffmpeg"), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", hostBin)
		paths, err := ResolveRuntimePackMedia(manifest, root, PlatformDarwin, ArchArm64)
		if err != nil {
			t.Fatalf("resolve media: %v", err)
		}
		if paths.FFmpeg != filepath.Join(root, "media", "ffmpeg") || paths.FFprobe != filepath.Join(root, "media", "ffprobe") {
			t.Fatalf("unexpected paths: %+v", paths)
		}
	})

	tests := []struct {
		name   string
		mutate func(string, *RuntimeManifest)
		want   error
		osName string
		arch   string
	}{
		{name: "template manifest", mutate: func(_ string, m *RuntimeManifest) { m.ManifestType = ManifestTypeTemplate }, want: ErrRuntimePackNotRelease, osName: PlatformDarwin, arch: ArchArm64},
		{name: "wrong architecture", mutate: func(_ string, _ *RuntimeManifest) {}, want: ErrManifestIncompatible, osName: PlatformDarwin, arch: ArchAmd64},
		{name: "missing ffmpeg", mutate: func(root string, m *RuntimeManifest) { _ = os.Remove(filepath.Join(root, m.Binaries[0].RelativePath)) }, want: ErrArtifactMissing, osName: PlatformDarwin, arch: ArchArm64},
		{name: "missing ffprobe declaration", mutate: func(_ string, m *RuntimeManifest) { m.Binaries = m.Binaries[:1] }, want: ErrRuntimePackMediaMissing, osName: PlatformDarwin, arch: ArchArm64},
		{name: "incorrect hash", mutate: func(_ string, m *RuntimeManifest) { m.Binaries[0].SHA256 = strings.Repeat("0", 64) }, want: ErrArtifactCorrupted, osName: PlatformDarwin, arch: ArchArm64},
		{name: "incorrect size", mutate: func(_ string, m *RuntimeManifest) { m.Binaries[0].SizeBytes++ }, want: ErrArtifactCorrupted, osName: PlatformDarwin, arch: ArchArm64},
		{name: "not executable", mutate: func(root string, m *RuntimeManifest) {
			_ = os.Chmod(filepath.Join(root, m.Binaries[0].RelativePath), 0o644)
		}, want: ErrRuntimePackMediaNotExecutable, osName: PlatformDarwin, arch: ArchArm64},
		{name: "path traversal", mutate: func(_ string, m *RuntimeManifest) { m.Binaries[0].RelativePath = "../outside/ffmpeg" }, want: ErrArtifactSecurity, osName: PlatformDarwin, arch: ArchArm64},
		{name: "external symlink", mutate: func(root string, m *RuntimeManifest) {
			path := filepath.Join(root, m.Binaries[0].RelativePath)
			outside := filepath.Join(t.TempDir(), "ffmpeg")
			if err := os.WriteFile(outside, []byte("synthetic-ffmpeg"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, path); err != nil {
				t.Fatal(err)
			}
		}, want: ErrArtifactSecurity, osName: PlatformDarwin, arch: ArchArm64},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, manifest := syntheticMediaPack(t)
			tt.mutate(root, manifest)
			_, err := ResolveRuntimePackMedia(manifest, root, tt.osName, tt.arch)
			if !errors.Is(err, tt.want) {
				t.Fatalf("expected error %v, got %v", tt.want, err)
			}
		})
	}
}

func TestBuildRuntimePackWorkerLaunchSpec(t *testing.T) {
	root, manifest := syntheticMediaPack(t)
	// No host Python, Homebrew or other host tools are visible to this test.
	t.Setenv("PATH", t.TempDir())
	modelsDir := filepath.Join(t.TempDir(), "persistent", "models")
	stateDir := filepath.Join(t.TempDir(), "persistent", "worker")
	spec, err := BuildRuntimePackWorkerLaunchSpec(manifest, root, modelsDir, stateDir, PlatformDarwin, ArchArm64)
	if err != nil {
		t.Fatalf("build launch spec: %v", err)
	}
	if spec.Command != filepath.Join(root, manifest.Python.RelativeBinaryPath) {
		t.Fatalf("worker command is not bundled Python: %s", spec.Command)
	}
	if entry, ok := workerbootstrap.Entrypoint(spec.Args); !ok || entry != filepath.Join(root, manifest.Worker.Entrypoint) {
		t.Fatalf("unexpected isolated worker args: %#v", spec.Args)
	}
	for _, forbidden := range []string{"PYTHONPATH=", "PYTHONHOME=", "http_proxy=", "https_proxy="} {
		for _, variable := range spec.Environment {
			if strings.HasPrefix(strings.ToLower(variable), strings.ToLower(forbidden)) {
				t.Fatalf("environment contains forbidden inherited variable %q", variable)
			}
		}
	}
	if spec.StartTimeout <= 0 || spec.FFmpeg != filepath.Join(root, "media", "ffmpeg") || spec.FFprobe != filepath.Join(root, "media", "ffprobe") {
		t.Fatalf("incomplete launch contract: %+v", spec)
	}
}

func TestBuildRuntimePackWorkerLaunchSpecRequiresPersistentAbsoluteModelsPath(t *testing.T) {
	root, manifest := syntheticMediaPack(t)
	if _, err := BuildRuntimePackWorkerLaunchSpec(manifest, root, "models", "state", PlatformDarwin, ArchArm64); err == nil {
		t.Fatal("expected relative models directory to be rejected")
	}
}

func TestBuildRuntimePackWorkerLaunchSpecRejectsNonExecutablePython(t *testing.T) {
	root, manifest := syntheticMediaPack(t)
	if err := os.Chmod(filepath.Join(root, manifest.Python.RelativeBinaryPath), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := BuildRuntimePackWorkerLaunchSpec(manifest, root, filepath.Join(t.TempDir(), "models"), filepath.Join(t.TempDir(), "state"), PlatformDarwin, ArchArm64)
	if !errors.Is(err, ErrRuntimePackPythonNotExecutable) {
		t.Fatalf("expected non-executable Python rejection, got %v", err)
	}
}

func TestBuildRuntimePackWorkerLaunchSpecAdversarialIsolation(t *testing.T) {
	root, manifest := syntheticMediaPack(t)
	// Inject adversarial environment into the host test process
	t.Setenv("HTTP_PROXY", "http://proxy.attacker.internal:8080")
	t.Setenv("HTTPS_PROXY", "http://proxy.attacker.internal:8080")
	t.Setenv("ALL_PROXY", "socks5://proxy.attacker.internal:1080")
	t.Setenv("FTP_PROXY", "http://proxy.attacker.internal:8080")
	t.Setenv("NO_PROXY", "localhost,127.0.0.1")
	t.Setenv("http_proxy", "http://proxy.attacker.internal:8080")
	t.Setenv("https_proxy", "http://proxy.attacker.internal:8080")
	t.Setenv("all_proxy", "socks5://proxy.attacker.internal:1080")
	t.Setenv("CURL_CA_BUNDLE", "/etc/ssl/evil.crt")
	t.Setenv("SSL_CERT_FILE", "/etc/ssl/evil.crt")
	t.Setenv("RES_OPTIONS", "nameserver:8.8.8.8")
	t.Setenv("DYLD_INSERT_LIBRARIES", "/usr/local/lib/inject.dylib")
	t.Setenv("LD_PRELOAD", "/usr/local/lib/preload.so")

	modelsDir := filepath.Join(t.TempDir(), "models")
	stateDir := filepath.Join(t.TempDir(), "state")

	spec, err := BuildRuntimePackWorkerLaunchSpec(manifest, root, modelsDir, stateDir, PlatformDarwin, ArchArm64)
	if err != nil {
		t.Fatalf("BuildRuntimePackWorkerLaunchSpec failed: %v", err)
	}

	forbiddenPrefixes := []string{
		"http_proxy=", "https_proxy=", "all_proxy=", "ftp_proxy=", "no_proxy=",
		"curl_ca_bundle=", "ssl_cert_file=", "res_options=", "dyld_insert_libraries=", "ld_preload=",
		"pythonpath=", "pythonhome=",
	}

	for _, entry := range spec.Environment {
		lower := strings.ToLower(entry)
		for _, prefix := range forbiddenPrefixes {
			if strings.HasPrefix(lower, prefix) {
				t.Fatalf("forbidden variable leaked into hermetic worker environment: %s", entry)
			}
		}
	}
}
