package cameracreds

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func testKey(t *testing.T) []byte {
	t.Helper()
	key, err := LoadOrCreateMasterKey(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func TestCrypto_RoundTrip(t *testing.T) {
	key := testKey(t)
	enc, err := encryptSecret(key, "s3cr3t-password")
	if err != nil {
		t.Fatal(err)
	}
	got, err := decryptSecret(key, enc)
	if err != nil {
		t.Fatal(err)
	}
	if got != "s3cr3t-password" {
		t.Fatalf("got %q, want %q", got, "s3cr3t-password")
	}
}

func TestStore_ApplyAndPersist_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	key := testKey(t)

	store, err := OpenStore(dir, key)
	if err != nil {
		t.Fatal(err)
	}
	cred := Credential{ID: "c1", Scope: ScopeDevice, CandidateKeys: []string{"onvif-abc123"}, Username: "admin", Password: "hunter2", Revision: 1}
	if _, err := store.Apply([]Credential{cred}); err != nil {
		t.Fatal(err)
	}

	// Reopen from disk with the same key to prove persistence + decryption.
	reopened, err := OpenStore(dir, key)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := NewProvider(reopened).Resolve("onvif-abc123")
	if !ok {
		t.Fatal("expected credential to be resolvable after reopen")
	}
	if got.Password != "hunter2" || got.Username != "admin" {
		t.Fatalf("got %+v", got)
	}
}

func TestStore_NoPlaintextOnDisk(t *testing.T) {
	dir := t.TempDir()
	key := testKey(t)
	store, err := OpenStore(dir, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Apply([]Credential{
		{ID: "c1", Scope: ScopeDevice, CandidateKeys: []string{"dev-1"}, Username: "admin", Password: "hunter2", Revision: 1},
	}); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, fileName))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("hunter2")) {
		t.Fatal("plaintext password found in camera_credentials.json")
	}
}

func TestStore_AtomicWrite_NoOrphanedTempFile(t *testing.T) {
	dir := t.TempDir()
	key := testKey(t)
	store, err := OpenStore(dir, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Apply([]Credential{
		{ID: "c1", Scope: ScopeDevice, CandidateKeys: []string{"dev-1"}, Username: "admin", Password: "hunter2", Revision: 1},
	}); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != fileName {
		t.Fatalf("unexpected dir contents after write: %v", entries)
	}
}

func TestStore_Apply_SameRevisionIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	key := testKey(t)
	store, err := OpenStore(dir, key)
	if err != nil {
		t.Fatal(err)
	}
	cred := Credential{ID: "c1", Scope: ScopeDevice, CandidateKeys: []string{"dev-1"}, Username: "admin", Password: "hunter2", Revision: 3}
	if _, err := store.Apply([]Credential{cred}); err != nil {
		t.Fatal(err)
	}

	stats, err := store.Apply([]Credential{cred})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Changed() {
		t.Fatal("re-applying the same revision should be a no-op")
	}
	got, ok := NewProvider(store).Resolve("dev-1")
	if !ok || !reflect.DeepEqual(got, cred) {
		t.Fatalf("cache mutated by idempotent apply: %+v", got)
	}
}

func TestStore_Apply_HigherRevisionReplaces(t *testing.T) {
	store, err := OpenStore(t.TempDir(), testKey(t))
	if err != nil {
		t.Fatal(err)
	}
	old := Credential{ID: "c1", Scope: ScopeDevice, CandidateKeys: []string{"dev-1"}, Username: "admin", Password: "old-pass", Revision: 1}
	if _, err := store.Apply([]Credential{old}); err != nil {
		t.Fatal(err)
	}
	newer := Credential{ID: "c1", Scope: ScopeDevice, CandidateKeys: []string{"dev-1"}, Username: "admin", Password: "new-pass", Revision: 2}
	stats, err := store.Apply([]Credential{newer})
	if err != nil {
		t.Fatal(err)
	}
	if !stats.Changed() {
		t.Fatal("higher revision should replace and report changed")
	}
	got, ok := NewProvider(store).Resolve("dev-1")
	if !ok || got.Password != "new-pass" {
		t.Fatalf("got %+v", got)
	}
}

func TestStore_Apply_StaleRevisionIgnored(t *testing.T) {
	store, err := OpenStore(t.TempDir(), testKey(t))
	if err != nil {
		t.Fatal(err)
	}
	current := Credential{ID: "c1", Scope: ScopeDevice, CandidateKeys: []string{"dev-1"}, Username: "admin", Password: "current-pass", Revision: 5}
	if _, err := store.Apply([]Credential{current}); err != nil {
		t.Fatal(err)
	}
	stale := Credential{ID: "c1", Scope: ScopeDevice, CandidateKeys: []string{"dev-1"}, Username: "admin", Password: "stale-pass", Revision: 2}
	stats, err := store.Apply([]Credential{stale})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Changed() {
		t.Fatal("stale revision must not be reported as a change")
	}
	got, ok := NewProvider(store).Resolve("dev-1")
	if !ok || got.Password != "current-pass" {
		t.Fatalf("stale revision overwrote current cache: %+v", got)
	}
}

func TestStore_Apply_AbsentEntryIsRevoked(t *testing.T) {
	store, err := OpenStore(t.TempDir(), testKey(t))
	if err != nil {
		t.Fatal(err)
	}
	cred := Credential{ID: "c1", Scope: ScopeDevice, CandidateKeys: []string{"dev-1"}, Username: "admin", Password: "pass", Revision: 1}
	if _, err := store.Apply([]Credential{cred}); err != nil {
		t.Fatal(err)
	}

	stats, err := store.Apply(nil) // SaaS no longer lists this credential
	if err != nil {
		t.Fatal(err)
	}
	if !stats.Changed() {
		t.Fatal("removing the last entry should report changed")
	}
	if _, ok := NewProvider(store).Resolve("dev-1"); ok {
		t.Fatal("credential should have been revoked/removed")
	}
}

func TestStore_Apply_PersistFailureKeepsMemoryInSyncWithDisk(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root bypasses directory permission checks")
	}
	dir := t.TempDir()
	store, err := OpenStore(dir, testKey(t))
	if err != nil {
		t.Fatal(err)
	}
	cred := Credential{ID: "c1", Scope: ScopeDevice, CandidateKeys: []string{"dev-1"}, Username: "admin", Password: "hunter2", Revision: 1}

	// Make the data dir unwritable so persistLocked's temp-file creation fails.
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })

	if _, err := store.Apply([]Credential{cred}); err == nil {
		t.Fatal("expected Apply to fail while data dir is unwritable")
	}
	if _, ok := NewProvider(store).Resolve("dev-1"); ok {
		t.Fatal("memory should still reflect the state before the failed Apply")
	}

	// Fix the underlying problem and retry with the same payload: it must not
	// be silently treated as changed=false forever.
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	stats, err := store.Apply([]Credential{cred})
	if err != nil {
		t.Fatal(err)
	}
	if !stats.Changed() {
		t.Fatal("retry with the same payload after fixing the disk problem must persist, not report changed=false")
	}
	if _, ok := NewProvider(store).Resolve("dev-1"); !ok {
		t.Fatal("credential should be resolvable after the successful retry")
	}
}

func TestOpenStore_CorruptFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, fileName), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenStore(dir, testKey(t)); err == nil {
		t.Fatal("expected error opening corrupt camera_credentials.json")
	}
}

func TestStore_Apply_SameRevisionDerivedCandidateIdentitiesConverge(t *testing.T) {
	dir := t.TempDir()
	key := testKey(t)
	store, err := OpenStore(dir, key)
	if err != nil {
		t.Fatal(err)
	}

	legacyCandidateHash := "11e9edbf00a0291a250e1fa707a4a8fc4ede60d8cdf024719d41ba34109dbe30"
	stableIdentity := "epr:uuid:3fa1fe68-b915-4053-a3e1-5ca6e67f02cd"

	// 1. Pre-existing cached state: id="6", revision=1, candidate_keys=[legacyCandidateHash]
	initial := Credential{
		ID:            "6",
		Scope:         ScopeDevice,
		CandidateKeys: []string{legacyCandidateHash},
		Username:      "admin",
		Password:      "tapo-pass-123",
		Revision:      1,
	}
	if _, err := store.Apply([]Credential{initial}); err != nil {
		t.Fatalf("initial Apply: %v", err)
	}

	provider := NewProvider(store)
	if _, ok := provider.Resolve(legacyCandidateHash); !ok {
		t.Fatal("expected legacy candidate hash to resolve before update")
	}
	if _, ok := provider.Resolve(stableIdentity); ok {
		t.Fatal("stable identity should not resolve before update")
	}

	// 2. Incoming authoritative snapshot with SAME revision=1, but projected candidate_keys=[stableIdentity]
	updatedSnapshot := Credential{
		ID:            "6",
		Scope:         ScopeDevice,
		CandidateKeys: []string{stableIdentity},
		Username:      "admin",
		Password:      "tapo-pass-123",
		Revision:      1,
	}
	stats, err := store.Apply([]Credential{updatedSnapshot})
	if err != nil {
		t.Fatalf("Apply with projected identities: %v", err)
	}
	if !stats.Changed() || stats.Updated != 1 {
		t.Fatalf("expected 1 updated entry, got stats: %+v", stats)
	}

	// 3. Provider now resolves by stable identity, NOT legacy hash
	if cred, ok := provider.Resolve(stableIdentity); !ok || cred.Password != "tapo-pass-123" {
		t.Fatalf("expected stable identity to resolve to tapo-pass-123, got ok=%v, cred=%+v", ok, cred)
	}
	if _, ok := provider.Resolve(legacyCandidateHash); ok {
		t.Fatal("legacy hash must no longer resolve after convergence")
	}

	// 4. Repeated apply of same snapshot is idempotent (changed=false)
	statsRepeat, err := store.Apply([]Credential{updatedSnapshot})
	if err != nil {
		t.Fatalf("repeat Apply: %v", err)
	}
	if statsRepeat.Changed() {
		t.Fatalf("repeated Apply should be no-op, got stats: %+v", statsRepeat)
	}

	// 5. Stale incoming lower revision (revision=0) does NOT overwrite revision=1
	stale := Credential{
		ID:            "6",
		Scope:         ScopeDevice,
		CandidateKeys: []string{"some-other-key"},
		Username:      "admin",
		Password:      "stale-pass",
		Revision:      0,
	}
	statsStale, err := store.Apply([]Credential{stale})
	if err != nil {
		t.Fatalf("stale Apply: %v", err)
	}
	if statsStale.Changed() {
		t.Fatalf("stale revision should be ignored, got stats: %+v", statsStale)
	}
	if cred, ok := provider.Resolve(stableIdentity); !ok || cred.Password != "tapo-pass-123" {
		t.Fatalf("stale payload mutated cache! got ok=%v, cred=%+v", ok, cred)
	}

	// 6. Persistence across restart: re-opening store from disk retains converged state
	reopened, err := OpenStore(dir, key)
	if err != nil {
		t.Fatalf("OpenStore after restart: %v", err)
	}
	providerReopened := NewProvider(reopened)
	if cred, ok := providerReopened.Resolve(stableIdentity); !ok || cred.Password != "tapo-pass-123" {
		t.Fatalf("reopened store failed to resolve stable identity: ok=%v, cred=%+v", ok, cred)
	}
	if _, ok := providerReopened.Resolve(legacyCandidateHash); ok {
		t.Fatal("reopened store still resolves legacy hash")
	}
}
