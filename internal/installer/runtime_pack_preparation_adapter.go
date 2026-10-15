package installer

import (
	"context"
	"errors"
	"strings"
	"sync"
)

// RuntimePackPreparationBackend is the explicitly injected H6B transactional
// backend. The default desktop adapter remains UnavailableFullEdgePreparation.
type RuntimePackPreparationBackend interface {
	Prepare(context.Context) (RuntimePackProvisioningResult, error)
	Recover(context.Context) (RuntimePackProvisioningResult, error)
}

// RuntimePackPreparationAdapter exposes a fail-closed H5A view of H6B.
// It never issues READY without subsequent independent functional certification.
type RuntimePackPreparationAdapter struct {
	mu          sync.Mutex
	backend     RuntimePackPreparationBackend
	snapshot    FullEdgePreparationSnapshot
	running     bool
	requestID   string
	completedID string
}

func NewRuntimePackPreparationAdapter(backend RuntimePackPreparationBackend, optIn bool) (*RuntimePackPreparationAdapter, error) {
	if !optIn {
		return nil, ErrRuntimePackProvisioningDisabled
	}
	if backend == nil {
		return nil, errors.New("transactional runtime backend is required")
	}
	return &RuntimePackPreparationAdapter{backend: backend, snapshot: FullEdgePreparationSnapshot{
		State: StateNotPrepared, CanStart: true, DiagnosticsAvailable: true, Components: DefaultComponentProgress(),
	}}, nil
}

func (a *RuntimePackPreparationAdapter) Status(ctx context.Context) (FullEdgePreparationSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return mapPackResult(RuntimePackProvisioningResult{}, err), err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.snapshot, nil
}

// RestoreFromTransaction reconstructs state from H6A/H6B journal and active-pack
// evidence after a desktop restart. It does not trust or deserialize a UI
// snapshot. Recover may reconcile an interrupted transaction on disk, so callers
// must use it only after explicit release-owned opt-in.
func (a *RuntimePackPreparationAdapter) RestoreFromTransaction(ctx context.Context) (FullEdgePreparationSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return mapPackResult(RuntimePackProvisioningResult{}, err), err
	}
	a.mu.Lock()
	if a.running {
		snapshot := a.snapshot
		a.mu.Unlock()
		return snapshot, ErrPreparationInProgress
	}
	a.running = true
	a.snapshot = FullEdgePreparationSnapshot{
		State: StateChecking, ProgressPercent: 1,
		DiagnosticsAvailable: true, Components: DefaultComponentProgress(),
	}
	a.mu.Unlock()

	result, err := a.backend.Recover(ctx)
	snapshot := mapPackResult(result, err)
	// Recovery evidence is not a durable request-id receipt.
	snapshot.OperationID = ""
	a.mu.Lock()
	a.snapshot = snapshot
	a.running = false
	a.requestID = ""
	a.completedID = ""
	a.mu.Unlock()
	return snapshot, err
}

func (a *RuntimePackPreparationAdapter) Start(ctx context.Context, requestID string) (FullEdgePreparationSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return mapPackResult(RuntimePackProvisioningResult{}, err), err
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return FullEdgePreparationSnapshot{}, errors.New("request ID is required")
	}
	a.mu.Lock()
	if a.running {
		s := a.snapshot
		same := a.requestID == requestID
		a.mu.Unlock()
		if same {
			return s, nil
		}
		return s, ErrPreparationInProgress
	}
	if a.completedID == requestID {
		s := a.snapshot
		a.mu.Unlock()
		return s, nil
	}
	a.running = true
	a.requestID = requestID
	a.snapshot = FullEdgePreparationSnapshot{State: StateChecking, OperationID: requestID, ProgressPercent: 1, DiagnosticsAvailable: true, Components: DefaultComponentProgress()}
	a.mu.Unlock()
	result, err := a.backend.Prepare(ctx)
	next := mapPackResult(result, err)
	next.OperationID = requestID
	a.mu.Lock()
	a.snapshot = next
	a.running = false
	a.completedID = requestID
	a.requestID = ""
	a.mu.Unlock()
	return next, err
}

func (a *RuntimePackPreparationAdapter) Recover(ctx context.Context, operationID string) (FullEdgePreparationSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return mapPackResult(RuntimePackProvisioningResult{}, err), err
	}
	operationID = strings.TrimSpace(operationID)
	if operationID == "" {
		return FullEdgePreparationSnapshot{}, errors.New("operation ID is required")
	}
	a.mu.Lock()
	if a.running {
		s := a.snapshot
		a.mu.Unlock()
		return s, ErrPreparationInProgress
	}
	a.running = true
	a.requestID = operationID
	a.snapshot = FullEdgePreparationSnapshot{State: StatePreparing, OperationID: operationID, ProgressPercent: 1, DiagnosticsAvailable: true, Components: DefaultComponentProgress()}
	a.mu.Unlock()
	result, err := a.backend.Recover(ctx)
	next := mapPackResult(result, err)
	next.OperationID = operationID
	a.mu.Lock()
	a.snapshot = next
	a.running = false
	a.requestID = ""
	a.mu.Unlock()
	return next, err
}

// Cancellation during transactional filesystem activation is not yet wired.
// Do not pretend that an ongoing transaction was cancelled.
func (a *RuntimePackPreparationAdapter) Cancel(ctx context.Context, operationID string) (FullEdgePreparationSnapshot, error) {
	s, err := a.Status(ctx)
	if err != nil {
		return s, err
	}
	return s, errors.New("runtime pack cancellation is not supported; allow recovery manager to reconcile")
}

func (a *RuntimePackPreparationAdapter) Diagnostic(ctx context.Context, operationID string) (FullEdgePreparationDiagnostic, error) {
	s, err := a.Status(ctx)
	if err != nil {
		return FullEdgePreparationDiagnostic{}, err
	}
	return FullEdgePreparationDiagnostic{Code: s.ErrorCode, Summary: s.SafeMessage, Checks: []PreparationCheckResult{
		{Name: "Transactional runtime pack", Status: string(s.State), Detail: s.Stage},
		{Name: "Functional offline inference certification", Status: "not_certified"},
	}}, nil
}

func mapPackResult(result RuntimePackProvisioningResult, err error) FullEdgePreparationSnapshot {
	s := FullEdgePreparationSnapshot{State: StateFailed, ProgressPercent: 0, ErrorCode: result.ErrorCode, DiagnosticsAvailable: true, Components: DefaultComponentProgress()}
	if err == nil && result.TransactionCommitted && result.RuntimeIntegrityVerified && !result.RuntimeReady && !result.InferenceReady {
		s.ProgressPercent = 80
		s.ErrorCode = "RUNTIME_UNCERTIFIED"
		s.Stage = "TRANSACTION_COMMITTED"
		s.SafeMessage = "Runtime Pack transaction verified. Functional offline certification is still required; Full Edge is not ready."
		// Every runtime artifact was hash-verified and installed; only the
		// model authorization/inference certification stays pending.
		for i := range s.Components {
			if s.Components[i].Key != "authorized_models" {
				s.Components[i].Status = ComponentStatusReady
			}
		}
		return s
	}
	if err == nil && result.State == "NOT_PREPARED" {
		return FullEdgePreparationSnapshot{State: StateNotPrepared, CanStart: true, Components: DefaultComponentProgress()}
	}
	if result.State == "RECOVERY_REQUIRED" || result.State == "ROLLED_BACK_UNCERTIFIED" {
		s.State = StateRecoveryRequired
		s.RecoveryAvailable = true
		s.SafeMessage = "Transactional recovery or independent validation is required before Full Edge can be prepared."
		if s.ErrorCode == "" {
			s.ErrorCode = "RUNTIME_RECOVERY_REQUIRED"
		}
		return s
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		s.State = StateCancelled
		s.CanStart = true
		s.ErrorCode = "PREPARATION_CANCELLED"
		s.SafeMessage = "Preparation was cancelled before certification."
		return s
	}
	if s.ErrorCode == "" {
		s.ErrorCode = "RUNTIME_PREPARATION_FAILED"
	}
	s.SafeMessage = "Offline runtime preparation did not produce verified functional readiness."
	return s
}
