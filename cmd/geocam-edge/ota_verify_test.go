package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// buildOTATestTarball writes a minimal valid appliance .tar.gz at path.
func buildOTATestTarball(t *testing.T, path, version, arch string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()
	files := map[string]string{"geocam-edge": "fake binary", "VERSION": version, "ARCH": arch}
	for name, content := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(content))}); err != nil {
			t.Fatalf("tar header %s: %v", name, err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("tar write %s: %v", name, err)
		}
	}
}

// otaReleaseMetadata mirrors internal/ota.ReleaseMetadata's JSON shape
// without importing an internal package from this external test binary's
// module path -- field tags must stay in sync with internal/ota.ReleaseMetadata.
type otaReleaseMetadata struct {
	ReleaseID    string    `json:"release_id"`
	Version      string    `json:"version"`
	Architecture string    `json:"architecture"`
	ArtifactName string    `json:"artifact_name"`
	StagedAt     time.Time `json:"staged_at"`
}

// buildOTAReleaseDir builds a complete, validly-signed staged release
// directory (metadata.json, SHA256SUMS, SHA256SUMS.sig, artifact) and
// returns its path plus the hex-encoded Ed25519 public key that verifies
// it -- the exact shape `geocam-edge ota verify --artifact-dir` and
// FetchAndStage both consume.
func buildOTAReleaseDir(t *testing.T, artifactName, version, arch string) (dir, pubKeyHex string) {
	t.Helper()
	dir = t.TempDir()
	artifactPath := filepath.Join(dir, artifactName)
	buildOTATestTarball(t, artifactPath, version, arch)

	artifactBytes, err := os.ReadFile(artifactPath)
	if err != nil {
		t.Fatalf("read artifact: %v", err)
	}
	sum := sha256.Sum256(artifactBytes)
	manifest := []byte(hex.EncodeToString(sum[:]) + "  " + artifactName + "\n")

	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	sig := ed25519.Sign(priv, manifest)

	if err := os.WriteFile(filepath.Join(dir, "SHA256SUMS"), manifest, 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SHA256SUMS.sig"), sig, 0o644); err != nil {
		t.Fatalf("write signature: %v", err)
	}
	meta := otaReleaseMetadata{
		ReleaseID: "rel-1", Version: version, Architecture: arch,
		ArtifactName: artifactName, StagedAt: time.Now().UTC(),
	}
	metaBytes, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "metadata.json"), metaBytes, 0o644); err != nil {
		t.Fatalf("write metadata: %v", err)
	}

	pubKeyPath := filepath.Join(t.TempDir(), "ota-pubkey.hex")
	if err := os.WriteFile(pubKeyPath, []byte(hex.EncodeToString(pub)), 0o644); err != nil {
		t.Fatalf("write public key: %v", err)
	}
	return dir, pubKeyPath
}

// TestOTAVerifyArtifactDir_Success is BLOCKER 1's contract test 1: on full
// success, stdout contains exactly one line "artifact=<artifact_name>".
func TestOTAVerifyArtifactDir_Success(t *testing.T) {
	bin := buildTestBinary(t)
	artifactName := "geocam-edge-v2.0.0-linux-amd64.tar.gz"
	dir, pubKeyPath := buildOTAReleaseDir(t, artifactName, "v2.0.0", "amd64")

	out, err := exec.Command(bin, "ota", "verify", "--artifact-dir", dir, "-public-key", pubKeyPath, "-current-version", "v1.0.0").CombinedOutput()
	if err != nil {
		t.Fatalf("ota verify --artifact-dir failed: %v\n%s", err, out)
	}

	wantLine := "artifact=" + artifactName
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	var artifactLines []string
	for _, l := range lines {
		if strings.HasPrefix(l, "artifact=") {
			artifactLines = append(artifactLines, l)
		}
	}
	if len(artifactLines) != 1 {
		t.Fatalf("expected exactly one 'artifact=' line, got %d:\n%s", len(artifactLines), out)
	}
	if artifactLines[0] != wantLine {
		t.Errorf("artifact line = %q, want %q", artifactLines[0], wantLine)
	}
}

// TestOTAVerifyArtifactDir_InvalidSignatureNeverPrintsArtifact is BLOCKER
// 1's contract test 2: on failure, no "artifact=" line is ever printed,
// and the process exits non-zero.
func TestOTAVerifyArtifactDir_InvalidSignatureNeverPrintsArtifact(t *testing.T) {
	bin := buildTestBinary(t)
	artifactName := "geocam-edge-v2.0.0-linux-amd64.tar.gz"
	dir, _ := buildOTAReleaseDir(t, artifactName, "v2.0.0", "amd64")

	// A DIFFERENT public key than the one that actually signed this
	// release's SHA256SUMS -- the signature must fail to verify.
	otherPub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	wrongKeyPath := filepath.Join(t.TempDir(), "wrong.hex")
	if err := os.WriteFile(wrongKeyPath, []byte(hex.EncodeToString(otherPub)), 0o644); err != nil {
		t.Fatalf("write wrong key: %v", err)
	}

	cmd := exec.Command(bin, "ota", "verify", "--artifact-dir", dir, "-public-key", wrongKeyPath, "-current-version", "v1.0.0")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected non-zero exit for an invalid signature, got success:\n%s", out)
	}
	if strings.Contains(string(out), "artifact=") {
		t.Errorf("output leaked an 'artifact=' line on a failed verification:\n%s", out)
	}
}

// TestOTAVerifyArtifactDir_RejectsTraversalArtifactName is BLOCKER 1's
// contract test 3: a metadata.json whose artifact_name attempts a path
// traversal (or points at a symlink) must be rejected, never surfaced via
// "artifact=".
func TestOTAVerifyArtifactDir_RejectsTraversalArtifactName(t *testing.T) {
	bin := buildTestBinary(t)
	dir, pubKeyPath := buildOTAReleaseDir(t, "geocam-edge-v2.0.0-linux-amd64.tar.gz", "v2.0.0", "amd64")

	// Overwrite metadata.json with a malicious artifact_name.
	meta := otaReleaseMetadata{
		ReleaseID: "rel-1", Version: "v2.0.0", Architecture: "amd64",
		ArtifactName: "../outside.tar.gz", StagedAt: time.Now().UTC(),
	}
	metaBytes, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "metadata.json"), metaBytes, 0o644); err != nil {
		t.Fatalf("overwrite metadata: %v", err)
	}

	out, err := exec.Command(bin, "ota", "verify", "--artifact-dir", dir, "-public-key", pubKeyPath, "-current-version", "v1.0.0").CombinedOutput()
	if err == nil {
		t.Fatalf("expected a path-traversal artifact_name to be rejected, got success:\n%s", out)
	}
	if strings.Contains(string(out), "artifact=") {
		t.Errorf("output leaked an 'artifact=' line despite a rejected artifact_name:\n%s", out)
	}
}

// TestOTAVerifyArtifactDir_MultiArchManifestReturnsOnlyStagedArtifact is
// BLOCKER 1/3's multi-arch case: a SHA256SUMS signing both architectures'
// artifacts still verifies successfully against a directory that only
// stages one of them, and reports only that one.
func TestOTAVerifyArtifactDir_MultiArchManifestReturnsOnlyStagedArtifact(t *testing.T) {
	bin := buildTestBinary(t)
	dir := t.TempDir()

	amd64Name := "geocam-edge-v2.0.0-linux-amd64.tar.gz"
	arm64Name := "geocam-edge-v2.0.0-linux-arm64.tar.gz"
	amd64Path := filepath.Join(dir, amd64Name)
	buildOTATestTarball(t, amd64Path, "v2.0.0", "amd64")
	amd64Bytes, err := os.ReadFile(amd64Path)
	if err != nil {
		t.Fatalf("read amd64 artifact: %v", err)
	}
	amd64Sum := sha256.Sum256(amd64Bytes)
	arm64Sum := sha256.Sum256([]byte("arm64 bytes never staged locally"))

	manifest := []byte(
		hex.EncodeToString(amd64Sum[:]) + "  " + amd64Name + "\n" +
			hex.EncodeToString(arm64Sum[:]) + "  " + arm64Name + "\n",
	)
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	sig := ed25519.Sign(priv, manifest)
	if err := os.WriteFile(filepath.Join(dir, "SHA256SUMS"), manifest, 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SHA256SUMS.sig"), sig, 0o644); err != nil {
		t.Fatalf("write signature: %v", err)
	}
	meta := otaReleaseMetadata{
		ReleaseID: "rel-multiarch", Version: "v2.0.0", Architecture: "amd64",
		ArtifactName: amd64Name, StagedAt: time.Now().UTC(),
	}
	metaBytes, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "metadata.json"), metaBytes, 0o644); err != nil {
		t.Fatalf("write metadata: %v", err)
	}
	pubKeyPath := filepath.Join(t.TempDir(), "ota-pubkey.hex")
	if err := os.WriteFile(pubKeyPath, []byte(hex.EncodeToString(pub)), 0o644); err != nil {
		t.Fatalf("write public key: %v", err)
	}

	out, err := exec.Command(bin, "ota", "verify", "--artifact-dir", dir, "-public-key", pubKeyPath, "-current-version", "v1.0.0").CombinedOutput()
	if err != nil {
		t.Fatalf("ota verify --artifact-dir failed: %v\n%s", err, out)
	}
	wantLine := "artifact=" + amd64Name
	if !strings.Contains(string(out), wantLine) {
		t.Errorf("expected output to contain %q:\n%s", wantLine, out)
	}
	if strings.Contains(string(out), "artifact="+arm64Name) {
		t.Errorf("output referenced the never-staged arm64 artifact:\n%s", out)
	}
}
