package ota

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ErrMissingSignature covers every case where no usable signature or
// public key is available. OTA fails closed: there is no checksum-only
// fallback anywhere in this package.
var ErrMissingSignature = errors.New("ota: no valid signature/public key available (fail closed, no checksum-only fallback)")

// LoadPublicKey reads an Ed25519 public key from path, provisioned onto
// the appliance out-of-band from the artifact channel (see
// GEOCAM_OTA_PUBLIC_KEY_FILE in internal/config). Accepts a PEM block or a
// raw 32-byte / hex-encoded key. A missing or malformed key is a fail-closed
// rejection, not a fallback to unsigned trust.
func LoadPublicKey(path string) (ed25519.PublicKey, error) {
	if path == "" {
		return nil, fmt.Errorf("%w: no public key file configured", ErrMissingSignature)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%w: read public key: %v", ErrMissingSignature, err)
	}
	raw, err := decodeKeyBytes(data)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMissingSignature, err)
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("%w: public key at %s is not %d bytes", ErrMissingSignature, path, ed25519.PublicKeySize)
	}
	return ed25519.PublicKey(raw), nil
}

// LoadPrivateKey reads an Ed25519 private key file. Used only by the
// release signing path (`geocam-edge ota sign`, driven from a GitHub
// Actions secret) -- never read on an appliance, never bundled in any OTA
// artifact.
func LoadPrivateKey(path string) (ed25519.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("ota: read private key: %w", err)
	}
	raw, err := decodeKeyBytes(data)
	if err != nil {
		return nil, fmt.Errorf("ota: %w", err)
	}
	if len(raw) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("ota: private key at %s is not %d bytes", path, ed25519.PrivateKeySize)
	}
	return ed25519.PrivateKey(raw), nil
}

// decodeKeyBytes accepts a PEM block, or trimmed raw/hex bytes -- whichever
// form the key file happens to be provisioned in.
func decodeKeyBytes(data []byte) ([]byte, error) {
	if block, _ := pem.Decode(data); block != nil {
		return block.Bytes, nil
	}
	trimmed := strings.TrimSpace(string(data))
	if decoded, err := hex.DecodeString(trimmed); err == nil {
		return decoded, nil
	}
	return []byte(trimmed), nil
}

// SignManifest produces a detached Ed25519 signature of manifest. Used
// only by the release pipeline, never by the Edge appliance.
func SignManifest(priv ed25519.PrivateKey, manifest []byte) []byte {
	return ed25519.Sign(priv, manifest)
}

// VerifyManifestSignature verifies sig is a valid Ed25519 signature of
// manifest under pubKey. Fails closed on an empty key or signature rather
// than treating either as "no signature required".
func VerifyManifestSignature(manifest, sig []byte, pubKey ed25519.PublicKey) error {
	if len(pubKey) != ed25519.PublicKeySize {
		return ErrMissingSignature
	}
	if len(sig) == 0 {
		return fmt.Errorf("%w: empty signature", ErrMissingSignature)
	}
	if !ed25519.Verify(pubKey, manifest, sig) {
		return errors.New("ota: SHA256SUMS signature verification failed")
	}
	return nil
}

// VerifyArtifactChecksum parses manifest as a `sha256sum`-format SHA256SUMS
// file and checks artifactName's recorded hash against the real SHA-256 of
// the file at artifactPath. Only meaningful once VerifyManifestSignature
// has already accepted manifest -- this function trusts manifest's
// contents, it does not itself verify any signature (fail-closed order:
// signature first, checksum second -- see internal/ota.FileDownloader).
func VerifyArtifactChecksum(artifactPath string, manifest []byte, artifactName string) error {
	want, err := lookupSHA256(manifest, artifactName)
	if err != nil {
		return err
	}
	got, err := sha256File(artifactPath)
	if err != nil {
		return err
	}
	if !strings.EqualFold(want, got) {
		return fmt.Errorf("ota: checksum mismatch for %s: manifest says %s, artifact hashes to %s", artifactName, want, got)
	}
	return nil
}

func lookupSHA256(manifest []byte, name string) (string, error) {
	scanner := bufio.NewScanner(strings.NewReader(string(manifest)))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 {
			continue
		}
		hash, fname := fields[0], strings.TrimPrefix(fields[1], "*")
		if fname == name {
			return hash, nil
		}
	}
	return "", fmt.Errorf("ota: %s not listed in SHA256SUMS manifest", name)
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("ota: open %s: %w", path, err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("ota: hash %s: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// VerifyEligible re-checks the T1 forward-update rule at verification
// time (defence in depth against the descriptor changing between
// CheckOnce's initial look and the artifact actually being verified).
func VerifyEligible(current, candidate string) error {
	ok, err := IsUpdateEligible(current, candidate)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("ota: candidate version %s is not a forward update from %s", candidate, current)
	}
	return nil
}

// requiredArchiveEntries are the appliance package.sh layout entries this
// artifact must contain -- rejects a reduced/short tarball (like the
// pre-Hito-T release.yml binary-only tarball) before it is ever handed to
// the privileged updater.
var requiredArchiveEntries = []string{"geocam-edge", "VERSION", "ARCH"}

// readArchiveLayout opens the tar.gz at artifactPath, checks it has the
// full appliance package.sh layout, and returns the exact content of its
// embedded VERSION and ARCH marker files.
func readArchiveLayout(artifactPath string) (version, arch string, err error) {
	f, err := os.Open(artifactPath)
	if err != nil {
		return "", "", fmt.Errorf("ota: open artifact: %w", err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return "", "", fmt.Errorf("ota: artifact is not a valid gzip archive: %w", err)
	}
	defer gz.Close()

	found := make(map[string]bool, len(requiredArchiveEntries))
	tr := tar.NewReader(gz)
	for {
		hdr, terr := tr.Next()
		if errors.Is(terr, io.EOF) {
			break
		}
		if terr != nil {
			return "", "", fmt.Errorf("ota: read artifact tar entries: %w", terr)
		}
		name := strings.TrimPrefix(hdr.Name, "./")
		found[name] = true
		switch name {
		case "VERSION":
			b, _ := io.ReadAll(io.LimitReader(tr, 64))
			version = strings.TrimSpace(string(b))
		case "ARCH":
			b, _ := io.ReadAll(io.LimitReader(tr, 64))
			arch = strings.TrimSpace(string(b))
		}
	}
	for _, name := range requiredArchiveEntries {
		if !found[name] {
			return "", "", fmt.Errorf("ota: artifact is missing required entry %q (not a full appliance package)", name)
		}
	}
	return version, arch, nil
}

// VerifyArchiveBinding opens the tar.gz at artifactPath and binds its OWN
// embedded VERSION/ARCH markers to the authenticated release descriptor
// (wantVersion/wantArch): a descriptor and its artifact must never be
// allowed to diverge silently. Both markers are required, non-empty, and
// must match EXACTLY (string equality against the descriptor's own
// vX.Y.Z, not semver equality) -- fail closed on any mismatch, including
// an artifact whose VERSION happens to be a valid-but-different version
// than the one the signed manifest/descriptor claims.
func VerifyArchiveBinding(artifactPath, wantVersion, wantArch string) error {
	if wantVersion == "" || wantArch == "" {
		return errors.New("ota: VerifyArchiveBinding requires both an expected version and architecture")
	}
	gotVersion, gotArch, err := readArchiveLayout(artifactPath)
	if err != nil {
		return err
	}
	if gotVersion == "" || gotVersion != wantVersion {
		return fmt.Errorf("ota: artifact VERSION %q does not match the release descriptor's version %q", gotVersion, wantVersion)
	}
	if gotArch == "" || gotArch != wantArch {
		return fmt.Errorf("ota: artifact ARCH %q does not match the expected architecture %q", gotArch, wantArch)
	}
	return nil
}

// VerifyReleaseFiles is the single reusable T4 verification pipeline,
// shared by FileDownloader.FetchAndStage (via VerifyReleaseDir, the real
// Edge download path), `geocam-edge ota verify` (flag mode), and
// VerifyReleaseDir (--artifact-dir mode, also reusable unmodified by IA2's
// privileged updater against a root-owned snapshot). Fail-closed at every
// step, no checksum-only fallback anywhere in the sequence:
//
//  1. Ed25519 signature of manifest under pubKey.
//  2. artifactPath's real SHA-256 against manifest's entry for artifactName.
//  3. candidateVersion is a valid, strictly newer version than currentVersion.
//  4. The artifact's own embedded VERSION/ARCH exactly match
//     candidateVersion/candidateArch (VerifyArchiveBinding).
func VerifyReleaseFiles(artifactPath string, manifest, sig []byte, pubKey ed25519.PublicKey, artifactName, currentVersion, candidateVersion, candidateArch string) error {
	if err := VerifyManifestSignature(manifest, sig, pubKey); err != nil {
		return fmt.Errorf("ota: manifest signature: %w", err)
	}
	if err := VerifyArtifactChecksum(artifactPath, manifest, artifactName); err != nil {
		return fmt.Errorf("ota: artifact checksum: %w", err)
	}
	if err := VerifyEligible(currentVersion, candidateVersion); err != nil {
		return fmt.Errorf("ota: version check: %w", err)
	}
	if err := VerifyArchiveBinding(artifactPath, candidateVersion, candidateArch); err != nil {
		return fmt.Errorf("ota: artifact binding: %w", err)
	}
	return nil
}

// VerifyReleaseDir runs VerifyReleaseFiles against a staged release
// directory (DataDir/ota/pending/<release-id>/, or a root-owned snapshot
// of one) using its metadata.json for the release's identity -- the
// `--artifact-dir` contract shared between this appliance's own
// FetchAndStage, the `geocam-edge ota verify --artifact-dir` CLI, and
// IA2's privileged updater. The directory must contain metadata.json,
// SHA256SUMS, SHA256SUMS.sig, and the file named by
// metadata.json's artifact_name.
func VerifyReleaseDir(dir string, pubKey ed25519.PublicKey, currentVersion string) error {
	meta, err := LoadReleaseMetadata(dir)
	if err != nil {
		return err
	}
	manifestPath := filepath.Join(dir, "SHA256SUMS")
	sigPath := filepath.Join(dir, "SHA256SUMS.sig")
	artifactPath := filepath.Join(dir, meta.ArtifactName)

	manifest, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("ota: read SHA256SUMS: %w", err)
	}
	sig, err := os.ReadFile(sigPath)
	if err != nil {
		return fmt.Errorf("ota: read SHA256SUMS.sig: %w", err)
	}
	if _, err := os.Stat(artifactPath); err != nil {
		return fmt.Errorf("ota: artifact %q named in metadata.json not found: %w", meta.ArtifactName, err)
	}

	return VerifyReleaseFiles(artifactPath, manifest, sig, pubKey, meta.ArtifactName, currentVersion, meta.Version, meta.Architecture)
}
