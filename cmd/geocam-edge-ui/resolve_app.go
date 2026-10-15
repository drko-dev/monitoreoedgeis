package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/drko-dev/monitoreoedgeis/internal/installer"
)

// resolveApp inspects the environment and bundle context for explicit opt-in
// runtime pack configuration. By default, it returns NewApp(), ensuring
// fail-closed behavior when no authorized signed Runtime Pack is configured.
func resolveApp() *App {
	optIn := os.Getenv("GEOCAM_OFFLINE_PROVISIONING")
	packDir := os.Getenv("GEOCAM_RUNTIME_PACK_DIR")
	manifestPin := os.Getenv("GEOCAM_RUNTIME_MANIFEST_SHA256")
	signedPath := os.Getenv("GEOCAM_RUNTIME_SIGNED_MANIFEST")
	trustPath := os.Getenv("GEOCAM_RUNTIME_TRUST_ANCHOR")

	// If no opt-in is requested, maintain the default fail-closed state.
	if optIn != "1" && optIn != "true" && packDir == "" {
		return NewApp()
	}

	// Resolve macOS bundle resources if running from an .app bundle.
	exe, exeErr := os.Executable()
	var resourcesDir string
	if exeErr == nil && filepath.Base(filepath.Dir(exe)) == "MacOS" {
		resourcesDir = filepath.Clean(filepath.Join(filepath.Dir(exe), "..", "Resources"))
		if packDir == "" || packDir == "bundled" {
			bundledPack := filepath.Join(resourcesDir, "RuntimePack")
			if info, err := os.Stat(bundledPack); err == nil && info.IsDir() {
				packDir = bundledPack
			}
		}
		if signedPath == "" && resourcesDir != "" {
			candidate := filepath.Join(resourcesDir, "runtime-manifest.signed.json")
			if _, err := os.Stat(candidate); err == nil {
				signedPath = candidate
			}
		}
		if trustPath == "" && resourcesDir != "" {
			candidate := filepath.Join(resourcesDir, "trust-anchors.json")
			if _, err := os.Stat(candidate); err == nil {
				trustPath = candidate
			}
		}
		if manifestPin == "" && resourcesDir != "" {
			candidate := filepath.Join(resourcesDir, "runtime-manifest.sha256")
			if pinBytes, err := os.ReadFile(candidate); err == nil {
				manifestPin = strings.TrimSpace(string(pinBytes))
			}
		}
	}

	// If signed manifest path is still empty, look inside packDir.
	if signedPath == "" && packDir != "" {
		candidate := filepath.Join(packDir, "runtime-manifest.signed.json")
		if _, err := os.Stat(candidate); err == nil {
			signedPath = candidate
		}
	}

	dataDir := os.Getenv("GEOCAM_DATA_DIR")
	if dataDir == "" {
		home, err := os.UserHomeDir()
		if err == nil {
			dataDir = filepath.Join(home, ".geocam", "data")
		} else {
			dataDir = filepath.Join(".", "data")
		}
	}

	options := installer.RuntimePackProvisionerOptions{
		Enabled:                true,
		PackDir:                packDir,
		DataDir:                dataDir,
		ExpectedManifestSHA256: manifestPin,
		TargetOS:               runtime.GOOS,
		TargetArch:             runtime.GOARCH,
	}

	// Signed manifest + trust store branch (preferred, cryptographically authenticated).
	if signedPath != "" && trustPath != "" {
		signedManifest, err := installer.LoadSignedRuntimeManifest(signedPath)
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "[GEO-CAM Edge] Failed to load signed manifest: %v\n", err)
			return NewApp()
		}
		trustStore, err := installer.LoadTrustStoreFromFile(trustPath)
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "[GEO-CAM Edge] Failed to load trust store: %v\n", err)
			return NewApp()
		}
		app, err := NewAppWithSignedRuntimePack(options, signedManifest, trustStore)
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "[GEO-CAM Edge] Failed to initialize signed Runtime Pack: %v\n", err)
			return NewApp()
		}
		return app
	}

	// Unsigned pinned pack branch (fallback only when explicitly opted-in).
	if os.Getenv("GEOCAM_RUNTIME_PACK_PINNED") == "1" && manifestPin != "" && packDir != "" {
		app, err := NewAppWithPinnedRuntimePack(options)
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "[GEO-CAM Edge] Failed to initialize pinned Runtime Pack: %v\n", err)
			return NewApp()
		}
		return app
	}

	// Incomplete configuration: fail closed to default safe app.
	return NewApp()
}
