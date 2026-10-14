package installer

import (
	"context"
	"errors"
	"sync"
	"testing"
)

type testRuntimePackBackend struct {
	mu            sync.Mutex
	prepares      int
	recovers      int
	prepareResult RuntimePackProvisioningResult
	prepareError  error
	recoverResult RuntimePackProvisioningResult
	recoverError  error
}

func (b *testRuntimePackBackend) Prepare(context.Context) (RuntimePackProvisioningResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.prepares++
	return b.prepareResult, b.prepareError
}
func (b *testRuntimePackBackend) Recover(context.Context) (RuntimePackProvisioningResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.recovers++
	return b.recoverResult, b.recoverError
}

func TestH5BPreparationAdapterRequiresExplicitOptIn(t *testing.T) {
	b := &testRuntimePackBackend{}
	if _, err := NewRuntimePackPreparationAdapter(b, false); !errors.Is(err, ErrRuntimePackProvisioningDisabled) {
		t.Fatalf("disabled adapter = %v", err)
	}
	if _, err := NewRuntimePackPreparationAdapter(nil, true); err == nil {
		t.Fatal("accepted nil backend")
	}
	if b.prepares != 0 {
		t.Fatal("backend invoked in disabled mode")
	}
}

func TestH5BPreparationAdapterNeverCertifiesTransactionalIntegrityAsReady(t *testing.T) {
	b := &testRuntimePackBackend{prepareResult: RuntimePackProvisioningResult{State: "COMMITTED_UNCERTIFIED", TransactionCommitted: true, RuntimeIntegrityVerified: true}}
	a, err := NewRuntimePackPreparationAdapter(b, true)
	if err != nil {
		t.Fatal(err)
	}
	before, err := a.Status(context.Background())
	if err != nil || before.State != StateNotPrepared || !before.CanStart {
		t.Fatalf("before=%+v err=%v", before, err)
	}
	first, err := a.Start(context.Background(), "req-1")
	if err != nil || first.State != StateFailed || first.ErrorCode != "RUNTIME_UNCERTIFIED" || first.CertifiedReady() || first.Certification != nil {
		t.Fatalf("unsafe success=%+v err=%v", first, err)
	}
	if err := ValidateFullEdgePreparationSnapshot(first); err != nil {
		t.Fatal(err)
	}
	again, err := a.Start(context.Background(), "req-1")
	if err != nil || again.State != StateFailed || b.prepares != 1 {
		t.Fatalf("not idempotent: snap=%+v err=%v count=%d", again, err, b.prepares)
	}
}

func TestH5BPreparationAdapterRecoveryNeverClaimsReady(t *testing.T) {
	b := &testRuntimePackBackend{recoverResult: RuntimePackProvisioningResult{State: "ROLLED_BACK_UNCERTIFIED", ErrorCode: "ACTIVE_RUNTIME_NOT_VERIFIED"}, recoverError: errors.New("new release pin mismatch")}
	a, _ := NewRuntimePackPreparationAdapter(b, true)
	snap, err := a.Recover(context.Background(), "restore-1")
	if err == nil || snap.State != StateRecoveryRequired || !snap.RecoveryAvailable || snap.CertifiedReady() {
		t.Fatalf("recovery=%+v err=%v", snap, err)
	}
	if err := ValidateFullEdgePreparationSnapshot(snap); err != nil {
		t.Fatal(err)
	}
	diag, err := a.Diagnostic(context.Background(), "restore-1")
	if err != nil || len(diag.Checks) != 2 {
		t.Fatalf("diagnostic=%+v err=%v", diag, err)
	}
}

func TestH5BPreparationAdapterCancellationNeverPretendsSuccess(t *testing.T) {
	b := &testRuntimePackBackend{}
	a, _ := NewRuntimePackPreparationAdapter(b, true)
	snap, err := a.Cancel(context.Background(), "request-1")
	if err == nil || snap.State != StateNotPrepared || b.prepares != 0 {
		t.Fatalf("cancel=%+v err=%v", snap, err)
	}
}
