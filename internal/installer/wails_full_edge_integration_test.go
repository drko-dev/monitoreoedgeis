package installer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestWailsFullEdgeIntegration_FullFlow exercises the complete end-to-end integration:
// Wails Bridge -> Preparation Adapter -> Transactional Provisioner -> RecoveryManager -> Persistent Store.
// It verifies:
// 1. Initial status before preparation is uncertified (NOT_PREPARED).
// 2. Start initiates preflight, stages artifacts, and commits through H6A transaction.
// 3. Persistent filesystem layout matches the pinned manifest.
// 4. Result is COMMITTED_UNCERTIFIED, which H5B and Wails map strictly to FAILED/RUNTIME_UNCERTIFIED (never READY).
// 5. Subsequent calls with the same request ID are idempotent in-memory.
// 6. Calls with new request ID recognize the active verified runtime and return uncertified without re-staging.
// 7. Desktop restart reconstructs state from persistent transaction evidence, never claiming readiness.
// 8. Explicit recovery preserves integrity without fabricating certification.
func TestWailsFullEdgeIntegration_FullFlow(t *testing.T) {
	packDir, digest, manifest := writeProvisionerFixture(t, "1.0.0")
	dataDir := filepath.Join(t.TempDir(), "persistent-data")

	provisioner := newTestProvisioner(t, packDir, dataDir, digest)
	adapter, err := NewRuntimePackPreparationAdapter(provisioner, true)
	if err != nil {
		t.Fatalf("NewRuntimePackPreparationAdapter error = %v", err)
	}
	bridge := NewWailsSafeFullEdgePreparationAPI(adapter)

	ctx := context.Background()

	// 1. Initial State Check via Wails Bridge
	initialSnap, err := bridge.Status(ctx)
	if err != nil {
		t.Fatalf("bridge.Status initial failed: %v", err)
	}
	if initialSnap.State != StateNotPrepared || !initialSnap.CanStart || initialSnap.CertifiedReady() || initialSnap.Certification != nil {
		t.Fatalf("unexpected initial snapshot: %+v", initialSnap)
	}
	if err := ValidateFullEdgePreparationSnapshot(initialSnap); err != nil {
		t.Fatalf("initial snapshot failed validation: %v", err)
	}

	// 2. Full Preparation Flow via Wails Bridge
	prepSnap, err := bridge.Start(ctx, "req-flow-100")
	if err != nil {
		t.Fatalf("bridge.Start failed: %v", err)
	}
	// Under H5B, transactionally committed pack maps to FAILED / RUNTIME_UNCERTIFIED
	// because offline inference functional certification is separate and pending.
	if prepSnap.State != StateFailed || prepSnap.ErrorCode != "RUNTIME_UNCERTIFIED" || prepSnap.ProgressPercent != 80 {
		t.Fatalf("expected uncertified committed state, got: %+v", prepSnap)
	}
	if prepSnap.CertifiedReady() || prepSnap.Certification != nil {
		t.Fatalf("FAIL-CLOSED VIOLATION: preparation snapshot claimed readiness: %+v", prepSnap)
	}
	if err := ValidateFullEdgePreparationSnapshot(prepSnap); err != nil {
		t.Fatalf("prep snapshot failed validation: %v", err)
	}

	// 3. Verify Persistent Filesystem State on Disk
	activePath := filepath.Join(dataDir, DefaultRuntimePackTargetName)
	inspected, err := inspectRuntimePack(activePath, DefaultRuntimePackManifestName, digest, PlatformDarwin, ArchArm64)
	if err != nil {
		t.Fatalf("active runtime pack failed inspection on disk: %v", err)
	}
	if inspected.manifest.RuntimeVersion != manifest.RuntimeVersion {
		t.Fatalf("active runtime version = %q, want %q", inspected.manifest.RuntimeVersion, manifest.RuntimeVersion)
	}
	if inspected.manifestSHA256 != digest {
		t.Fatalf("active manifest hash = %q, want %q", inspected.manifestSHA256, digest)
	}

	// 4. In-Memory Idempotency with Same Request ID
	dupSnap, err := bridge.Start(ctx, "req-flow-100")
	if err != nil {
		t.Fatalf("idempotent start error: %v", err)
	}
	if dupSnap.State != prepSnap.State || dupSnap.ErrorCode != prepSnap.ErrorCode || dupSnap.ProgressPercent != prepSnap.ProgressPercent {
		t.Fatalf("idempotent snapshot mismatch: got %+v, want %+v", dupSnap, prepSnap)
	}

	// 5. Repeated Start with Different Request ID recognizes active pack
	nextSnap, err := bridge.Start(ctx, "req-flow-200")
	if err != nil {
		t.Fatalf("subsequent start error: %v", err)
	}
	if nextSnap.State != StateFailed || nextSnap.ErrorCode != "RUNTIME_UNCERTIFIED" || nextSnap.CertifiedReady() {
		t.Fatalf("subsequent start unexpectedly claimed ready: %+v", nextSnap)
	}

	// 6. Bridge Diagnostic Check
	diag, err := bridge.Diagnostic(ctx, "req-flow-100")
	if err != nil {
		t.Fatalf("bridge.Diagnostic error: %v", err)
	}
	if diag.Code != "RUNTIME_UNCERTIFIED" {
		t.Fatalf("diagnostic code = %q, want RUNTIME_UNCERTIFIED", diag.Code)
	}
	if len(diag.Checks) < 2 {
		t.Fatalf("diagnostic checks incomplete: %+v", diag.Checks)
	}

	// 7. Desktop Restart Simulation (reconstructed from H6A persistent journal & active directory)
	restartedAdapter, err := NewRuntimePackPreparationAdapter(provisioner, true)
	if err != nil {
		t.Fatalf("restart adapter creation error: %v", err)
	}
	recoveryCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	restoredSnap, err := restartedAdapter.RestoreFromTransaction(recoveryCtx)
	if err != nil {
		t.Fatalf("RestoreFromTransaction error: %v", err)
	}
	if restoredSnap.State != StateFailed || restoredSnap.ErrorCode != "RUNTIME_UNCERTIFIED" || restoredSnap.CertifiedReady() {
		t.Fatalf("restored snapshot claimed readiness: %+v", restoredSnap)
	}
	if restoredSnap.OperationID != "" {
		t.Fatalf("restored snapshot fabricated operation ID: %q", restoredSnap.OperationID)
	}

	restartedBridge := NewWailsSafeFullEdgePreparationAPI(restartedAdapter)
	restartedStatus, err := restartedBridge.Status(ctx)
	if err != nil {
		t.Fatalf("restarted bridge.Status error: %v", err)
	}
	if restartedStatus.State != StateFailed || restartedStatus.ErrorCode != "RUNTIME_UNCERTIFIED" || restartedStatus.CertifiedReady() {
		t.Fatalf("restarted status snapshot claimed readiness: %+v", restartedStatus)
	}

	// 8. Explicit Recovery via Bridge
	recSnap, err := restartedBridge.Recover(ctx, "op-recover-flow")
	if err != nil {
		t.Fatalf("explicit recovery error: %v", err)
	}
	if recSnap.State != StateFailed || recSnap.ErrorCode != "RUNTIME_UNCERTIFIED" || recSnap.CertifiedReady() {
		t.Fatalf("recovered snapshot claimed readiness: %+v", recSnap)
	}
	if err := ValidateFullEdgePreparationSnapshot(recSnap); err != nil {
		t.Fatalf("recovered snapshot failed validation: %v", err)
	}
}

// TestWailsFullEdgeIntegration_Cancellation exercises cancellation semantics across the Wails bridge:
//  1. Direct Cancel on an idle or active adapter is explicitly rejected to avoid corrupting filesystem transactions.
//  2. Cancellation via Context correctly halts preparation, yields StateCancelled with PREPARATION_CANCELLED,
//     allows subsequent start (CanStart=true), and never emits READY or RUNTIME_READY.
func TestWailsFullEdgeIntegration_Cancellation(t *testing.T) {
	packDir, digest, _ := writeProvisionerFixture(t, "1.0.0")
	dataDir := filepath.Join(t.TempDir(), "cancel-data")

	provisioner := newTestProvisioner(t, packDir, dataDir, digest)
	adapter, err := NewRuntimePackPreparationAdapter(provisioner, true)
	if err != nil {
		t.Fatalf("adapter init: %v", err)
	}
	bridge := NewWailsSafeFullEdgePreparationAPI(adapter)

	// 1. Direct Cancel via Bridge
	cancelSnap, err := bridge.Cancel(context.Background(), "op-cancel-attempt")
	if err == nil {
		t.Fatal("expected Cancel() to return an error since transactional cancellation is unsupported")
	}
	if !strings.Contains(err.Error(), "cancellation is not supported") {
		t.Fatalf("unexpected cancel error: %v", err)
	}
	if cancelSnap.State == StateReady || cancelSnap.CertifiedReady() {
		t.Fatalf("cancel snapshot falsely reported ready: %+v", cancelSnap)
	}

	// 2. Context Cancellation during Preparation
	cancelledCtx, cancelFn := context.WithCancel(context.Background())
	cancelFn() // Pre-cancel context

	snap, err := bridge.Start(cancelledCtx, "req-ctx-cancel")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled error, got: %v", err)
	}
	if snap.State != StateCancelled || snap.ErrorCode != "PREPARATION_CANCELLED" {
		t.Fatalf("expected StateCancelled with PREPARATION_CANCELLED, got: %+v", snap)
	}
	if !snap.CanStart {
		t.Fatalf("cancelled snapshot must allow restarting (CanStart=true): %+v", snap)
	}
	if snap.CertifiedReady() || snap.Certification != nil {
		t.Fatalf("cancelled snapshot claimed readiness: %+v", snap)
	}
	if err := ValidateFullEdgePreparationSnapshot(snap); err != nil {
		t.Fatalf("cancelled snapshot failed validation: %v", err)
	}

	// Ensure no active pack was installed in persistent storage
	activePath := filepath.Join(dataDir, DefaultRuntimePackTargetName)
	if _, err := os.Stat(activePath); !os.IsNotExist(err) {
		t.Fatalf("active pack directory was created despite context cancellation: stat err=%v", err)
	}
}

// TestWailsFullEdgeIntegration_AbruptActivationCrashAndRecovery simulates an abrupt interruption
// (power failure / crash) during the critical activation rename step, and verifies that:
// 1. The bridge reports RECOVERY_REQUIRED and fails closed.
// 2. Startup reconciliation repairs the journal and restores verified prior state.
// 3. No intermediate or broken state ever emits READY.
// 4. Subsequent retry after recovery cleanly upgrades to the target version.
func TestWailsFullEdgeIntegration_AbruptActivationCrashAndRecovery(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "crash-data")

	// Phase 1: Install initial version 1.0.0 cleanly
	packV1, digestV1, manifestV1 := writeProvisionerFixture(t, "1.0.0")
	provV1 := newTestProvisioner(t, packV1, dataDir, digestV1)
	adapterV1, _ := NewRuntimePackPreparationAdapter(provV1, true)
	bridgeV1 := NewWailsSafeFullEdgePreparationAPI(adapterV1)

	if _, err := bridgeV1.Start(context.Background(), "req-v1"); err != nil {
		t.Fatalf("v1 installation failed: %v", err)
	}

	// Phase 2: Attempt upgrade to v1.0.1 with an injected crash during the activation rename
	packV2, digestV2, manifestV2 := writeProvisionerFixture(t, "1.0.1")
	provV2 := newTestProvisioner(t, packV2, dataDir, digestV2)
	provV2.recoveryManagerFactory = func(path string) (*RecoveryManager, error) {
		manager, err := NewRecoveryManager(path)
		if err != nil {
			return nil, err
		}
		manager.SetFaultInjector(&FaultInjector{AfterRename: func(from, to string) error {
			if strings.HasPrefix(filepath.Base(from), ".staging.") && filepath.Base(to) == DefaultRuntimePackTargetName {
				return errors.New("simulated abrupt power failure after activation rename")
			}
			return nil
		}})
		return manager, nil
	}

	adapterV2, _ := NewRuntimePackPreparationAdapter(provV2, true)
	bridgeV2 := NewWailsSafeFullEdgePreparationAPI(adapterV2)

	crashSnap, err := bridgeV2.Start(context.Background(), "req-v2-crash")
	if err == nil {
		t.Fatal("expected injected crash error during upgrade, got nil")
	}
	if crashSnap.State != StateRecoveryRequired || !crashSnap.RecoveryAvailable {
		t.Fatalf("expected RECOVERY_REQUIRED snapshot after crash, got: %+v (err=%v)", crashSnap, err)
	}
	if crashSnap.CertifiedReady() || crashSnap.State == StateReady {
		t.Fatalf("FAIL-CLOSED VIOLATION: crash snapshot reported ready: %+v", crashSnap)
	}
	if err := ValidateFullEdgePreparationSnapshot(crashSnap); err != nil {
		t.Fatalf("crash snapshot failed validation: %v", err)
	}

	// Phase 3: Desktop restart and startup reconciliation
	// New clean provisioner without fault injector
	cleanProvV2 := newTestProvisioner(t, packV2, dataDir, digestV2)
	restartAdapter, _ := NewRuntimePackPreparationAdapter(cleanProvV2, true)
	recoveryCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	restoredSnap, err := restartAdapter.RestoreFromTransaction(recoveryCtx)
	// Because rollback restored v1.0.0, but current provisioner is pinned to v1.0.1,
	// Recover() returns ROLLED_BACK_UNCERTIFIED, which maps to StateRecoveryRequired.
	if restoredSnap.State != StateRecoveryRequired || !restoredSnap.RecoveryAvailable {
		t.Fatalf("restored snapshot after crash recovery must report RECOVERY_REQUIRED: %+v (err=%v)", restoredSnap, err)
	}
	if restoredSnap.CertifiedReady() {
		t.Fatalf("restored snapshot claimed readiness: %+v", restoredSnap)
	}

	restartBridge := NewWailsSafeFullEdgePreparationAPI(restartAdapter)
	statusSnap, err := restartBridge.Status(context.Background())
	if err != nil {
		t.Fatalf("status after restart failed: %v", err)
	}
	if statusSnap.State != StateRecoveryRequired || !statusSnap.RecoveryAvailable || statusSnap.CertifiedReady() {
		t.Fatalf("status snapshot after crash recovery is unsafe: %+v", statusSnap)
	}

	// Verify that the restored active directory is indeed valid v1.0.0
	activePath := filepath.Join(dataDir, DefaultRuntimePackTargetName)
	inspectedActive, err := inspectRuntimePack(activePath, DefaultRuntimePackManifestName, digestV1, PlatformDarwin, ArchArm64)
	if err != nil || inspectedActive.manifest.RuntimeVersion != manifestV1.RuntimeVersion {
		t.Fatalf("prior v1 runtime was not restored on disk: %+v (err=%v)", inspectedActive, err)
	}

	// Phase 4: Retry upgrade cleanly without crashes
	retrySnap, err := restartBridge.Start(context.Background(), "req-v2-retry")
	if err != nil {
		t.Fatalf("clean upgrade retry failed: %v", err)
	}
	if retrySnap.State != StateFailed || retrySnap.ErrorCode != "RUNTIME_UNCERTIFIED" || retrySnap.CertifiedReady() {
		t.Fatalf("retry snapshot unsafe: %+v", retrySnap)
	}

	// Verify that v1.0.1 is now active on disk
	inspectedV2, err := inspectRuntimePack(activePath, DefaultRuntimePackManifestName, digestV2, PlatformDarwin, ArchArm64)
	if err != nil || inspectedV2.manifest.RuntimeVersion != manifestV2.RuntimeVersion {
		t.Fatalf("v2 runtime not active after retry: %+v (err=%v)", inspectedV2, err)
	}
}

// TestWailsFullEdgeIntegration_CorruptedManifestAndArtifacts tests preflight and transaction rejection
// when packs are malformed, missing, tampered, or contain unauthorized assets.
func TestWailsFullEdgeIntegration_CorruptedManifestAndArtifacts(t *testing.T) {
	t.Run("manifest digest mismatch against release pin", func(t *testing.T) {
		packDir, _, _ := writeProvisionerFixture(t, "1.0.0")
		dataDir := filepath.Join(t.TempDir(), "mismatch-data")
		bogusPin := strings.Repeat("f", 64)

		prov := newTestProvisioner(t, packDir, dataDir, bogusPin)
		adapter, _ := NewRuntimePackPreparationAdapter(prov, true)
		bridge := NewWailsSafeFullEdgePreparationAPI(adapter)

		snap, err := bridge.Start(context.Background(), "req-pin-mismatch")
		if !errors.Is(err, ErrRuntimePackManifestPinMismatch) {
			t.Fatalf("expected ErrRuntimePackManifestPinMismatch, got: %v", err)
		}
		if snap.State != StateFailed || snap.ErrorCode != "MANIFEST_PIN_MISMATCH" || snap.CertifiedReady() {
			t.Fatalf("unsafe snapshot on pin mismatch: %+v", snap)
		}
		if err := ValidateFullEdgePreparationSnapshot(snap); err != nil {
			t.Fatalf("snapshot invalid: %v", err)
		}
	})

	t.Run("corrupt JSON manifest", func(t *testing.T) {
		packDir, _, _ := writeProvisionerFixture(t, "1.0.0")
		dataDir := filepath.Join(t.TempDir(), "json-err-data")
		manifestPath := filepath.Join(packDir, DefaultRuntimePackManifestName)

		corruptData := []byte(`{"version": "1.0.0", invalid-json`)
		if err := os.WriteFile(manifestPath, corruptData, 0o600); err != nil {
			t.Fatal(err)
		}
		digestBytes := sha256.Sum256(corruptData)
		matchingPin := hex.EncodeToString(digestBytes[:])

		prov := newTestProvisioner(t, packDir, dataDir, matchingPin)
		adapter, _ := NewRuntimePackPreparationAdapter(prov, true)
		bridge := NewWailsSafeFullEdgePreparationAPI(adapter)

		snap, err := bridge.Start(context.Background(), "req-json-corrupt")
		if !errors.Is(err, ErrManifestInvalidJSON) {
			t.Fatalf("expected ErrManifestInvalidJSON, got: %v", err)
		}
		if snap.State != StateFailed || snap.ErrorCode != "MANIFEST_INVALID_JSON" || snap.CertifiedReady() {
			t.Fatalf("unsafe snapshot on corrupt manifest: %+v", snap)
		}
		if err := ValidateFullEdgePreparationSnapshot(snap); err != nil {
			t.Fatalf("snapshot invalid: %v", err)
		}
	})

	t.Run("missing declared binary artifact", func(t *testing.T) {
		packDir, digest, manifest := writeProvisionerFixture(t, "1.0.0")
		dataDir := filepath.Join(t.TempDir(), "missing-data")

		// Remove ffmpeg
		ffmpegPath := filepath.Join(packDir, manifest.Binaries[0].RelativePath)
		if err := os.Remove(ffmpegPath); err != nil {
			t.Fatal(err)
		}

		prov := newTestProvisioner(t, packDir, dataDir, digest)
		adapter, _ := NewRuntimePackPreparationAdapter(prov, true)
		bridge := NewWailsSafeFullEdgePreparationAPI(adapter)

		snap, err := bridge.Start(context.Background(), "req-missing-artifact")
		if !errors.Is(err, ErrArtifactMissing) {
			t.Fatalf("expected ErrArtifactMissing, got: %v", err)
		}
		if snap.State != StateFailed || snap.ErrorCode != "ARTIFACT_MISSING" || snap.CertifiedReady() {
			t.Fatalf("unsafe snapshot on missing artifact: %+v", snap)
		}
	})

	t.Run("tampered artifact content (hash mismatch)", func(t *testing.T) {
		packDir, digest, manifest := writeProvisionerFixture(t, "1.0.0")
		dataDir := filepath.Join(t.TempDir(), "tampered-data")

		// Tamper with worker entrypoint
		workerPath := filepath.Join(packDir, manifest.Worker.Entrypoint)
		if err := os.WriteFile(workerPath, []byte("tampered worker code"), 0o644); err != nil {
			t.Fatal(err)
		}

		prov := newTestProvisioner(t, packDir, dataDir, digest)
		adapter, _ := NewRuntimePackPreparationAdapter(prov, true)
		bridge := NewWailsSafeFullEdgePreparationAPI(adapter)

		snap, err := bridge.Start(context.Background(), "req-tampered-artifact")
		if !errors.Is(err, ErrArtifactCorrupted) {
			t.Fatalf("expected ErrArtifactCorrupted, got: %v", err)
		}
		if snap.State != StateFailed || snap.ErrorCode != "ARTIFACT_CORRUPTED" || snap.CertifiedReady() {
			t.Fatalf("unsafe snapshot on tampered artifact: %+v", snap)
		}
	})

	t.Run("unauthorized model weights in pack", func(t *testing.T) {
		packDir, digest, _ := writeProvisionerFixture(t, "1.0.0")
		dataDir := filepath.Join(t.TempDir(), "model-gov-data")

		// Inject unapproved model file
		modelPath := filepath.Join(packDir, "yolo11n.pt")
		if err := os.WriteFile(modelPath, []byte("unapproved weights"), 0o600); err != nil {
			t.Fatal(err)
		}

		prov := newTestProvisioner(t, packDir, dataDir, digest)
		adapter, _ := NewRuntimePackPreparationAdapter(prov, true)
		bridge := NewWailsSafeFullEdgePreparationAPI(adapter)

		snap, err := bridge.Start(context.Background(), "req-unauthorized-model")
		if !errors.Is(err, ErrRuntimePackModelGovernance) {
			t.Fatalf("expected ErrRuntimePackModelGovernance, got: %v", err)
		}
		if snap.State != StateFailed || snap.ErrorCode != "MODEL_GOVERNANCE_BLOCKED" || snap.CertifiedReady() {
			t.Fatalf("unsafe snapshot on model governance: %+v", snap)
		}
	})

	t.Run("symlink traversal rejected", func(t *testing.T) {
		packDir, digest, manifest := writeProvisionerFixture(t, "1.0.0")
		dataDir := filepath.Join(t.TempDir(), "symlink-data")

		// Replace python with external symlink
		pyPath := filepath.Join(packDir, manifest.Python.RelativeBinaryPath)
		outsideFile := filepath.Join(t.TempDir(), "evil-python")
		if err := os.WriteFile(outsideFile, []byte("evil"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(pyPath); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outsideFile, pyPath); err != nil {
			t.Fatal(err)
		}

		prov := newTestProvisioner(t, packDir, dataDir, digest)
		adapter, _ := NewRuntimePackPreparationAdapter(prov, true)
		bridge := NewWailsSafeFullEdgePreparationAPI(adapter)

		snap, err := bridge.Start(context.Background(), "req-symlink")
		if err == nil || !strings.Contains(err.Error(), "symlink") {
			t.Fatalf("expected symlink rejection error, got: %v", err)
		}
		if snap.State != StateFailed || snap.CertifiedReady() {
			t.Fatalf("unsafe snapshot on symlink: %+v", snap)
		}
	})
}

// TestWailsFullEdgeIntegration_StrictNoFalseReady tests adversarial conditions and invalid snapshots
// to guarantee that the Wails bridge boundary NEVER leaks READY or RUNTIME_READY=true to the frontend.
func TestWailsFullEdgeIntegration_StrictNoFalseReady(t *testing.T) {
	// Adversarial backend that fabricates READY alongside an error
	fakeErr := errors.New("underlying failure")

	t.Run("adversarial backend returning StateReady with error", func(t *testing.T) {
		adversary := &testAdversarialPreparationAPI{
			snapshot: FullEdgePreparationSnapshot{
				State:           StateReady,
				ProgressPercent: 100,
			},
			err: fakeErr,
		}
		bridge := NewWailsSafeFullEdgePreparationAPI(adversary)

		// Test all 4 snapshot-returning methods
		ops := []struct {
			name string
			fn   func() (FullEdgePreparationSnapshot, error)
		}{
			{"Status", func() (FullEdgePreparationSnapshot, error) { return bridge.Status(context.Background()) }},
			{"Start", func() (FullEdgePreparationSnapshot, error) { return bridge.Start(context.Background(), "req-fake") }},
			{"Cancel", func() (FullEdgePreparationSnapshot, error) { return bridge.Cancel(context.Background(), "req-fake") }},
			{"Recover", func() (FullEdgePreparationSnapshot, error) { return bridge.Recover(context.Background(), "req-fake") }},
		}

		for _, op := range ops {
			t.Run(op.name, func(t *testing.T) {
				snap, err := op.fn()
				if !errors.Is(err, fakeErr) {
					t.Fatalf("backend error was lost: %v", err)
				}
				if snap.State == StateReady || snap.CertifiedReady() || snap.Certification != nil {
					t.Fatalf("FAIL-CLOSED VIOLATION: bridge leaked StateReady: %+v", snap)
				}
				if snap.State != StateFailed || snap.ErrorCode != "INVALID_PREPARATION_STATUS" {
					t.Fatalf("expected INVALID_PREPARATION_STATUS, got: %+v", snap)
				}
			})
		}
	})

	t.Run("adversarial backend returning valid certification but operation failed", func(t *testing.T) {
		certFixture := FullEdgePreparationSnapshot{
			State:           StateReady,
			ProgressPercent: 100,
			Components: func() []ComponentProgressItem {
				comps := DefaultComponentProgress()
				for i := range comps {
					comps[i].Status = ComponentStatusReady
				}
				return comps
			}(),
			Certification: &RuntimeCertification{
				CertificationID:          "synth-cert-1",
				RuntimePackVersion:       "1.0.0",
				RuntimeManifestSHA256:    strings.Repeat("a", 64),
				Platform:                 "darwin/arm64",
				EvidenceLevel:            "verified",
				RuntimePackVerified:      true,
				DependenciesVerified:     true,
				VisionWorkerReady:        true,
				FFmpegVerified:           true,
				FFprobeVerified:          true,
				ModelPackVersion:         "synth-models-1",
				ModelManifestSHA256:      strings.Repeat("b", 64),
				ModelAuthorized:          true,
				ModelIntegrityVerified:   true,
				InferenceSmokeTestPassed: true,
				CertifiedAt:              time.Now(),
			},
		}

		adversary := &testAdversarialPreparationAPI{snapshot: certFixture, err: fakeErr}
		bridge := NewWailsSafeFullEdgePreparationAPI(adversary)

		snap, err := bridge.Status(context.Background())
		if !errors.Is(err, fakeErr) {
			t.Fatalf("backend error lost: %v", err)
		}
		if snap.State == StateReady || snap.CertifiedReady() || snap.Certification != nil {
			t.Fatalf("FAIL-CLOSED VIOLATION: bridge leaked certification despite error: %+v", snap)
		}
		if snap.State != StateFailed || snap.ErrorCode != "PREPARATION_BACKEND_ERROR" {
			t.Fatalf("expected PREPARATION_BACKEND_ERROR, got: %+v", snap)
		}
	})

	t.Run("nil inner API defaults to UnavailableFullEdgePreparation", func(t *testing.T) {
		bridge := NewWailsSafeFullEdgePreparationAPI(nil)
		snap, err := bridge.Status(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if snap.State != StateFailed || snap.ErrorCode != "PREPARATION_NOT_INTEGRATED" || snap.CertifiedReady() {
			t.Fatalf("nil bridge did not fail closed: %+v", snap)
		}
	})
}

// TestWailsFullEdgeIntegration_IdempotentRecoveryReboot tests that repeated restart cycles,
// recovery invocations, and status queries remain completely idempotent, deterministic,
// and thread-safe without data races or lock leaks.
func TestWailsFullEdgeIntegration_IdempotentRecoveryReboot(t *testing.T) {
	packDir, digest, _ := writeProvisionerFixture(t, "1.0.0")
	dataDir := filepath.Join(t.TempDir(), "idempotent-reboot-data")

	prov := newTestProvisioner(t, packDir, dataDir, digest)
	adapter, err := NewRuntimePackPreparationAdapter(prov, true)
	if err != nil {
		t.Fatal(err)
	}
	bridge := NewWailsSafeFullEdgePreparationAPI(adapter)

	// Initial installation
	snap, err := bridge.Start(context.Background(), "req-init")
	if err != nil {
		t.Fatalf("start error: %v", err)
	}
	if snap.State != StateFailed || snap.ErrorCode != "RUNTIME_UNCERTIFIED" {
		t.Fatalf("initial install state unexpected: %+v", snap)
	}

	// Run 5 consecutive simulated restarts
	for i := 1; i <= 5; i++ {
		rebootAdapter, err := NewRuntimePackPreparationAdapter(prov, true)
		if err != nil {
			t.Fatalf("cycle %d: adapter init error: %v", i, err)
		}

		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		restored, err := rebootAdapter.RestoreFromTransaction(ctx)
		cancel()
		if err != nil {
			t.Fatalf("cycle %d: RestoreFromTransaction error: %v", i, err)
		}
		if restored.State != StateFailed || restored.ErrorCode != "RUNTIME_UNCERTIFIED" || restored.CertifiedReady() {
			t.Fatalf("cycle %d: restored snapshot unexpected: %+v", i, restored)
		}

		rebootBridge := NewWailsSafeFullEdgePreparationAPI(rebootAdapter)
		status, err := rebootBridge.Status(context.Background())
		if err != nil {
			t.Fatalf("cycle %d: Status error: %v", i, err)
		}
		if status.State != StateFailed || status.ErrorCode != "RUNTIME_UNCERTIFIED" || status.CertifiedReady() {
			t.Fatalf("cycle %d: status snapshot unexpected: %+v", i, status)
		}

		// Explicit recovery must also be idempotent
		recovered, err := rebootBridge.Recover(context.Background(), "op-reboot-recover")
		if err != nil {
			t.Fatalf("cycle %d: Recover error: %v", i, err)
		}
		if recovered.State != StateFailed || recovered.ErrorCode != "RUNTIME_UNCERTIFIED" || recovered.CertifiedReady() {
			t.Fatalf("cycle %d: recovered snapshot unexpected: %+v", i, recovered)
		}
	}

	// Verify active runtime on disk was not modified or corrupted
	activePath := filepath.Join(dataDir, DefaultRuntimePackTargetName)
	inspected, err := inspectRuntimePack(activePath, DefaultRuntimePackManifestName, digest, PlatformDarwin, ArchArm64)
	if err != nil || inspected.manifest.RuntimeVersion != "1.0.0" {
		t.Fatalf("active runtime altered during reboot cycles: %+v (err=%v)", inspected, err)
	}
}

// TestWailsFullEdgeIntegration_ProductionDefaultRemainsFailClosed verifies that the default
// production preparation adapter (UnavailableFullEdgePreparation) wrapped in the Wails-safe bridge
// remains strictly fail-closed, does not allow starting preparation of untrusted local packages,
// and reports PREPARATION_NOT_INTEGRATED with no false readiness.
func TestWailsFullEdgeIntegration_ProductionDefaultRemainsFailClosed(t *testing.T) {
	bridge := NewWailsSafeFullEdgePreparationAPI(UnavailableFullEdgePreparation{})

	// 1. Status
	status, err := bridge.Status(context.Background())
	if err != nil {
		t.Fatalf("unexpected error on default status: %v", err)
	}
	if status.State != StateFailed || status.ErrorCode != "PREPARATION_NOT_INTEGRATED" || status.CanStart || status.CertifiedReady() {
		t.Fatalf("default status is not fail-closed: %+v", status)
	}
	if err := ValidateFullEdgePreparationSnapshot(status); err != nil {
		t.Fatalf("default status invalid: %v", err)
	}

	// 2. Start must be rejected with ErrPreparationNotWired
	startSnap, err := bridge.Start(context.Background(), "req-prod-attempt")
	if !errors.Is(err, ErrPreparationNotWired) {
		t.Fatalf("expected ErrPreparationNotWired, got: %v", err)
	}
	if startSnap.State != StateFailed || startSnap.ErrorCode != "PREPARATION_NOT_INTEGRATED" || startSnap.CertifiedReady() {
		t.Fatalf("default start is not fail-closed: %+v", startSnap)
	}

	// 3. Cancel must be rejected
	cancelSnap, err := bridge.Cancel(context.Background(), "op-prod-attempt")
	if !errors.Is(err, ErrPreparationNotWired) {
		t.Fatalf("expected ErrPreparationNotWired on Cancel, got: %v", err)
	}
	if cancelSnap.CertifiedReady() {
		t.Fatalf("default cancel claimed ready: %+v", cancelSnap)
	}

	// 4. Recover must be rejected
	recSnap, err := bridge.Recover(context.Background(), "op-prod-attempt")
	if !errors.Is(err, ErrPreparationNotWired) {
		t.Fatalf("expected ErrPreparationNotWired on Recover, got: %v", err)
	}
	if recSnap.CertifiedReady() {
		t.Fatalf("default recover claimed ready: %+v", recSnap)
	}

	// 5. Diagnostics report non-integrated state
	diag, err := bridge.Diagnostic(context.Background(), "op-prod-attempt")
	if err != nil {
		t.Fatalf("unexpected diagnostic error: %v", err)
	}
	if diag.Code != "PREPARATION_NOT_INTEGRATED" {
		t.Fatalf("diagnostic code = %q, want PREPARATION_NOT_INTEGRATED", diag.Code)
	}
}

// TestWailsFullEdgeIntegration_ConcurrentBridgeAccess verifies thread-safety when multiple UI
// callers invoke Status, Start, Diagnostic, and Recover concurrently.
func TestWailsFullEdgeIntegration_ConcurrentBridgeAccess(t *testing.T) {
	packDir, digest, _ := writeProvisionerFixture(t, "1.0.0")
	dataDir := filepath.Join(t.TempDir(), "concurrent-bridge-data")

	prov := newTestProvisioner(t, packDir, dataDir, digest)
	adapter, _ := NewRuntimePackPreparationAdapter(prov, true)
	bridge := NewWailsSafeFullEdgePreparationAPI(adapter)

	// Perform initial installation so concurrent Recover/Start/Status operate on a valid repository
	if _, err := bridge.Start(context.Background(), "concurrent-init"); err != nil {
		t.Fatalf("initial install error: %v", err)
	}

	const callers = 10
	var wg sync.WaitGroup
	errs := make(chan error, callers*4)

	for i := 0; i < callers; i++ {
		wg.Add(4)

		go func(id int) {
			defer wg.Done()
			s, err := bridge.Status(context.Background())
			if err != nil {
				errs <- err
			}
			if s.CertifiedReady() || s.State == StateReady {
				errs <- errors.New("concurrent status returned READY")
			}
		}(i)

		go func(id int) {
			defer wg.Done()
			s, err := bridge.Start(context.Background(), "concurrent-req-1")
			if err != nil && !errors.Is(err, ErrPreparationInProgress) && !errors.Is(err, ErrRecoveryLockHeld) {
				errs <- err
			}
			if s.CertifiedReady() || s.State == StateReady {
				errs <- errors.New("concurrent start returned READY")
			}
		}(i)

		go func(id int) {
			defer wg.Done()
			_, err := bridge.Diagnostic(context.Background(), "concurrent-req-1")
			if err != nil {
				errs <- err
			}
		}(i)

		go func(id int) {
			defer wg.Done()
			s, err := bridge.Recover(context.Background(), "concurrent-op-1")
			if err != nil && !errors.Is(err, ErrPreparationInProgress) && !errors.Is(err, ErrRecoveryLockHeld) {
				errs <- err
			}
			if s.CertifiedReady() || s.State == StateReady {
				errs <- errors.New("concurrent recover returned READY")
			}
		}(i)
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("concurrent bridge access error: %v", err)
	}
}

// testAdversarialPreparationAPI is a mock used to test that WailsSafeFullEdgePreparationAPI
// intercepts and sanitizes corrupted or maliciously crafted snapshots.
type testAdversarialPreparationAPI struct {
	snapshot FullEdgePreparationSnapshot
	err      error
}

func (m *testAdversarialPreparationAPI) Status(context.Context) (FullEdgePreparationSnapshot, error) {
	return m.snapshot, m.err
}
func (m *testAdversarialPreparationAPI) Start(context.Context, string) (FullEdgePreparationSnapshot, error) {
	return m.snapshot, m.err
}
func (m *testAdversarialPreparationAPI) Cancel(context.Context, string) (FullEdgePreparationSnapshot, error) {
	return m.snapshot, m.err
}
func (m *testAdversarialPreparationAPI) Diagnostic(context.Context, string) (FullEdgePreparationDiagnostic, error) {
	return FullEdgePreparationDiagnostic{Code: "ADVERSARIAL"}, m.err
}
func (m *testAdversarialPreparationAPI) Recover(context.Context, string) (FullEdgePreparationSnapshot, error) {
	return m.snapshot, m.err
}
