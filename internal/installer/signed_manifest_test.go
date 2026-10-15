package installer

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// helper to create a valid synthetic release manifest and on-disk files
func setupSyntheticPackWithFiles(t *testing.T) (string, *RuntimeManifest) {
	t.Helper()
	packRoot := t.TempDir()

	pyRel := "python/bin/python3"
	pyPath := filepath.Join(packRoot, pyRel)
	if err := os.MkdirAll(filepath.Dir(pyPath), 0755); err != nil {
		t.Fatalf("failed to create python dir: %v", err)
	}
	pyContent := []byte("#!/usr/bin/env bash\necho 3.11.9\n")
	if err := os.WriteFile(pyPath, pyContent, 0755); err != nil {
		t.Fatalf("failed to write python binary: %v", err)
	}
	pyHash := sha256.Sum256(pyContent)

	workerRel := "worker/worker.py"
	workerPath := filepath.Join(packRoot, workerRel)
	if err := os.MkdirAll(filepath.Dir(workerPath), 0755); err != nil {
		t.Fatalf("failed to create worker dir: %v", err)
	}
	workerContent := []byte("print('worker ready')\n")
	if err := os.WriteFile(workerPath, workerContent, 0644); err != nil {
		t.Fatalf("failed to write worker entrypoint: %v", err)
	}
	workerHash := sha256.Sum256(workerContent)

	ffRel := "ffmpeg/ffmpeg"
	ffPath := filepath.Join(packRoot, ffRel)
	if err := os.MkdirAll(filepath.Dir(ffPath), 0755); err != nil {
		t.Fatalf("failed to create ffmpeg dir: %v", err)
	}
	ffContent := []byte("ffmpeg version 8.1.3 synthetic\n")
	if err := os.WriteFile(ffPath, ffContent, 0755); err != nil {
		t.Fatalf("failed to write ffmpeg binary: %v", err)
	}
	ffHash := sha256.Sum256(ffContent)

	whRel := "wheelhouse/certifi-2026.7.22-py3-none-any.whl"
	whPath := filepath.Join(packRoot, whRel)
	if err := os.MkdirAll(filepath.Dir(whPath), 0755); err != nil {
		t.Fatalf("failed to create wheelhouse dir: %v", err)
	}
	whContent := []byte("PK\x03\x04synthetic-wheel-content")
	if err := os.WriteFile(whPath, whContent, 0644); err != nil {
		t.Fatalf("failed to write wheel: %v", err)
	}
	whHash := sha256.Sum256(whContent)

	manifest := &RuntimeManifest{
		SchemaVersion:  CurrentSchemaVersion,
		RuntimeVersion: "1.0.0",
		ManifestType:   ManifestTypeRelease,
		Platform:       PlatformDarwin,
		Architecture:   ArchArm64,
		Python: PythonSpec{
			Version:            "3.11.9",
			ABI:                "cp311",
			RelativeBinaryPath: pyRel,
			SHA256:             hex.EncodeToString(pyHash[:]),
			SizeBytes:          int64(len(pyContent)),
		},
		Worker: WorkerSpec{
			Version:    "1.0.0",
			Entrypoint: workerRel,
			SHA256:     hex.EncodeToString(workerHash[:]),
			SizeBytes:  int64(len(workerContent)),
		},
		Binaries: []BinarySpec{
			{
				Name:         "ffmpeg",
				RelativePath: ffRel,
				Version:      "8.1.3",
				SHA256:       hex.EncodeToString(ffHash[:]),
				SizeBytes:    int64(len(ffContent)),
			},
		},
		Dependencies: []DependencySpec{
			{
				Name:         "certifi",
				Version:      "2026.7.22",
				Filename:     "certifi-2026.7.22-py3-none-any.whl",
				RelativePath: whRel,
				SHA256:       hex.EncodeToString(whHash[:]),
				SizeBytes:    int64(len(whContent)),
			},
		},
		Compatibility: CompatibilitySpec{
			EdgeMinVersion:       "1.0.0",
			ModelsPackMinVersion: "1.0.0",
		},
	}

	return packRoot, manifest
}

func TestSignedManifest_Scenario1_ValidEd25519Signature(t *testing.T) {
	packRoot, manifest := setupSyntheticPackWithFiles(t)

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate ed25519 key: %v", err)
	}

	keyID := "geocam-edge-root-2026-v1"
	store := NewMemoryTrustStore()
	anchor := NewEd25519TrustAnchor(keyID, pub)
	if err := store.AddAnchor(anchor); err != nil {
		t.Fatalf("failed to add anchor: %v", err)
	}

	now := time.Now().UTC()
	expires := now.Add(24 * time.Hour)
	notBefore := now.Add(-1 * time.Hour)

	signed, err := SignManifestEd25519(manifest, keyID, priv, SignOptions{
		IssuedAt:  now,
		ExpiresAt: &expires,
		NotBefore: &notBefore,
	})
	if err != nil {
		t.Fatalf("failed to sign manifest: %v", err)
	}

	opts := VerifySignedManifestOptions{
		Now:            now,
		TargetPlatform: PlatformDarwin,
		TargetArch:     ArchArm64,
		PackRoot:       packRoot,
		RequireRelease: true,
	}

	if err := ValidateSignedManifest(signed, store, opts); err != nil {
		t.Fatalf("expected valid signature and artifacts, got error: %v", err)
	}
}

func TestSignedManifest_Scenario2_ValidECDSAP256Signature(t *testing.T) {
	packRoot, manifest := setupSyntheticPackWithFiles(t)

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate ecdsa key: %v", err)
	}

	keyID := "geocam-edge-ecdsa-2026-v1"
	store := NewMemoryTrustStore()
	anchor, err := NewECDSAP256TrustAnchor(keyID, &priv.PublicKey)
	if err != nil {
		t.Fatalf("failed to create ecdsa anchor: %v", err)
	}
	if err := store.AddAnchor(anchor); err != nil {
		t.Fatalf("failed to add anchor: %v", err)
	}

	now := time.Now().UTC()
	signed, err := SignManifestECDSAP256(manifest, keyID, priv, SignOptions{
		IssuedAt: now,
	})
	if err != nil {
		t.Fatalf("failed to sign manifest: %v", err)
	}

	opts := VerifySignedManifestOptions{
		Now:            now,
		TargetPlatform: PlatformDarwin,
		TargetArch:     ArchArm64,
		PackRoot:       packRoot,
	}

	if err := ValidateSignedManifest(signed, store, opts); err != nil {
		t.Fatalf("expected valid ecdsa signature, got error: %v", err)
	}
}

func TestSignedManifest_Scenario3_CorruptedSignatureFails(t *testing.T) {
	packRoot, manifest := setupSyntheticPackWithFiles(t)

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}

	keyID := "geocam-edge-2026"
	store := NewMemoryTrustStore()
	_ = store.AddAnchor(NewEd25519TrustAnchor(keyID, pub))

	signed, err := SignManifestEd25519(manifest, keyID, priv, SignOptions{})
	if err != nil {
		t.Fatalf("sign failed: %v", err)
	}

	// Corrupt the signature by flipping bytes
	rawSig, err := base64.StdEncoding.DecodeString(signed.Signature.Signature)
	if err != nil {
		t.Fatalf("decode sig failed: %v", err)
	}
	rawSig[5] ^= 0xFF
	signed.Signature.Signature = base64.StdEncoding.EncodeToString(rawSig)

	err = ValidateSignedManifest(signed, store, VerifySignedManifestOptions{
		PackRoot: packRoot,
	})
	if !errors.Is(err, ErrSignatureInvalid) {
		t.Fatalf("expected ErrSignatureInvalid, got %v", err)
	}
}

func TestSignedManifest_Scenario4_WrongPublicKeyOrKeyID(t *testing.T) {
	packRoot, manifest := setupSyntheticPackWithFiles(t)

	_, privA, _ := ed25519.GenerateKey(rand.Reader)
	pubB, _, _ := ed25519.GenerateKey(rand.Reader)

	keyID := "geocam-edge-2026"
	store := NewMemoryTrustStore()
	// Anchor in store uses pubB
	_ = store.AddAnchor(NewEd25519TrustAnchor(keyID, pubB))

	// Signed with privA
	signed, err := SignManifestEd25519(manifest, keyID, privA, SignOptions{})
	if err != nil {
		t.Fatalf("sign failed: %v", err)
	}

	err = ValidateSignedManifest(signed, store, VerifySignedManifestOptions{
		PackRoot: packRoot,
	})
	// Should fail because signature was made with A but verified with B,
	// OR because embedded public key in manifest does not match trust anchor
	if !errors.Is(err, ErrSignatureInvalid) && !errors.Is(err, ErrPublicKeyMismatch) {
		t.Fatalf("expected ErrSignatureInvalid or ErrPublicKeyMismatch, got %v", err)
	}

	// Sub-case: Unknown KeyID not in trust store
	signed.Signature.KeyID = "unknown-untrusted-key-id"
	err = ValidateSignedManifest(signed, store, VerifySignedManifestOptions{
		PackRoot: packRoot,
	})
	if !errors.Is(err, ErrUntrustedKey) {
		t.Fatalf("expected ErrUntrustedKey, got %v", err)
	}
}

func TestSignedManifest_Scenario5_ExpiredManifestFails(t *testing.T) {
	packRoot, manifest := setupSyntheticPackWithFiles(t)

	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	keyID := "geocam-edge-key"
	store := NewMemoryTrustStore()
	_ = store.AddAnchor(NewEd25519TrustAnchor(keyID, pub))

	now := time.Now().UTC()
	past := now.Add(-2 * time.Hour)
	expiredAt := now.Add(-1 * time.Hour)

	signed, err := SignManifestEd25519(manifest, keyID, priv, SignOptions{
		IssuedAt:  past,
		ExpiresAt: &expiredAt,
	})
	if err != nil {
		t.Fatalf("sign failed: %v", err)
	}

	err = ValidateSignedManifest(signed, store, VerifySignedManifestOptions{
		Now:      now,
		PackRoot: packRoot,
	})
	if !errors.Is(err, ErrManifestExpired) {
		t.Fatalf("expected ErrManifestExpired, got %v", err)
	}
}

func TestSignedManifest_Scenario6_NotYetValidManifestFails(t *testing.T) {
	packRoot, manifest := setupSyntheticPackWithFiles(t)

	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	keyID := "geocam-edge-key"
	store := NewMemoryTrustStore()
	_ = store.AddAnchor(NewEd25519TrustAnchor(keyID, pub))

	now := time.Now().UTC()
	future := now.Add(2 * time.Hour)

	signed, err := SignManifestEd25519(manifest, keyID, priv, SignOptions{
		IssuedAt:  now,
		NotBefore: &future,
	})
	if err != nil {
		t.Fatalf("sign failed: %v", err)
	}

	err = ValidateSignedManifest(signed, store, VerifySignedManifestOptions{
		Now:      now,
		PackRoot: packRoot,
	})
	if !errors.Is(err, ErrManifestNotYetValid) {
		t.Fatalf("expected ErrManifestNotYetValid, got %v", err)
	}
}

func TestSignedManifest_Scenario7_RevokedKeyFails(t *testing.T) {
	packRoot, manifest := setupSyntheticPackWithFiles(t)

	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	keyID := "geocam-edge-revoked-key"
	store := NewMemoryTrustStore()
	anchor := NewEd25519TrustAnchor(keyID, pub)
	_ = store.AddAnchor(anchor)

	// Now revoke the key
	revokedAt := time.Now().UTC()
	if err := store.RevokeKey(keyID, revokedAt, "Compromised build server"); err != nil {
		t.Fatalf("failed to revoke key: %v", err)
	}

	signed, err := SignManifestEd25519(manifest, keyID, priv, SignOptions{})
	if err != nil {
		t.Fatalf("sign failed: %v", err)
	}

	err = ValidateSignedManifest(signed, store, VerifySignedManifestOptions{
		PackRoot: packRoot,
	})
	if !errors.Is(err, ErrKeyRevoked) {
		t.Fatalf("expected ErrKeyRevoked, got %v", err)
	}
	var se *SafeError
	if errors.As(err, &se) {
		if !strings.Contains(se.SafeMessage, "Compromised build server") {
			t.Errorf("expected revocation reason in message, got %s", se.SafeMessage)
		}
	}
}

func TestSignedManifest_Scenario8_TamperedDeclaredArtifactFails(t *testing.T) {
	packRoot, manifest := setupSyntheticPackWithFiles(t)

	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	keyID := "geocam-edge-2026"
	store := NewMemoryTrustStore()
	_ = store.AddAnchor(NewEd25519TrustAnchor(keyID, pub))

	signed, err := SignManifestEd25519(manifest, keyID, priv, SignOptions{})
	if err != nil {
		t.Fatalf("sign failed: %v", err)
	}

	// Signature verification alone should pass
	if err := ValidateSignedManifest(signed, store, VerifySignedManifestOptions{}); err != nil {
		t.Fatalf("manifest alone should be valid: %v", err)
	}

	// Tamper with the physical ffmpeg binary on disk
	ffPath := filepath.Join(packRoot, manifest.Binaries[0].RelativePath)
	if err := os.WriteFile(ffPath, []byte("MALICIOUS BINARY CONTENT HERE"), 0755); err != nil {
		t.Fatalf("failed to tamper file: %v", err)
	}

	// Now validate with PackRoot
	err = ValidateSignedManifest(signed, store, VerifySignedManifestOptions{
		PackRoot: packRoot,
	})
	if !errors.Is(err, ErrArtifactCorrupted) {
		t.Fatalf("expected ErrArtifactCorrupted due to tampered artifact, got %v", err)
	}
}

func TestSignedManifest_Scenario9_TamperedManifestPayloadFailsSignature(t *testing.T) {
	packRoot, manifest := setupSyntheticPackWithFiles(t)

	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	keyID := "geocam-edge-2026"
	store := NewMemoryTrustStore()
	_ = store.AddAnchor(NewEd25519TrustAnchor(keyID, pub))

	signed, err := SignManifestEd25519(manifest, keyID, priv, SignOptions{})
	if err != nil {
		t.Fatalf("sign failed: %v", err)
	}

	// Modify one hash inside the manifest payload after signing
	signed.Manifest.Dependencies[0].SHA256 = strings.Repeat("0", 64)

	err = ValidateSignedManifest(signed, store, VerifySignedManifestOptions{
		PackRoot: packRoot,
	})
	if !errors.Is(err, ErrSignatureInvalid) {
		t.Fatalf("expected ErrSignatureInvalid due to altered payload, got %v", err)
	}
}

func TestSignedManifest_Scenario10_MissingDeclaredArtifactFails(t *testing.T) {
	packRoot, manifest := setupSyntheticPackWithFiles(t)

	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	keyID := "geocam-edge-2026"
	store := NewMemoryTrustStore()
	_ = store.AddAnchor(NewEd25519TrustAnchor(keyID, pub))

	signed, err := SignManifestEd25519(manifest, keyID, priv, SignOptions{})
	if err != nil {
		t.Fatalf("sign failed: %v", err)
	}

	// Delete wheelhouse file
	whPath := filepath.Join(packRoot, manifest.Dependencies[0].RelativePath)
	_ = os.Remove(whPath)

	err = ValidateSignedManifest(signed, store, VerifySignedManifestOptions{
		PackRoot: packRoot,
	})
	if !errors.Is(err, ErrArtifactMissing) {
		t.Fatalf("expected ErrArtifactMissing, got %v", err)
	}
}

func TestSignedManifest_Scenario11_PlatformArchitectureMismatch(t *testing.T) {
	_, manifest := setupSyntheticPackWithFiles(t)

	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	keyID := "geocam-edge-2026"
	store := NewMemoryTrustStore()
	_ = store.AddAnchor(NewEd25519TrustAnchor(keyID, pub))

	signed, err := SignManifestEd25519(manifest, keyID, priv, SignOptions{})
	if err != nil {
		t.Fatalf("sign failed: %v", err)
	}

	// Verify with target platform linux
	err = ValidateSignedManifest(signed, store, VerifySignedManifestOptions{
		TargetPlatform: PlatformLinux,
		TargetArch:     ArchAmd64,
	})
	if !errors.Is(err, ErrManifestIncompatible) {
		t.Fatalf("expected ErrManifestIncompatible, got %v", err)
	}
}

func TestSignedManifest_Scenario12_RequireReleaseRejectsTemplate(t *testing.T) {
	_, manifest := setupSyntheticPackWithFiles(t)
	manifest.ManifestType = ManifestTypeTemplate

	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	keyID := "geocam-edge-2026"
	store := NewMemoryTrustStore()
	_ = store.AddAnchor(NewEd25519TrustAnchor(keyID, pub))

	signed, err := SignManifestEd25519(manifest, keyID, priv, SignOptions{})
	if err != nil {
		t.Fatalf("sign failed: %v", err)
	}

	err = ValidateSignedManifest(signed, store, VerifySignedManifestOptions{
		RequireRelease: true,
	})
	if !errors.Is(err, ErrManifestTypeNotRelease) {
		t.Fatalf("expected ErrManifestTypeNotRelease, got %v", err)
	}
}

func TestSignedManifest_Scenario13_FileSerializationRoundTrip(t *testing.T) {
	_, manifest := setupSyntheticPackWithFiles(t)

	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	keyID := "geocam-edge-save-load"
	store := NewMemoryTrustStore()
	_ = store.AddAnchor(NewEd25519TrustAnchor(keyID, pub))

	signed, err := SignManifestEd25519(manifest, keyID, priv, SignOptions{})
	if err != nil {
		t.Fatalf("sign failed: %v", err)
	}

	tmpFile := filepath.Join(t.TempDir(), "signed-runtime-manifest.json")
	if err := signed.SaveToFile(tmpFile); err != nil {
		t.Fatalf("failed to save signed manifest: %v", err)
	}

	loaded, err := LoadSignedRuntimeManifest(tmpFile)
	if err != nil {
		t.Fatalf("failed to load signed manifest: %v", err)
	}

	if err := ValidateSignedManifest(loaded, store, VerifySignedManifestOptions{}); err != nil {
		t.Fatalf("loaded manifest validation failed: %v", err)
	}

	// Test non-existent file
	_, err = LoadSignedRuntimeManifest(filepath.Join(t.TempDir(), "nonexistent.json"))
	if !errors.Is(err, ErrManifestNotFound) {
		t.Fatalf("expected ErrManifestNotFound, got %v", err)
	}
}

func TestSignedManifest_Scenario14_MalformedJSONAndSchemaErrors(t *testing.T) {
	_, err := ParseSignedRuntimeManifest([]byte(""))
	if !errors.Is(err, ErrSignedManifestSchemaInvalid) {
		t.Fatalf("expected ErrSignedManifestSchemaInvalid on empty input, got %v", err)
	}

	_, err = ParseSignedRuntimeManifest([]byte("NOT JSON AT ALL"))
	if !errors.Is(err, ErrManifestInvalidJSON) {
		t.Fatalf("expected ErrManifestInvalidJSON on bad json, got %v", err)
	}

	// Missing fields
	badSchemaJSON := []byte(`{
		"schema_version": "1.0.0",
		"manifest": {},
		"signature": {
			"key_id": "",
			"algorithm": "ed25519"
		}
	}`)
	_, err = ParseSignedRuntimeManifest(badSchemaJSON)
	if !errors.Is(err, ErrSignedManifestSchemaInvalid) {
		t.Fatalf("expected ErrSignedManifestSchemaInvalid on missing key_id, got %v", err)
	}
}

func TestLoadTrustStore(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	anchor := NewEd25519TrustAnchor("test-key", pub)
	data, err := json.Marshal([]*TrustAnchor{anchor})
	if err != nil {
		t.Fatal(err)
	}

	store, err := LoadTrustStore(data)
	if err != nil {
		t.Fatalf("LoadTrustStore failed: %v", err)
	}
	got, err := store.GetTrustAnchor("test-key")
	if err != nil {
		t.Fatalf("GetTrustAnchor failed: %v", err)
	}
	if !bytes.Equal(got.PublicKey, pub) {
		t.Fatalf("public key mismatch")
	}

	// Single anchor JSON
	singleData, err := json.Marshal(anchor)
	if err != nil {
		t.Fatal(err)
	}
	singleStore, err := LoadTrustStore(singleData)
	if err != nil {
		t.Fatalf("LoadTrustStore with single anchor failed: %v", err)
	}
	if _, err := singleStore.GetTrustAnchor("test-key"); err != nil {
		t.Fatalf("GetTrustAnchor on single store failed: %v", err)
	}

	// File loading
	tmp := filepath.Join(t.TempDir(), "trust-anchors.json")
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		t.Fatal(err)
	}
	fileStore, err := LoadTrustStoreFromFile(tmp)
	if err != nil {
		t.Fatalf("LoadTrustStoreFromFile failed: %v", err)
	}
	if _, err := fileStore.GetTrustAnchor("test-key"); err != nil {
		t.Fatalf("GetTrustAnchor on file store failed: %v", err)
	}

	// Missing file
	if _, err := LoadTrustStoreFromFile(filepath.Join(t.TempDir(), "nonexistent.json")); err == nil {
		t.Fatal("expected error on nonexistent file")
	}

	// Bad JSON
	if _, err := LoadTrustStore([]byte("invalid json")); err == nil {
		t.Fatal("expected error on bad json")
	}
}
