package remoteconfig

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/drko-dev/monitoreoedgeis/internal/auditjournal"
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

// Blocker 2 / test A, and Blocker 3 / test 1: v1 -> v2 -> v3 fails.
// Rollback must target v2 (the config actually live before v3's attempt),
// never v1 (an older historical snapshot).
func TestEngine_RollbackTargetsCurrentApplied_NotOlderSnapshot(t *testing.T) {
	e, adapter := newTestEngine(t)
	ctx := context.Background()

	if _, _, err := e.ReceiveDesired(ctx, cfg(1, `{"a":1}`)); err != nil {
		t.Fatalf("apply v1: %v", err)
	}
	if _, _, err := e.ReceiveDesired(ctx, cfg(2, `{"a":2}`)); err != nil {
		t.Fatalf("apply v2: %v", err)
	}

	adapter.applyErr = errors.New("v3 rejected by runtime")
	status, code, err := e.ReceiveDesired(ctx, cfg(3, `{"a":3}`))
	if err != nil {
		t.Fatalf("ReceiveDesired v3: %v", err)
	}
	if status != ApplyStatusRolledBack || code != ErrCodeApplyFailed {
		t.Fatalf("status=%q code=%q, want rolled_back/%s", status, code, ErrCodeApplyFailed)
	}

	target, ok := adapter.lastRollback()
	if !ok || target.Version != 2 {
		t.Fatalf("rollback target = %+v (ok=%v), want v2 (the config live before v3), not v1", target, ok)
	}
	if st := e.store.Get(); st.AppliedVersion != 2 {
		t.Fatalf("AppliedVersion = %d, want 2 (unchanged)", st.AppliedVersion)
	}
}

// Blocker 2 / test B, and Blocker 3 / test 2: restart with
// Applied=v2/PreviousKnownGood=v1/Staging=v3 (crash mid-apply of v3).
// Recover must roll back to v2, not v1.
func TestEngine_Recover_RollsBackToCurrentApplied_NotOlderSnapshot(t *testing.T) {
	dir := t.TempDir()

	store1, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	e1 := NewEngine(store1, &fakeAdapter{})
	ctx := context.Background()
	if _, _, err := e1.ReceiveDesired(ctx, cfg(1, `{"a":1}`)); err != nil {
		t.Fatalf("apply v1: %v", err)
	}
	if _, _, err := e1.ReceiveDesired(ctx, cfg(2, `{"a":2}`)); err != nil {
		t.Fatalf("apply v2: %v", err)
	}

	// Simulate a crash mid-apply of v3: Staging set, never resolved.
	if _, err := store1.Update(func(s State) State {
		staged := cfg(3, `{"a":3}`)
		s.Staging = &staged
		s.LastApplyStatus = ApplyStatusApplying
		return s
	}); err != nil {
		t.Fatalf("simulate crash state: %v", err)
	}

	store2, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("OpenStore after restart: %v", err)
	}
	adapter2 := &fakeAdapter{}
	e2 := NewEngine(store2, adapter2)
	if err := e2.Recover(ctx); err != nil {
		t.Fatalf("Recover: %v", err)
	}

	target, ok := adapter2.lastRollback()
	if !ok || target.Version != 2 {
		t.Fatalf("recovery rollback target = %+v (ok=%v), want v2, not v1", target, ok)
	}
	if st := e2.store.Get(); st.AppliedVersion != 2 {
		t.Fatalf("AppliedVersion after recovery = %d, want 2", st.AppliedVersion)
	}
}

// Blocker 3 / test 3: RollbackRuntimeConfig itself fails -> Staging must
// be left in place, never cleared, since it's the only durable record
// that the runtime's real state is unknown.
func TestEngine_RollbackFailure_LeavesStagingUnresolved(t *testing.T) {
	e, adapter := newTestEngine(t)
	ctx := context.Background()

	if _, _, err := e.ReceiveDesired(ctx, cfg(1, `{"a":1}`)); err != nil {
		t.Fatalf("apply v1: %v", err)
	}

	adapter.applyErr = errors.New("v2 rejected")
	adapter.rollbackErr = errors.New("rollback also rejected")
	_, _, err := e.ReceiveDesired(ctx, cfg(2, `{"a":2}`))
	if err == nil {
		t.Fatal("expected a non-nil error when both apply and rollback fail")
	}

	st := e.store.Get()
	if st.Staging == nil || st.Staging.Version != 2 {
		t.Fatalf("Staging = %+v, want non-nil pointing at v2 (unresolved)", st.Staging)
	}
}

// Blocker 3 / test 4: with Staging left unresolved (a prior rollback
// failure), a new ReceiveDesired must refuse to apply at all -- it must
// never call ApplyRuntimeConfig while the runtime's real state is
// unknown.
func TestEngine_UnresolvedStaging_RefusesNewApply(t *testing.T) {
	e, adapter := newTestEngine(t)
	ctx := context.Background()

	if _, _, err := e.ReceiveDesired(ctx, cfg(1, `{"a":1}`)); err != nil {
		t.Fatalf("apply v1: %v", err)
	}
	adapter.applyErr = errors.New("v2 rejected")
	adapter.rollbackErr = errors.New("rollback also rejected")
	if _, _, err := e.ReceiveDesired(ctx, cfg(2, `{"a":2}`)); err == nil {
		t.Fatal("expected the v2 attempt itself to fail closed")
	}

	callsBefore := adapter.applyCallCount()
	adapter.applyErr = nil
	adapter.rollbackErr = nil
	status, code, err := e.ReceiveDesired(ctx, cfg(3, `{"a":3}`))
	if err != nil {
		t.Fatalf("ReceiveDesired: %v", err)
	}
	if status != ApplyStatusFailed || code != ErrCodeStagingUnresolved {
		t.Fatalf("status=%q code=%q, want failed/%s", status, code, ErrCodeStagingUnresolved)
	}
	if adapter.applyCallCount() != callsBefore {
		t.Fatalf("ApplyRuntimeConfig was called while Staging was unresolved")
	}
}

// Blocker 3 / test 5: the runtime apply succeeds, but the final durable
// publish fails. The engine must roll back the runtime to what was live
// before, and AppliedVersion must never advance.
func TestEngine_PublishFailureAfterSuccessfulApply_RollsBackAndDoesNotAdvance(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	adapter := &fakeAdapter{}
	e := NewEngine(store, adapter)
	ctx := context.Background()

	if _, _, err := e.ReceiveDesired(ctx, cfg(1, `{"a":1}`)); err != nil {
		t.Fatalf("apply v1: %v", err)
	}

	// Let exactly one more write through (v2's staging write, step 3),
	// then fail every write after that (v2's publish write, step 6).
	store.limitWrites = true
	store.writeBudget = 1

	_, _, err = e.ReceiveDesired(ctx, cfg(2, `{"a":2}`))
	if err == nil {
		t.Fatal("expected a non-nil error when the final publish cannot be persisted")
	}

	target, ok := adapter.lastRollback()
	if !ok || target.Version != 1 {
		t.Fatalf("rollback target after publish failure = %+v (ok=%v), want v1", target, ok)
	}
	if st := e.store.Get(); st.AppliedVersion != 1 {
		t.Fatalf("AppliedVersion = %d, want 1 (must not advance without a durable commit)", st.AppliedVersion)
	}
}

// Blocker 3 / test 6: a Store.Update write failure must leave the
// in-memory state exactly as it was -- never a change without a durable
// commit backing it.
func TestStore_UpdateWriteFailureLeavesMemoryUnchanged(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	if _, err := store.Update(func(s State) State { s.AppliedVersion = 1; return s }); err != nil {
		t.Fatalf("seed write: %v", err)
	}

	before := store.Get()
	store.limitWrites = true
	store.writeBudget = 0

	if _, err := store.Update(func(s State) State { s.AppliedVersion = 2; return s }); err == nil {
		t.Fatal("expected the write to fail")
	}

	after := store.Get()
	if after.AppliedVersion != before.AppliedVersion {
		t.Fatalf("AppliedVersion changed despite a failed write: before=%d after=%d", before.AppliedVersion, after.AppliedVersion)
	}
}

// Blocker 3 / test 7: after a persistence failure at the final publish
// step, a fresh restart must still see the unresolved Staging on disk --
// the last successful write (staging) is what's durable, not the failed
// one.
func TestEngine_RestartAfterPublishFailure_StillSeesStaging(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	e := NewEngine(store, &fakeAdapter{})
	ctx := context.Background()

	if _, _, err := e.ReceiveDesired(ctx, cfg(1, `{"a":1}`)); err != nil {
		t.Fatalf("apply v1: %v", err)
	}

	store.limitWrites = true
	store.writeBudget = 1 // let v2's staging write through, fail its publish
	if _, _, err := e.ReceiveDesired(ctx, cfg(2, `{"a":2}`)); err == nil {
		t.Fatal("expected the publish step to fail")
	}

	// Restart: open a fresh Store over the same dataDir (unaffected by the
	// in-memory write-limiting on the old Store instance).
	store2, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("OpenStore after restart: %v", err)
	}
	st := store2.Get()
	if st.Staging == nil || st.Staging.Version != 2 {
		t.Fatalf("Staging after restart = %+v, want non-nil pointing at v2", st.Staging)
	}
	if st.AppliedVersion != 1 {
		t.Fatalf("AppliedVersion after restart = %d, want 1", st.AppliedVersion)
	}
}

type mockRemoteConfigAuditSink struct {
	mu         sync.Mutex
	records    []auditjournal.Record
	failAppend error
}

func (m *mockRemoteConfigAuditSink) Append(rec auditjournal.Record) (auditjournal.Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failAppend != nil {
		return auditjournal.Record{}, m.failAppend
	}
	rec.Sequence = uint64(len(m.records) + 1)
	m.records = append(m.records, rec)
	return rec, nil
}

func (m *mockRemoteConfigAuditSink) snapshot() []auditjournal.Record {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]auditjournal.Record, len(m.records))
	copy(out, m.records)
	return out
}

func TestEngine_AuditEvents(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	adapter := &fakeAdapter{}
	sink := &mockRemoteConfigAuditSink{}
	e := NewEngine(store, adapter, WithEngineAuditSink(sink))
	ctx := context.Background()

	// 1. Success apply: records REMOTE_CONFIG_APPLY with ConfigVersion and no payload
	status, code, err := e.ReceiveDesired(ctx, cfg(1, `{"secret_password":"super-sensitive-config"}`))
	if err != nil || status != ApplyStatusApplied || code != "" {
		t.Fatalf("ReceiveDesired v1: status=%s, code=%s, err=%v", status, code, err)
	}

	records := sink.snapshot()
	if len(records) != 1 {
		t.Fatalf("expected 1 audit record, got %d", len(records))
	}
	if records[0].EventType != auditjournal.EventRemoteConfigApply || records[0].ConfigVersion != "1" || records[0].Result != auditjournal.ResultSuccess {
		t.Errorf("record 0 = %+v, want EventRemoteConfigApply for v1", records[0])
	}
	// Verify no payload or secret leakage in any field
	for idx, r := range records {
		if strings.Contains(r.SafeReason, "super-sensitive-config") || strings.Contains(r.SafeReason, "secret_password") {
			t.Errorf("record %d leaked secret in SafeReason: %+v", idx, r)
		}
	}

	// 2. Idempotent poll of identical applied version: returns ApplyStatusApplied but does NOT re-audit apply
	status2, _, _ := e.ReceiveDesired(ctx, cfg(1, `{"secret_password":"super-sensitive-config"}`))
	if status2 != ApplyStatusApplied {
		t.Fatalf("idempotent poll status = %s, want %s", status2, ApplyStatusApplied)
	}
	if len(sink.snapshot()) != 1 {
		t.Fatalf("audit count after idempotent poll = %d, want 1 (must not re-audit)", len(sink.snapshot()))
	}

	// 3. Apply failure triggering rollback: records REMOTE_CONFIG_ROLLBACK
	adapter.applyErr = errors.New("pipeline refused config")
	status3, code3, _ := e.ReceiveDesired(ctx, cfg(2, `{"some":"new-config"}`))
	if status3 != ApplyStatusRolledBack || code3 != ErrCodeApplyFailed {
		t.Fatalf("ReceiveDesired v2: status=%s, code=%s", status3, code3)
	}
	records3 := sink.snapshot()
	if len(records3) != 2 {
		t.Fatalf("expected 2 audit records, got %d", len(records3))
	}
	if records3[1].EventType != auditjournal.EventRemoteConfigRollback || records3[1].ConfigVersion != "2" || records3[1].SafeReason != ErrCodeApplyFailed {
		t.Errorf("record 1 = %+v, want EventRemoteConfigRollback", records3[1])
	}

	// 4. Validation failure: records REMOTE_CONFIG_FAILURE with ErrCodeValidationFailed
	adapter.applyErr = nil
	adapter.validateErr = errors.New("syntax error")
	status4, code4, _ := e.ReceiveDesired(ctx, cfg(3, `{"bad":"syntax"}`))
	if status4 != ApplyStatusFailed || code4 != ErrCodeValidationFailed {
		t.Fatalf("ReceiveDesired v3: status=%s, code=%s", status4, code4)
	}
	records4 := sink.snapshot()
	if len(records4) != 3 {
		t.Fatalf("expected 3 audit records, got %d", len(records4))
	}
	if records4[2].EventType != auditjournal.EventRemoteConfigFailure || records4[2].ConfigVersion != "3" || records4[2].SafeReason != ErrCodeValidationFailed {
		t.Errorf("record 2 = %+v, want EventRemoteConfigFailure", records4[2])
	}

	// 5. Version conflict (divergent payload for same version): records REMOTE_CONFIG_FAILURE
	adapter.validateErr = nil
	status5, code5, _ := e.ReceiveDesired(ctx, cfg(1, `{"secret_password":"tampered-payload"}`))
	if status5 != ApplyStatusFailed || code5 != ErrCodeVersionConflict {
		t.Fatalf("ReceiveDesired v1 conflict: status=%s, code=%s", status5, code5)
	}
	records5 := sink.snapshot()
	if len(records5) != 4 {
		t.Fatalf("expected 4 audit records, got %d", len(records5))
	}
	if records5[3].EventType != auditjournal.EventRemoteConfigFailure || records5[3].SafeReason != ErrCodeVersionConflict {
		t.Errorf("record 3 = %+v, want EventRemoteConfigFailure for version conflict", records5[3])
	}

	// 6. Recovery rollback audit: simulate staging config left over from restart
	_, _ = store.Update(func(s State) State {
		c := cfg(4, `{"crash":"mid-apply"}`)
		s.Staging = &c
		return s
	})
	if err := e.Recover(ctx); err != nil {
		t.Fatalf("Recover error: %v", err)
	}
	records6 := sink.snapshot()
	if len(records6) != 5 {
		t.Fatalf("expected 5 audit records after recovery, got %d", len(records6))
	}
	if records6[4].EventType != auditjournal.EventRemoteConfigRollback || records6[4].ConfigVersion != "4" {
		t.Errorf("record 4 = %+v, want EventRemoteConfigRollback after recovery", records6[4])
	}

	// 7. Failing audit sink does not break apply
	sink.failAppend = errors.New("audit disk full")
	status7, code7, err7 := e.ReceiveDesired(ctx, cfg(5, `{"valid":true}`))
	if err7 != nil || status7 != ApplyStatusApplied || code7 != "" {
		t.Fatalf("ReceiveDesired with failing audit sink: status=%s, code=%s, err=%v", status7, code7, err7)
	}
}
