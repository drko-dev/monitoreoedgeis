package ota

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeTempFile(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func TestVerifyManifestSignature_Valid(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	manifest := []byte("abc123  artifact.tar.gz\n")
	sig := SignManifest(priv, manifest)

	if err := VerifyManifestSignature(manifest, sig, pub); err != nil {
		t.Fatalf("expected valid signature to pass, got: %v", err)
	}
}

func TestVerifyManifestSignature_Invalid(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	manifest := []byte("abc123  artifact.tar.gz\n")
	sig := SignManifest(priv, manifest)

	// A different key must not validate this signature.
	otherPub, _, _ := ed25519.GenerateKey(nil)
	if err := VerifyManifestSignature(manifest, sig, otherPub); err == nil {
		t.Fatal("expected signature verification to fail against the wrong key")
	}
}

func TestVerifyManifestSignature_ManifestModified(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	manifest := []byte("abc123  artifact.tar.gz\n")
	sig := SignManifest(priv, manifest)

	tampered := []byte("deadbeef  artifact.tar.gz\n")
	if err := VerifyManifestSignature(tampered, sig, pub); err == nil {
		t.Fatal("expected a modified manifest to fail signature verification")
	}
}

func TestVerifyManifestSignature_MissingSignature(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(nil)
	manifest := []byte("abc123  artifact.tar.gz\n")

	if err := VerifyManifestSignature(manifest, nil, pub); err == nil {
		t.Fatal("expected missing signature to be rejected")
	}
	if err := VerifyManifestSignature(manifest, []byte{}, nil); err == nil {
		t.Fatal("expected missing public key to be rejected (fail closed)")
	}
}

func TestVerifyArtifactChecksum(t *testing.T) {
	dir := t.TempDir()
	content := []byte("fake appliance tarball bytes")
	artifactPath := writeTempFile(t, dir, "geocam-edge-v1.0.0-linux-amd64.tar.gz", content)

	sum := sha256.Sum256(content)
	manifest := []byte(hex.EncodeToString(sum[:]) + "  geocam-edge-v1.0.0-linux-amd64.tar.gz\n")

	if err := VerifyArtifactChecksum(artifactPath, manifest, "geocam-edge-v1.0.0-linux-amd64.tar.gz"); err != nil {
		t.Fatalf("expected matching checksum to pass, got: %v", err)
	}
}

func TestVerifyArtifactChecksum_ArtifactModified(t *testing.T) {
	dir := t.TempDir()
	original := []byte("original bytes")
	artifactPath := writeTempFile(t, dir, "artifact.tar.gz", original)

	sum := sha256.Sum256(original)
	manifest := []byte(hex.EncodeToString(sum[:]) + "  artifact.tar.gz\n")

	// Tamper with the artifact after the manifest was computed.
	if err := os.WriteFile(artifactPath, []byte("tampered bytes"), 0o644); err != nil {
		t.Fatalf("tamper artifact: %v", err)
	}

	if err := VerifyArtifactChecksum(artifactPath, manifest, "artifact.tar.gz"); err == nil {
		t.Fatal("expected a modified artifact to fail checksum verification")
	}
}

func TestVerifyArchiveBinding_WrongArchitecture(t *testing.T) {
	dir := t.TempDir()
	artifactPath := filepath.Join(dir, "artifact.tar.gz")
	buildApplianceTarball(t, artifactPath, map[string]string{
		"geocam-edge": "binary",
		"VERSION":     "v1.0.0",
		"ARCH":        "arm64",
	})

	if err := VerifyArchiveBinding(artifactPath, "v1.0.0", "amd64"); err == nil {
		t.Fatal("expected architecture mismatch to be rejected")
	}
	if err := VerifyArchiveBinding(artifactPath, "v1.0.0", "arm64"); err != nil {
		t.Fatalf("expected matching version+architecture to pass, got: %v", err)
	}
}

func TestVerifyArchiveBinding_MissingEntries(t *testing.T) {
	dir := t.TempDir()
	artifactPath := filepath.Join(dir, "reduced.tar.gz")
	// The pre-Hito-T release.yml shape: binary only, no VERSION/ARCH/scripts.
	buildApplianceTarball(t, artifactPath, map[string]string{
		"geocam-edge": "binary",
	})

	if err := VerifyArchiveBinding(artifactPath, "v1.0.0", "amd64"); err == nil {
		t.Fatal("expected a reduced (binary-only) tarball to be rejected as not a full appliance package")
	}
}

// TestVerifyArchiveBinding_RejectsDivergedVersion is BLOCKER 4's core
// regression: the artifact's OWN embedded VERSION marker must exactly
// match the authenticated release descriptor's version -- a descriptor
// claiming v2.0.0 must not accept an artifact internally built as v0.9.0,
// even though nothing about the checksum/signature alone would catch that.
func TestVerifyArchiveBinding_RejectsDivergedVersion(t *testing.T) {
	dir := t.TempDir()
	artifactPath := filepath.Join(dir, "artifact.tar.gz")
	buildApplianceTarball(t, artifactPath, map[string]string{
		"geocam-edge": "binary", "VERSION": "v0.9.0", "ARCH": "amd64",
	})

	if err := VerifyArchiveBinding(artifactPath, "v2.0.0", "amd64"); err == nil {
		t.Fatal("expected a diverged artifact VERSION to be rejected")
	}
}

// TestVerifyReleaseFiles_RejectsDivergedVersionRegression is BLOCKER 4's
// exact scenario end to end: current v1.0.0, descriptor v2.0.0 (a valid
// forward update on its own), but the artifact was signed/built with
// VERSION v0.9.0 -> must still REJECT.
func TestVerifyReleaseFiles_RejectsDivergedVersionRegression(t *testing.T) {
	dir := t.TempDir()
	name := "geocam-edge-v2.0.0-linux-amd64.tar.gz"
	artifactPath := filepath.Join(dir, name)
	buildApplianceTarball(t, artifactPath, map[string]string{
		"geocam-edge": "binary", "VERSION": "v0.9.0", "ARCH": "amd64",
	})
	artifactBytes, err := os.ReadFile(artifactPath)
	if err != nil {
		t.Fatalf("read artifact: %v", err)
	}
	sum := sha256.Sum256(artifactBytes)
	manifest := []byte(hex.EncodeToString(sum[:]) + "  " + name + "\n")
	pub, priv, _ := ed25519.GenerateKey(nil)
	sig := SignManifest(priv, manifest)

	err = VerifyReleaseFiles(artifactPath, manifest, sig, pub, name, "v1.0.0", "v2.0.0", "amd64")
	if err == nil {
		t.Fatal("expected the descriptor/artifact VERSION divergence to be rejected end to end")
	}
}

// TestVerifyReleaseDir_MultiArchManifestSingleStagedArtifact is BLOCKER
// 3's regression: a real SHA256SUMS signs BOTH architectures' artifacts,
// but this Edge only ever downloads and stages its own. VerifyReleaseDir
// must succeed by looking up ONLY the staged artifact's entry (via
// metadata.json's artifact_name) -- it must never require every
// architecture in the manifest to be present locally.
func TestVerifyReleaseDir_MultiArchManifestSingleStagedArtifact(t *testing.T) {
	dir := t.TempDir()
	amd64Name := "geocam-edge-v2.0.0-linux-amd64.tar.gz"
	arm64Name := "geocam-edge-v2.0.0-linux-arm64.tar.gz"

	amd64Path := filepath.Join(dir, amd64Name)
	buildApplianceTarball(t, amd64Path, map[string]string{
		"geocam-edge": "b", "VERSION": "v2.0.0", "ARCH": "amd64",
	})
	amd64Bytes, err := os.ReadFile(amd64Path)
	if err != nil {
		t.Fatalf("read amd64 artifact: %v", err)
	}
	amd64Sum := sha256.Sum256(amd64Bytes)
	// arm64's bytes are never staged locally -- only its manifest line
	// exists, exactly like a real multi-arch release this Edge only
	// partially downloads.
	arm64Sum := sha256.Sum256([]byte("would-be arm64 artifact bytes, never downloaded by this edge"))

	manifest := []byte(
		hex.EncodeToString(amd64Sum[:]) + "  " + amd64Name + "\n" +
			hex.EncodeToString(arm64Sum[:]) + "  " + arm64Name + "\n",
	)
	pub, priv, _ := ed25519.GenerateKey(nil)
	sig := SignManifest(priv, manifest)

	if err := os.WriteFile(filepath.Join(dir, "SHA256SUMS"), manifest, 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SHA256SUMS.sig"), sig, 0o644); err != nil {
		t.Fatalf("write signature: %v", err)
	}
	meta := ReleaseMetadata{
		ReleaseID: "rel-multiarch", Version: "v2.0.0", Architecture: "amd64",
		ArtifactName: amd64Name, StagedAt: time.Now().UTC(),
	}
	if err := writeJSONAtomic(filepath.Join(dir, "metadata.json"), meta); err != nil {
		t.Fatalf("write metadata: %v", err)
	}

	if err := VerifyReleaseDir(dir, pub, "v1.0.0"); err != nil {
		t.Fatalf("VerifyReleaseDir: %v", err)
	}
}

func TestLoadPublicKey_Missing(t *testing.T) {
	if _, err := LoadPublicKey(""); err == nil {
		t.Fatal("expected empty public key path to be rejected (fail closed)")
	}
	if _, err := LoadPublicKey(filepath.Join(t.TempDir(), "does-not-exist")); err == nil {
		t.Fatal("expected an unreadable public key file to be rejected")
	}
}

func TestLoadPublicKeyPrivateKey_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	pub, priv, _ := ed25519.GenerateKey(nil)

	pubPath := writeTempFile(t, dir, "ota.pub", []byte(hex.EncodeToString(pub)))
	privPath := writeTempFile(t, dir, "ota.key", []byte(hex.EncodeToString(priv)))

	loadedPub, err := LoadPublicKey(pubPath)
	if err != nil {
		t.Fatalf("LoadPublicKey: %v", err)
	}
	loadedPriv, err := LoadPrivateKey(privPath)
	if err != nil {
		t.Fatalf("LoadPrivateKey: %v", err)
	}

	manifest := []byte("round trip manifest")
	sig := SignManifest(loadedPriv, manifest)
	if err := VerifyManifestSignature(manifest, sig, loadedPub); err != nil {
		t.Fatalf("round-tripped keys failed to verify: %v", err)
	}
}
