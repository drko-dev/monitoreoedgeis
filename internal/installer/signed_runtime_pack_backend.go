package installer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
)

// ErrRuntimePackSignatureRequired signals that the release owner did not supply
// an independently authenticated signed manifest and a publisher trust store.
var ErrRuntimePackSignatureRequired = errors.New("independently trusted signed Runtime Pack manifest is required")

// ErrRuntimePackSignedManifestMismatch indicates a valid signature that does
// not authenticate the exact runtime-manifest.json pinned by the release owner.
var ErrRuntimePackSignedManifestMismatch = errors.New("signed Runtime Pack payload and pinned on-disk manifest differ")

// SignedRuntimePackPreparationBackend is an EXPLICIT opt-in security boundary
// between an external publisher trust anchor and the H6B transactional backend.
// Neither the trust anchor nor this signed manifest is loaded from packDir.
// Normal NewApp and NewAppWithPinnedRuntimePack are unchanged.
type SignedRuntimePackPreparationBackend struct {
	backend *TransactionalRuntimePackProvisioner
	signed  *SignedRuntimeManifest
	trust   TrustStore
}

// NewSignedRuntimePackPreparationBackend requires BOTH an external pinned
// SHA-256 digest AND a signature validated against an independently supplied
// TrustStore. It never creates or trusts publisher keys from pack contents.
//
// The signed manifest is copied at construction time, preventing caller-side
// mutations after establishing the release-owned binding. Key revocations are
// consulted again on each Prepare and Recover.
func NewSignedRuntimePackPreparationBackend(
	options RuntimePackProvisionerOptions,
	signed *SignedRuntimeManifest,
	trust TrustStore,
) (*SignedRuntimePackPreparationBackend, error) {
	if !options.Enabled {
		return nil, ErrRuntimePackProvisioningDisabled
	}
	if !isSHA256(options.ExpectedManifestSHA256) {
		return nil, ErrRuntimePackManifestPinRequired
	}
	if signed == nil || trust == nil || (reflect.ValueOf(trust).Kind() == reflect.Ptr && reflect.ValueOf(trust).IsNil()) {
		return nil, ErrRuntimePackSignatureRequired
	}
	data, err := json.Marshal(signed)
	if err != nil {
		return nil, fmt.Errorf("clone independently supplied signature: %w", err)
	}
	var frozen SignedRuntimeManifest
	if err := json.Unmarshal(data, &frozen); err != nil {
		return nil, fmt.Errorf("parse independently supplied signature: %w", err)
	}
	if err := frozen.ValidateSchema(); err != nil {
		return nil, err
	}
	backend, err := NewTransactionalRuntimePackProvisioner(options)
	if err != nil {
		return nil, err
	}
	return &SignedRuntimePackPreparationBackend{backend: backend, signed: &frozen, trust: trust}, nil
}

// verify checks signature validity, revocation/expiry, artifact integrity and
// precise binding to the *pinned file* before any H6A lock, staging or recovery
// mutation. H6B repeats file pin/allowlist checks during staging and commit.
//
// A successful call is not a release or functional-inference certification.
func (p *SignedRuntimePackPreparationBackend) verify(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if p == nil || p.backend == nil || p.signed == nil || p.trust == nil {
		return ErrRuntimePackSignatureRequired
	}
	o := p.backend.options
	packRoot, err := canonicalDirectory(o.PackDir)
	if err != nil {
		return err
	}
	if err := ensureDisjointRuntimePaths(packRoot, o.DataDir); err != nil {
		return err
	}
	manifestPath := filepath.Join(packRoot, filepath.FromSlash(o.ManifestName))
	if err := requireRegularPath(packRoot, manifestPath); err != nil {
		return err
	}
	manifestBytes, err := loadPinnedRuntimeManifestBytes(manifestPath, o.ExpectedManifestSHA256)
	if err != nil {
		return err
	}
	var diskManifest RuntimeManifest
	if err := json.Unmarshal(manifestBytes, &diskManifest); err != nil {
		return fmt.Errorf("read pinned signed runtime manifest: %w", err)
	}
	// A valid signature on some OTHER manifest (even from the same trusted
	// publisher) must not authorize the currently selected file.
	if !reflect.DeepEqual(diskManifest, p.signed.Manifest) {
		return ErrRuntimePackSignedManifestMismatch
	}
	// Uses an external trust anchor, not PublicKey embedded in signed bytes.
	// Also checks expiry, platform, all declared artifact hashes, and release type.
	return ValidateSignedManifest(p.signed, p.trust, VerifySignedManifestOptions{
		TargetPlatform: o.TargetOS,
		TargetArch:     o.TargetArch,
		PackRoot:       packRoot,
		RequireRelease: true,
	})
}

func loadPinnedRuntimeManifestBytes(path, expectedDigest string) ([]byte, error) {
	data, err := readLimitedSignedManifest(path)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(data)
	if !strings.EqualFold(hex.EncodeToString(digest[:]), expectedDigest) {
		return nil, ErrRuntimePackManifestPinMismatch
	}
	return data, nil
}

// readLimitedSignedManifest provides a bounded, non-symlink input read.
func readLimitedSignedManifest(path string) ([]byte, error) {
	const maxBytes = 4 << 20
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxBytes {
		return nil, errors.New("signed Runtime Pack manifest exceeds maximum size")
	}
	return data, nil
}

// Prepare verifies signed source before delegating to the existing
// transaction machinery. Any failure leaves persistent state untouched.
func (p *SignedRuntimePackPreparationBackend) Prepare(ctx context.Context) (RuntimePackProvisioningResult, error) {
	if err := p.verify(ctx); err != nil {
		return failedPackResult(RuntimePackProvisioningResult{State: "CHECKING"}, "SIGNED_MANIFEST_REJECTED", err)
	}
	result, err := p.backend.Prepare(ctx)
	result.RuntimeReady, result.InferenceReady = false, false
	return result, err
}

// Recover also rechecks current signer revocation and payload identity. A
// failed verification is intentionally not silently downgraded to an
// unsigned rollback/READY; human recovery is then required.
func (p *SignedRuntimePackPreparationBackend) Recover(ctx context.Context) (RuntimePackProvisioningResult, error) {
	if err := p.verify(ctx); err != nil {
		return failedPackResult(RuntimePackProvisioningResult{State: "RECOVERY_REQUIRED"}, "SIGNED_MANIFEST_REJECTED", err)
	}
	result, err := p.backend.Recover(ctx)
	result.RuntimeReady, result.InferenceReady = false, false
	return result, err
}

var _ RuntimePackPreparationBackend = (*SignedRuntimePackPreparationBackend)(nil)
