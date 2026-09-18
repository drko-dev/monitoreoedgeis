package remoteconfig

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
)

// fakeAdapter is a controllable RuntimeAdapter for testing the O5-O8
// lifecycle without any real runtime knobs.
type fakeAdapter struct {
	mu sync.Mutex

	validateErr error
	applyErr    error
	rollbackErr error

	applyCalls    []Config
	rollbackCalls []Config
}

func (f *fakeAdapter) ValidateRuntimeConfig(_ context.Context, _ Config) error {
	return f.validateErr
}

func (f *fakeAdapter) ApplyRuntimeConfig(_ context.Context, cfg Config) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.applyCalls = append(f.applyCalls, cfg)
	return f.applyErr
}

func (f *fakeAdapter) RollbackRuntimeConfig(_ context.Context, cfg Config) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rollbackCalls = append(f.rollbackCalls, cfg)
	return f.rollbackErr
}

func (f *fakeAdapter) applyCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.applyCalls)
}

func (f *fakeAdapter) lastRollback() (Config, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.rollbackCalls) == 0 {
		return Config{}, false
	}
	return f.rollbackCalls[len(f.rollbackCalls)-1], true
}

func cfg(version int64, payload string) Config {
	return Config{Version: version, Payload: json.RawMessage(payload)}
}

func newTestEngine(t *testing.T) (*Engine, *fakeAdapter) {
	t.Helper()
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	adapter := &fakeAdapter{}
	return NewEngine(store, adapter), adapter
}

// 1. config válida aplica
func TestEngine_ValidConfigApplies(t *testing.T) {
	e, adapter := newTestEngine(t)

	status, code, err := e.ReceiveDesired(context.Background(), cfg(1, `{"a":1}`))
	if err != nil {
		t.Fatalf("ReceiveDesired: %v", err)
	}
	if status != ApplyStatusApplied || code != "" {
		t.Fatalf("status=%q code=%q, want applied/\"\"", status, code)
	}
	if adapter.applyCallCount() != 1 {
		t.Fatalf("apply calls = %d, want 1", adapter.applyCallCount())
	}

	st := e.store.Get()
	if st.AppliedVersion != 1 || st.AppliedConfig == nil || string(st.AppliedConfig.Payload) != `{"a":1}` {
		t.Fatalf("unexpected state after apply: %+v", st)
	}
}

// 2. config inválida no modifica current
func TestEngine_InvalidConfigDoesNotModifyCurrent(t *testing.T) {
	e, adapter := newTestEngine(t)

	// Establish a known-good current first.
	if _, _, err := e.ReceiveDesired(context.Background(), cfg(1, `{"a":1}`)); err != nil {
		t.Fatalf("seed apply: %v", err)
	}

	adapter.validateErr = errors.New("bad knob value")
	status, code, err := e.ReceiveDesired(context.Background(), cfg(2, `{"a":2}`))
	if err != nil {
		t.Fatalf("ReceiveDesired: %v", err)
	}
	if status != ApplyStatusFailed || code != ErrCodeValidationFailed {
		t.Fatalf("status=%q code=%q, want failed/%s", status, code, ErrCodeValidationFailed)
	}
	if adapter.applyCallCount() != 1 {
		t.Fatalf("apply must not be called when validation fails; calls = %d", adapter.applyCallCount())
	}

	st := e.store.Get()
	if st.AppliedVersion != 1 || string(st.AppliedConfig.Payload) != `{"a":1}` {
		t.Fatalf("current was modified by a failed-validation attempt: %+v", st)
	}
}

// 3. misma versión/config es idempotente
func TestEngine_SameVersionSameContentIdempotent(t *testing.T) {
	e, adapter := newTestEngine(t)
	ctx := context.Background()

	if _, _, err := e.ReceiveDesired(ctx, cfg(1, `{"a":1}`)); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	status, code, err := e.ReceiveDesired(ctx, cfg(1, `{"a":1}`))
	if err != nil {
		t.Fatalf("ReceiveDesired: %v", err)
	}
	if status != ApplyStatusApplied || code != "" {
		t.Fatalf("status=%q code=%q, want applied/\"\" (idempotent)", status, code)
	}
	if adapter.applyCallCount() != 1 {
		t.Fatalf("apply must not be called again for an idempotent repeat; calls = %d", adapter.applyCallCount())
	}
}

// 4. versión vieja no pisa versión nueva
func TestEngine_OldVersionRejected(t *testing.T) {
	e, adapter := newTestEngine(t)
	ctx := context.Background()

	if _, _, err := e.ReceiveDesired(ctx, cfg(5, `{"a":5}`)); err != nil {
		t.Fatalf("seed apply: %v", err)
	}
	status, code, err := e.ReceiveDesired(ctx, cfg(3, `{"a":3}`))
	if err != nil {
		t.Fatalf("ReceiveDesired: %v", err)
	}
	if status != ApplyStatusFailed || code != ErrCodeStaleVersion {
		t.Fatalf("status=%q code=%q, want failed/%s", status, code, ErrCodeStaleVersion)
	}
	if adapter.applyCallCount() != 1 {
		t.Fatalf("apply must not be called for a stale version; calls = %d", adapter.applyCallCount())
	}
	if st := e.store.Get(); st.AppliedVersion != 5 {
		t.Fatalf("AppliedVersion = %d, want 5 (unchanged)", st.AppliedVersion)
	}
}

// 5. misma versión divergente falla
func TestEngine_SameVersionDivergentContentFails(t *testing.T) {
	e, _ := newTestEngine(t)
	ctx := context.Background()

	if _, _, err := e.ReceiveDesired(ctx, cfg(1, `{"a":1}`)); err != nil {
		t.Fatalf("seed apply: %v", err)
	}
	status, code, err := e.ReceiveDesired(ctx, cfg(1, `{"a":999}`))
	if err != nil {
		t.Fatalf("ReceiveDesired: %v", err)
	}
	if status != ApplyStatusFailed || code != ErrCodeVersionConflict {
		t.Fatalf("status=%q code=%q, want failed/%s", status, code, ErrCodeVersionConflict)
	}
	if st := e.store.Get(); string(st.AppliedConfig.Payload) != `{"a":1}` {
		t.Fatalf("current was overwritten by a version-conflicting attempt: %+v", st.AppliedConfig)
	}
}

// 6. apply failure conserva/restaura previous config
func TestEngine_ApplyFailureRollsBackToPreviousKnownGood(t *testing.T) {
	e, adapter := newTestEngine(t)
	ctx := context.Background()

	if _, _, err := e.ReceiveDesired(ctx, cfg(1, `{"a":1}`)); err != nil {
		t.Fatalf("seed apply: %v", err)
	}

	adapter.applyErr = errors.New("runtime rejected knob")
	status, code, err := e.ReceiveDesired(ctx, cfg(2, `{"a":2}`))
	if err != nil {
		t.Fatalf("ReceiveDesired: %v", err)
	}
	if status != ApplyStatusRolledBack || code != ErrCodeApplyFailed {
		t.Fatalf("status=%q code=%q, want rolled_back/%s", status, code, ErrCodeApplyFailed)
	}

	rolledBackTo, ok := adapter.lastRollback()
	if !ok || rolledBackTo.Version != 1 {
		t.Fatalf("rollback was not called with the previous known-good config (v1): %+v ok=%v", rolledBackTo, ok)
	}

	st := e.store.Get()
	if st.AppliedVersion != 1 || string(st.AppliedConfig.Payload) != `{"a":1}` {
		t.Fatalf("current config was not preserved after a failed apply: %+v", st)
	}
	if st.Staging != nil {
		t.Fatalf("staging must be cleared after a terminal outcome, got %+v", st.Staging)
	}
	if st.RollbackCount != 1 {
		t.Fatalf("RollbackCount = %d, want 1", st.RollbackCount)
	}
}

// 7. restart conserva applied_version
func TestEngine_RestartPreservesAppliedVersion(t *testing.T) {
	dir := t.TempDir()

	store1, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	e1 := NewEngine(store1, &fakeAdapter{})
	if _, _, err := e1.ReceiveDesired(context.Background(), cfg(7, `{"a":7}`)); err != nil {
		t.Fatalf("apply: %v", err)
	}

	// Simulate a restart: open a fresh Store/Engine over the same dataDir.
	store2, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("OpenStore after restart: %v", err)
	}
	e2 := NewEngine(store2, &fakeAdapter{})
	if err := e2.Recover(context.Background()); err != nil {
		t.Fatalf("Recover: %v", err)
	}

	st := e2.store.Get()
	if st.AppliedVersion != 7 || st.AppliedConfig == nil || !st.AppliedConfig.Equal(cfg(7, `{"a":7}`)) {
		t.Fatalf("state not recovered after restart: version=%d config=%+v", st.AppliedVersion, st.AppliedConfig)
	}
}

// 8. rollback recupera previous known-good
func TestEngine_RollbackRecoversPreviousKnownGood_AfterCrashMidApply(t *testing.T) {
	dir := t.TempDir()

	store1, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	e1 := NewEngine(store1, &fakeAdapter{})
	if _, _, err := e1.ReceiveDesired(context.Background(), cfg(1, `{"a":1}`)); err != nil {
		t.Fatalf("seed apply: %v", err)
	}

	// Simulate a crash mid-apply: staging was written, but the process died
	// before Apply/rollback/promote resolved -- write that state directly,
	// the way Engine's own staging step would have left it.
	if _, err := store1.Update(func(s State) State {
		staged := cfg(2, `{"a":2}`)
		s.Staging = &staged
		s.LastApplyStatus = ApplyStatusApplying
		return s
	}); err != nil {
		t.Fatalf("simulate crash state: %v", err)
	}

	// Restart: new Store/Engine over the same dataDir, then Recover.
	store2, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("OpenStore after restart: %v", err)
	}
	adapter2 := &fakeAdapter{}
	e2 := NewEngine(store2, adapter2)
	if err := e2.Recover(context.Background()); err != nil {
		t.Fatalf("Recover: %v", err)
	}

	rolledBackTo, ok := adapter2.lastRollback()
	if !ok || rolledBackTo.Version != 1 {
		t.Fatalf("Recover did not roll back to the previous known-good config (v1): %+v ok=%v", rolledBackTo, ok)
	}

	st := e2.store.Get()
	if st.Staging != nil {
		t.Fatalf("staging must be cleared after recovery, got %+v", st.Staging)
	}
	if st.AppliedVersion != 1 {
		t.Fatalf("AppliedVersion after recovery = %d, want 1 (unchanged, previous known-good)", st.AppliedVersion)
	}
	if st.LastFailedVersion != 2 {
		t.Fatalf("LastFailedVersion = %d, want 2 (the interrupted version)", st.LastFailedVersion)
	}
}

// 9. versión fallida no entra en loop infinito
func TestEngine_FailedVersionNotRetried(t *testing.T) {
	e, adapter := newTestEngine(t)
	ctx := context.Background()

	adapter.validateErr = errors.New("bad knob")
	if _, _, err := e.ReceiveDesired(ctx, cfg(1, `{"a":1}`)); err != nil {
		t.Fatalf("first attempt: %v", err)
	}
	if adapter.applyCallCount() != 0 {
		t.Fatalf("validation-failing config must never reach Apply")
	}

	// The exact same version+content offered again on the next poll must
	// not re-invoke validate/apply -- it's the identical failed version.
	status, code, err := e.ReceiveDesired(ctx, cfg(1, `{"a":1}`))
	if err != nil {
		t.Fatalf("second attempt: %v", err)
	}
	if status != ApplyStatusFailed || code != ErrCodePreviouslyFailed {
		t.Fatalf("status=%q code=%q, want failed/%s", status, code, ErrCodePreviouslyFailed)
	}
}

// 10. status/logs no filtran secrets
func TestSanitizeApplyError_RedactsSecrets(t *testing.T) {
	err := errors.New("connect failed: password=hunter2 token=abc123 bearer=xyz")
	got := sanitizeApplyError(err)
	for _, leaked := range []string{"hunter2", "abc123", "xyz"} {
		if strings.Contains(got, leaked) {
			t.Fatalf("sanitizeApplyError leaked a secret-shaped value %q in: %s", leaked, got)
		}
	}
}

func TestStatus_NeverExposesPayload(t *testing.T) {
	e, _ := newTestEngine(t)
	if _, _, err := e.ReceiveDesired(context.Background(), cfg(1, `{"roi":"very sensitive coordinates"}`)); err != nil {
		t.Fatalf("apply: %v", err)
	}
	st := e.store.Get().toStatus(1)
	data, err := json.Marshal(st)
	if err != nil {
		t.Fatalf("marshal status: %v", err)
	}
	if strings.Contains(string(data), "sensitive") {
		t.Fatalf("Status leaked payload content: %s", data)
	}
}
