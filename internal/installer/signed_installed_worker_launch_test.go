package installer

import (
	"context"
	"errors"
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
	// Source packages may be writable during H6B staging; a launchable
	// installed release must go through a separate owner-controlled seal.
	if _, err := backend.BuildSignedInstalledWorkerLaunchSpec(context.Background(), modelsDir, stateDir); !errors.Is(err, ErrRuntimePackNotReadOnly) {
		t.Fatalf("mutable installed Runtime Pack must not be launchable: %v", err)
	}
	sealSignedInstalledPackFixture(t, options)
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
	// A same-user process can explicitly remove a chmod seal. The pre-spawn
	// recheck must detect this mutation rather than trusting initial modes.
	if err := os.Chmod(installedWorker, 0o600); err != nil {
		t.Fatal(err)
	}
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
	sealSignedInstalledPackFixture(t, options)
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
	sealSignedInstalledPackFixture(t, options)
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

// sealSignedInstalledPackFixture only changes disposable synthetic test
// filesystem entries. Never call it on local runtime-lab or product installs.
func sealSignedInstalledPackFixture(t *testing.T, options RuntimePackProvisionerOptions) {
	t.Helper()
	root := filepath.Join(options.DataDir, DefaultRuntimePackTargetName)
	// t.TempDir cleanup cannot recursively unlink children from unwritable
	// directories. Restore directory mode before test cleanup only.
	t.Cleanup(func() {
		_ = filepath.WalkDir(root, func(path string, e os.DirEntry, err error) error {
			if err == nil && e.IsDir() {
				_ = os.Chmod(path, 0o700)
			}
			return nil
		})
	})
	if err := filepath.WalkDir(root, func(path string, e os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		var mode os.FileMode = 0o444
		if e.IsDir() || info.Mode().Perm()&0o111 != 0 {
			mode = 0o555
		}
		return os.Chmod(path, mode)
	}); err != nil {
		t.Fatalf("seal synthetic installed test tree: %v", err)
	}
}

func TestSignedInstalledLaunchGuardRefusesMutableInstalledTreeModes(t *testing.T) {
	options, signed, trust := newSignedPackFixture(t)
	backend, err := NewSignedRuntimePackPreparationBackend(options, signed, trust)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := backend.Prepare(context.Background()); err != nil || !got.TransactionCommitted {
		t.Fatalf("setup: %+v, %v", got, err)
	}
	models, state := t.TempDir(), t.TempDir()
	if _, err := backend.BuildSignedInstalledWorkerLaunchSpec(context.Background(), models, state); !errors.Is(err, ErrRuntimePackNotReadOnly) {
		t.Fatalf("unsealed installed pack accepted: %v", err)
	}
	sealSignedInstalledPackFixture(t, options)
	spec, err := backend.BuildSignedInstalledWorkerLaunchSpec(context.Background(), models, state)
	if err != nil {
		t.Fatal(err)
	}
	if err := spec.VerifyBeforeSpawn(context.Background()); err != nil {
		t.Fatal(err)
	}

	root := filepath.Join(options.DataDir, DefaultRuntimePackTargetName)
	for _, tc := range []struct {
		name, path string
		mode       os.FileMode
	}{
		{"writable file", filepath.Join(root, signed.Manifest.Worker.Entrypoint), 0o664},
		{"writable directory", root, 0o775},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.Chmod(tc.path, tc.mode); err != nil {
				t.Fatal(err)
			}
			if err := spec.VerifyBeforeSpawn(context.Background()); !errors.Is(err, ErrRuntimePackNotReadOnly) {
				t.Fatalf("writable signed-pack mode accepted: %v", err)
			}
			// Return to read-only mode without changing file contents.
			mode := os.FileMode(0o444)
			if tc.path == root {
				mode = 0o555
			}
			if err := os.Chmod(tc.path, mode); err != nil {
				t.Fatal(err)
			}
		})
	}
	if err := spec.VerifyBeforeSpawn(context.Background()); err != nil {
		t.Fatalf("read-only modes restored but still rejected: %v", err)
	}
}

func TestSignedInstalledLaunchContractDefensiveCopies(t *testing.T) {
	options, signed, trust := newSignedPackFixture(t)
	backend, err := NewSignedRuntimePackPreparationBackend(options, signed, trust)
	if err != nil {
		t.Fatal(err)
	}
	if prepared, err := backend.Prepare(context.Background()); err != nil || !prepared.TransactionCommitted {
		t.Fatalf("setup: %+v %v", prepared, err)
	}
	sealSignedInstalledPackFixture(t, options)
	spec, err := backend.BuildSignedInstalledWorkerLaunchSpec(context.Background(), t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	originalCmd := spec.CommandPath()
	originalRoot := spec.RuntimePackRootPath()
	args := spec.CommandArgs()
	if len(args) == 0 {
		t.Fatal("signed launch args missing")
	}
	args[0] = "/tmp/attacker.py"
	env := spec.ProcessEnvironment()
	if len(env) == 0 {
		t.Fatal("signed launch environment missing")
	}
	env[0] = "PATH=/tmp/attacker"
	if spec.CommandPath() != originalCmd || spec.RuntimePackRootPath() != originalRoot {
		t.Fatal("signed contract command/root changed through accessors")
	}
	if spec.CommandArgs()[0] == "/tmp/attacker.py" ||
		spec.ProcessEnvironment()[0] == "PATH=/tmp/attacker" {
		t.Fatal("signed contract allows caller to mutate launch args or env")
	}
	if err := spec.VerifyBeforeSpawn(context.Background()); err != nil {
		t.Fatalf("external accessors changed authenticated launch: %v", err)
	}
	if !spec.ArtifactsVerified() || spec.ModelsAuthorized() {
		t.Fatal("signed contract promoted model authorization or lost artifact verification")
	}
}
