package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/drko-dev/monitoreoedgeis/internal/installer"
)

func TestWailsSignedRuntimePackExplicitOptInStaysFailClosed(t *testing.T) {
	app := NewApp()
	app.startup(context.Background())
	snapshot, err := app.GetFullEdgePreparationStatus()
	if err != nil || snapshot.CertifiedReady() || snapshot.CanStart {
		t.Fatalf("default Wails must remain unavailable: %+v %v", snapshot, err)
	}

	options := installer.RuntimePackProvisionerOptions{
		Enabled: false, PackDir: t.TempDir(), DataDir: t.TempDir(),
		TargetOS: installer.PlatformDarwin, TargetArch: installer.ArchArm64,
	}
	if _, err := NewAppWithSignedRuntimePack(options, nil, nil); !errors.Is(err, installer.ErrRuntimePackProvisioningDisabled) {
		t.Fatalf("signed constructor allowed disabled opt-in: %v", err)
	}
	options.Enabled = true
	if _, err := NewAppWithSignedRuntimePack(options, nil, nil); !errors.Is(err, installer.ErrRuntimePackManifestPinRequired) {
		t.Fatalf("signed constructor allowed no independent pin: %v", err)
	}
	options.ExpectedManifestSHA256 = strings.Repeat("0", 64)
	if _, err := NewAppWithSignedRuntimePack(options, nil, nil); !errors.Is(err, installer.ErrRuntimePackSignatureRequired) {
		t.Fatalf("signed constructor allowed absent publisher signature: %v", err)
	}
	if _, err := NewAppWithSignedRuntimePack(options, &installer.SignedRuntimeManifest{}, nil); !errors.Is(err, installer.ErrRuntimePackSignatureRequired) {
		t.Fatalf("signed constructor allowed absent trust anchor: %v", err)
	}
	if _, err := NewAppWithSignedRuntimePack(options, &installer.SignedRuntimeManifest{}, installer.NewMemoryTrustStore()); err == nil {
		t.Fatal("signed constructor allowed malformed release signature/manifest")
	}
}
