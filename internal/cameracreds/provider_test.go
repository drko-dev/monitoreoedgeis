package cameracreds

import "testing"

func TestProvider_DevicePrecedesGroup(t *testing.T) {
	store := newTestStore(t)
	if _, err := store.Apply([]Credential{
		{ID: "group-cred", Scope: ScopeGroup, TargetID: "group-1", Username: "group-user", Password: "group-pass", Revision: 1},
		{ID: "device-cred", Scope: ScopeDevice, TargetID: "dev-1", Username: "device-user", Password: "device-pass", Revision: 1},
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
	// all, it's a pure string match against TargetID.
	if _, ok := p.Resolve("", ""); ok {
		t.Fatal("empty identity must never match")
	}
}
