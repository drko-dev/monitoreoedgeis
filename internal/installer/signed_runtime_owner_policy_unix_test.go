//go:build darwin || linux

package installer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSignedOwnerPolicyRealSameUserFixtureCannotClaimSeparation(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("privileged CI runner cannot certify independent ownership")
	}
	options, signed, trust := newSignedPackFixture(t)
	backend, err := NewSignedRuntimePackPreparationBackend(options, signed, trust)
	if err != nil {
		t.Fatal(err)
	}
	prepare, err := backend.Prepare(context.Background())
	if err != nil || !prepare.TransactionCommitted {
		t.Fatalf("fixture Prepare: %+v %v", prepare, err)
	}
	sealSignedInstalledPackFixture(t, options)
	// A file tree chmoded 0444/0555 but owned by this SAME process UID
	// is still freely re-chmodable and must fail the strict owner gate.
	if _, err := backend.AuditIndependentInstalledPackOwnership(context.Background()); !errors.Is(err, ErrRuntimePackOwnershipNotSeparated) {
		t.Fatalf("same UID pack claimed independently owned: %v", err)
	}
	if _, err := backend.BuildOwnershipSeparatedSignedWorkerLaunchSpec(
		context.Background(), t.TempDir(), t.TempDir()); !errors.Is(err, ErrRuntimePackOwnershipNotSeparated) {
		t.Fatalf("same UID signed launch accepted: %v", err)
	}
	// Legacy non-release signed fixture remains separately available:
	// its contract does not claim independent ownership or authorize models.
	spec, err := backend.BuildSignedInstalledWorkerLaunchSpec(
		context.Background(), t.TempDir(), t.TempDir())
	if err != nil || !spec.ArtifactsVerified() || spec.ModelsAuthorized() {
		t.Fatalf("lab-only signed contract regressed: %+v %v", spec, err)
	}
	workerPath := filepath.Join(options.DataDir, DefaultRuntimePackTargetName,
		signed.Manifest.Worker.Entrypoint)
	// The practical weakness that strict gate detects: user can chmod then
	// replace a file even though it was initially mode read-only.
	if err := os.Chmod(workerPath, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(workerPath, []byte("same-uid-tamper"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := spec.VerifyBeforeSpawn(context.Background()); err == nil {
		t.Fatal("post-seal same-user file tamper went undetected")
	}
}

func TestSignedOwnerPolicyCancelledAudit(t *testing.T) {
	var p *SignedRuntimePackPreparationBackend
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.AuditIndependentInstalledPackOwnership(ctx); !errors.Is(err, ErrRuntimePackSignatureRequired) {
		t.Fatalf("nil backend unexpectedly accepted: %v", err)
	}
}
