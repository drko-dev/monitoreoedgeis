package cameracreds

import "testing"

// TestProvider_DevicePrecedesGroupByCandidateKey pins the real resolution
// contract: BOTH scopes are matched by candidate key, and DEVICE wins.
//
// The SaaS sends genuine per-camera candidate keys for both scopes — a GROUP
// credential's candidate_keys are the cameras assigned to that group, not a
// group identifier (monitoreoia's camera_credentials docs, "Sync
// device-facing"). It also resolves DEVICE-over-GROUP precedence server-side
// before sending. This test therefore exercises the same candidate key
// appearing under both scopes.
func TestProvider_DevicePrecedesGroupByCandidateKey(t *testing.T) {
	store := newTestStore(t)
	if _, err := store.Apply([]Credential{
		{ID: "group-cred", Scope: ScopeGroup, CandidateKeys: []string{"cam-1", "cam-2"}, Username: "group-user", Password: "group-pass", Revision: 1},
		{ID: "device-cred", Scope: ScopeDevice, CandidateKeys: []string{"cam-1"}, Username: "device-user", Password: "device-pass", Revision: 1},
	}); err != nil {
		t.Fatal(err)
	}

	p := NewProvider(store)

	// cam-1 has both a DEVICE and a GROUP credential: DEVICE must win.
	got, ok := p.Resolve("cam-1")
	if !ok {
		t.Fatal("expected a match for cam-1")
	}
	if got.ID != "device-cred" || got.Username != "device-user" {
		t.Fatalf("DEVICE assignment should win over GROUP, got %+v", got)
	}

	// cam-2 has only the GROUP credential: it must still resolve, by its
	// candidate key — no groupID is required or invented.
	got, ok = p.Resolve("cam-2")
	if !ok || got.ID != "group-cred" {
		t.Fatalf("GROUP credential should resolve by candidate key, got %+v ok=%v", got, ok)
	}

	// A candidate no credential is assigned to.
	if _, ok := p.Resolve("cam-3"); ok {
		t.Fatal("expected no match for an unassigned candidate key")
	}

	// The empty key must never match, even when a credential exists whose
	// candidate list would otherwise be scanned.
	if _, ok := p.Resolve(""); ok {
		t.Fatal("empty candidate key must never match")
	}
}

// TestProvider_GroupResolvesByAnyCandidateKey covers a GROUP credential
// assigned to several cameras: every one of its candidate keys resolves to it.
func TestProvider_GroupResolvesByAnyCandidateKey(t *testing.T) {
	store := newTestStore(t)
	if _, err := store.Apply([]Credential{
		{ID: "group-cred", Scope: ScopeGroup, CandidateKeys: []string{"cam-a", "cam-b", "cam-c"}, Username: "group-user", Password: "group-pass", Revision: 1},
	}); err != nil {
		t.Fatal(err)
	}

	p := NewProvider(store)
	for _, key := range []string{"cam-a", "cam-b", "cam-c"} {
		got, ok := p.Resolve(key)
		if !ok || got.ID != "group-cred" {
			t.Fatalf("group candidate key %q did not resolve: got=%+v ok=%v", key, got, ok)
		}
	}
	if _, ok := p.Resolve("cam-d"); ok {
		t.Fatal("an unassigned candidate key must not match a group credential")
	}
}

// TestProvider_NoIPFallback documents that resolution is a pure membership
// check on candidate keys: there is no IP parsing, no address normalization,
// and no fallback that could match a camera by its address.
func TestProvider_NoIPFallback(t *testing.T) {
	store := newTestStore(t)
	if _, err := store.Apply([]Credential{
		{ID: "cred-1", Scope: ScopeDevice, CandidateKeys: []string{"epr:abc"}, Username: "u", Password: "p", Revision: 1},
	}); err != nil {
		t.Fatal(err)
	}

	p := NewProvider(store)
	for _, ip := range []string{"192.168.1.50", "192.168.1.50:554", "10.0.0.1/stream1"} {
		if _, ok := p.Resolve(ip); ok {
			t.Fatalf("resolved %q — candidate resolution must never match by address", ip)
		}
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
		got, ok := p.Resolve("dev-1")
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
