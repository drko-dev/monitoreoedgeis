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

	signedPath := filepath.Join(t.TempDir(), "runtime-manifest.signed.json")
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
	trustPath := filepath.Join(t.TempDir(), "trust-anchors.json")
	if err := os.WriteFile(trustPath, trustBytes, 0644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("GEOCAM_OFFLINE_PROVISIONING", "1")
	t.Setenv("GEOCAM_RUNTIME_PACK_DIR", packDir)
	t.Setenv("GEOCAM_RUNTIME_MANIFEST_SHA256", pin)
	t.Setenv("GEOCAM_RUNTIME_SIGNED_MANIFEST", signedPath)
	t.Setenv("GEOCAM_RUNTIME_TRUST_ANCHOR", trustPath)
	t.Setenv("GEOCAM_DATA_DIR", dataDir)

	publicKeyDigest := sha256.Sum256(pub)
	app := resolveAppWithReleaseTrust(releaseRuntimeTrust{
		manifestSHA256:  pin,
		keyID:           keyID,
		publicKeySHA256: hex.EncodeToString(publicKeyDigest[:]),
	})
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
	untrustedApp := resolveApp()
	untrusted, err := untrustedApp.GetFullEdgePreparationStatus()
	if err != nil || untrusted.ErrorCode != "PREPARATION_NOT_INTEGRATED" {
		t.Fatalf("env-only self-signed pack was unexpectedly authorized: %+v %v", untrusted, err)
	}
	wrongRoot := resolveAppWithReleaseTrust(releaseRuntimeTrust{
		manifestSHA256:  pin,
		keyID:           keyID,
		publicKeySHA256: "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
	})
	wrongSnapshot, err := wrongRoot.GetFullEdgePreparationStatus()
	if err != nil || wrongSnapshot.ErrorCode != "PREPARATION_NOT_INTEGRATED" {
		t.Fatalf("mismatched publisher key was unexpectedly trusted: %+v %v", wrongSnapshot, err)
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
	if snapshot.ErrorCode != "PREPARATION_NOT_INTEGRATED" {
		t.Fatalf("environment-only digest must not enable unsigned preparation: %+v", snapshot)
	}
}

func TestResolveApp_EnvOnlySelfSignedReleaseRemainsDisabled(t *testing.T) {
	t.Setenv("GEOCAM_OFFLINE_PROVISIONING", "1")
	t.Setenv("GEOCAM_RUNTIME_PACK_DIR", t.TempDir())
	t.Setenv("GEOCAM_RUNTIME_MANIFEST_SHA256", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	t.Setenv("GEOCAM_RUNTIME_PACK_PINNED", "1")
	t.Setenv("GEOCAM_RUNTIME_SIGNED_MANIFEST", filepath.Join(t.TempDir(), "signed.json"))
	t.Setenv("GEOCAM_RUNTIME_TRUST_ANCHOR", filepath.Join(t.TempDir(), "trust.json"))
	snapshot, err := resolveApp().GetFullEdgePreparationStatus()
	if err != nil || snapshot.ErrorCode != "PREPARATION_NOT_INTEGRATED" || snapshot.CanStart {
		t.Fatalf("untrusted environment activated preparation: %+v, %v", snapshot, err)
	}
}

func TestResolveApp_RejectsBuildKeyPinMismatch(t *testing.T) {
	t.Setenv("GEOCAM_OFFLINE_PROVISIONING", "1")
	t.Setenv("GEOCAM_RUNTIME_PACK_DIR", t.TempDir())
	t.Setenv("GEOCAM_RUNTIME_MANIFEST_SHA256", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	snapshot, err := resolveAppWithReleaseTrust(releaseRuntimeTrust{
		manifestSHA256:  "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		keyID:           "publisher",
		publicKeySHA256: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
	}).GetFullEdgePreparationStatus()
	if err != nil || snapshot.ErrorCode != "PREPARATION_NOT_INTEGRATED" || snapshot.CanStart {
		t.Fatalf("environment overwrote build-time release pin: %+v, %v", snapshot, err)
	}
}

func TestResolveApp_TrustStoreInsidePackRejected(t *testing.T) {
	root := t.TempDir()
	pack := filepath.Join(root, "pack")
	if err := os.Mkdir(pack, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GEOCAM_OFFLINE_PROVISIONING", "1")
	t.Setenv("GEOCAM_RUNTIME_PACK_DIR", pack)
	t.Setenv("GEOCAM_RUNTIME_TRUST_ANCHOR", filepath.Join(pack, "trust-anchors.json"))
	t.Setenv("GEOCAM_RUNTIME_SIGNED_MANIFEST", filepath.Join(pack, "signed.json"))
	snapshot, err := resolveAppWithReleaseTrust(releaseRuntimeTrust{
		manifestSHA256:  "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		keyID:           "publisher",
		publicKeySHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	}).GetFullEdgePreparationStatus()
	if err != nil || snapshot.ErrorCode != "PREPARATION_NOT_INTEGRATED" || snapshot.CanStart {
		t.Fatalf("embedded pack trust anchor accepted: %+v, %v", snapshot, err)
	}
}
