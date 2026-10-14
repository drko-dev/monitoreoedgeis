package installer

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSignedInstalledLaunchGuardFailClosedAndNoModelAuthorization(t *testing.T) {
	options, signed, trust := newSignedPackFixture(t)
	backend, err := NewSignedRuntimePackPreparationBackend(options, signed, trust)
	if err != nil {
		t.Fatal(err)
	}
	modelsDir, stateDir := t.TempDir(), t.TempDir()
	// A source manifest alone is not an installed Runtime Pack.
	if _, err := backend.BuildSignedInstalledWorkerLaunchSpec(context.Background(), modelsDir, stateDir); err == nil {
		t.Fatal("source-only pack must not be allowed to construct installed launch spec")
	}
	prepared, err := backend.Prepare(context.Background())
	if err != nil || !prepared.TransactionCommitted {
		t.Fatalf("signed fixture Prepare: %+v %v", prepared, err)
	}
	spec, err := backend.BuildSignedInstalledWorkerLaunchSpec(context.Background(), modelsDir, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if !spec.ArtifactsVerified() || spec.ModelsAuthorized() {
		t.Fatalf("verified launch must not authorize real model inference: %#v", spec)
	}
	if err := spec.VerifyBeforeSpawn(context.Background()); err != nil {
		t.Fatalf("unchanged installed files should verify: %v", err)
	}
	if err := os.RemoveAll(options.PackDir); err != nil {
		t.Fatal(err)
	}
	if err := spec.VerifyBeforeSpawn(context.Background()); err != nil {
		t.Fatalf("original source media not needed at process startup: %v", err)
	}
	installedWorker := filepath.Join(options.DataDir, DefaultRuntimePackTargetName, signed.Manifest.Worker.Entrypoint)
	if err := os.WriteFile(installedWorker, []byte("modified after launch contract creation"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := spec.VerifyBeforeSpawn(context.Background()); err == nil {
		t.Fatal("installed worker changed after contract creation; pre-spawn verifier bypassed")
	}
}

func TestSignedInstalledLaunchGuardRevocationAfterConstruction(t *testing.T) {
	options, signed, trust := newSignedPackFixture(t)
	backend, err := NewSignedRuntimePackPreparationBackend(options, signed, trust)
	if err != nil {
		t.Fatal(err)
	}
	if result, err := backend.Prepare(context.Background()); err != nil || !result.TransactionCommitted {
		t.Fatalf("fixture: %+v %v", result, err)
	}
	spec, err := backend.BuildSignedInstalledWorkerLaunchSpec(context.Background(), t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := trust.RevokeKey("fixture-publisher", time.Now().UTC(), "revoked before process launch"); err != nil {
		t.Fatal(err)
	}
	if err := spec.VerifyBeforeSpawn(context.Background()); err == nil {
		t.Fatal("revoked signer still allows a pre-spawn check")
	}
}

func TestSignedInstalledLaunchGuardRequiresExplicitStatePaths(t *testing.T) {
	options, signed, trust := newSignedPackFixture(t)
	backend, err := NewSignedRuntimePackPreparationBackend(options, signed, trust)
	if err != nil {
		t.Fatal(err)
	}
	if result, err := backend.Prepare(context.Background()); err != nil || !result.TransactionCommitted {
		t.Fatalf("fixture: %+v %v", result, err)
	}
	if _, err := backend.BuildSignedInstalledWorkerLaunchSpec(context.Background(), "models", t.TempDir()); err == nil {
		t.Fatal("relative model path accepted")
	}
	if _, err := backend.BuildSignedInstalledWorkerLaunchSpec(context.Background(), t.TempDir(), "state"); err == nil {
		t.Fatal("relative worker state accepted")
	}
}

func TestSignedInstalledLaunchGuardUnboundSpecFails(t *testing.T) {
	var empty SignedInstalledRuntimeWorkerLaunchSpec
	if err := empty.VerifyBeforeSpawn(context.Background()); err == nil {
		t.Fatal("zero-valued signed guard accepted")
	}
}
