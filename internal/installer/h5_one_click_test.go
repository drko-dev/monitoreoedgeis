package installer

import (
	"context"
	"strings"
	"testing"
)

func TestH5ManifestFilesAreValidatedAndAllowListed(t *testing.T) {
	m := createValidSyntheticManifest()
	m.Files = []FileSpec{{RelativePath: "python/lib/python3.11/os.py", SHA256: strings.Repeat("a", 64), SizeBytes: 0}}
	if err := m.ValidateSchema(); err != nil {
		t.Fatalf("valid files entry rejected: %v", err)
	}
	found := false
	for _, a := range m.allArtifacts() {
		found = found || a.RelativePath == "python/lib/python3.11/os.py"
	}
	if !found {
		t.Fatal("files entry missing from artifact targets")
	}
	for name, f := range map[string]FileSpec{
		"duplicate": {RelativePath: m.Worker.Entrypoint, SHA256: strings.Repeat("a", 64)},
		"traversal": {RelativePath: "../etc/passwd", SHA256: strings.Repeat("a", 64)},
		"bad hash":  {RelativePath: "x.py", SHA256: "nothex"},
		"negative":  {RelativePath: "x.py", SHA256: strings.Repeat("a", 64), SizeBytes: -1},
	} {
		bad := createValidSyntheticManifest()
		bad.Files = []FileSpec{f}
		if err := bad.ValidateSchema(); err == nil {
			t.Errorf("%s files entry accepted", name)
		}
	}
}

func TestH5FirstInstallRecoverIsStartable(t *testing.T) {
	p, err := NewTransactionalRuntimePackProvisioner(RuntimePackProvisionerOptions{
		Enabled: true, PackDir: t.TempDir(), DataDir: t.TempDir(),
		ExpectedManifestSHA256: strings.Repeat("0", 64), TargetOS: "darwin", TargetArch: "arm64",
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := p.Recover(context.Background())
	if err != nil || result.State != "NOT_PREPARED" || result.RuntimeReady || result.InferenceReady {
		t.Fatalf("first install not startable: %+v %v", result, err)
	}
	s := mapPackResult(result, err)
	if s.State != StateNotPrepared || !s.CanStart || s.CertifiedReady() {
		t.Fatalf("bad first-install snapshot: %+v", s)
	}
}

func TestH5ModelPathHeuristic(t *testing.T) {
	for path, want := range map[string]bool{
		"models/yolo11n.pt": true,
		"weights/x.onnx":    true,
		"python/lib/python3.11/site-packages/torch/hub/model.pth":            true,
		"python/lib/python3.11/site-packages/distutils-precedence.pth":       false,
		"python/lib/python3.11/site-packages/ultralytics/models/yolo/val.py": false,
	} {
		if got := isModelPath(path); got != want {
			t.Errorf("isModelPath(%q) = %v, want %v", path, got, want)
		}
	}
}
