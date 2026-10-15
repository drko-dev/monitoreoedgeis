package main

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/installer"
)

// TestOneClickPreparationWithRealSignedPack drives the same Wails-bound calls
// the "Preparar Full Edge" button uses, against a real signed pack produced by
// cmd/runtime-pack-sign, with the build-time opt-in and no environment opt-in.
// Opt-in: H5_REAL_PACK_DIR, H5_REAL_SIGNED, H5_REAL_TRUST, H5_REAL_LDFLAGS.
func TestOneClickPreparationWithRealSignedPack(t *testing.T) {
	pack := os.Getenv("H5_REAL_PACK_DIR")
	if pack == "" {
		t.Skip("H5_REAL_PACK_DIR not set")
	}
	pins := map[string]string{}
	for _, field := range strings.Fields(os.Getenv("H5_REAL_LDFLAGS")) {
		if k, v, ok := strings.Cut(strings.TrimPrefix(field, "main."), "="); ok {
			pins[k] = v
		}
	}
	t.Setenv("GEOCAM_OFFLINE_PROVISIONING", "")
	t.Setenv("GEOCAM_RUNTIME_PACK_DIR", pack)
	t.Setenv("GEOCAM_RUNTIME_SIGNED_MANIFEST", os.Getenv("H5_REAL_SIGNED"))
	t.Setenv("GEOCAM_RUNTIME_TRUST_ANCHOR", os.Getenv("H5_REAL_TRUST"))
	dataDir := os.Getenv("H5_REAL_DATA_DIR") // keep the install for daemon checks
	if dataDir == "" {
		dataDir = t.TempDir()
	}
	t.Setenv("GEOCAM_DATA_DIR", dataDir)
	if cfg := os.Getenv("H5_REAL_CONFIG_FILE"); cfg != "" {
		t.Setenv("GEOCAM_CONFIG_FILE", cfg)
	} else {
		t.Setenv("GEOCAM_CONFIG_FILE", t.TempDir()+"/edge.env")
	}
	prev := releaseOfflineProvisioning
	releaseOfflineProvisioning = "1"
	t.Cleanup(func() { releaseOfflineProvisioning = prev })

	app := resolveAppWithReleaseTrust(releaseRuntimeTrust{
		manifestSHA256:  pins["releaseRuntimeManifestSHA256"],
		keyID:           pins["releaseRuntimeKeyID"],
		publicKeySHA256: pins["releaseRuntimePublicKeySHA256"],
	})
	status, err := waitPastChecking(app)
	if err != nil || status.ErrorCode == "PREPARATION_NOT_INTEGRATED" || !status.CanStart {
		t.Fatalf("one-click preparation not available: %+v %v", status, err)
	}
	snap, err := app.StartFullEdgePreparation("h5-one-click")
	deadline := time.Now().Add(10 * time.Minute)
	for err == nil && (snap.State == installer.StateChecking || snap.State == installer.StatePreparing || snap.State == installer.StateVerifying) && time.Now().Before(deadline) {
		time.Sleep(500 * time.Millisecond)
		snap, err = app.GetFullEdgePreparationStatus()
	}
	// With models and the fixture bundled next to the pack, the installed
	// runtime must pass the H8 inference certification and report READY.
	if err != nil || snap.State != installer.StateReady || !snap.CertifiedReady() {
		t.Fatalf("preparation not certified READY: %+v %v", snap, err)
	}
	if again, err := app.GetFullEdgePreparationStatus(); err != nil || !again.CertifiedReady() {
		t.Fatalf("READY not retained in status: %+v %v", again, err)
	}
	t.Logf("final snapshot: state=%s progress=%d code=%s msg=%s", snap.State, snap.ProgressPercent, snap.ErrorCode, snap.SafeMessage)
}

// waitPastChecking waits for the background startup reconciliation.
func waitPastChecking(app *App) (installer.FullEdgePreparationSnapshot, error) {
	deadline := time.Now().Add(5 * time.Minute)
	for {
		s, err := app.GetFullEdgePreparationStatus()
		if err != nil || s.State != installer.StateChecking || time.Now().After(deadline) {
			return s, err
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// TestRestartKeepsCertifiedReady reopens the app on an already prepared and
// certified data dir (H5_REAL_DATA_DIR) and expects READY without re-running
// the preparation.
func TestRestartKeepsCertifiedReady(t *testing.T) {
	if os.Getenv("H5_REAL_RESTART") == "" {
		t.Skip("H5_REAL_RESTART not set")
	}
	pins := map[string]string{}
	for _, field := range strings.Fields(os.Getenv("H5_REAL_LDFLAGS")) {
		if k, v, ok := strings.Cut(strings.TrimPrefix(field, "main."), "="); ok {
			pins[k] = v
		}
	}
	t.Setenv("GEOCAM_RUNTIME_PACK_DIR", os.Getenv("H5_REAL_PACK_DIR"))
	t.Setenv("GEOCAM_RUNTIME_SIGNED_MANIFEST", os.Getenv("H5_REAL_SIGNED"))
	t.Setenv("GEOCAM_RUNTIME_TRUST_ANCHOR", os.Getenv("H5_REAL_TRUST"))
	t.Setenv("GEOCAM_DATA_DIR", os.Getenv("H5_REAL_DATA_DIR"))
	t.Setenv("GEOCAM_CONFIG_FILE", os.Getenv("H5_REAL_CONFIG_FILE"))
	prev := releaseOfflineProvisioning
	releaseOfflineProvisioning = "1"
	t.Cleanup(func() { releaseOfflineProvisioning = prev })
	app := resolveAppWithReleaseTrust(releaseRuntimeTrust{
		manifestSHA256:  pins["releaseRuntimeManifestSHA256"],
		keyID:           pins["releaseRuntimeKeyID"],
		publicKeySHA256: pins["releaseRuntimePublicKeySHA256"],
	})
	s, err := waitPastChecking(app)
	if err != nil || !s.CertifiedReady() {
		t.Fatalf("not READY after restart: %+v %v", s, err)
	}
}
