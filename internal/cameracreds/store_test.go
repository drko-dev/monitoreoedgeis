package cameracreds

import (
	"bytes"
	"os"
	"path/filepath"
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
	cred := Credential{ID: "c1", Scope: ScopeDevice, TargetID: "onvif-abc123", Username: "admin", Password: "hunter2", Revision: 1}
	if _, err := store.Apply([]Credential{cred}); err != nil {
		t.Fatal(err)
	}

	// Reopen from disk with the same key to prove persistence + decryption.
	reopened, err := OpenStore(dir, key)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := NewProvider(reopened).Resolve("onvif-abc123", "")
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
		{ID: "c1", Scope: ScopeDevice, TargetID: "dev-1", Username: "admin", Password: "hunter2", Revision: 1},
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
		{ID: "c1", Scope: ScopeDevice, TargetID: "dev-1", Username: "admin", Password: "hunter2", Revision: 1},
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
	cred := Credential{ID: "c1", Scope: ScopeDevice, TargetID: "dev-1", Username: "admin", Password: "hunter2", Revision: 3}
	if _, err := store.Apply([]Credential{cred}); err != nil {
		t.Fatal(err)
	}

	changed, err := store.Apply([]Credential{cred})
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("re-applying the same revision should be a no-op")
	}
	got, ok := NewProvider(store).Resolve("dev-1", "")
	if !ok || got != cred {
		t.Fatalf("cache mutated by idempotent apply: %+v", got)
	}
}

func TestStore_Apply_HigherRevisionReplaces(t *testing.T) {
	store, err := OpenStore(t.TempDir(), testKey(t))
	if err != nil {
		t.Fatal(err)
	}
	old := Credential{ID: "c1", Scope: ScopeDevice, TargetID: "dev-1", Username: "admin", Password: "old-pass", Revision: 1}
	if _, err := store.Apply([]Credential{old}); err != nil {
		t.Fatal(err)
	}
	newer := Credential{ID: "c1", Scope: ScopeDevice, TargetID: "dev-1", Username: "admin", Password: "new-pass", Revision: 2}
	changed, err := store.Apply([]Credential{newer})
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("higher revision should replace and report changed")
	}
	got, ok := NewProvider(store).Resolve("dev-1", "")
	if !ok || got.Password != "new-pass" {
		t.Fatalf("got %+v", got)
	}
}

func TestStore_Apply_StaleRevisionIgnored(t *testing.T) {
	store, err := OpenStore(t.TempDir(), testKey(t))
	if err != nil {
		t.Fatal(err)
	}
	current := Credential{ID: "c1", Scope: ScopeDevice, TargetID: "dev-1", Username: "admin", Password: "current-pass", Revision: 5}
	if _, err := store.Apply([]Credential{current}); err != nil {
		t.Fatal(err)
	}
	stale := Credential{ID: "c1", Scope: ScopeDevice, TargetID: "dev-1", Username: "admin", Password: "stale-pass", Revision: 2}
	changed, err := store.Apply([]Credential{stale})
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("stale revision must not be reported as a change")
	}
	got, ok := NewProvider(store).Resolve("dev-1", "")
	if !ok || got.Password != "current-pass" {
		t.Fatalf("stale revision overwrote current cache: %+v", got)
	}
}

func TestStore_Apply_AbsentEntryIsRevoked(t *testing.T) {
	store, err := OpenStore(t.TempDir(), testKey(t))
	if err != nil {
		t.Fatal(err)
	}
	cred := Credential{ID: "c1", Scope: ScopeDevice, TargetID: "dev-1", Username: "admin", Password: "pass", Revision: 1}
	if _, err := store.Apply([]Credential{cred}); err != nil {
		t.Fatal(err)
	}

	changed, err := store.Apply(nil) // SaaS no longer lists this credential
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("removing the last entry should report changed")
	}
	if _, ok := NewProvider(store).Resolve("dev-1", ""); ok {
		t.Fatal("credential should have been revoked/removed")
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
