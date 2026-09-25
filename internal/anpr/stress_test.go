package anpr

import (
	"testing"
	"time"
)

// TestAdversarialStress: 1 camera, 10,000 synthetic candidate triggers.
// Active bursts must stay <= MaxActiveBurstsPerCamera, frames per burst
// <= MaxFramesPerBurst, dedupe/memory state bounded throughout, and Cleanup
// releases everything afterward. No sleeps (BurstManager's clock is
// injected and only ever advanced explicitly).
func TestAdversarialStress(t *testing.T) {
	clock := newFakeClock(time.Unix(0, 0))
	cfg := Config{
		Enabled:                  true,
		MaxActiveBurstsPerCamera: 5,
		MaxFramesPerBurst:        5,
		MaxContextFrames:         5,
		MaxCameras:               1,
		BurstTTL:                 3 * time.Second,
		FrameSelection:           SelectFirst,
		DedupeCacheSize:          256,
	}
	bm := NewBurstManager(cfg, clock.Now)

	const total = 10000
	for i := 0; i < total; i++ {
		v := testVehicleCandidate("cam-1", uint64(i+1))
		// Vary bbox and TrackID to create a realistic mix of groups,
		// duplicates and out-of-order-ish traffic without ever exceeding
		// bounded memory.
		v.TrackID = trackFor(i)
		v.VehicleBBox.X0 += float64(i % 37)
		v.VehicleBBox.X1 += float64(i % 37)

		bm.Process(v)

		if got := bm.ActiveBurstCount("cam-1"); got > cfg.MaxActiveBurstsPerCamera {
			t.Fatalf("iteration %d: active bursts %d exceeds bound %d", i, got, cfg.MaxActiveBurstsPerCamera)
		}
		if got := bm.DedupeCacheLen(); got > cfg.DedupeCacheSize {
			t.Fatalf("iteration %d: dedupe cache %d exceeds bound %d", i, got, cfg.DedupeCacheSize)
		}

		// Periodically advance the clock and clean up, exactly like a real
		// housekeeping loop would (no per-candidate timer, no sleep).
		if i%500 == 499 {
			clock.Advance(4 * time.Second) // past BurstTTL: everything currently open expires
			bm.Cleanup()
			if bm.TotalBurstCount() != 0 {
				t.Fatalf("iteration %d: expected cleanup to release all expired bursts, got %d remaining", i, bm.TotalBurstCount())
			}
		}
	}

	clock.Advance(4 * time.Second)
	bm.Cleanup()
	if bm.TotalBurstCount() != 0 {
		t.Fatalf("expected zero bursts remaining after final cleanup, got %d", bm.TotalBurstCount())
	}
}

func trackFor(i int) string {
	// A handful of rotating track ids, so groups form and dissolve
	// realistically instead of every candidate being its own group.
	return "track-" + string(rune('A'+(i%5)))
}
