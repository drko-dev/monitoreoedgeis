package discovery

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestInventory_UpsertAndList(t *testing.T) {
	inv := NewInventory()
	if inv.Count() != 0 {
		t.Fatalf("expected empty inventory, got %d", inv.Count())
	}

	dev1 := DiscoveredDevice{
		StableIdentity: "epr:uuid-1",
		EPRAddress:     "uuid-1",
		IP:             "192.168.1.50",
		Port:           80,
		Path:           "/onvif/device_service",
		Manufacturer:   "VendorA",
		Model:          "Model1",
		DeviceType:     DeviceTypeCamera,
	}

	saved := inv.Upsert(dev1)
	if saved == nil {
		t.Fatal("Upsert returned nil")
	}
	if inv.Count() != 1 {
		t.Errorf("expected 1 item, got %d", inv.Count())
	}

	firstSeen := saved.FirstSeen
	time.Sleep(10 * time.Millisecond)

	// Rediscovery updates mutable fields but keeps FirstSeen
	dev1Update := dev1
	dev1Update.Model = "Model1-Updated"
	updated := inv.Upsert(dev1Update)

	if updated.Model != "Model1-Updated" {
		t.Errorf("expected updated model, got %s", updated.Model)
	}
	if !updated.FirstSeen.Equal(firstSeen) {
		t.Errorf("FirstSeen was altered: %v vs %v", updated.FirstSeen, firstSeen)
	}
	if updated.LastSeen.Before(firstSeen) {
		t.Errorf("LastSeen %v should be after FirstSeen %v", updated.LastSeen, firstSeen)
	}

	list := inv.List()
	if len(list) != 1 {
		t.Errorf("expected 1 item in list, got %d", len(list))
	}

	got := inv.Get("epr:uuid-1")
	if got == nil || got.Model != "Model1-Updated" {
		t.Errorf("Get returned unexpected device: %v", got)
	}
}

func TestInventory_CapRespected(t *testing.T) {
	inv := NewInventory()
	for i := 0; i < MaxInventoryDevices; i++ {
		inv.Upsert(DiscoveredDevice{
			StableIdentity: fmt.Sprintf("epr:dev-%d", i),
			IP:             "192.168.1.10",
			Port:           i + 1,
		})
	}
	if inv.Count() != MaxInventoryDevices {
		t.Fatalf("expected %d devices, got %d", MaxInventoryDevices, inv.Count())
	}

	// One more device beyond the cap must be rejected (fail-closed), not evict an existing one.
	rejected := inv.Upsert(DiscoveredDevice{StableIdentity: "epr:overflow", IP: "192.168.1.11", Port: 9999})
	if inv.Count() != MaxInventoryDevices {
		t.Errorf("expected inventory to stay capped at %d, got %d", MaxInventoryDevices, inv.Count())
	}
	if inv.Get("epr:overflow") != nil {
		t.Error("overflow device must not be persisted in the inventory")
	}
	if rejected == nil || rejected.IP != "192.168.1.11" {
		t.Errorf("expected Upsert to still report the rejected device for this scan, got %+v", rejected)
	}
}

func TestInventory_TTLEvictsStaleDevice(t *testing.T) {
	inv := NewInventory()
	inv.Upsert(DiscoveredDevice{StableIdentity: "epr:stale", IP: "192.168.1.20", Port: 80})

	// Backdate LastSeen beyond DeviceTTL to simulate a randomized-EPR camera that vanished.
	inv.mu.Lock()
	inv.devices["epr:stale"].LastSeen = time.Now().UTC().Add(-DeviceTTL - time.Minute)
	inv.mu.Unlock()

	// Any subsequent Upsert triggers the eviction sweep.
	inv.Upsert(DiscoveredDevice{StableIdentity: "epr:new", IP: "192.168.1.21", Port: 80})

	if inv.Get("epr:stale") != nil {
		t.Error("expected stale device to be evicted by TTL")
	}
	if inv.Get("epr:new") == nil {
		t.Error("expected freshly upserted device to remain")
	}
}

func TestInventory_ActiveDeviceNotEvicted(t *testing.T) {
	inv := NewInventory()
	inv.Upsert(DiscoveredDevice{StableIdentity: "epr:active", IP: "192.168.1.30", Port: 80})

	// Recently seen (well within TTL) — an eviction sweep must not touch it.
	inv.mu.Lock()
	inv.devices["epr:active"].LastSeen = time.Now().UTC().Add(-time.Minute)
	inv.mu.Unlock()

	inv.Upsert(DiscoveredDevice{StableIdentity: "epr:other", IP: "192.168.1.31", Port: 80})

	if inv.Get("epr:active") == nil {
		t.Error("expected recently-seen device to survive the eviction sweep")
	}
}

func TestInventory_ConcurrentAccess(t *testing.T) {
	inv := NewInventory()
	var wg sync.WaitGroup

	for i := 0; i < 50; i++ {
		wg.Add(2)
		idx := i
		go func() {
			defer wg.Done()
			inv.Upsert(DiscoveredDevice{
				StableIdentity: "epr:test",
				IP:             "192.168.1.10",
				Port:           80 + idx,
			})
		}()
		go func() {
			defer wg.Done()
			_ = inv.List()
			_ = inv.Count()
			_ = inv.Get("epr:test")
		}()
	}

	wg.Wait()
	if inv.Count() != 1 {
		t.Errorf("expected exactly 1 device, got %d", inv.Count())
	}
}
