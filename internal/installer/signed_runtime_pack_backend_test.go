package installer

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newSignedPackFixture(t *testing.T) (
	RuntimePackProvisionerOptions, *SignedRuntimeManifest, *MemoryTrustStore,
) {
	t.Helper()
	pack, pin, manifest := writeProvisionerFixture(t, "1.2.3")
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	store := NewMemoryTrustStore()
	if err := store.AddAnchor(NewEd25519TrustAnchor("fixture-publisher", pub)); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	exp := now.Add(2 * time.Hour)
	signed, err := SignManifestEd25519(manifest, "fixture-publisher", priv, SignOptions{
		IssuedAt: now, ExpiresAt: &exp,
	})
	if err != nil {
		t.Fatal(err)
	}
	return RuntimePackProvisionerOptions{
		Enabled: true, PackDir: pack, DataDir: filepath.Join(t.TempDir(), "persistent"),
		TargetOS: PlatformDarwin, TargetArch: ArchArm64,
		ExpectedManifestSHA256: pin,
	}, signed, store
}

func TestSignedPackGateValidSyntheticPackNeverReady(t *testing.T) {
	o, signed, store := newSignedPackFixture(t)
	backend, err := NewSignedRuntimePackPreparationBackend(o, signed, store)
	if err != nil {
		t.Fatal(err)
	}
	result, err := backend.Prepare(context.Background())
	if err != nil {
		t.Fatalf("signed Prepare: %+v %v", result, err)
	}
	if result.State != "COMMITTED_UNCERTIFIED" || !result.TransactionCommitted ||
		!result.RuntimeIntegrityVerified || result.RuntimeReady || result.InferenceReady {
		t.Fatalf("signed pack must never grant runtime readiness: %+v", result)
	}
	recovered, err := backend.Recover(context.Background())
	if err != nil || recovered.State != "COMMITTED_UNCERTIFIED" ||
		!recovered.RuntimeIntegrityVerified || recovered.RuntimeReady || recovered.InferenceReady {
		t.Fatalf("Recover: %+v %v", recovered, err)
	}
}

func TestSignedPackGateNoTrustAndNoPinNeverMutate(t *testing.T) {
	for _, test := range []struct {
		name string
		mod  func(*RuntimePackProvisionerOptions, **SignedRuntimeManifest, *TrustStore)
		err  error
	}{
		{name: "no digest pin", mod: func(o *RuntimePackProvisionerOptions, _ **SignedRuntimeManifest, _ *TrustStore) {
			o.ExpectedManifestSHA256 = ""
		}, err: ErrRuntimePackManifestPinRequired},
		{name: "no signed envelope", mod: func(_ *RuntimePackProvisionerOptions, s **SignedRuntimeManifest, _ *TrustStore) {
			*s = nil
		}, err: ErrRuntimePackSignatureRequired},
		{name: "no external anchor", mod: func(_ *RuntimePackProvisionerOptions, _ **SignedRuntimeManifest, s *TrustStore) {
			*s = nil
		}, err: ErrRuntimePackSignatureRequired},
		{name: "disabled", mod: func(o *RuntimePackProvisionerOptions, _ **SignedRuntimeManifest, _ *TrustStore) {
			o.Enabled = false
		}, err: ErrRuntimePackProvisioningDisabled},
	} {
		t.Run(test.name, func(t *testing.T) {
			o, signed, original := newSignedPackFixture(t)
			var store TrustStore = original
			test.mod(&o, &signed, &store)
			_, err := NewSignedRuntimePackPreparationBackend(o, signed, store)
			if !errors.Is(err, test.err) {
				t.Fatalf("error %v, want %v", err, test.err)
			}
			if _, err := os.Stat(o.DataDir); !os.IsNotExist(err) {
				t.Fatalf("constructor mutated data dir: %v", err)
			}
		})
	}
}

func TestSignedPackGateBadSignatureBeforeTransaction(t *testing.T) {
	o, signed, store := newSignedPackFixture(t)
	data, err := base64.StdEncoding.DecodeString(signed.Signature.Signature)
	if err != nil {
		t.Fatal(err)
	}
	data[0] ^= 0x80
	signed.Signature.Signature = base64.StdEncoding.EncodeToString(data)
	backend, err := NewSignedRuntimePackPreparationBackend(o, signed, store)
	if err != nil {
		t.Fatal(err)
	}
	result, err := backend.Prepare(context.Background())
	if !errors.Is(err, ErrSignatureInvalid) || result.ErrorCode != "SIGNED_MANIFEST_REJECTED" ||
		result.TransactionCommitted || result.RuntimeReady {
		t.Fatalf("bad signature unexpectedly admitted: %+v %v", result, err)
	}
	if _, err := os.Stat(o.DataDir); !os.IsNotExist(err) {
		t.Fatalf("bad signer modified persistent storage: %v", err)
	}
}

func TestSignedPackGateDifferentValidSignedManifestRejected(t *testing.T) {
	o, signed, store := newSignedPackFixture(t)
	// Keep the signed bytes valid, then point at another *valid pinned file*.
	// This must not allow the original signature to bless a different release.
	pack2, digest2, _ := writeProvisionerFixture(t, "8.8.8")
	o.PackDir, o.ExpectedManifestSHA256 = pack2, digest2
	backend, err := NewSignedRuntimePackPreparationBackend(o, signed, store)
	if err != nil {
		t.Fatal(err)
	}
	result, err := backend.Prepare(context.Background())
	if !errors.Is(err, ErrRuntimePackSignedManifestMismatch) || result.TransactionCommitted {
		t.Fatalf("valid signed unrelated manifest admitted: %+v %v", result, err)
	}
	if _, err := os.Stat(o.DataDir); !os.IsNotExist(err) {
		t.Fatalf("mismatch changed data: %v", err)
	}
}

func TestSignedPackGateUntrustedAndRevokedKeys(t *testing.T) {
	for _, kind := range []string{"unknown", "revoked"} {
		t.Run(kind, func(t *testing.T) {
			o, signed, store := newSignedPackFixture(t)
			if kind == "unknown" {
				store = NewMemoryTrustStore()
			} else if err := store.RevokeKey("fixture-publisher", time.Now().UTC(), "lab key withdrawn"); err != nil {
				t.Fatal(err)
			}
			backend, err := NewSignedRuntimePackPreparationBackend(o, signed, store)
			if err != nil {
				t.Fatal(err)
			}
			result, err := backend.Prepare(context.Background())
			expected := ErrUntrustedKey
			if kind == "revoked" {
				expected = ErrKeyRevoked
			}
			if !errors.Is(err, expected) || result.TransactionCommitted || result.RuntimeReady {
				t.Fatalf("unauthorized publisher accepted: %+v %v", result, err)
			}
		})
	}
}

func TestSignedPackGateArtifactTamperingAndExternalPinMismatch(t *testing.T) {
	t.Run("tampered artifact", func(t *testing.T) {
		o, signed, store := newSignedPackFixture(t)
		artifact := filepath.Join(o.PackDir, signed.Manifest.Worker.Entrypoint)
		if err := os.WriteFile(artifact, []byte("tampered worker"), 0600); err != nil {
			t.Fatal(err)
		}
		backend, err := NewSignedRuntimePackPreparationBackend(o, signed, store)
		if err != nil {
			t.Fatal(err)
		}
		result, err := backend.Prepare(context.Background())
		if err == nil || result.TransactionCommitted || result.RuntimeReady {
			t.Fatalf("tamper accepted: %+v %v", result, err)
		}
	})
	t.Run("wrong external pin", func(t *testing.T) {
		o, signed, store := newSignedPackFixture(t)
		o.ExpectedManifestSHA256 = strings.Repeat("a", 64)
		backend, err := NewSignedRuntimePackPreparationBackend(o, signed, store)
		if err != nil {
			t.Fatal(err)
		}
		result, err := backend.Prepare(context.Background())
		if !errors.Is(err, ErrRuntimePackManifestPinMismatch) || result.TransactionCommitted {
			t.Fatalf("wrong external pin accepted: %+v %v", result, err)
		}
	})
}

func TestSignedPackGateRevocationAfterCommitBlocksRecovery(t *testing.T) {
	o, signed, store := newSignedPackFixture(t)
	backend, err := NewSignedRuntimePackPreparationBackend(o, signed, store)
	if err != nil {
		t.Fatal(err)
	}
	if result, err := backend.Prepare(context.Background()); err != nil || !result.TransactionCommitted {
		t.Fatalf("first transaction: %+v %v", result, err)
	}
	if err := store.RevokeKey("fixture-publisher", time.Now().UTC(), "revoked before recovery"); err != nil {
		t.Fatal(err)
	}
	result, err := backend.Recover(context.Background())
	if !errors.Is(err, ErrKeyRevoked) || result.State != "FAILED" ||
		result.RuntimeReady || result.InferenceReady {
		t.Fatalf("revoked signer silently reused: %+v %v", result, err)
	}
}

func TestSignedPackGateCopyPreventsCallerSignatureMutation(t *testing.T) {
	o, signed, store := newSignedPackFixture(t)
	backend, err := NewSignedRuntimePackPreparationBackend(o, signed, store)
	if err != nil {
		t.Fatal(err)
	}
	signed.Signature.Signature = "caller-corrupted-after-construction"
	result, err := backend.Prepare(context.Background())
	if err != nil || !result.TransactionCommitted || result.RuntimeReady {
		t.Fatalf("constructor did not isolate release-owned signature: %+v %v", result, err)
	}
}

func TestSignedPackGateECDSAP256Positive(t *testing.T) {
	o, _, _ := newSignedPackFixture(t)
	manifest, err := LoadRuntimeManifest(filepath.Join(o.PackDir, DefaultRuntimePackManifestName))
	if err != nil {
		t.Fatal(err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	anchor, err := NewECDSAP256TrustAnchor("test-p256", &key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	store := NewMemoryTrustStore()
	if err := store.AddAnchor(anchor); err != nil {
		t.Fatal(err)
	}
	signed, err := SignManifestECDSAP256(manifest, "test-p256", key, SignOptions{})
	if err != nil {
		t.Fatal(err)
	}
	backend, err := NewSignedRuntimePackPreparationBackend(o, signed, store)
	if err != nil {
		t.Fatal(err)
	}
	result, err := backend.Prepare(context.Background())
	if err != nil || !result.TransactionCommitted || result.RuntimeReady {
		t.Fatalf("P-256 release signed provisioning: %+v %v", result, err)
	}
}

func TestSignedPackGateExpiredSignatureFailsClosed(t *testing.T) {
	o, _, _ := newSignedPackFixture(t)
	manifest, err := LoadRuntimeManifest(filepath.Join(o.PackDir, DefaultRuntimePackManifestName))
	if err != nil {
		t.Fatal(err)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	store := NewMemoryTrustStore()
	if err := store.AddAnchor(NewEd25519TrustAnchor("expired", pub)); err != nil {
		t.Fatal(err)
	}
	issued := time.Now().UTC().Add(-48 * time.Hour)
	expires := time.Now().UTC().Add(-24 * time.Hour)
	signed, err := SignManifestEd25519(manifest, "expired", priv,
		SignOptions{IssuedAt: issued, ExpiresAt: &expires})
	if err != nil {
		t.Fatal(err)
	}
	backend, err := NewSignedRuntimePackPreparationBackend(o, signed, store)
	if err != nil {
		t.Fatal(err)
	}
	result, err := backend.Prepare(context.Background())
	if !errors.Is(err, ErrManifestExpired) || result.TransactionCommitted ||
		result.RuntimeReady || result.InferenceReady {
		t.Fatalf("expired signature accepted: %+v %v", result, err)
	}
	if _, err := os.Stat(o.DataDir); !os.IsNotExist(err) {
		t.Fatalf("expired signature mutated storage: %v", err)
	}
}

func TestSignedPackGateRejectsOversizedManifest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime-manifest.json")
	if err := os.WriteFile(path, []byte(strings.Repeat("a", (4<<20)+1)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readLimitedSignedManifest(path); err == nil {
		t.Fatal("oversized manifest unexpectedly accepted")
	}
}
