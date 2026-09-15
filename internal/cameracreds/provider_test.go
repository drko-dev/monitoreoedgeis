package cameracreds

import "testing"

func TestProvider_DevicePrecedesGroup(t *testing.T) {
	store := newTestStore(t)
	if _, err := store.Apply([]Credential{
		{ID: "group-cred", Scope: ScopeGroup, CandidateKeys: []string{"group-1"}, Username: "group-user", Password: "group-pass", Revision: 1},
		{ID: "device-cred", Scope: ScopeDevice, CandidateKeys: []string{"dev-1"}, Username: "device-user", Password: "device-pass", Revision: 1},
	}); err != nil {
		t.Fatal(err)
	}

	p := NewProvider(store)

	got, ok := p.Resolve("dev-1", "group-1")
	if !ok {
		t.Fatal("expected a match")
	}
	if got.Username != "device-user" {
		t.Fatalf("DEVICE assignment should win over GROUP, got %+v", got)
	}

	// No device match: falls back to group.
	got, ok = p.Resolve("dev-2", "group-1")
	if !ok || got.Username != "group-user" {
		t.Fatalf("expected group fallback, got %+v ok=%v", got, ok)
	}

	// Neither matches.
	if _, ok := p.Resolve("dev-2", "group-2"); ok {
		t.Fatal("expected no match")
	}

	// Never matches by IP-shaped identity unless it happens to equal a
	// stored stable identity — Resolve does no IP parsing/normalization at
	// all, it's a pure membership check against CandidateKeys.
	if _, ok := p.Resolve("", ""); ok {
		t.Fatal("empty identity must never match")
	}
}

func TestProvider_GroupMatchesAnyCandidateKey(t *testing.T) {
	store := newTestStore(t)
	if _, err := store.Apply([]Credential{
		{ID: "group-cred", Scope: ScopeGroup, CandidateKeys: []string{"group-1", "group-2", "group-3"}, Username: "group-user", Password: "group-pass", Revision: 1},
	}); err != nil {
		t.Fatal(err)
	}

	p := NewProvider(store)
	for _, key := range []string{"group-1", "group-2", "group-3"} {
		got, ok := p.Resolve("", key)
		if !ok || got.ID != "group-cred" {
			t.Fatalf("group candidate_key %q did not resolve: got=%+v ok=%v", key, got, ok)
		}
	}
	if _, ok := p.Resolve("", "group-4"); ok {
		t.Fatal("unassigned group id must not match")
	}
}

func TestProvider_Resolve_DeterministicOnDuplicateCandidateKey(t *testing.T) {
	// SaaS enforces uniqueness via a 409 on assignment; this exercises the
	// defense-in-depth path against a stale/duplicate cache with two DEVICE
	// credentials pointing at the same candidate key.
	store := newTestStore(t)
	if _, err := store.Apply([]Credential{
		{ID: "cred-b", Scope: ScopeDevice, CandidateKeys: []string{"dev-1"}, Username: "user-b", Password: "pass-b", Revision: 1},
		{ID: "cred-a", Scope: ScopeDevice, CandidateKeys: []string{"dev-1"}, Username: "user-a", Password: "pass-a", Revision: 1},
	}); err != nil {
		t.Fatal(err)
	}

	p := NewProvider(store)
	var first Credential
	for i := 0; i < 50; i++ {
		got, ok := p.Resolve("dev-1", "")
		if !ok {
			t.Fatal("expected a match")
		}
		if i == 0 {
			first = got
		} else if got.ID != first.ID {
			t.Fatalf("non-deterministic resolution: run %d got %q, run 0 got %q", i, got.ID, first.ID)
		}
	}
	if first.ID != "cred-a" {
		t.Fatalf("expected lexicographically smallest ID cred-a to win, got %q", first.ID)
	}
}
