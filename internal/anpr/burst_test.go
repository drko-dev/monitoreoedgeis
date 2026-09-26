package anpr

import (
	"testing"
	"time"
)

func burstTestConfig() Config {
	return Config{
		Enabled:                  true,
		MaxActiveBurstsPerCamera: 2,
		MaxFramesPerBurst:        3,
		MaxContextFrames:         5,
		MaxCameras:               10,
		BurstTTL:                 5 * time.Second,
		FrameSelection:           SelectFirst,
		DedupeCacheSize:          100,
	}
}

// B8: burst starts.
func TestB8_BurstStarts(t *testing.T) {
	clock := newFakeClock(time.Unix(0, 0))
	bm := NewBurstManager(burstTestConfig(), clock.Now)

	burst, id, outcome := bm.Process(testVehicleCandidate("cam-1", 1))
	if outcome != OutcomeAccepted {
		t.Fatalf("expected accepted, got %v", outcome)
	}
	if burst.State != BurstOpen && burst.State != BurstFull {
		t.Fatalf("expected OPEN (or FULL if MaxFrames==1), got %v", burst.State)
	}
	if id == "" {
		t.Fatal("expected non-empty candidate id")
	}
	if bm.ActiveBurstCount("cam-1") != 1 {
		t.Fatalf("expected 1 active burst, got %d", bm.ActiveBurstCount("cam-1"))
	}
}

// B9: burst max frames enforced — 100 candidates in, at most MaxFrames
// retained/accepted for that burst.
func TestB9_BurstMaxFramesEnforced(t *testing.T) {
	clock := newFakeClock(time.Unix(0, 0))
	cfg := burstTestConfig()
	cfg.MaxFramesPerBurst = 5
	bm := NewBurstManager(cfg, clock.Now)

	accepted := 0
	for i := uint64(1); i <= 100; i++ {
		v := testVehicleCandidate("cam-1", i)
		v.VehicleBBox.X0 += float64(i) // vary bbox slightly to avoid dedupe collisions
		v.VehicleBBox.X1 += float64(i)
		_, _, outcome := bm.Process(v)
		if outcome == OutcomeAccepted {
			accepted++
		}
	}
	if accepted != 5 {
		t.Fatalf("expected exactly MaxFramesPerBurst=5 accepted, got %d", accepted)
	}
}

// B10: burst expires — no sleeps, injectable clock advanced past TTL.
func TestB10_BurstExpires(t *testing.T) {
	clock := newFakeClock(time.Unix(0, 0))
	cfg := burstTestConfig()
	cfg.BurstTTL = 2 * time.Second
	bm := NewBurstManager(cfg, clock.Now)

	burst, _, outcome := bm.Process(testVehicleCandidate("cam-1", 1))
	if outcome != OutcomeAccepted {
		t.Fatalf("expected accepted, got %v", outcome)
	}
	firstID := burst.BurstID

	clock.Advance(3 * time.Second)
	// Any Process call lazily evaluates expiry.
	v2 := testVehicleCandidate("cam-1", 2)
	newBurst, _, outcome2 := bm.Process(v2)
	if outcome2 != OutcomeAccepted {
		t.Fatalf("expected new burst accepted after expiry, got %v", outcome2)
	}
	if newBurst.BurstID == firstID {
		t.Fatal("expected a new burst id after the old one expired")
	}
}

// B11: closed burst cannot reopen.
func TestB11_ClosedBurstCannotReopen(t *testing.T) {
	clock := newFakeClock(time.Unix(0, 0))
	bm := NewBurstManager(burstTestConfig(), clock.Now)

	v := testVehicleCandidate("cam-1", 1)
	bm.Process(v)
	bm.Close(v.CameraKey, v.TrackID, v.CorrelationID)

	// Same group, but the burst is CLOSED: a new candidate must create a
	// brand new burst, never resurrect the closed one.
	v2 := testVehicleCandidate("cam-1", 2)
	burst, _, outcome := bm.Process(v2)
	if outcome != OutcomeAccepted {
		t.Fatalf("expected accepted into a fresh burst, got %v", outcome)
	}
	if burst.State == BurstClosed {
		t.Fatal("burst must never be CLOSED right after being (re)created")
	}
}

// B12: new event creates new burst (same as B11's second half, verified
// directly against BurstID uniqueness).
func TestB12_NewEventCreatesNewBurst(t *testing.T) {
	clock := newFakeClock(time.Unix(0, 0))
	bm := NewBurstManager(burstTestConfig(), clock.Now)

	v := testVehicleCandidate("cam-1", 1)
	b1, _, _ := bm.Process(v)
	bm.Close(v.CameraKey, v.TrackID, v.CorrelationID)

	v2 := testVehicleCandidate("cam-1", 2)
	b2, _, _ := bm.Process(v2)
	if b1.BurstID == b2.BurstID {
		t.Fatal("expected distinct burst ids across events")
	}
}

// B13: camera isolation — camera A's burst activity never affects camera B.
func TestB13_CameraIsolation(t *testing.T) {
	clock := newFakeClock(time.Unix(0, 0))
	cfg := burstTestConfig()
	cfg.MaxActiveBurstsPerCamera = 1
	bm := NewBurstManager(cfg, clock.Now)

	// Camera A: fill its single-burst capacity.
	bm.Process(testVehicleCandidate("cam-A", 1))
	bm.Close("cam-A", "", "")
	_, _, outcomeA := bm.Process(testVehicleCandidate("cam-A", 2))
	if outcomeA != OutcomeAccepted {
		t.Fatalf("cam-A: expected accepted (previous closed), got %v", outcomeA)
	}

	// Camera B is untouched and idle: it should freely accept its own first
	// candidate regardless of what happened on A.
	_, _, outcomeB := bm.Process(testVehicleCandidate("cam-B", 1))
	if outcomeB != OutcomeAccepted {
		t.Fatalf("cam-B: expected accepted, got %v", outcomeB)
	}
	if bm.ActiveBurstCount("cam-A") != 1 || bm.ActiveBurstCount("cam-B") != 1 {
		t.Fatalf("expected each camera to have its own independent count of 1, got A=%d B=%d",
			bm.ActiveBurstCount("cam-A"), bm.ActiveBurstCount("cam-B"))
	}

	// Camera C at capacity (0 allowed) is independently rejected while A/B stay healthy.
	cfgC := burstTestConfig()
	cfgC.MaxActiveBurstsPerCamera = 0
	bmC := NewBurstManager(cfgC, clock.Now)
	_, _, outcomeC := bmC.Process(testVehicleCandidate("cam-C", 1))
	if outcomeC != OutcomeCapacityBursts {
		t.Fatalf("cam-C: expected capacity rejection, got %v", outcomeC)
	}
}

// B14: group/track isolation — two vehicles on the SAME camera get
// independent bursts when TrackID differs.
func TestB14_GroupTrackIsolation(t *testing.T) {
	clock := newFakeClock(time.Unix(0, 0))
	bm := NewBurstManager(burstTestConfig(), clock.Now)

	vA := testVehicleCandidate("cam-1", 1)
	vA.TrackID = "vehicle-A"
	vB := testVehicleCandidate("cam-1", 1)
	vB.TrackID = "vehicle-B"
	vB.VehicleBBox = BBox{X0: 200, Y0: 200, X1: 260, Y1: 260}

	burstA, _, outcomeA := bm.Process(vA)
	burstB, _, outcomeB := bm.Process(vB)
	if outcomeA != OutcomeAccepted || outcomeB != OutcomeAccepted {
		t.Fatalf("expected both accepted, got %v / %v", outcomeA, outcomeB)
	}
	if burstA.BurstID == burstB.BurstID {
		t.Fatal("expected independent bursts for different TrackIDs on the same camera")
	}
	if bm.ActiveBurstCount("cam-1") != 2 {
		t.Fatalf("expected 2 active bursts for cam-1, got %d", bm.ActiveBurstCount("cam-1"))
	}
}

// B15: duplicate candidate deduped — same camera+frame_seq+vehicle bbox
// processed twice must not duplicate burst/evidence.
func TestB15_DuplicateCandidateDeduped(t *testing.T) {
	clock := newFakeClock(time.Unix(0, 0))
	bm := NewBurstManager(burstTestConfig(), clock.Now)

	v := testVehicleCandidate("cam-1", 1)
	burst1, _, outcome1 := bm.Process(v)
	if outcome1 != OutcomeAccepted {
		t.Fatalf("expected first accepted, got %v", outcome1)
	}

	burst2, id2, outcome2 := bm.Process(v)
	if outcome2 != OutcomeDuplicate {
		t.Fatalf("expected duplicate outcome, got %v", outcome2)
	}
	if burst2 != nil || id2 != "" {
		t.Fatalf("expected no burst/id for a duplicate, got %v %q", burst2, id2)
	}
	if burst1.FramesAdded != 1 {
		t.Fatalf("expected exactly 1 frame added despite duplicate submit, got %d", burst1.FramesAdded)
	}
}

// B16: out-of-order deterministic — frames 100, 102, 101 always resolve the
// same way regardless of goroutine scheduling: 100 and 102 accepted, 101
// rejected as stale (strictly-increasing policy).
func TestB16_OutOfOrderDeterministic(t *testing.T) {
	for i := 0; i < 5; i++ {
		clock := newFakeClock(time.Unix(0, 0))
		bm := NewBurstManager(burstTestConfig(), clock.Now)

		v100 := testVehicleCandidate("cam-1", 100)
		v102 := testVehicleCandidate("cam-1", 102)
		v102.VehicleBBox = BBox{X0: 20, Y0: 20, X1: 120, Y1: 100}
		v101 := testVehicleCandidate("cam-1", 101)
		v101.VehicleBBox = BBox{X0: 30, Y0: 30, X1: 130, Y1: 110}

		_, _, o1 := bm.Process(v100)
		_, _, o2 := bm.Process(v102)
		_, _, o3 := bm.Process(v101)

		if o1 != OutcomeAccepted || o2 != OutcomeAccepted {
			t.Fatalf("run %d: expected 100 and 102 accepted, got %v / %v", i, o1, o2)
		}
		if o3 != OutcomeStaleOutOfOrder {
			t.Fatalf("run %d: expected 101 rejected as stale, got %v", i, o3)
		}
	}
}

// B19: max active bursts enforced.
func TestB19_MaxActiveBurstsEnforced(t *testing.T) {
	clock := newFakeClock(time.Unix(0, 0))
	cfg := burstTestConfig()
	cfg.MaxActiveBurstsPerCamera = 2
	bm := NewBurstManager(cfg, clock.Now)

	// Two distinct groups on the same camera fill capacity.
	v1 := testVehicleCandidate("cam-1", 1)
	v1.TrackID = "t1"
	v2 := testVehicleCandidate("cam-1", 1)
	v2.TrackID = "t2"
	v2.VehicleBBox = BBox{X0: 300, Y0: 300, X1: 360, Y1: 360}
	bm.Process(v1)
	bm.Process(v2)

	// A third distinct group must be rejected: capacity is exhausted.
	v3 := testVehicleCandidate("cam-1", 1)
	v3.TrackID = "t3"
	v3.VehicleBBox = BBox{X0: 500, Y0: 500, X1: 560, Y1: 560}
	_, _, outcome := bm.Process(v3)
	if outcome != OutcomeCapacityBursts {
		t.Fatalf("expected capacity rejection, got %v", outcome)
	}
	if bm.ActiveBurstCount("cam-1") != 2 {
		t.Fatalf("expected active count capped at 2, got %d", bm.ActiveBurstCount("cam-1"))
	}
}

// B20: cleanup releases burst memory.
func TestB20_CleanupReleasesBurstMemory(t *testing.T) {
	clock := newFakeClock(time.Unix(0, 0))
	cfg := burstTestConfig()
	cfg.BurstTTL = time.Second
	bm := NewBurstManager(cfg, clock.Now)

	bm.Process(testVehicleCandidate("cam-1", 1))
	if bm.TotalBurstCount() != 1 {
		t.Fatalf("expected 1 burst in memory, got %d", bm.TotalBurstCount())
	}

	clock.Advance(2 * time.Second)
	bm.Cleanup()
	if bm.TotalBurstCount() != 0 {
		t.Fatalf("expected cleanup to release the expired burst, got %d remaining", bm.TotalBurstCount())
	}
}
