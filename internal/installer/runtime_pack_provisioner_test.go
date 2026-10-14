package installer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestTransactionalRuntimePackProvisionerDisabledDoesNotMutate(t *testing.T) {
	pack, digest, _ := writeProvisionerFixture(t, "1.0.0")
	data := filepath.Join(t.TempDir(), "runtime-data")
	p := newTestProvisioner(t, pack, data, digest)
	p.options.Enabled = false

	result, err := p.Prepare(context.Background())
	if !errors.Is(err, ErrRuntimePackProvisioningDisabled) || result.State != "DISABLED" {
		t.Fatalf("Prepare() = %+v, %v; want disabled", result, err)
	}
	if _, err := os.Stat(data); !os.IsNotExist(err) {
		t.Fatalf("disabled provisioning created persistent data: stat err = %v", err)
	}
}

func TestTransactionalRuntimePackProvisionerRequiresPinnedManifest(t *testing.T) {
	pack, _, _ := writeProvisionerFixture(t, "1.0.0")
	p := newTestProvisioner(t, pack, filepath.Join(t.TempDir(), "data"), "")
	result, err := p.Prepare(context.Background())
	if !errors.Is(err, ErrRuntimePackManifestPinRequired) || result.ErrorCode != "MANIFEST_PIN_REQUIRED" {
		t.Fatalf("Prepare() = %+v, %v; want manifest pin error", result, err)
	}
}

func TestTransactionalRuntimePackProvisionerRejectsTemplateAndPinMismatch(t *testing.T) {
	t.Run("template manifest", func(t *testing.T) {
		pack, digest, manifest := writeProvisionerFixture(t, "1.0.0")
		manifest.ManifestType = ManifestTypeTemplate
		digest = rewriteProvisionerManifest(t, pack, manifest)
		p := newTestProvisioner(t, pack, filepath.Join(t.TempDir(), "data"), digest)
		_, err := p.Prepare(context.Background())
		if !errors.Is(err, ErrRuntimePackNotRelease) {
			t.Fatalf("Prepare() error = %v; want template rejection", err)
		}
	})

	t.Run("digest pin mismatch", func(t *testing.T) {
		pack, _, _ := writeProvisionerFixture(t, "1.0.0")
		p := newTestProvisioner(t, pack, filepath.Join(t.TempDir(), "data"), strings.Repeat("a", 64))
		_, err := p.Prepare(context.Background())
		if !errors.Is(err, ErrRuntimePackManifestPinMismatch) {
			t.Fatalf("Prepare() error = %v; want pin mismatch", err)
		}
	})

	t.Run("malformed pinned manifest", func(t *testing.T) {
		pack, _, _ := writeProvisionerFixture(t, "1.0.0")
		manifestPath := filepath.Join(pack, DefaultRuntimePackManifestName)
		if err := os.WriteFile(manifestPath, []byte("{"), 0o600); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(manifestPath)
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(data)
		p := newTestProvisioner(t, pack, filepath.Join(t.TempDir(), "data"), hex.EncodeToString(digest[:]))
		_, err = p.Prepare(context.Background())
		if !errors.Is(err, ErrManifestInvalidJSON) {
			t.Fatalf("Prepare() error = %v; want malformed manifest rejection", err)
		}
	})
}

func TestTransactionalRuntimePackProvisionerRejectsCorruptAndUnlistedArtifacts(t *testing.T) {
	t.Run("missing bundled ffprobe", func(t *testing.T) {
		pack, digest, manifest := writeProvisionerFixture(t, "1.0.0")
		if err := os.Remove(filepath.Join(pack, manifest.Binaries[1].RelativePath)); err != nil {
			t.Fatal(err)
		}
		p := newTestProvisioner(t, pack, filepath.Join(t.TempDir(), "data"), digest)
		_, err := p.Prepare(context.Background())
		if !errors.Is(err, ErrArtifactMissing) {
			t.Fatalf("Prepare() error = %v; want missing bundled media rejection", err)
		}
	})

	t.Run("unsupported architecture", func(t *testing.T) {
		pack, digest, _ := writeProvisionerFixture(t, "1.0.0")
		p := newTestProvisioner(t, pack, filepath.Join(t.TempDir(), "data"), digest)
		p.options.TargetArch = ArchAmd64
		_, err := p.Prepare(context.Background())
		if !errors.Is(err, ErrManifestIncompatible) {
			t.Fatalf("Prepare() error = %v; want architecture rejection", err)
		}
	})

	t.Run("truncated declared artifact", func(t *testing.T) {
		pack, digest, manifest := writeProvisionerFixture(t, "1.0.0")
		if err := os.WriteFile(filepath.Join(pack, manifest.Python.RelativeBinaryPath), []byte("truncated"), 0o755); err != nil {
			t.Fatal(err)
		}
		p := newTestProvisioner(t, pack, filepath.Join(t.TempDir(), "data"), digest)
		_, err := p.Prepare(context.Background())
		if !errors.Is(err, ErrArtifactCorrupted) {
			t.Fatalf("Prepare() error = %v; want corruption rejection", err)
		}
	})

	t.Run("unauthorized model file", func(t *testing.T) {
		pack, digest, _ := writeProvisionerFixture(t, "1.0.0")
		if err := os.WriteFile(filepath.Join(pack, "yolo11n.pt"), []byte("not authorized weights"), 0o600); err != nil {
			t.Fatal(err)
		}
		p := newTestProvisioner(t, pack, filepath.Join(t.TempDir(), "data"), digest)
		_, err := p.Prepare(context.Background())
		if !errors.Is(err, ErrRuntimePackModelGovernance) {
			t.Fatalf("Prepare() error = %v; want model governance gate", err)
		}
	})

	t.Run("symlinked artifact", func(t *testing.T) {
		pack, digest, manifest := writeProvisionerFixture(t, "1.0.0")
		artifact := filepath.Join(pack, manifest.Worker.Entrypoint)
		outside := filepath.Join(t.TempDir(), "worker.py")
		if err := os.WriteFile(outside, []byte("synthetic worker"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(artifact); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, artifact); err != nil {
			t.Fatal(err)
		}
		p := newTestProvisioner(t, pack, filepath.Join(t.TempDir(), "data"), digest)
		_, err := p.Prepare(context.Background())
		if err == nil || !strings.Contains(err.Error(), "symlink") {
			t.Fatalf("Prepare() error = %v; want symlink rejection", err)
		}
	})
}

func TestTransactionalRuntimePackProvisionerActivatesVerifiedPackIdempotently(t *testing.T) {
	pack, digest, manifest := writeProvisionerFixture(t, "1.0.0")
	data := filepath.Join(t.TempDir(), "data")
	p := newTestProvisioner(t, pack, data, digest)

	first, err := p.Prepare(context.Background())
	if err != nil {
		t.Fatalf("first Prepare(): %v", err)
	}
	if first.State != "COMMITTED_UNCERTIFIED" || !first.TransactionCommitted || !first.RuntimeIntegrityVerified || !first.PinnedReleaseVerified {
		t.Fatalf("first activation result = %+v", first)
	}
	if first.RuntimeReady || first.InferenceReady {
		t.Fatalf("transactional integrity must not certify readiness: %+v", first)
	}
	active := filepath.Join(data, DefaultRuntimePackTargetName)
	if _, err := inspectRuntimePack(active, DefaultRuntimePackManifestName, digest, PlatformDarwin, ArchArm64); err != nil {
		t.Fatalf("active pack failed verification: %v", err)
	}
	if _, err := os.Stat(filepath.Join(data, "models")); !os.IsNotExist(err) {
		t.Fatalf("provisioning unexpectedly created model storage: %v", err)
	}

	second, err := p.Prepare(context.Background())
	if err != nil {
		t.Fatalf("repeated Prepare(): %v", err)
	}
	if !second.AlreadyActive || !second.TransactionCommitted || !second.PinnedReleaseVerified || second.RuntimeReady || second.InferenceReady {
		t.Fatalf("repeated activation was not an uncertified no-op: %+v", second)
	}
	recovered, err := p.Recover(context.Background())
	if err != nil || !recovered.RuntimeIntegrityVerified || recovered.RuntimeReady || recovered.InferenceReady {
		t.Fatalf("Recover() = %+v, %v", recovered, err)
	}
	if recovered.RuntimeVersion != manifest.RuntimeVersion {
		t.Fatalf("recovered version = %q, want %q", recovered.RuntimeVersion, manifest.RuntimeVersion)
	}
}

func TestTransactionalRuntimePackProvisionerCrashDuringUpgradePreservesOldRuntime(t *testing.T) {
	packV1, digestV1, _ := writeProvisionerFixture(t, "1.0.0")
	data := filepath.Join(t.TempDir(), "data")
	initial := newTestProvisioner(t, packV1, data, digestV1)
	if _, err := initial.Prepare(context.Background()); err != nil {
		t.Fatalf("install v1: %v", err)
	}

	packV2, digestV2, _ := writeProvisionerFixture(t, "1.0.1")
	upgrade := newTestProvisioner(t, packV2, data, digestV2)
	upgrade.recoveryManagerFactory = func(path string) (*RecoveryManager, error) {
		manager, err := NewRecoveryManager(path)
		if err != nil {
			return nil, err
		}
		manager.SetFaultInjector(&FaultInjector{AfterRename: func(from, to string) error {
			if (strings.HasPrefix(filepath.Base(from), ".staging.") || strings.Contains(from, ".staging.")) && filepath.Base(to) == DefaultRuntimePackTargetName {
				return errors.New("simulated abrupt interruption after activation rename")
			}
			return nil
		}})
		return manager, nil
	}
	failed, err := upgrade.Prepare(context.Background())
	if err == nil || failed.State != "RECOVERY_REQUIRED" || failed.RuntimeReady || failed.InferenceReady {
		t.Fatalf("interrupted upgrade = %+v, %v; want recovery required", failed, err)
	}

	upgrade.recoveryManagerFactory = func(path string) (*RecoveryManager, error) {
		manager, err := NewRecoveryManager(path)
		if err != nil {
			return nil, err
		}
		manager.SetFaultInjector(&FaultInjector{BeforeRename: func(from, to string) error {
			if strings.Contains(filepath.Base(from), ".backup.") && filepath.Base(to) == DefaultRuntimePackTargetName {
				return errors.New("simulated interruption during backup restoration")
			}
			return nil
		}})
		return manager, nil
	}
	interruptedRollback, err := upgrade.Recover(context.Background())
	if err == nil || interruptedRollback.State != "RECOVERY_REQUIRED" {
		t.Fatalf("interrupted rollback = %+v, %v; want recovery required", interruptedRollback, err)
	}
	backups, err := filepath.Glob(filepath.Join(data, ".backup.full-edge-runtime.*"))
	if err != nil || len(backups) != 1 {
		t.Fatalf("backup must remain available after interrupted recovery: backups=%v err=%v", backups, err)
	}

	upgrade.recoveryManagerFactory = NewRecoveryManager
	recovered, err := upgrade.Recover(context.Background())
	if err != nil || recovered.State != "ROLLED_BACK_UNCERTIFIED" || recovered.TransactionCommitted || recovered.PinnedReleaseVerified || recovered.RuntimeReady || recovered.InferenceReady {
		t.Fatalf("recovery against newer pack pin = %+v, %v; want verified old runtime but uncertified current release", recovered, err)
	}
	active, verifyErr := inspectRuntimePack(filepath.Join(data, DefaultRuntimePackTargetName), DefaultRuntimePackManifestName, digestV1, PlatformDarwin, ArchArm64)
	if verifyErr != nil || active.manifest.RuntimeVersion != "1.0.0" {
		t.Fatalf("prior runtime was not restored: manifest=%+v err=%v", active.manifest, verifyErr)
	}
	repeatedRecovery, err := upgrade.Recover(context.Background())
	if err != nil || repeatedRecovery.State != "ROLLED_BACK_UNCERTIFIED" || repeatedRecovery.RuntimeReady {
		t.Fatalf("repeated recovery = %+v, %v; want idempotent non-ready recovery", repeatedRecovery, err)
	}

	// Verify that after successful rollback recovery, a subsequent Prepare cleanly upgrades to v2
	rePrepare, err := upgrade.Prepare(context.Background())
	if err != nil || rePrepare.State != "COMMITTED_UNCERTIFIED" || !rePrepare.TransactionCommitted || rePrepare.RuntimeReady {
		t.Fatalf("subsequent Prepare after recovery = %+v, %v; want successful upgrade", rePrepare, err)
	}
	activeV2, verifyV2Err := inspectRuntimePack(filepath.Join(data, DefaultRuntimePackTargetName), DefaultRuntimePackManifestName, digestV2, PlatformDarwin, ArchArm64)
	if verifyV2Err != nil || activeV2.manifest.RuntimeVersion != "1.0.1" {
		t.Fatalf("runtime was not upgraded to v2: manifest=%+v err=%v", activeV2.manifest, verifyV2Err)
	}
}

func TestTransactionalRuntimePackProvisionerDoesNotReplaceAlteredActiveRuntime(t *testing.T) {
	packV1, digestV1, manifestV1 := writeProvisionerFixture(t, "1.0.0")
	data := filepath.Join(t.TempDir(), "data")
	initial := newTestProvisioner(t, packV1, data, digestV1)
	if _, err := initial.Prepare(context.Background()); err != nil {
		t.Fatalf("install v1: %v", err)
	}
	activeWorker := filepath.Join(data, DefaultRuntimePackTargetName, manifestV1.Worker.Entrypoint)
	if err := os.WriteFile(activeWorker, []byte("altered active runtime"), 0o644); err != nil {
		t.Fatal(err)
	}

	packV2, digestV2, _ := writeProvisionerFixture(t, "1.0.1")
	upgrade := newTestProvisioner(t, packV2, data, digestV2)
	result, err := upgrade.Prepare(context.Background())
	if err == nil || result.TransactionCommitted || result.RuntimeReady || result.InferenceReady {
		t.Fatalf("altered active runtime result = %+v, %v; must fail closed", result, err)
	}
	contents, readErr := os.ReadFile(activeWorker)
	if readErr != nil || string(contents) != "altered active runtime" {
		t.Fatalf("ambiguous active artifact was modified: contents=%q err=%v", contents, readErr)
	}
}

func TestTransactionalRuntimePackProvisionerSyncFailureDoesNotCommit(t *testing.T) {
	pack, digest, _ := writeProvisionerFixture(t, "1.0.0")
	data := filepath.Join(t.TempDir(), "data")
	p := newTestProvisioner(t, pack, data, digest)
	p.recoveryManagerFactory = func(path string) (*RecoveryManager, error) {
		manager, err := NewRecoveryManager(path)
		if err == nil {
			manager.SetFaultInjector(&FaultInjector{BeforeSync: func(string) error { return errors.New("simulated fsync failure") }})
		}
		return manager, err
	}
	result, err := p.Prepare(context.Background())
	if err == nil || result.TransactionCommitted || result.RuntimeReady || result.InferenceReady {
		t.Fatalf("sync failure result = %+v, %v; must not commit", result, err)
	}
	if _, err := os.Stat(filepath.Join(data, DefaultRuntimePackTargetName)); !os.IsNotExist(err) {
		t.Fatalf("sync failure activated a runtime: %v", err)
	}
}

func TestTransactionalRuntimePackProvisionerFilesystemFailureDoesNotActivate(t *testing.T) {
	pack, digest, _ := writeProvisionerFixture(t, "1.0.0")
	data := filepath.Join(t.TempDir(), "data-is-a-file")
	if err := os.WriteFile(data, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := newTestProvisioner(t, pack, data, digest)
	result, err := p.Prepare(context.Background())
	if err == nil || result.TransactionCommitted || result.RuntimeReady || result.InferenceReady {
		t.Fatalf("filesystem failure result = %+v, %v; must fail closed", result, err)
	}
}

func TestTransactionalRuntimePackProvisionerConcurrentPrepareIsSafe(t *testing.T) {
	pack, digest, _ := writeProvisionerFixture(t, "1.0.0")
	p := newTestProvisioner(t, pack, filepath.Join(t.TempDir(), "data"), digest)
	const callers = 8
	var wg sync.WaitGroup
	errs := make(chan error, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := p.Prepare(context.Background())
			if err != nil && !errors.Is(err, ErrRecoveryLockHeld) {
				errs <- err
				return
			}
			if err == nil && (result.RuntimeReady || result.InferenceReady) {
				errs <- errors.New("concurrent preparation fabricated readiness")
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent Prepare(): %v", err)
	}
	active, err := inspectRuntimePack(filepath.Join(p.options.DataDir, DefaultRuntimePackTargetName), DefaultRuntimePackManifestName, digest, PlatformDarwin, ArchArm64)
	if err != nil || active.manifest.RuntimeVersion != "1.0.0" {
		t.Fatalf("concurrent activation did not converge to verified runtime: manifest=%+v err=%v", active.manifest, err)
	}
}

func writeProvisionerFixture(t *testing.T, version string) (string, string, *RuntimeManifest) {
	t.Helper()
	root, manifest := syntheticMediaPack(t)
	manifest.RuntimeVersion = version
	digest := rewriteProvisionerManifest(t, root, manifest)
	return root, digest, manifest
}

func rewriteProvisionerManifest(t *testing.T, root string, manifest *RuntimeManifest) string {
	t.Helper()
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, DefaultRuntimePackManifestName), data, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func newTestProvisioner(t *testing.T, pack, data, digest string) *TransactionalRuntimePackProvisioner {
	t.Helper()
	p, err := NewTransactionalRuntimePackProvisioner(RuntimePackProvisionerOptions{
		Enabled:                true,
		PackDir:                pack,
		DataDir:                data,
		ExpectedManifestSHA256: digest,
		TargetOS:               PlatformDarwin,
		TargetArch:             ArchArm64,
	})
	if err != nil {
		t.Fatal(err)
	}
	return p
}
