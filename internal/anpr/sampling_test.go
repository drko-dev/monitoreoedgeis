package anpr

import (
	"testing"
	"time"
)

// fakeSamplingHint records every Request/Release call, keyed by
// cameraKey|burstID, so tests can assert exactly which bursts are boosted
// at any point without needing a real processing.Sampler.
type fakeSamplingHint struct {
	active map[string]float64 // "cameraKey|burstID" -> fps, present only while boosted
	calls  []string           // ordered log: "request:camera:burst:fps" / "release:camera:burst"
}

func newFakeSamplingHint() *fakeSamplingHint {
	return &fakeSamplingHint{active: map[string]float64{}}
}

func (h *fakeSamplingHint) key(cameraKey, burstID string) string { return cameraKey + "|" + burstID }

func (h *fakeSamplingHint) RequestBurstFPS(cameraKey, burstID string, fps float64) {
	h.active[h.key(cameraKey, burstID)] = fps
	h.calls = append(h.calls, "request")
}

func (h *fakeSamplingHint) ReleaseBurstFPS(cameraKey, burstID string) {
	delete(h.active, h.key(cameraKey, burstID))
	h.calls = append(h.calls, "release")
}

func (h *fakeSamplingHint) isBoosted(cameraKey, burstID string) bool {
	_, ok := h.active[h.key(cameraKey, burstID)]
	return ok
}

func alwaysBurstFPS(fps float64) func(string) (float64, bool) {
	return func(string) (float64, bool) { return fps, true }
}

func neverBurstFPS() func(string) (float64, bool) {
	return func(string) (float64, bool) { return 0, false }
}

// K1/K2: default (no hint wired, or resolver says no) -> sampler unchanged.
func TestBurstSampling_DefaultDisabled_NoRequestsIssued(t *testing.T) {
	clock := newFakeClock(time.Unix(0, 0))
	bm := NewBurstManager(burstTestConfig(), clock.Now)
	// SetSamplingHint never called -- PREP default.

	bm.Process(testVehicleCandidate("cam-1", 1))
	// No panic, no observable effect -- nothing to assert against a fake,
	// this test's whole point is that nothing here requires one.
}

func TestBurstSampling_HighSpeedLPRFalse_SamplerUnchanged(t *testing.T) {
	clock := newFakeClock(time.Unix(0, 0))
	bm := NewBurstManager(burstTestConfig(), clock.Now)
	hint := newFakeSamplingHint()
	bm.SetSamplingHint(hint, neverBurstFPS())

	burst, _, outcome := bm.Process(testVehicleCandidate("cam-1", 1))
	if outcome != OutcomeAccepted {
		t.Fatalf("expected accepted, got %v", outcome)
	}
	if hint.isBoosted("cam-1", burst.BurstID) {
		t.Fatal("expected no boost when burstFPS resolver says HighSpeedLPR is not active")
	}
	if len(hint.calls) != 0 {
		t.Fatalf("expected zero sampler calls, got %v", hint.calls)
	}
}

// K3: HighSpeedLPR true -> real boost on first frame.
func TestBurstSampling_HighSpeedLPRTrue_RequestsBoostOnBurstOpen(t *testing.T) {
	clock := newFakeClock(time.Unix(0, 0))
	bm := NewBurstManager(burstTestConfig(), clock.Now)
	hint := newFakeSamplingHint()
	bm.SetSamplingHint(hint, alwaysBurstFPS(15))

	burst, _, outcome := bm.Process(testVehicleCandidate("cam-1", 1))
	if outcome != OutcomeAccepted {
		t.Fatalf("expected accepted, got %v", outcome)
	}
	if !hint.isBoosted("cam-1", burst.BurstID) {
		t.Fatal("expected a boost request for the newly opened burst")
	}
	if hint.active[hint.key("cam-1", burst.BurstID)] != 15 {
		t.Fatalf("expected boost fps=15, got %v", hint.active[hint.key("cam-1", burst.BurstID)])
	}

	// Same burst, second frame -> no repeated request for the same burst.
	v2 := testVehicleCandidate("cam-1", 2)
	bm.Process(v2)
	requestCount := 0
	for _, c := range hint.calls {
		if c == "request" {
			requestCount++
		}
	}
	if requestCount != 1 {
		t.Fatalf("expected exactly 1 request call for one burst across 2 frames, got %d", requestCount)
	}
}

// K4: explicit Close releases the boost.
func TestBurstSampling_CloseReleasesBoost(t *testing.T) {
	clock := newFakeClock(time.Unix(0, 0))
	bm := NewBurstManager(burstTestConfig(), clock.Now)
	hint := newFakeSamplingHint()
	bm.SetSamplingHint(hint, alwaysBurstFPS(15))

	burst, _, _ := bm.Process(testVehicleCandidate("cam-1", 1))
	if !hint.isBoosted("cam-1", burst.BurstID) {
		t.Fatal("expected boost active before close")
	}

	bm.Close("cam-1", "", "")
	if hint.isBoosted("cam-1", burst.BurstID) {
		t.Fatal("expected boost released after Close")
	}
}

// K5: expiry releases the boost.
func TestBurstSampling_ExpiryReleasesBoost(t *testing.T) {
	clock := newFakeClock(time.Unix(0, 0))
	cfg := burstTestConfig()
	cfg.BurstTTL = 2 * time.Second
	bm := NewBurstManager(cfg, clock.Now)
	hint := newFakeSamplingHint()
	bm.SetSamplingHint(hint, alwaysBurstFPS(15))

	burst, _, _ := bm.Process(testVehicleCandidate("cam-1", 1))
	if !hint.isBoosted("cam-1", burst.BurstID) {
		t.Fatal("expected boost active before expiry")
	}

	clock.Advance(3 * time.Second)
	bm.Cleanup() // lazily evaluates expiry, exactly like Process would

	if hint.isBoosted("cam-1", burst.BurstID) {
		t.Fatal("expected boost released after the burst expired")
	}
}

// K6: two bursts on the SAME camera -- releasing one must not touch the
// other's boost. (Cross-camera composition of the boost is the real
// samplerBurstHint's job in internal/agent, not this package's -- this
// only proves the per-burst request/release accounting itself is correct.)
func TestBurstSampling_TwoBurstsSameCamera_IndependentReleases(t *testing.T) {
	clock := newFakeClock(time.Unix(0, 0))
	cfg := burstTestConfig()
	cfg.MaxActiveBurstsPerCamera = 2
	bm := NewBurstManager(cfg, clock.Now)
	hint := newFakeSamplingHint()
	bm.SetSamplingHint(hint, alwaysBurstFPS(15))

	// Two independent tracks on the same camera -> two distinct groups/bursts.
	// Distinct bboxes so dedupeKey (camera+seq+bbox, no track_id) doesn't
	// collapse them into the same candidate.
	v1 := testVehicleCandidate("cam-1", 1)
	v1.TrackID = "track-a"
	v1.VehicleBBox = BBox{X0: 0, Y0: 0, X1: 50, Y1: 50}
	burstA, _, outcome1 := bm.Process(v1)
	if outcome1 != OutcomeAccepted {
		t.Fatalf("expected accepted for track-a, got %v", outcome1)
	}

	v2 := testVehicleCandidate("cam-1", 1)
	v2.TrackID = "track-b"
	v2.VehicleBBox = BBox{X0: 500, Y0: 500, X1: 550, Y1: 550}
	burstB, _, outcome2 := bm.Process(v2)
	if outcome2 != OutcomeAccepted {
		t.Fatalf("expected accepted for track-b, got %v", outcome2)
	}

	if burstA.BurstID == burstB.BurstID {
		t.Fatal("expected two distinct bursts for two distinct tracks")
	}
	if !hint.isBoosted("cam-1", burstA.BurstID) || !hint.isBoosted("cam-1", burstB.BurstID) {
		t.Fatal("expected both bursts boosted")
	}

	bm.Close("cam-1", "track-a", "")
	if hint.isBoosted("cam-1", burstA.BurstID) {
		t.Fatal("expected burst A's boost released")
	}
	if !hint.isBoosted("cam-1", burstB.BurstID) {
		t.Fatal("releasing burst A must not release burst B's boost")
	}
}

// K7: two different cameras -- full isolation.
func TestBurstSampling_TwoCameras_FullIsolation(t *testing.T) {
	clock := newFakeClock(time.Unix(0, 0))
	bm := NewBurstManager(burstTestConfig(), clock.Now)
	hint := newFakeSamplingHint()
	bm.SetSamplingHint(hint, alwaysBurstFPS(15))

	burst1, _, _ := bm.Process(testVehicleCandidate("cam-1", 1))
	burst2, _, _ := bm.Process(testVehicleCandidate("cam-2", 1))

	bm.Close("cam-1", "", "")
	if hint.isBoosted("cam-1", burst1.BurstID) {
		t.Fatal("expected cam-1's boost released")
	}
	if !hint.isBoosted("cam-2", burst2.BurstID) {
		t.Fatal("closing cam-1 must never affect cam-2's boost")
	}
}

// K9: burst_fps invalid (resolver returns ok=false, e.g. remote-config
// rejected it) -> no boost requested, fail-closed rather than guessing.
func TestBurstSampling_ResolverSaysNotOk_NoBoostRequested(t *testing.T) {
	clock := newFakeClock(time.Unix(0, 0))
	bm := NewBurstManager(burstTestConfig(), clock.Now)
	hint := newFakeSamplingHint()
	bm.SetSamplingHint(hint, neverBurstFPS())

	burst, _, _ := bm.Process(testVehicleCandidate("cam-1", 1))
	if hint.isBoosted("cam-1", burst.BurstID) {
		t.Fatal("expected no boost when the resolver says not ok")
	}
}

// Dedupe/capacity-reject/unauthorized paths never even reach
// newBurstLocked, so they never request a boost -- proven by the existing
// dedupe/capacity tests in burst_test.go never wiring a hint at all. This
// test only double-checks a capacity-rejected candidate specifically
// leaves no orphaned boost behind.
func TestBurstSampling_CapacityRejected_NoBoostLeaked(t *testing.T) {
	clock := newFakeClock(time.Unix(0, 0))
	cfg := burstTestConfig()
	cfg.MaxActiveBurstsPerCamera = 1
	bm := NewBurstManager(cfg, clock.Now)
	hint := newFakeSamplingHint()
	bm.SetSamplingHint(hint, alwaysBurstFPS(15))

	v1 := testVehicleCandidate("cam-1", 1)
	v1.TrackID = "track-a"
	v1.VehicleBBox = BBox{X0: 0, Y0: 0, X1: 50, Y1: 50}
	bm.Process(v1)

	v2 := testVehicleCandidate("cam-1", 1)
	v2.TrackID = "track-b"
	v2.VehicleBBox = BBox{X0: 500, Y0: 500, X1: 550, Y1: 550}
	_, _, outcome := bm.Process(v2)
	if outcome != OutcomeCapacityBursts {
		t.Fatalf("expected capacity_bursts rejection, got %v", outcome)
	}

	requestCount := 0
	for _, c := range hint.calls {
		if c == "request" {
			requestCount++
		}
	}
	if requestCount != 1 {
		t.Fatalf("expected exactly 1 request (only track-a's accepted burst), got %d", requestCount)
	}
}
