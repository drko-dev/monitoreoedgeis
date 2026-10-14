package installer

import (
	"context"
)

// WailsSafeFullEdgePreparationAPI decorates any FullEdgePreparationAPI to enforce
// the fail-closed Wails trust boundary contract. It guarantees that no invalid
// state or false readiness leaks to the Wails desktop frontend even if a backend
// or adapter returns malformed data or fails unexpectedly.
type WailsSafeFullEdgePreparationAPI struct {
	inner FullEdgePreparationAPI
}

// NewWailsSafeFullEdgePreparationAPI wraps the given FullEdgePreparationAPI with
// fail-closed validation. If inner is nil, it falls back to UnavailableFullEdgePreparation.
func NewWailsSafeFullEdgePreparationAPI(inner FullEdgePreparationAPI) FullEdgePreparationAPI {
	if inner == nil {
		inner = UnavailableFullEdgePreparation{}
	}
	return &WailsSafeFullEdgePreparationAPI{inner: inner}
}

// Status returns the current preparation snapshot sanitized through the Wails trust boundary.
func (w *WailsSafeFullEdgePreparationAPI) Status(ctx context.Context) (FullEdgePreparationSnapshot, error) {
	s, err := w.inner.Status(ctx)
	return ValidateAndSanitizeWailsSnapshot(s, err)
}

// Start initiates preparation and sanitizes the returned snapshot through the Wails trust boundary.
func (w *WailsSafeFullEdgePreparationAPI) Start(ctx context.Context, requestID string) (FullEdgePreparationSnapshot, error) {
	s, err := w.inner.Start(ctx, requestID)
	return ValidateAndSanitizeWailsSnapshot(s, err)
}

// Cancel attempts cancellation and sanitizes the returned snapshot through the Wails trust boundary.
func (w *WailsSafeFullEdgePreparationAPI) Cancel(ctx context.Context, operationID string) (FullEdgePreparationSnapshot, error) {
	s, err := w.inner.Cancel(ctx, operationID)
	return ValidateAndSanitizeWailsSnapshot(s, err)
}

// Diagnostic forwards the diagnostic query. Diagnostics are read-only inspection structures.
func (w *WailsSafeFullEdgePreparationAPI) Diagnostic(ctx context.Context, operationID string) (FullEdgePreparationDiagnostic, error) {
	return w.inner.Diagnostic(ctx, operationID)
}

// Recover initiates runtime recovery and sanitizes the returned snapshot through the Wails trust boundary.
func (w *WailsSafeFullEdgePreparationAPI) Recover(ctx context.Context, operationID string) (FullEdgePreparationSnapshot, error) {
	s, err := w.inner.Recover(ctx, operationID)
	return ValidateAndSanitizeWailsSnapshot(s, err)
}

// ValidateAndSanitizeWailsSnapshot validates and enforces fail-closed guarantees on any snapshot
// crossing the Wails boundary:
//  1. Any snapshot failing schema validation is replaced with a safe FAILED / INVALID_PREPARATION_STATUS.
//  2. If an error is returned alongside a snapshot claiming READY or CertifiedReady(), it is replaced
//     with a safe FAILED / PREPARATION_BACKEND_ERROR snapshot.
//  3. The original backend error is always preserved, never discarded or hidden.
func ValidateAndSanitizeWailsSnapshot(snapshot FullEdgePreparationSnapshot, err error) (FullEdgePreparationSnapshot, error) {
	if ValidateFullEdgePreparationSnapshot(snapshot) != nil {
		return BlockedPreparationSnapshot("INVALID_PREPARATION_STATUS"), err
	}
	if err != nil {
		if snapshot.State == StateReady || snapshot.CertifiedReady() {
			return BlockedPreparationSnapshot("PREPARATION_BACKEND_ERROR"), err
		}
		return snapshot, err
	}
	return snapshot, nil
}

// BlockedPreparationSnapshot creates a standardized fail-closed FAILED snapshot with the given error code.
func BlockedPreparationSnapshot(code string) FullEdgePreparationSnapshot {
	return FullEdgePreparationSnapshot{
		State:                StateFailed,
		ProgressPercent:      0,
		ErrorCode:            code,
		SafeMessage:          "Full Edge preparation was not certified; no readiness can be reported.",
		DiagnosticsAvailable: true,
		Components:           DefaultComponentProgress(),
	}
}
