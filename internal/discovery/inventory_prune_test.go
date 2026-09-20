package discovery

import (
	"testing"
	"time"
)

// seedDevice upserts a device and then backdates its LastSeen directly.
// Upsert always stamps LastSeen with the current time on insert, so a stale
// device can only be staged by editing the stored entry — the same technique
// the existing TTL test uses.
func seedDevice(inv *Inventory, id string, lastSeen time.Time) {
	inv.Upsert(DiscoveredDevice{
		StableIdentity: id,
		IP:             "192.168.1.10",
		Port:           80,
		Path:           "/onvif/device_service",
	})
	inv.mu.Lock()
	if d, ok := inv.devices[id]; ok {
		d.LastSeen = lastSeen
	}
	inv.mu.Unlock()
}

// TestInventory_PruneExpired_ActiveRetained covers the "a single multicast miss
// must not remove a camera" rule: a device seen recently survives.
// setLastSeen backdates a stored device without going through Upsert (which
// would re-stamp it). Each Upsert also runs an eviction sweep, so tests that
// stage several devices must seed them all first and backdate afterwards.
func setLastSeen(inv *Inventory, id string, when time.Time) {
	inv.mu.Lock()
	if d, ok := inv.devices[id]; ok {
		d.LastSeen = when
	}
	inv.mu.Unlock()
}

func TestInventory_PruneExpired_ActiveRetained(t *testing.T) {
	inv := NewInventory()
	now := time.Now().UTC()
	seedDevice(inv, "cam-active", now.Add(-time.Minute))

	if removed := inv.PruneExpired(now); removed != 0 {
		t.Fatalf("pruned %d devices, want 0 — a recently seen device must survive", removed)
	}
	if inv.Count() != 1 {
		t.Fatalf("count = %d, want 1", inv.Count())
	}
}

// TestInventory_PruneExpired_ExpiredRemoved: a device older than DeviceTTL goes.
func TestInventory_PruneExpired_ExpiredRemoved(t *testing.T) {
	inv := NewInventory()
	now := time.Now().UTC()
	seedDevice(inv, "cam-stale", now.Add(-DeviceTTL-time.Hour))

	if removed := inv.PruneExpired(now); removed != 1 {
		t.Fatalf("pruned %d devices, want 1", removed)
	}
	if inv.Count() != 0 {
		t.Fatalf("count = %d, want 0 after pruning", inv.Count())
	}
	if got := inv.Get("cam-stale"); got != nil {
		t.Fatalf("Get returned a pruned device: %+v", got)
	}
}

// TestInventory_PruneExpired_BoundaryIsNotYetExpired pins that the TTL is a
// strict "older than", so a device exactly at the boundary is still active.
func TestInventory_PruneExpired_BoundaryIsNotYetExpired(t *testing.T) {
	inv := NewInventory()
	now := time.Now().UTC()
	seedDevice(inv, "cam-boundary", now.Add(-DeviceTTL))

	if removed := inv.PruneExpired(now); removed != 0 {
		t.Fatalf("pruned %d devices, want 0 — exactly at DeviceTTL is not yet expired", removed)
	}
	if inv.Count() != 1 || len(inv.List()) != 1 {
		t.Fatal("Count and List disagree with the prune result")
	}
}

// TestInventory_PruneExpired_ListAndCountAgreeAfterPrune keeps the two
// readers consistent with the pruned state.
func TestInventory_PruneExpired_ListAndCountAgreeAfterPrune(t *testing.T) {
	inv := NewInventory()
	now := time.Now().UTC()
	// Seed first (each Upsert sweeps expired entries), then backdate.
	for _, id := range []string{"cam-a", "cam-b", "cam-c"} {
		seedDevice(inv, id, now)
	}
	setLastSeen(inv, "cam-a", now.Add(-time.Minute))
	setLastSeen(inv, "cam-b", now.Add(-DeviceTTL-time.Minute))
	setLastSeen(inv, "cam-c", now.Add(-2*DeviceTTL))

	if removed := inv.PruneExpired(now); removed != 2 {
		t.Fatalf("pruned %d, want 2", removed)
	}
	if inv.Count() != 1 {
		t.Fatalf("count = %d, want 1", inv.Count())
	}
	list := inv.List()
	if len(list) != 1 {
		t.Fatalf("list = %d entries, want 1", len(list))
	}
	if list[0].StableIdentity != "cam-a" {
		t.Fatalf("surviving device = %q, want cam-a", list[0].StableIdentity)
	}
}

// TestInventory_PruneExpired_Idempotent: running it twice changes nothing.
func TestInventory_PruneExpired_Idempotent(t *testing.T) {
	inv := NewInventory()
	now := time.Now().UTC()
	seedDevice(inv, "cam-stale", now.Add(-DeviceTTL-time.Hour))

	if removed := inv.PruneExpired(now); removed != 1 {
		t.Fatalf("first prune removed %d, want 1", removed)
	}
	if removed := inv.PruneExpired(now); removed != 0 {
		t.Fatalf("second prune removed %d, want 0", removed)
	}
}

// TestInventory_PruneExpired_EmptyInventoryIsSafe covers the zero-device case,
// which is what a successful scan that found nothing leaves behind.
func TestInventory_PruneExpired_EmptyInventoryIsSafe(t *testing.T) {
	inv := NewInventory()
	if removed := inv.PruneExpired(time.Now().UTC()); removed != 0 {
		t.Fatalf("pruned %d from an empty inventory, want 0", removed)
	}
	if inv.Count() != 0 || len(inv.List()) != 0 {
		t.Fatal("empty inventory reported devices after a prune")
	}
}
