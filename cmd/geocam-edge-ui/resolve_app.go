package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/installer"
)

// These values are assigned by an approved release build, not the filesystem,
// GUI, environment or signed Runtime Pack. Without ALL three, the normal
// desktop application stays disabled for transactional provisioning.
//
// Example release linker flags (not a release authorization):
//
//	-X main.releaseRuntimeManifestSHA256=<trusted manifest SHA256>
//	-X main.releaseRuntimeKeyID=<trusted publisher key ID>
//	-X main.releaseRuntimePublicKeySHA256=<trusted raw public key SHA256>
var (
	releaseRuntimeManifestSHA256  string
	releaseRuntimeKeyID           string
	releaseRuntimePublicKeySHA256 string
	// "1" makes a pinned build prepare from its bundled pack without any
	// environment opt-in, so a Finder-launched .app works with one click.
	// It never relaxes the trust checks below.
	releaseOfflineProvisioning string
)

type releaseRuntimeTrust struct {
	manifestSHA256  string
	keyID           string
	publicKeySHA256 string
}

func (c releaseRuntimeTrust) configured() bool {
	return validHash(c.manifestSHA256) &&
		strings.TrimSpace(c.keyID) != "" &&
		validHash(c.publicKeySHA256)
}

func validHash(value string) bool {
	b, err := hex.DecodeString(strings.TrimSpace(value))
	return err == nil && len(b) == sha256.Size
}

// resolveApp is intentionally fail-closed without a separately pinned,
// build-time publisher root. Wails cannot opt into unsigned/digest-only mode.
func resolveApp() *App {
	return resolveAppWithReleaseTrust(releaseRuntimeTrust{
		manifestSHA256:  releaseRuntimeManifestSHA256,
		keyID:           releaseRuntimeKeyID,
		publicKeySHA256: releaseRuntimePublicKeySHA256,
	})
}

func resolveAppWithReleaseTrust(root releaseRuntimeTrust) *App {
	off := NewApp()
	optIn := os.Getenv("GEOCAM_OFFLINE_PROVISIONING")
	if releaseOfflineProvisioning == "1" {
		optIn = "1"
	}
	if (optIn != "1" && optIn != "true") || !root.configured() {
		return off
	}

	// An env pin can narrow an approved release but cannot authorize a new
	// manifest. Do NOT accept an environment-only trusted SHA256/key/identity.
	if supplied := strings.TrimSpace(os.Getenv("GEOCAM_RUNTIME_MANIFEST_SHA256")); supplied != "" &&
		!strings.EqualFold(supplied, strings.TrimSpace(root.manifestSHA256)) {
		return off
	}

	packDir := os.Getenv("GEOCAM_RUNTIME_PACK_DIR")
	signedPath := os.Getenv("GEOCAM_RUNTIME_SIGNED_MANIFEST")
	trustPath := os.Getenv("GEOCAM_RUNTIME_TRUST_ANCHOR")

	exe, exeErr := os.Executable()
	if exeErr == nil && filepath.Base(filepath.Dir(exe)) == "MacOS" {
		resourcesDir := filepath.Clean(filepath.Join(filepath.Dir(exe), "..", "Resources"))
		if packDir == "" || packDir == "bundled" {
			bundled := filepath.Join(resourcesDir, "RuntimePack")
			if info, err := os.Lstat(bundled); err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
				packDir = bundled
			}
		}
		if signedPath == "" {
			signedPath = filepath.Join(resourcesDir, "runtime-manifest.signed.json")
		}
		if trustPath == "" {
			trustPath = filepath.Join(resourcesDir, "trust-anchors.json")
		}
	}
	if packDir == "" || signedPath == "" || trustPath == "" || packDir == "bundled" {
		return off
	}
	// No trust anchor may be taken from within the candidate Runtime Pack,
	// even if signed metadata exists there. Publisher trust comes from
	// separately supplied release-owned material and is hash pinned at build.
	if insidePack(packDir, trustPath) {
		return off
	}

	signed, err := installer.LoadSignedRuntimeManifest(signedPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "[GEO-CAM Edge] signed manifest unavailable; preparation disabled")
		return off
	}
	loaded, err := installer.LoadTrustStoreFromFile(trustPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "[GEO-CAM Edge] publisher trust unavailable; preparation disabled")
		return off
	}
	anchor, err := loaded.GetTrustAnchor(root.keyID)
	if err != nil || (anchor.Algorithm != installer.SignatureAlgorithmEd25519 &&
		anchor.Algorithm != installer.SignatureAlgorithmECDSAP256) || anchor.Revoked {
		return off
	}
	digest := sha256.Sum256(anchor.PublicKey)
	if !strings.EqualFold(hex.EncodeToString(digest[:]), strings.TrimSpace(root.publicKeySHA256)) ||
		signed.Signature.KeyID != root.keyID {
		return off
	}

	// Discard any other signer in the supplied file. The release binary only
	// trusts the specifically pinned publisher key and digest.
	trust := installer.NewMemoryTrustStore()
	if err := trust.AddAnchor(anchor); err != nil {
		return off
	}

	dataDir := os.Getenv("GEOCAM_DATA_DIR")
	if dataDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return off
		}
		dataDir = filepath.Join(home, ".geocam", "data")
	}
	options := installer.RuntimePackProvisionerOptions{
		Enabled:                true,
		PackDir:                packDir,
		DataDir:                dataDir,
		ExpectedManifestSHA256: strings.TrimSpace(root.manifestSHA256),
		TargetOS:               runtime.GOOS,
		TargetArch:             runtime.GOARCH,
	}
	app, err := NewAppWithSignedRuntimePack(options, signed, trust)
	if err != nil {
		fmt.Fprintln(os.Stderr, "[GEO-CAM Edge] signed Runtime Pack rejected; preparation disabled")
		return off
	}
	// Models and the certification fixture ship next to the pack in
	// Resources, outside the signed pack.
	resources := filepath.Dir(packDir)
	modelsDir := filepath.Join(resources, "Models")
	fixture := filepath.Join(resources, "Fixtures", "person-annexb.h264")
	pin := strings.TrimSpace(root.manifestSHA256)
	app.onPrepared = func() error { return installer.ConfigureDaemonForInstalledRuntime(dataDir, modelsDir) }
	app.certify = func() (*installer.RuntimeCertification, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		cert, err := installer.CertifyInstalledRuntime(ctx, dataDir, fixture, signed.Manifest.RuntimeVersion, pin)
		if err != nil {
			return nil, err
		}
		return cert, installer.SaveRuntimeCertification(dataDir, cert)
	}
	app.certification = installer.LoadRuntimeCertification(dataDir, pin)
	app.restoreInBackground()
	return app
}

func insidePack(pack, file string) bool {
	root, err := filepath.Abs(pack)
	if err != nil {
		return true
	}
	path, err := filepath.Abs(file)
	if err != nil {
		return true
	}
	// Resolve existing symlinks to avoid a trust anchor smuggled through
	// an otherwise external-looking symlink path.
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return true
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}
