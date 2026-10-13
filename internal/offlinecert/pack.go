package offlinecert

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/drko-dev/monitoreoedgeis/internal/installer"
)

const ManifestFilename = "runtime-manifest.json"

// SyntheticPack contains only tiny dummy payloads. The generated Python,
// worker, binary and wheel artifacts are not executable or installable.
type SyntheticPack struct {
	Root         string
	ManifestPath string
	Manifest     *installer.RuntimeManifest
}

func NewSyntheticPack(parent, platform, architecture string) (*SyntheticPack, error) {
	root, err := os.MkdirTemp(parent, "geocam-runtime-pack-")
	if err != nil {
		return nil, err
	}
	files := map[string][]byte{
		"python/bin/python3":               []byte("synthetic-python-not-executable\n"),
		"worker/worker.py":                 []byte("# synthetic worker fixture\n"),
		"bin/ffmpeg":                       []byte("synthetic-ffmpeg-not-executable\n"),
		"wheelhouse/example-1.0-cp311.whl": []byte("synthetic-wheel-not-installable\n"),
	}
	for rel, contents := range files {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			_ = os.RemoveAll(root)
			return nil, err
		}
		if err := os.WriteFile(path, contents, 0600); err != nil {
			_ = os.RemoveAll(root)
			return nil, err
		}
	}

	manifest := &installer.RuntimeManifest{
		SchemaVersion:  installer.CurrentSchemaVersion,
		RuntimeVersion: "0.0.0-synthetic",
		ManifestType:   installer.ManifestTypeRelease,
		Platform:       platform,
		Architecture:   architecture,
		Python: installer.PythonSpec{
			Version: "3.11.9", ABI: "cp311", RelativeBinaryPath: "python/bin/python3",
		},
		Worker: installer.WorkerSpec{Version: "0.0.0-synthetic", Entrypoint: "worker/worker.py"},
		Binaries: []installer.BinarySpec{{
			Name: "ffmpeg", RelativePath: "bin/ffmpeg", Version: "0.0.0-synthetic",
		}},
		Dependencies: []installer.DependencySpec{{
			Name: "example", Version: "1.0", Filename: "example-1.0-cp311.whl",
			RelativePath: "wheelhouse/example-1.0-cp311.whl",
		}},
		Compatibility: installer.CompatibilitySpec{
			EdgeMinVersion: "0.0.0", ModelsPackMinVersion: "0.0.0",
		},
	}
	for _, artifact := range []struct {
		path string
		set  func(string, int64)
	}{
		{"python/bin/python3", func(sum string, size int64) { manifest.Python.SHA256, manifest.Python.SizeBytes = sum, size }},
		{"worker/worker.py", func(sum string, size int64) { manifest.Worker.SHA256, manifest.Worker.SizeBytes = sum, size }},
		{"bin/ffmpeg", func(sum string, size int64) { manifest.Binaries[0].SHA256, manifest.Binaries[0].SizeBytes = sum, size }},
		{"wheelhouse/example-1.0-cp311.whl", func(sum string, size int64) {
			manifest.Dependencies[0].SHA256, manifest.Dependencies[0].SizeBytes = sum, size
		}},
	} {
		contents := files[artifact.path]
		digest := sha256.Sum256(contents)
		artifact.set(hex.EncodeToString(digest[:]), int64(len(contents)))
	}

	pack := &SyntheticPack{Root: root, ManifestPath: filepath.Join(root, ManifestFilename), Manifest: manifest}
	if err := pack.WriteManifest(); err != nil {
		_ = os.RemoveAll(root)
		return nil, err
	}
	return pack, nil
}

func (p *SyntheticPack) Close() error {
	if p == nil || p.Root == "" {
		return nil
	}
	return os.RemoveAll(p.Root)
}

func (p *SyntheticPack) WriteManifest() error {
	if p == nil || p.Manifest == nil {
		return fmt.Errorf("synthetic pack manifest is nil")
	}
	data, err := json.MarshalIndent(p.Manifest, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p.ManifestPath, append(data, '\n'), 0600)
}

// Validate runs the existing manifest parser, schema validator, platform check,
// and artifact hashes without invoking Python, pip, or the provisioning path.
func (p *SyntheticPack) Validate(targetOS, targetArchitecture string) (*installer.ArtifactsValidationReport, error) {
	if p == nil {
		return nil, fmt.Errorf("synthetic pack is nil")
	}
	manifest, err := installer.LoadRuntimeManifest(p.ManifestPath)
	if err != nil {
		return nil, err
	}
	if err := manifest.ValidateCompatibility(targetOS, targetArchitecture); err != nil {
		return nil, err
	}
	return manifest.ValidateArtifacts(p.Root)
}
