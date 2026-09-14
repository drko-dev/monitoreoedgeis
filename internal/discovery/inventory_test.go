package discovery

import (
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
