package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/drko-dev/monitoreoedgeis/internal/installer"
)

func TestWailsH6BExplicitWiringRemainsFailClosed(t *testing.T) {
	base := NewApp()
	base.startup(context.Background())
	s, err := base.GetFullEdgePreparationStatus()
	if err != nil || s.CertifiedReady() || s.CanStart {
		t.Fatalf("default unsafe: %+v %v", s, err)
	}
	options := installer.RuntimePackProvisionerOptions{PackDir: t.TempDir(), DataDir: t.TempDir(), TargetOS: "darwin", TargetArch: "arm64"}
	if _, err := NewAppWithPinnedRuntimePack(options); !errors.Is(err, installer.ErrRuntimePackProvisioningDisabled) {
		t.Fatalf("disabled: %v", err)
	}
	options.Enabled = true
	if _, err := NewAppWithPinnedRuntimePack(options); !errors.Is(err, installer.ErrRuntimePackManifestPinRequired) {
		t.Fatalf("without pin: %v", err)
	}
	options.ExpectedManifestSHA256 = strings.Repeat("0", 64)
	app, err := NewAppWithPinnedRuntimePack(options)
	if err != nil {
		t.Fatal(err)
	}
	app.startup(context.Background())
	before, err := app.GetFullEdgePreparationStatus()
	if err != nil || before.State != installer.StateNotPrepared || !before.CanStart || before.CertifiedReady() {
		t.Fatalf("opt-in snapshot: %+v %v", before, err)
	}
	after, err := app.StartFullEdgePreparation("test-request")
	if err == nil {
		t.Fatalf("untrusted pack must fail: %+v", after)
	}
	status, err := app.GetFullEdgePreparationStatus()
	if err != nil || status.CertifiedReady() || status.State == installer.StateReady {
		t.Fatalf("unsafe failure: %+v %v", status, err)
	}
}
