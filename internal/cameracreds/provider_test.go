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

func TestProvider_MultichannelInheritance(t *testing.T) {
	store := newTestStore(t)
	if _, err := store.Apply([]Credential{
		{ID: "dvr-cred", Scope: ScopeDevice, CandidateKeys: []string{"epr:dvr-1"}, Username: "admin", Password: "pass-dvr", Revision: 1},
	}); err != nil {
		t.Fatal(err)
	}

	p := NewProvider(store)

	// Channels inherit recorder credential
	got1, ok := p.Resolve("epr:dvr-1|ch=1")
	if !ok || got1.ID != "dvr-cred" || got1.Username != "admin" {
		t.Fatalf("channel 1 failed to inherit recorder credential: got=%+v ok=%v", got1, ok)
	}

	got2, ok := p.Resolve("epr:dvr-1|ch=2")
	if !ok || got2.ID != "dvr-cred" || got2.Username != "admin" {
		t.Fatalf("channel 2 failed to inherit recorder credential: got=%+v ok=%v", got2, ok)
	}

	// Different device does not inherit
	if _, ok := p.Resolve("epr:dvr-2|ch=1"); ok {
		t.Fatal("unrelated device channel unexpectedly inherited credential")
	}
}

func TestProvider_ChannelOverridePrecedesRecorderCredential(t *testing.T) {
	store := newTestStore(t)
	if _, err := store.Apply([]Credential{
		{ID: "dvr-shared", Scope: ScopeDevice, CandidateKeys: []string{"epr:dvr-1"}, Username: "admin", Password: "pass-dvr", Revision: 1},
		{ID: "ch2-specific", Scope: ScopeDevice, CandidateKeys: []string{"epr:dvr-1|ch=2"}, Username: "special", Password: "pass-special", Revision: 1},
		{ID: "ch3-group", Scope: ScopeGroup, CandidateKeys: []string{"epr:dvr-1|ch=3"}, Username: "groupuser", Password: "pass-group", Revision: 1},
	}); err != nil {
		t.Fatal(err)
	}

	p := NewProvider(store)

	// Channel 1 inherits shared
	got1, ok := p.Resolve("epr:dvr-1|ch=1")
	if !ok || got1.ID != "dvr-shared" || got1.Username != "admin" {
		t.Fatalf("channel 1 should inherit shared: got %+v ok=%v", got1, ok)
	}

	// Channel 2 uses specific ScopeDevice override
	got2, ok := p.Resolve("epr:dvr-1|ch=2")
	if !ok || got2.ID != "ch2-specific" || got2.Username != "special" {
		t.Fatalf("channel 2 should use specific override: got %+v ok=%v", got2, ok)
	}

	// Channel 3 uses specific ScopeGroup override over inherited ScopeDevice
	got3, ok := p.Resolve("epr:dvr-1|ch=3")
	if !ok || got3.ID != "ch3-group" || got3.Username != "groupuser" {
		t.Fatalf("channel 3 should use specific group override over shared: got %+v ok=%v", got3, ok)
	}
}

func TestProvider_RecorderBootstrapsFromChannelCredential(t *testing.T) {
	store := newTestStore(t)
	if _, err := store.Apply([]Credential{
		{ID: "cred-2", Scope: ScopeDevice, CandidateKeys: []string{"epr:dvr-1|ch=2"}, Username: "user2", Password: "pass2", Revision: 1},
		{ID: "cred-1", Scope: ScopeDevice, CandidateKeys: []string{"epr:dvr-1|ch=1"}, Username: "user1", Password: "pass1", Revision: 1},
	}); err != nil {
		t.Fatal(err)
	}

	p := NewProvider(store)

	// No direct epr:dvr-1 exists; discovery/rediscovery calls p.Resolve("epr:dvr-1")
	// Deterministically picks cred-1 (smallest channel key)
	got, ok := p.Resolve("epr:dvr-1")
	if !ok || got.ID != "cred-1" || got.Username != "user1" {
		t.Fatalf("recorder failed to bootstrap from channel credential: got %+v ok=%v", got, ok)
	}
}

func TestProvider_ChannelIsolationNeverCrossesSiblings(t *testing.T) {
	store := newTestStore(t)
	if _, err := store.Apply([]Credential{
		{ID: "cred-ch1", Scope: ScopeDevice, CandidateKeys: []string{"epr:dvr-1|ch=1"}, Username: "user1", Password: "pass1", Revision: 1},
	}); err != nil {
		t.Fatal(err)
	}

	p := NewProvider(store)

	// Channel 1 has cred
	if _, ok := p.Resolve("epr:dvr-1|ch=1"); !ok {
		t.Fatal("channel 1 should resolve")
	}

	// Channel 2 must NOT inherit channel 1's cred!
	if got, ok := p.Resolve("epr:dvr-1|ch=2"); ok {
		t.Fatalf("channel 2 should NOT resolve from sibling channel 1: got %+v", got)
	}
}

func TestProvider_DeviceIsolationGuaranteed(t *testing.T) {
	store := newTestStore(t)
	if _, err := store.Apply([]Credential{
		{ID: "cred-dvr10-ch1", Scope: ScopeDevice, CandidateKeys: []string{"epr:dvr-10|ch=1"}, Username: "user10", Password: "pass10", Revision: 1},
		{ID: "cred-dvr1", Scope: ScopeDevice, CandidateKeys: []string{"epr:dvr-1"}, Username: "user1", Password: "pass1", Revision: 1},
	}); err != nil {
		t.Fatal(err)
	}

	p := NewProvider(store)

	// epr:dvr-1 has exact cred-dvr1; should NOT match epr:dvr-10|ch=1
	got1, ok := p.Resolve("epr:dvr-1")
	if !ok || got1.ID != "cred-dvr1" {
		t.Fatalf("epr:dvr-1 resolved wrong credential: got %+v", got1)
	}

	// Another device epr:dvr-2 has no creds; should not match epr:dvr-1 or epr:dvr-10
	if _, ok := p.Resolve("epr:dvr-2"); ok {
		t.Fatal("epr:dvr-2 unexpectedly resolved")
	}
	if _, ok := p.Resolve("epr:dvr-2|ch=1"); ok {
		t.Fatal("epr:dvr-2|ch=1 unexpectedly resolved")
	}
}

func TestProvider_RotationAndRevocation(t *testing.T) {
	store := newTestStore(t)
	// Initial state: shared recorder cred + channel 1 override
	if _, err := store.Apply([]Credential{
		{ID: "dvr-shared", Scope: ScopeDevice, CandidateKeys: []string{"epr:dvr-1"}, Username: "admin", Password: "initial-pass", Revision: 1},
		{ID: "ch1-override", Scope: ScopeDevice, CandidateKeys: []string{"epr:dvr-1|ch=1"}, Username: "operator1", Password: "ch1-pass", Revision: 1},
	}); err != nil {
		t.Fatal(err)
	}

	p := NewProvider(store)

	// Check initial resolution
	gotCh1, _ := p.Resolve("epr:dvr-1|ch=1")
	if gotCh1.Password != "ch1-pass" {
		t.Fatalf("want ch1-pass, got %s", gotCh1.Password)
	}
	gotCh2, _ := p.Resolve("epr:dvr-1|ch=2")
	if gotCh2.Password != "initial-pass" {
		t.Fatalf("want initial-pass, got %s", gotCh2.Password)
	}

	// Step 1: Rotate shared recorder credential
	if _, err := store.Apply([]Credential{
		{ID: "dvr-shared", Scope: ScopeDevice, CandidateKeys: []string{"epr:dvr-1"}, Username: "admin", Password: "rotated-pass", Revision: 2},
		{ID: "ch1-override", Scope: ScopeDevice, CandidateKeys: []string{"epr:dvr-1|ch=1"}, Username: "operator1", Password: "ch1-pass", Revision: 1},
	}); err != nil {
		t.Fatal(err)
	}

	// Channel 2 gets rotated pass; Channel 1 still has its override
	gotCh1, _ = p.Resolve("epr:dvr-1|ch=1")
	if gotCh1.Password != "ch1-pass" {
		t.Fatalf("channel 1 override corrupted: got %s", gotCh1.Password)
	}
	gotCh2, _ = p.Resolve("epr:dvr-1|ch=2")
	if gotCh2.Password != "rotated-pass" {
		t.Fatalf("channel 2 did not get rotated pass: got %s", gotCh2.Password)
	}

	// Step 2: Revoke Channel 1 override (sync payload no longer contains ch1-override)
	if _, err := store.Apply([]Credential{
		{ID: "dvr-shared", Scope: ScopeDevice, CandidateKeys: []string{"epr:dvr-1"}, Username: "admin", Password: "rotated-pass", Revision: 2},
	}); err != nil {
		t.Fatal(err)
	}

	// Channel 1 now falls back to inherited shared recorder cred!
	gotCh1, ok := p.Resolve("epr:dvr-1|ch=1")
	if !ok || gotCh1.Password != "rotated-pass" {
		t.Fatalf("channel 1 did not fall back to shared credential after revoke: got %+v ok=%v", gotCh1, ok)
	}

	// Step 3: Revoke shared recorder credential
	if _, err := store.Apply([]Credential{}); err != nil {
		t.Fatal(err)
	}

	if _, ok := p.Resolve("epr:dvr-1|ch=1"); ok {
		t.Fatal("channel 1 should not resolve after total revocation")
	}
	if _, ok := p.Resolve("epr:dvr-1|ch=2"); ok {
		t.Fatal("channel 2 should not resolve after total revocation")
	}
	if _, ok := p.Resolve("epr:dvr-1"); ok {
		t.Fatal("recorder should not resolve after total revocation")
	}
}
