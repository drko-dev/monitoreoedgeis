package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/installer"
)

func TestResolveApp_DefaultReturnsFailClosed(t *testing.T) {
	app := resolveApp()
	if app == nil {
		t.Fatal("expected non-nil app")
	}
	snapshot, err := app.GetFullEdgePreparationStatus()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if snapshot.ErrorCode != "PREPARATION_NOT_INTEGRATED" {
		t.Fatalf("expected PREPARATION_NOT_INTEGRATED on unconfigured app, got %q", snapshot.ErrorCode)
	}
	if snapshot.CertifiedReady() || snapshot.CanStart {
		t.Fatalf("unconfigured app must remain unavailable: %+v", snapshot)
	}
}

func TestResolveApp_IncompleteOptInFailsClosed(t *testing.T) {
	t.Setenv("GEOCAM_OFFLINE_PROVISIONING", "1")
	t.Setenv("GEOCAM_RUNTIME_PACK_DIR", t.TempDir())
	// Missing pin, signed manifest, and trust store
	app := resolveApp()
	if app == nil {
		t.Fatal("expected non-nil app")
	}
	snapshot, err := app.GetFullEdgePreparationStatus()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if snapshot.ErrorCode != "PREPARATION_NOT_INTEGRATED" {
		t.Fatalf("expected PREPARATION_NOT_INTEGRATED on incomplete opt-in app, got %q", snapshot.ErrorCode)
	}
	if snapshot.CertifiedReady() || snapshot.CanStart {
		t.Fatalf("incomplete opt-in app must remain unavailable: %+v", snapshot)
	}
}

func TestResolveApp_CorruptSignedManifestFailsClosed(t *testing.T) {
	dir := t.TempDir()
	badSigned := filepath.Join(dir, "bad-signed.json")
	if err := os.WriteFile(badSigned, []byte("NOT JSON"), 0644); err != nil {
		t.Fatal(err)
	}
	trustFile := filepath.Join(dir, "trust.json")
	if err := os.WriteFile(trustFile, []byte("[]"), 0644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("GEOCAM_OFFLINE_PROVISIONING", "1")
	t.Setenv("GEOCAM_RUNTIME_PACK_DIR", dir)
	t.Setenv("GEOCAM_RUNTIME_SIGNED_MANIFEST", badSigned)
	t.Setenv("GEOCAM_RUNTIME_TRUST_ANCHOR", trustFile)
	t.Setenv("GEOCAM_RUNTIME_MANIFEST_SHA256", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")

	app := resolveApp()
	if app == nil {
		t.Fatal("expected non-nil app")
	}
	snapshot, err := app.GetFullEdgePreparationStatus()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if snapshot.ErrorCode != "PREPARATION_NOT_INTEGRATED" {
		t.Fatalf("expected PREPARATION_NOT_INTEGRATED on corrupt manifest app, got %q", snapshot.ErrorCode)
	}
	if snapshot.CertifiedReady() || snapshot.CanStart {
		t.Fatalf("app with corrupt manifest must fail closed: %+v", snapshot)
	}
}

func TestResolveApp_ValidSignedManifestInitializesBackend(t *testing.T) {
	packDir := t.TempDir()
	dataDir := t.TempDir()

	// Write synthetic manifest on disk
	manifest := installer.RuntimeManifest{
		SchemaVersion:  installer.CurrentSchemaVersion,
		RuntimeVersion: "1.0.0",
		ManifestType:   installer.ManifestTypeRelease,
		Platform:       installer.PlatformDarwin,
		Architecture:   installer.ArchArm64,
		Python: installer.PythonSpec{
			Version:            "3.11.9",
			ABI:                "cp311",
			RelativeBinaryPath: "python/bin/python3",
			SHA256:             "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			SizeBytes:          100,
		},
		Worker: installer.WorkerSpec{
			Version:    "1.0.0",
			Entrypoint: "worker/worker.py",
			SHA256:     "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			SizeBytes:  100,
		},
		Binaries: []installer.BinarySpec{
			{Name: "ffmpeg", RelativePath: "bin/ffmpeg", Version: "8.1.3", SHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", SizeBytes: 100},
			{Name: "ffprobe", RelativePath: "bin/ffprobe", Version: "8.1.3", SHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", SizeBytes: 100},
		},
		Dependencies: []installer.DependencySpec{
			{Name: "certifi", Version: "2026.7.22", Filename: "certifi.whl", RelativePath: "wheelhouse/certifi.whl", SHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", SizeBytes: 100},
		},
		Compatibility: installer.CompatibilitySpec{
			EdgeMinVersion:       "1.0.0",
			ModelsPackMinVersion: "1.0.0",
		},
	}

	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(packDir, "runtime-manifest.json")
	if err := os.WriteFile(manifestPath, manifestBytes, 0644); err != nil {
		t.Fatal(err)
	}

	hasher := sha256.New()
	hasher.Write(manifestBytes)
	pin := hex.EncodeToString(hasher.Sum(nil))

	// Sign manifest
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyID := "test-release-key"
	expires := time.Now().Add(24 * time.Hour)
	signed, err := installer.SignManifestEd25519(&manifest, keyID, priv, installer.SignOptions{
		ExpiresAt: &expires,
	})
	if err != nil {
		t.Fatal(err)
	}

	signedPath := filepath.Join(packDir, "runtime-manifest.signed.json")
	signedBytes, err := json.Marshal(signed)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(signedPath, signedBytes, 0644); err != nil {
		t.Fatal(err)
	}

	// Trust anchor file
	anchor := installer.NewEd25519TrustAnchor(keyID, pub)
	trustBytes, err := json.Marshal([]*installer.TrustAnchor{anchor})
	if err != nil {
		t.Fatal(err)
	}
	trustPath := filepath.Join(packDir, "trust-anchors.json")
	if err := os.WriteFile(trustPath, trustBytes, 0644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("GEOCAM_OFFLINE_PROVISIONING", "1")
	t.Setenv("GEOCAM_RUNTIME_PACK_DIR", packDir)
	t.Setenv("GEOCAM_RUNTIME_MANIFEST_SHA256", pin)
	t.Setenv("GEOCAM_RUNTIME_SIGNED_MANIFEST", signedPath)
	t.Setenv("GEOCAM_RUNTIME_TRUST_ANCHOR", trustPath)
	t.Setenv("GEOCAM_DATA_DIR", dataDir)

	app := resolveApp()
	if app == nil {
		t.Fatal("expected non-nil app")
	}

	// The app must have the signed preparation backend wired, not UnavailableFullEdgePreparation
	snapshot, err := app.GetFullEdgePreparationStatus()
	if err != nil {
		t.Fatalf("unexpected error getting status: %v", err)
	}
	// It should NOT report PREPARATION_NOT_INTEGRATED anymore!
	if snapshot.ErrorCode == "PREPARATION_NOT_INTEGRATED" {
		t.Fatalf("expected signed preparation backend, got PREPARATION_NOT_INTEGRATED")
	}
	// And it must never claim CertifiedReady without real execution
	if snapshot.CertifiedReady() {
		t.Fatalf("synthetic pack must not report CertifiedReady")
	}
}

func TestResolveApp_PinnedPackOptIn(t *testing.T) {
	packDir := t.TempDir()
	dataDir := t.TempDir()
	pin := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	t.Setenv("GEOCAM_OFFLINE_PROVISIONING", "1")
	t.Setenv("GEOCAM_RUNTIME_PACK_PINNED", "1")
	t.Setenv("GEOCAM_RUNTIME_PACK_DIR", packDir)
	t.Setenv("GEOCAM_RUNTIME_MANIFEST_SHA256", pin)
	t.Setenv("GEOCAM_DATA_DIR", dataDir)

	app := resolveApp()
	if app == nil {
		t.Fatal("expected non-nil app")
	}
	snapshot, err := app.GetFullEdgePreparationStatus()
	if err != nil {
		t.Fatalf("unexpected error getting status: %v", err)
	}
	if snapshot.ErrorCode == "PREPARATION_NOT_INTEGRATED" {
		t.Fatalf("expected pinned preparation backend, got PREPARATION_NOT_INTEGRATED")
	}
}
