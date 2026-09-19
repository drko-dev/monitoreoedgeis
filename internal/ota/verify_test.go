package ota

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
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

func TestVerifyArchiveLayout_WrongArchitecture(t *testing.T) {
	dir := t.TempDir()
	artifactPath := filepath.Join(dir, "artifact.tar.gz")
	buildApplianceTarball(t, artifactPath, map[string]string{
		"geocam-edge": "binary",
		"VERSION":     "v1.0.0",
		"ARCH":        "arm64",
	})

	if err := VerifyArchiveLayout(artifactPath, "amd64"); err == nil {
		t.Fatal("expected architecture mismatch to be rejected")
	}
	if err := VerifyArchiveLayout(artifactPath, "arm64"); err != nil {
		t.Fatalf("expected matching architecture to pass, got: %v", err)
	}
}

func TestVerifyArchiveLayout_MissingEntries(t *testing.T) {
	dir := t.TempDir()
	artifactPath := filepath.Join(dir, "reduced.tar.gz")
	// The pre-Hito-T release.yml shape: binary only, no VERSION/ARCH/scripts.
	buildApplianceTarball(t, artifactPath, map[string]string{
		"geocam-edge": "binary",
	})

	if err := VerifyArchiveLayout(artifactPath, "amd64"); err == nil {
		t.Fatal("expected a reduced (binary-only) tarball to be rejected as not a full appliance package")
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
