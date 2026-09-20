package cameracreds

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/drko-dev/monitoreoedgeis/internal/transport"
)

type fakeFetcher struct {
	resp transport.CameraCredentialsResponse
	err  error
}

func (f *fakeFetcher) FetchCameraCredentials(ctx context.Context, deviceID, credential string) (transport.CameraCredentialsResponse, error) {
	return f.resp, f.err
}

func newTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := OpenStore(t.TempDir(), testKey(t))
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func mustSyncer(t *testing.T, f Fetcher, store *Store) *Syncer {
	t.Helper()
	s, err := NewSyncer(SyncOptions{Client: f, Store: store, DeviceID: "d1", Credential: "cred"})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// devCred builds a payload entry in the REAL SaaS wire shape: numeric id and
// lowercase scope.
func devCred(id int64, candidate string, revision int) transport.CameraCredentialPayload {
	return transport.CameraCredentialPayload{
		ID: id, Scope: "device", CandidateKeys: []string{candidate},
		Username: "admin", Password: "pass", Revision: revision,
	}
}

func TestSyncer_Success(t *testing.T) {
	store := newTestStore(t)
	fetcher := &fakeFetcher{resp: transport.CameraCredentialsResponse{
		Credentials: []transport.CameraCredentialPayload{devCred(1, "dev-1", 1)},
	}}
	if err := mustSyncer(t, fetcher, store).Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, ok := NewProvider(store).Resolve("dev-1")
	if !ok || got.Password != "pass" {
		t.Fatalf("got %+v ok=%v", got, ok)
	}
	// The numeric SaaS id becomes the canonical decimal string internally.
	if got.ID != "1" {
		t.Fatalf("internal id = %q, want %q (numeric SaaS id rendered as canonical decimal)", got.ID, "1")
	}
}

// TestSyncer_NumericIDAndLowercaseScope is the contract-drift guard. The SaaS
// sends id as a JSON NUMBER (BIGSERIAL) and scope LOWERCASE. An Edge that
// expected a string id or an uppercase scope would fail to decode every sync,
// so both are pinned here.
func TestSyncer_NumericIDAndLowercaseScope(t *testing.T) {
	store := newTestStore(t)
	fetcher := &fakeFetcher{resp: transport.CameraCredentialsResponse{
		Credentials: []transport.CameraCredentialPayload{
			{ID: 42, Scope: "device", CandidateKeys: []string{"epr:abc"}, Username: "u", Password: "p", Revision: 3},
			{ID: 43, Scope: "group", CandidateKeys: []string{"cam-a", "cam-b"}, Username: "gu", Password: "gp", Revision: 1},
		},
	}}
	if err := mustSyncer(t, fetcher, store).Sync(context.Background()); err != nil {
		t.Fatal(err)
	}

	dev, ok := NewProvider(store).Resolve("epr:abc")
	if !ok || dev.ID != "42" || dev.Scope != ScopeDevice {
		t.Fatalf("device credential not decoded as expected: %+v ok=%v", dev, ok)
	}
	grp, ok := NewProvider(store).Resolve("cam-a")
	if !ok || grp.ID != "43" || grp.Scope != ScopeGroup {
		t.Fatalf("group credential not decoded as expected: %+v ok=%v", grp, ok)
	}
}

// TestSyncer_RejectsUnknownScope: a scope the Edge does not understand must
// reject the whole payload rather than cache a credential it cannot interpret.
// The uppercase wire form is included because the SaaS never sends it —
// accepting it would hide a contract drift.
func TestSyncer_RejectsUnknownScope(t *testing.T) {
	for _, scope := range []string{"DEVICE", "GROUP", "", "camera", "group_id"} {
		t.Run("scope="+scope, func(t *testing.T) {
			store := newTestStore(t)
			seed := &fakeFetcher{resp: transport.CameraCredentialsResponse{
				Credentials: []transport.CameraCredentialPayload{devCred(1, "dev-1", 1)},
			}}
			if err := mustSyncer(t, seed, store).Sync(context.Background()); err != nil {
				t.Fatal(err)
			}

			bad := &fakeFetcher{resp: transport.CameraCredentialsResponse{
				Credentials: []transport.CameraCredentialPayload{
					{ID: 2, Scope: scope, CandidateKeys: []string{"dev-2"}, Username: "u", Password: "p", Revision: 1},
				},
			}}
			if err := mustSyncer(t, bad, store).Sync(context.Background()); err == nil {
				t.Fatalf("scope %q must be rejected as a malformed payload", scope)
			}
			// The last good cache is untouched.
			if _, ok := NewProvider(store).Resolve("dev-1"); !ok {
				t.Fatal("cache lost after an unknown scope was rejected")
			}
			if _, ok := NewProvider(store).Resolve("dev-2"); ok {
				t.Fatal("a credential with an unknown scope must not be cached")
			}
		})
	}
}

// ── Full authoritative snapshot semantics ────────────────────────────────────
//
// Absence from a SUCCESSFUL snapshot means revoked/unassigned. A valid empty
// list is authoritative. Last-good-cache behaviour applies only to fetch and
// payload failures, never to a successful snapshot.

// 1. cache [A], successful snapshot [] => cache is empty.
func TestSyncer_AuthoritativeEmptySnapshotClearsCache(t *testing.T) {
	store := newTestStore(t)
	seed := &fakeFetcher{resp: transport.CameraCredentialsResponse{
		Credentials: []transport.CameraCredentialPayload{devCred(1, "dev-1", 1)},
	}}
	if err := mustSyncer(t, seed, store).Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.Snapshot()) != 1 {
		t.Fatal("precondition: expected one cached credential")
	}

	empty := &fakeFetcher{resp: transport.CameraCredentialsResponse{Credentials: []transport.CameraCredentialPayload{}}}
	if err := mustSyncer(t, empty, store).Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := len(store.Snapshot()); got != 0 {
		t.Fatalf("cache = %d entries, want 0: an authoritative empty snapshot must clear it", got)
	}
	if _, ok := NewProvider(store).Resolve("dev-1"); ok {
		t.Fatal("a credential absent from the snapshot must no longer resolve")
	}
}

// 2. cache [A,B], successful snapshot [B] => A removed, B preserved.
func TestSyncer_ShortenedSnapshotRemovesOnlyAbsentEntries(t *testing.T) {
	store := newTestStore(t)
	seed := &fakeFetcher{resp: transport.CameraCredentialsResponse{
		Credentials: []transport.CameraCredentialPayload{devCred(1, "dev-a", 1), devCred(2, "dev-b", 1)},
	}}
	if err := mustSyncer(t, seed, store).Sync(context.Background()); err != nil {
		t.Fatal(err)
	}

	short := &fakeFetcher{resp: transport.CameraCredentialsResponse{
		Credentials: []transport.CameraCredentialPayload{devCred(2, "dev-b", 1)},
	}}
	if err := mustSyncer(t, short, store).Sync(context.Background()); err != nil {
		t.Fatal(err)
	}

	if _, ok := NewProvider(store).Resolve("dev-a"); ok {
		t.Fatal("dev-a was absent from the snapshot and must be removed")
	}
	got, ok := NewProvider(store).Resolve("dev-b")
	if !ok || got.ID != "2" {
		t.Fatalf("dev-b must be preserved, got %+v ok=%v", got, ok)
	}
}

// 3. cache [A], fetch error => A preserved exactly.
func TestSyncer_SaaSUnreachable_KeepsCache(t *testing.T) {
	store := newTestStore(t)
	ok := &fakeFetcher{resp: transport.CameraCredentialsResponse{
		Credentials: []transport.CameraCredentialPayload{devCred(1, "dev-1", 1)},
	}}
	if err := mustSyncer(t, ok, store).Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := store.Snapshot()

	down := &fakeFetcher{err: transport.ErrSaaSUnavailable}
	if err := mustSyncer(t, down, store).Sync(context.Background()); !errors.Is(err, transport.ErrSaaSUnavailable) {
		t.Fatalf("expected ErrSaaSUnavailable, got %v", err)
	}

	got, present := NewProvider(store).Resolve("dev-1")
	if !present || got.Password != "pass" {
		t.Fatalf("cache lost after SaaS outage: %+v present=%v", got, present)
	}
	if len(store.Snapshot()) != len(before) {
		t.Fatalf("cache size changed after a fetch error: %d -> %d", len(before), len(store.Snapshot()))
	}
}

// 3b. An unauthorized fetch is equally non-destructive.
func TestSyncer_UnauthorizedFetch_KeepsCache(t *testing.T) {
	store := newTestStore(t)
	ok := &fakeFetcher{resp: transport.CameraCredentialsResponse{
		Credentials: []transport.CameraCredentialPayload{devCred(1, "dev-1", 1)},
	}}
	if err := mustSyncer(t, ok, store).Sync(context.Background()); err != nil {
		t.Fatal(err)
	}

	denied := &fakeFetcher{err: transport.ErrUnauthorized}
	if err := mustSyncer(t, denied, store).Sync(context.Background()); err == nil {
		t.Fatal("expected an error for a 401")
	}
	if len(store.Snapshot()) != 1 {
		t.Fatal("a 401 must not mutate the cache")
	}
}

// 4. cache [A], malformed response => A preserved exactly.
func TestSyncer_MalformedPayload_KeepsCache(t *testing.T) {
	store := newTestStore(t)
	good := &fakeFetcher{resp: transport.CameraCredentialsResponse{
		Credentials: []transport.CameraCredentialPayload{devCred(1, "dev-1", 1)},
	}}
	if err := mustSyncer(t, good, store).Sync(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Malformed: an unknown scope AND a missing username.
	bad := &fakeFetcher{resp: transport.CameraCredentialsResponse{
		Credentials: []transport.CameraCredentialPayload{
			{ID: 2, Scope: "BOGUS", CandidateKeys: []string{"dev-2"}, Username: "", Password: "x", Revision: 1},
		},
	}}
	if err := mustSyncer(t, bad, store).Sync(context.Background()); err == nil {
		t.Fatal("expected error for malformed payload")
	}

	got, present := NewProvider(store).Resolve("dev-1")
	if !present || got.Password != "pass" {
		t.Fatalf("cache corrupted by malformed payload: %+v present=%v", got, present)
	}
	if _, present := NewProvider(store).Resolve("dev-2"); present {
		t.Fatal("malformed entry must not have been applied")
	}
}

// 5. persist failure => memory and disk both remain the previous state.
func TestSyncer_PersistFailureKeepsPreviousState(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root bypasses directory permission checks")
	}
	root := t.TempDir()
	// One master key for both opens: testKey generates a fresh random key per
	// call, and reopening with a different key would fail to decrypt (which is
	// the fail-closed behaviour, but not what this test is about).
	key := testKey(t)
	store, err := OpenStore(root, key)
	if err != nil {
		t.Fatal(err)
	}
	seed := &fakeFetcher{resp: transport.CameraCredentialsResponse{
		Credentials: []transport.CameraCredentialPayload{devCred(1, "dev-1", 1)},
	}}
	if err := mustSyncer(t, seed, store).Sync(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Make the directory unwritable so the atomic persist fails.
	if err := os.Chmod(root, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(root, 0o700) })

	next := &fakeFetcher{resp: transport.CameraCredentialsResponse{
		Credentials: []transport.CameraCredentialPayload{devCred(1, "dev-1", 2), devCred(2, "dev-2", 1)},
	}}
	if err := mustSyncer(t, next, store).Sync(context.Background()); err == nil {
		t.Fatal("expected the persist failure to surface")
	}

	// In-memory state is rolled back to the previous snapshot, not half-applied.
	snap := store.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("memory = %d entries after a persist failure, want 1 (previous state)", len(snap))
	}
	if snap[0].Revision != 1 {
		t.Fatalf("memory revision = %d, want 1 (the previous state)", snap[0].Revision)
	}

	// And a reopen from disk agrees.
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(root, key)
	if err != nil {
		t.Fatal(err)
	}
	if len(reopened.Snapshot()) != 1 {
		t.Fatal("disk and memory disagree after a persist failure")
	}
}

// TestSyncer_ApplyStatsAreCounted pins the sanitized added/updated/removed
// observability without exposing any secret.
func TestSyncer_ApplyStatsAreCounted(t *testing.T) {
	store := newTestStore(t)

	first := &fakeFetcher{resp: transport.CameraCredentialsResponse{
		Credentials: []transport.CameraCredentialPayload{devCred(1, "dev-a", 1), devCred(2, "dev-b", 1)},
	}}
	if err := mustSyncer(t, first, store).Sync(context.Background()); err != nil {
		t.Fatal(err)
	}

	// dev-a updated (higher revision), dev-b removed, dev-c added.
	second := &fakeFetcher{resp: transport.CameraCredentialsResponse{
		Credentials: []transport.CameraCredentialPayload{devCred(1, "dev-a", 2), devCred(3, "dev-c", 1)},
	}}
	syncer := mustSyncer(t, second, store)
	if err := syncer.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}

	snap := store.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("cache = %d entries, want 2", len(snap))
	}
	if _, ok := NewProvider(store).Resolve("dev-b"); ok {
		t.Fatal("dev-b should have been removed")
	}
	got, ok := NewProvider(store).Resolve("dev-a")
	if !ok || got.Revision != 2 {
		t.Fatalf("dev-a should have been updated to revision 2, got %+v ok=%v", got, ok)
	}
	if _, ok := NewProvider(store).Resolve("dev-c"); !ok {
		t.Fatal("dev-c should have been added")
	}
}

func TestSyncer_DecodesCandidateKeysArray(t *testing.T) {
	store := newTestStore(t)
	fetcher := &fakeFetcher{resp: transport.CameraCredentialsResponse{
		Credentials: []transport.CameraCredentialPayload{
			{ID: 7, Scope: "device", Username: "admin", Password: "p4ss", Revision: 7,
				CandidateKeys: []string{"MAC:00:11:22:33:44:55", "SN:ABC123"}},
		},
	}}
	if err := mustSyncer(t, fetcher, store).Sync(context.Background()); err != nil {
		t.Fatal(err)
	}

	provider := NewProvider(store)
	for _, key := range []string{"MAC:00:11:22:33:44:55", "SN:ABC123"} {
		got, ok := provider.Resolve(key)
		if !ok || got.ID != "7" {
			t.Fatalf("candidate_key %q did not resolve to cred 7: got=%+v ok=%v", key, got, ok)
		}
	}
}

// TestSyncer_OnSuccessFiresAfterApply is Hito Z G1-B's guard for section 4:
// OnSuccess must fire once Sync returns nil — after fetch, decode, and
// Store.Apply have already run — and must never fire on any failure path
// (transport error, malformed payload, persist failure all return before
// reaching it).
func TestSyncer_OnSuccessFiresAfterApply(t *testing.T) {
	store := newTestStore(t)
	var fired int
	var sawResolvable bool
	s, err := NewSyncer(SyncOptions{
		Client:     &fakeFetcher{resp: transport.CameraCredentialsResponse{Credentials: []transport.CameraCredentialPayload{devCred(1, "dev-1", 1)}}},
		Store:      store,
		DeviceID:   "d1",
		Credential: "cred",
		OnSuccess: func() {
			fired++
			// By the time OnSuccess runs, Store.Apply must have already
			// completed: the credential is resolvable right now, not on
			// some later tick.
			if _, ok := NewProvider(store).Resolve("dev-1"); ok {
				sawResolvable = true
			}
		},
	})
	if err != nil {
		t.Fatalf("NewSyncer: %v", err)
	}
	if err := s.Sync(context.Background()); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if fired != 1 {
		t.Fatalf("OnSuccess fired %d times, want 1", fired)
	}
	if !sawResolvable {
		t.Fatal("OnSuccess ran before Store.Apply took effect")
	}

	// A second sync with the SAME snapshot (nothing changed) must still
	// fire OnSuccess: the reconciler it drives is idempotent, and the
	// contract is "every successful sync", not "every sync that changed
	// something".
	if err := s.Sync(context.Background()); err != nil {
		t.Fatalf("Sync (second, unchanged): %v", err)
	}
	if fired != 2 {
		t.Fatalf("OnSuccess fired %d times after a second unchanged sync, want 2", fired)
	}
}

// TestSyncer_OnSuccessNeverFiresOnFailure covers every early-return path in
// Sync: transport failure, unauthorized, and a malformed payload. None of
// them may invoke OnSuccess — the last-good cache is left untouched and the
// reconciler must not be told anything changed.
func TestSyncer_OnSuccessNeverFiresOnFailure(t *testing.T) {
	cases := []struct {
		name    string
		fetcher Fetcher
	}{
		{"transport error", &fakeFetcher{err: transport.ErrSaaSUnavailable}},
		{"unauthorized", &fakeFetcher{err: transport.ErrUnauthorized}},
		{"malformed scope", &fakeFetcher{resp: transport.CameraCredentialsResponse{
			Credentials: []transport.CameraCredentialPayload{{ID: 1, Scope: "not-a-real-scope", CandidateKeys: []string{"dev-1"}, Username: "a", Password: "b", Revision: 1}},
		}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newTestStore(t)
			fired := false
			s, err := NewSyncer(SyncOptions{
				Client:     tc.fetcher,
				Store:      store,
				DeviceID:   "d1",
				Credential: "cred",
				OnSuccess:  func() { fired = true },
			})
			if err != nil {
				t.Fatalf("NewSyncer: %v", err)
			}
			if err := s.Sync(context.Background()); err == nil {
				t.Fatal("Sync succeeded, want an error for this case")
			}
			if fired {
				t.Errorf("OnSuccess fired on a failed sync (%s)", tc.name)
			}
		})
	}
}
