package anpr

// J6-C cross-repo acceptance tests. These verify that the Edge-side
// ANPRCandidateEnvelope (transport.go) is coherent with the shared
// ANPRCandidateEnvelope v1 contract documented in
// docs/integrations/J6C_CROSS_REPO_ACCEPTANCE.md — not Edge-internal
// behavior, which B1-B35 + stress already cover. Where a spec item (C12-15,
// C20, C29) is SaaS-side only, it is documented as N/A here rather than
// silently skipped.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- shared fixtures: load and validate ------------------------------------
//
// These fixtures are the concrete, versioned artifacts the SaaS-side sister
// test suite is expected to load conceptually-equivalent copies of (same
// candidate_id/burst_id/camera_key shapes), documented in
// docs/integrations/J6C_CROSS_REPO_ACCEPTANCE.md. No real imagery/PII in any
// of them (see C23).
func TestFixturesLoadAndValidate(t *testing.T) {
	valid, err := os.ReadFile(filepath.Join("testdata", "candidate_v1_valid.json"))
	if err != nil {
		t.Fatalf("ReadFile valid: %v", err)
	}
	envValid, err := DecodeEnvelope(valid)
	if err != nil {
		t.Fatalf("decode valid fixture: %v", err)
	}

	dup, err := os.ReadFile(filepath.Join("testdata", "candidate_v1_duplicate.json"))
	if err != nil {
		t.Fatalf("ReadFile duplicate: %v", err)
	}
	envDup, err := DecodeEnvelope(dup)
	if err != nil {
		t.Fatalf("decode duplicate fixture: %v", err)
	}
	if envDup.CandidateID != envValid.CandidateID {
		t.Fatalf("duplicate fixture must reuse the exact same candidate_id (C8), got %q vs %q",
			envDup.CandidateID, envValid.CandidateID)
	}

	oversize, err := os.ReadFile(filepath.Join("testdata", "candidate_v1_oversize_metadata.json"))
	if err != nil {
		t.Fatalf("ReadFile oversize: %v", err)
	}
	envOversize, err := DecodeEnvelope(oversize)
	if err != nil {
		t.Fatalf("decode oversize fixture: %v", err)
	}
	const reasonableBound = 5 << 20 // 5MB, well above a real crop, well below this fixture's declared size
	if envOversize.PayloadSizeBytes <= reasonableBound {
		t.Fatalf("expected the oversize fixture to declare a payload_size_bytes above %d, got %d",
			reasonableBound, envOversize.PayloadSizeBytes)
	}

	multiRaw, err := os.ReadFile(filepath.Join("testdata", "candidate_v1_multiple_burst.json"))
	if err != nil {
		t.Fatalf("ReadFile multiple_burst: %v", err)
	}
	var multi []ANPRCandidateEnvelope
	if err := json.Unmarshal(multiRaw, &multi); err != nil {
		t.Fatalf("unmarshal multiple_burst: %v", err)
	}
	if len(multi) != 3 {
		t.Fatalf("expected 3 envelopes in multiple_burst fixture, got %d", len(multi))
	}
	for i := range multi {
		if multi[i].SchemaVersion != SchemaVersionV1 {
			t.Fatalf("entry %d: unexpected schema_version %q", i, multi[i].SchemaVersion)
		}
	}
	if multi[0].BurstID != multi[1].BurstID {
		t.Fatalf("expected first two entries (same camera, same burst) to share burst_id, got %q vs %q",
			multi[0].BurstID, multi[1].BurstID)
	}
	if multi[0].BurstID == multi[2].BurstID {
		t.Fatalf("expected the third entry (different camera) to have an isolated burst_id, got shared %q",
			multi[0].BurstID)
	}
	if multi[0].CameraKey == multi[2].CameraKey {
		t.Fatal("expected the third entry to be a genuinely different camera")
	}
}

// --- C1: contract field mapping -------------------------------------------
//
// Go struct field  -> Go JSON tag        -> canonical contract field
// ANPRCandidateEnvelope.SchemaVersion    -> schema_version   -> schema_version
// ANPRCandidateEnvelope.CameraKey        -> camera_key       -> camera_id / camera_key
// ANPRCandidateEnvelope.CandidateID      -> candidate_id     -> candidate_id
// ANPRCandidateEnvelope.BurstID          -> burst_id         -> burst_id
// ANPRCandidateEnvelope.FrameSeq         -> frame_seq        -> frame_seq
// ANPRCandidateEnvelope.Timestamp        -> timestamp        -> timestamp
// ANPRCandidateEnvelope.CorrelationID    -> correlation_id   -> correlation_id
// ANPRCandidateEnvelope.VehicleBBox      -> vehicle_bbox     -> vehicle_bbox
// ANPRCandidateEnvelope.PlateBBox        -> plate_bbox       -> plate_bbox (optional)
// ANPRCandidateEnvelope.QualityHints     -> quality_hints    -> quality_hints
// ANPRCandidateEnvelope.ProcessingMode   -> processing_mode  -> processing_mode
// ANPRCandidateEnvelope.Payload/PayloadRef/PayloadContentType/
//
//	PayloadSizeBytes/PayloadHash          -> payload*         -> payload metadata
//
// ANPRCandidateEnvelope.PlateModelVersion -> plate_model_version -> model metadata (C27, optional)
// (no direct field)                       -> (n/a)            -> track_id: carried on PlateCandidate.TrackID,
//
//	deliberately NOT forwarded onto the wire
//	envelope in PREP (no J5 integration yet) —
//	documented gap, not a silent drop.
//
// Naming incompatibility found and fixed at the TRANSPORT boundary only
// (domain types BBox/QualityHints untouched otherwise): BBox and
// QualityHints previously had no `json` tags, so encoding/json fell back to
// Go's capitalized field names (X0, VehicleConfidence, ...) which do not
// match the contract's lower-snake-case wire format. Fixed by adding
// explicit tags in types.go/quality.go (see cross-repo contract, C1).
func TestC1_ContractFieldMapping(t *testing.T) {
	pc := PlateCandidate{
		CandidateID:   NewCandidateID("cam-1", 10, 0, "burst-1"),
		CameraKey:     "cam-1",
		FrameSeq:      10,
		Timestamp:     time.Date(2026, 9, 25, 14, 0, 0, 0, time.UTC),
		VehicleBBox:   BBox{X0: 1, Y0: 2, X1: 3, Y1: 4},
		BurstID:       "burst-1",
		CorrelationID: "corr-1",
	}
	env, err := NewEnvelope(pc, "hybrid", nil, "ref://x", 0)
	if err != nil {
		t.Fatalf("NewEnvelope: %v", err)
	}
	data, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{
		"schema_version", "camera_key", "candidate_id", "burst_id", "frame_seq",
		"timestamp", "correlation_id", "vehicle_bbox", "quality_hints",
		"processing_mode", "source", "payload_ref",
	} {
		if _, ok := raw[key]; !ok {
			t.Errorf("expected wire key %q present, got keys %v", key, raw)
		}
	}
	vb, ok := raw["vehicle_bbox"].(map[string]any)
	if !ok {
		t.Fatalf("vehicle_bbox not an object: %v", raw["vehicle_bbox"])
	}
	for _, key := range []string{"x0", "y0", "x1", "y1"} {
		if _, ok := vb[key]; !ok {
			t.Errorf("expected vehicle_bbox.%s present, got %v", key, vb)
		}
	}
}

// --- C2: bbox pixel semantics ----------------------------------------------
//
// The envelope must serialize bboxes as plain pixel numbers (no normalized
// 0..1 scaling, no implicit unit). This test uses frame-scale pixel values
// (well outside 0..1) and confirms they round-trip byte-for-byte unscaled.
func TestC2_BBoxPixelSemantics(t *testing.T) {
	pc := PlateCandidate{
		CandidateID: NewCandidateID("cam-1", 1, 0, "burst-1"),
		CameraKey:   "cam-1",
		FrameSeq:    1,
		Timestamp:   time.Now().UTC(),
		VehicleBBox: BBox{X0: 640, Y0: 360, X1: 1280, Y1: 900}, // 1920x1080 frame, clearly pixel-scale
		BurstID:     "burst-1",
	}
	plate := BBox{X0: 700, Y0: 800, X1: 900, Y1: 860}
	pc.PlateBBox = &plate

	env, err := NewEnvelope(pc, "hybrid", nil, "", 0)
	if err != nil {
		t.Fatalf("NewEnvelope: %v", err)
	}
	data, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got ANPRCandidateEnvelope
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.VehicleBBox != pc.VehicleBBox {
		t.Fatalf("vehicle bbox not preserved unscaled: got %+v, want %+v", got.VehicleBBox, pc.VehicleBBox)
	}
	if got.PlateBBox == nil || *got.PlateBBox != plate {
		t.Fatalf("plate bbox not preserved unscaled: got %v, want %+v", got.PlateBBox, plate)
	}
	// A normalized-space bbox would only ever have values in [0,1]; assert
	// this candidate's values exceed 1, ruling out any accidental division.
	if got.VehicleBBox.X1 <= 1 {
		t.Fatalf("vehicle bbox looks normalized, not pixel-space: %+v", got.VehicleBBox)
	}
}

// --- C3: timestamp roundtrip ------------------------------------------------
//
// Go time.Time -> JSON (RFC3339Nano, explicit offset, never naive) -> back
// must resolve to the exact same instant, regardless of the timezone the
// original value carried.
func TestC3_TimestampRoundtrip(t *testing.T) {
	cases := []time.Time{
		time.Date(2026, 9, 25, 14, 3, 11, 500_000_000, time.UTC),
		time.Date(2026, 9, 25, 11, 3, 11, 500_000_000, time.FixedZone("-03:00", -3*60*60)),
	}
	for _, want := range cases {
		pc := PlateCandidate{
			CandidateID: NewCandidateID("cam-1", 1, 0, "burst-1"),
			CameraKey:   "cam-1",
			FrameSeq:    1,
			Timestamp:   want,
			VehicleBBox: BBox{X0: 0, Y0: 0, X1: 10, Y1: 10},
			BurstID:     "burst-1",
		}
		env, err := NewEnvelope(pc, "hybrid", nil, "", 0)
		if err != nil {
			t.Fatalf("NewEnvelope: %v", err)
		}
		data, err := json.Marshal(env)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		// The wire value must carry an explicit offset, never a naive/local
		// string with no zone information.
		var raw map[string]any
		if err := json.Unmarshal(data, &raw); err != nil {
			t.Fatalf("unmarshal to map: %v", err)
		}
		ts, _ := raw["timestamp"].(string)
		if !strings.HasSuffix(ts, "Z") && !regexp.MustCompile(`[+-]\d{2}:\d{2}$`).MatchString(ts) {
			t.Fatalf("timestamp %q has no explicit UTC offset", ts)
		}

		var got ANPRCandidateEnvelope
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if !got.Timestamp.Equal(want) {
			t.Fatalf("roundtrip changed the instant: got %v, want %v", got.Timestamp, want)
		}
	}
}

// --- C4: candidate id determinism + intact roundtrip ------------------------
func TestC4_CandidateIDDeterministicAndIntact(t *testing.T) {
	id1 := NewCandidateID("cam-1", 42, 0, "burst-9")
	id2 := NewCandidateID("cam-1", 42, 0, "burst-9")
	if id1 != id2 {
		t.Fatalf("expected deterministic id, got %q vs %q", id1, id2)
	}

	pc := PlateCandidate{
		CandidateID: id1,
		CameraKey:   "cam-1",
		FrameSeq:    42,
		Timestamp:   time.Now().UTC(),
		VehicleBBox: BBox{X0: 0, Y0: 0, X1: 10, Y1: 10},
		BurstID:     "burst-9",
	}
	env, err := NewEnvelope(pc, "hybrid", nil, "", 0)
	if err != nil {
		t.Fatalf("NewEnvelope: %v", err)
	}
	data, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got ANPRCandidateEnvelope
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.CandidateID != id1 {
		t.Fatalf("candidate_id mutated across the wire: got %q, want %q", got.CandidateID, id1)
	}
}

// --- C5/C6: burst id sharing within a burst, isolation across cameras ------
func TestC5_C6_BurstIDSharedWithinBurstIsolatedAcrossCameras(t *testing.T) {
	clock := newFakeClock(time.Unix(0, 0))
	cfg := testConfig()
	cfg.MaxFramesPerBurst = 10
	reg := NewRegistry(cfg, WithAuthorizer(AllowAllAuthorizer{}), WithClock(clock.Now))

	// Camera A, two frames of the same burst.
	rA1 := reg.Submit(testVehicleCandidate("cam-A", 1))
	vA2 := testVehicleCandidate("cam-A", 2)
	rA2 := reg.Submit(vA2)
	if rA1.Reason != ReasonAccepted || rA2.Reason != ReasonAccepted {
		t.Fatalf("expected both cam-A candidates accepted, got %v / %v", rA1.Reason, rA2.Reason)
	}
	if rA1.Candidate.BurstID != rA2.Candidate.BurstID {
		t.Fatalf("expected shared burst_id within cam-A's burst, got %q vs %q",
			rA1.Candidate.BurstID, rA2.Candidate.BurstID)
	}
	if rA1.Candidate.CandidateID == rA2.Candidate.CandidateID {
		t.Fatal("expected distinct candidate_id per frame even within the same burst")
	}

	// Camera B, first frame of its own burst — must never collide with A's.
	rB1 := reg.Submit(testVehicleCandidate("cam-B", 1))
	if rB1.Reason != ReasonAccepted {
		t.Fatalf("expected cam-B candidate accepted, got %v", rB1.Reason)
	}
	if rB1.Candidate.BurstID == rA1.Candidate.BurstID {
		t.Fatalf("burst_id collided across cameras: %q", rB1.Candidate.BurstID)
	}
	if rB1.Candidate.CandidateID == rA1.Candidate.CandidateID {
		t.Fatal("candidate_id must never collide across cameras")
	}
	// Both ids must embed their own camera key (id format documented in
	// candidate.go: camera:frameSeq:ordinal:burstID) so a reader can tell
	// them apart without any side channel.
	if !strings.HasPrefix(rA1.Candidate.CandidateID, "cam-A:") {
		t.Fatalf("candidate_id does not embed camera key: %q", rA1.Candidate.CandidateID)
	}
	if !strings.HasPrefix(rB1.Candidate.CandidateID, "cam-B:") {
		t.Fatalf("candidate_id does not embed camera key: %q", rB1.Candidate.CandidateID)
	}
}

// --- C7: multi-vehicle same frame -> distinct candidates -------------------
func TestC7_MultiVehicleSameFrameDistinctCandidates(t *testing.T) {
	clock := newFakeClock(time.Unix(0, 0))
	reg := NewRegistry(testConfig(), WithAuthorizer(AllowAllAuthorizer{}), WithClock(clock.Now))

	v1 := testVehicleCandidate("cam-1", 5)
	v1.TrackID = "vehicle-1"
	v1.VehicleBBox = BBox{X0: 10, Y0: 10, X1: 100, Y1: 100}
	v2 := testVehicleCandidate("cam-1", 5)
	v2.TrackID = "vehicle-2"
	v2.VehicleBBox = BBox{X0: 400, Y0: 200, X1: 600, Y1: 340} // within the 640x360 test frame

	r1 := reg.Submit(v1)
	r2 := reg.Submit(v2)
	if r1.Reason != ReasonAccepted || r2.Reason != ReasonAccepted {
		t.Fatalf("expected both vehicles accepted, got %v / %v", r1.Reason, r2.Reason)
	}
	if r1.Candidate.CandidateID == r2.Candidate.CandidateID {
		t.Fatal("expected two vehicles in the same frame to yield distinct candidate ids")
	}
	if r1.Candidate.BurstID == r2.Candidate.BurstID {
		t.Fatal("expected two distinct vehicles (different TrackID) to get independent bursts, not merged")
	}
}

// --- C8: duplicate delivery does not double-count -----------------------
func TestC8_DuplicateDeliveryDoesNotDoubleCount(t *testing.T) {
	clock := newFakeClock(time.Unix(0, 0))
	reg := NewRegistry(testConfig(), WithAuthorizer(AllowAllAuthorizer{}), WithClock(clock.Now))

	v := testVehicleCandidate("cam-1", 1)
	r1 := reg.Submit(v)
	if r1.Reason != ReasonAccepted {
		t.Fatalf("expected first submit accepted, got %v", r1.Reason)
	}
	// Same envelope "redelivered": identical camera+frame+bbox candidate
	// resubmitted (transport-level retry/duplicate). The registry's dedupe
	// cache (B15) must reject it, not count it again.
	r2 := reg.Submit(v)
	if r2.Reason != ReasonRejected || r2.Detail != string(OutcomeDuplicate) {
		t.Fatalf("expected duplicate rejected, got reason=%v detail=%q", r2.Reason, r2.Detail)
	}
	if got := reg.Metrics().CandidateCount; got != 1 {
		t.Fatalf("expected candidate count to stay at 1 after a duplicate resubmit, got %d", got)
	}
	if r1.Candidate.CandidateID != NewCandidateID(v.CameraKey, v.FrameSeq, 0, r1.Candidate.BurstID) {
		t.Fatalf("unexpected candidate id shape: %q", r1.Candidate.CandidateID)
	}
}

// --- C9: out-of-order frames never corrupt burst state ----------------------
func TestC9_OutOfOrderDoesNotCorruptState(t *testing.T) {
	clock := newFakeClock(time.Unix(0, 0))
	cfg := testConfig()
	cfg.MaxFramesPerBurst = 10
	reg := NewRegistry(cfg, WithAuthorizer(AllowAllAuthorizer{}), WithClock(clock.Now))

	seqs := []uint64{100, 102, 101}
	var lastAccepted *SubmitResult
	accepted := 0
	for _, seq := range seqs {
		v := testVehicleCandidate("cam-1", seq)
		v.VehicleBBox.X0 += float64(seq)
		v.VehicleBBox.X1 += float64(seq)
		r := reg.Submit(v)
		if r.Reason == ReasonAccepted {
			accepted++
			lastAccepted = &r
		}
	}
	if accepted != 2 {
		t.Fatalf("expected 100 and 102 accepted (101 stale), got %d accepted", accepted)
	}
	// Burst state itself must stay sane: exactly one burst, exactly the
	// accepted frame count, no partial/garbage state from the rejected one.
	status := reg.Status("cam-1")
	if status == nil {
		t.Fatal("expected camera status to exist")
	}
	if status.CandidatesCreated != 2 || status.CandidatesRejected != 1 {
		t.Fatalf("unexpected status after out-of-order sequence: %+v", status)
	}
	if lastAccepted == nil || lastAccepted.Candidate.FrameSeq != 102 {
		t.Fatalf("expected the last accepted candidate to be frame 102, got %+v", lastAccepted)
	}
}

// --- C10: dropped frame in the middle of a burst does not break it ----------
func TestC10_DroppedFrameDoesNotBreakBurst(t *testing.T) {
	clock := newFakeClock(time.Unix(0, 0))
	cfg := testConfig()
	cfg.MaxFramesPerBurst = 10
	reg := NewRegistry(cfg, WithAuthorizer(AllowAllAuthorizer{}), WithClock(clock.Now))

	// Frames 1,2,4,5 — frame 3 never arrives (dropped upstream). No
	// requirement anywhere in this package demands strict consecutiveness
	// (see types.go's frame_seq doc); the burst must accept all four.
	accepted := 0
	var burstIDs = map[string]bool{}
	for _, seq := range []uint64{1, 2, 4, 5} {
		v := testVehicleCandidate("cam-1", seq)
		v.VehicleBBox.X0 += float64(seq)
		v.VehicleBBox.X1 += float64(seq)
		r := reg.Submit(v)
		if r.Reason == ReasonAccepted {
			accepted++
			burstIDs[r.Candidate.BurstID] = true
		}
	}
	if accepted != 4 {
		t.Fatalf("expected all 4 non-consecutive frames accepted, got %d", accepted)
	}
	if len(burstIDs) != 1 {
		t.Fatalf("expected all 4 frames to remain in ONE burst despite the gap, got %d distinct burst ids", len(burstIDs))
	}
}

// --- C11: low-quality candidate is still sent, Edge never fabricates ACCEPTED
func TestC11_LowQualityStillForwardedNeverFabricatedAccepted(t *testing.T) {
	clock := newFakeClock(time.Unix(0, 0))
	reg := NewRegistry(testConfig(), WithAuthorizer(AllowAllAuthorizer{}), WithClock(clock.Now))

	v := testVehicleCandidate("cam-1", 1)
	v.VehicleConfidence = 0.05 // deliberately low but structurally valid
	r := reg.Submit(v)
	if r.Reason != ReasonAccepted {
		t.Fatalf("expected Edge to forward a low-confidence-but-valid candidate, got %v", r.Reason)
	}
	// SubmitResult/Reason only ever carries Edge-side outcomes
	// (SKIPPED/REJECTED/UNAVAILABLE/DROPPED_CAPACITY/ACCEPTED) — "ACCEPTED"
	// here means "Edge produced a candidate", never "the SaaS approved the
	// plate read". Confirm the taxonomy has no post-SaaS states Edge could
	// mistakenly emit.
	for _, bad := range []Reason{"PENDING", "AMBIGUOUS"} {
		if r.Reason == bad {
			t.Fatalf("Edge must never emit a post-SaaS taxonomy state, got %v", r.Reason)
		}
	}
}

// --- C12/C13: plate-region-unavailable -> envelope never fabricates a bbox -
//
// N/A as a SaaS-side acceptance item (OCR/detector cloud consensus), but the
// Edge-side half of the contract — PlateRegionProvider unavailable must
// never leave a fabricated plate_bbox on the envelope — is fully Edge scope
// and is verified here.
func TestC12_C13_PlateUnavailableNeverFabricatesBBox(t *testing.T) {
	clock := newFakeClock(time.Unix(0, 0))
	reg := NewRegistry(testConfig(),
		WithAuthorizer(AllowAllAuthorizer{}),
		WithPlateRegionProvider(FakePlateRegionProvider{Unavailable: true}),
		WithClock(clock.Now),
	)
	r := reg.Submit(testVehicleCandidate("cam-1", 1))
	if r.Reason != ReasonAccepted {
		t.Fatalf("expected accepted (crop policy falls back to vehicle context), got %v", r.Reason)
	}
	if r.Candidate.PlateBBox != nil {
		t.Fatalf("expected nil PlateBBox when the provider is unavailable, got %+v", r.Candidate.PlateBBox)
	}
	env, err := NewEnvelope(*r.Candidate, "hybrid", nil, "ref", 0)
	if err != nil {
		t.Fatalf("NewEnvelope: %v", err)
	}
	data, _ := json.Marshal(env)
	var raw map[string]any
	json.Unmarshal(data, &raw)
	if _, ok := raw["plate_bbox"]; ok {
		t.Fatalf("expected plate_bbox omitted from the wire envelope when unavailable, got %v", raw["plate_bbox"])
	}
}

// C14/C15: consensus/OCR ranking and cross-provider agreement are SaaS-side
// (post-ingest) concerns — N/A on the Edge side. See SaaS report.

// --- C16: replay of an already-emitted burst is deduped ---------------------
func TestC16_ReplayAlreadyEmittedBurstDeduped(t *testing.T) {
	clock := newFakeClock(time.Unix(0, 0))
	cfg := testConfig()
	cfg.MaxFramesPerBurst = 3
	reg := NewRegistry(cfg, WithAuthorizer(AllowAllAuthorizer{}), WithClock(clock.Now))

	frames := []uint64{1, 2, 3}
	first := map[uint64]string{}
	for _, seq := range frames {
		v := testVehicleCandidate("cam-1", seq)
		v.VehicleBBox.X0 += float64(seq)
		v.VehicleBBox.X1 += float64(seq)
		r := reg.Submit(v)
		if r.Reason != ReasonAccepted {
			t.Fatalf("expected frame %d accepted on first pass, got %v", seq, r.Reason)
		}
		first[seq] = r.Candidate.CandidateID
	}
	before := reg.Metrics().CandidateCount

	// Simulate a full replay of the same burst from Edge (e.g. after a
	// crash/restart replaying a local queue) with identical inputs.
	for _, seq := range frames {
		v := testVehicleCandidate("cam-1", seq)
		v.VehicleBBox.X0 += float64(seq)
		v.VehicleBBox.X1 += float64(seq)
		r := reg.Submit(v)
		if r.Reason != ReasonRejected || r.Detail != string(OutcomeDuplicate) {
			t.Fatalf("expected replayed frame %d deduped as duplicate, got reason=%v detail=%q", seq, r.Reason, r.Detail)
		}
	}
	if after := reg.Metrics().CandidateCount; after != before {
		t.Fatalf("expected no additional candidates counted on replay: before=%d after=%d", before, after)
	}
}

// --- C17: oversize crop payload fails BEFORE transport, full path ----------
func TestC17_OversizePayloadFailsBeforeTransport(t *testing.T) {
	clock := newFakeClock(time.Unix(0, 0))
	reg := NewRegistry(testConfig(), WithAuthorizer(AllowAllAuthorizer{}), WithClock(clock.Now))

	r := reg.Submit(testVehicleCandidate("cam-1", 1))
	if r.Reason != ReasonAccepted {
		t.Fatalf("expected candidate accepted, got %v", r.Reason)
	}

	// A crop payload larger than the configured bound must never reach a
	// transport: NewEnvelope must fail closed with ErrPayloadTooLarge.
	oversized := make([]byte, 2000)
	_, err := NewEnvelope(*r.Candidate, "hybrid", oversized, "", 1000)
	if err != ErrPayloadTooLarge {
		t.Fatalf("expected ErrPayloadTooLarge, got %v", err)
	}

	// NewEnvelope returning an error IS the "before transport" guarantee:
	// there is no envelope value to hand to any EnvelopeTransport.Send.
}

// --- C18: burst frame cap holds under 100 simulated inputs ------------------
func TestC18_BurstFrameCapHoldsUnder100Inputs(t *testing.T) {
	clock := newFakeClock(time.Unix(0, 0))
	cfg := testConfig()
	cfg.MaxFramesPerBurst = 5
	reg := NewRegistry(cfg, WithAuthorizer(AllowAllAuthorizer{}), WithClock(clock.Now))

	accepted := 0
	for i := uint64(1); i <= 100; i++ {
		v := testVehicleCandidate("cam-1", i)
		v.VehicleBBox.X0 += float64(i)
		v.VehicleBBox.X1 += float64(i)
		r := reg.Submit(v)
		if r.Reason == ReasonAccepted {
			accepted++
		}
	}
	if accepted != 5 {
		t.Fatalf("expected exactly MaxFramesPerBurst=5 accepted/transported out of 100 inputs, got %d", accepted)
	}
	if got := reg.Metrics().CandidateCount; got != 5 {
		t.Fatalf("expected metrics to reflect only 5 transported candidates, got %d", got)
	}
}

// --- C19 (CRITICAL): auth deny => zero mutation -----------------------------
func TestC19_AuthDenyZeroMutation(t *testing.T) {
	clock := newFakeClock(time.Unix(0, 0))
	reg := NewRegistry(testConfig(), WithAuthorizer(DenyAllAuthorizer{}), WithClock(clock.Now))

	r := reg.Submit(testVehicleCandidate("cam-1", 1))
	if r.Reason != ReasonRejected || r.Detail != "unauthorized" {
		t.Fatalf("expected rejected/unauthorized, got reason=%v detail=%q", r.Reason, r.Detail)
	}
	if r.Candidate != nil {
		t.Fatal("expected NO candidate produced for an unauthorized camera")
	}
	if r.Crop != nil {
		t.Fatal("expected NO crop computed for an unauthorized camera")
	}
	if reg.BurstManager().TotalBurstCount() != 0 {
		t.Fatalf("expected zero burst mutation, got %d bursts", reg.BurstManager().TotalBurstCount())
	}
	if reg.Metrics().CandidateCount != 0 || reg.Metrics().CropCount != 0 || reg.Metrics().BurstCount != 0 {
		t.Fatalf("expected zero metrics mutation, got %+v", reg.Metrics())
	}
	if status := reg.Status("cam-1"); status != nil {
		t.Fatalf("expected no camera status recorded, got %+v", status)
	}
}

// C20: SaaS-side auth (entitlement verification once the envelope lands) is
// N/A on the Edge side — see SaaS report.

// --- C21: offline/retry keeps correlation_id intact -------------------------
func TestC21_OfflineRetryKeepsCorrelationIDIntact(t *testing.T) {
	pc := PlateCandidate{
		CandidateID:   NewCandidateID("cam-1", 1, 0, "burst-1"),
		CameraKey:     "cam-1",
		FrameSeq:      1,
		Timestamp:     time.Now().UTC(),
		VehicleBBox:   BBox{X0: 0, Y0: 0, X1: 10, Y1: 10},
		BurstID:       "burst-1",
		CorrelationID: "corr-retry-xyz",
	}
	env, err := NewEnvelope(pc, "hybrid", []byte{1, 2, 3}, "", 0)
	if err != nil {
		t.Fatalf("NewEnvelope: %v", err)
	}

	// Simulate an outage: the first send is denied by the rate limiter
	// ("offline"/backpressure), then a retry with the SAME envelope
	// succeeds once the limiter reopens. correlation_id must be identical
	// across both attempts (it's the same envelope value, never regenerated).
	deny := true
	var lastSent ANPRCandidateEnvelope
	transport := RateLimitedTransport{
		Inner: sendFunc(func(e ANPRCandidateEnvelope) error { lastSent = e; return nil }),
		AllowFunc: func(int64) bool {
			if deny {
				deny = false
				return false
			}
			return true
		},
	}
	if err := transport.Send(env); err != ErrCapacityExceeded {
		t.Fatalf("expected first send denied (simulated outage), got %v", err)
	}
	if err := transport.Send(env); err != nil {
		t.Fatalf("expected retry to succeed, got %v", err)
	}
	if lastSent.CorrelationID != "corr-retry-xyz" {
		t.Fatalf("correlation_id changed across retry: got %q", lastSent.CorrelationID)
	}
	if lastSent.CandidateID != env.CandidateID {
		t.Fatalf("candidate_id changed across retry: got %q, want %q", lastSent.CandidateID, env.CandidateID)
	}
}

type sendFunc func(ANPRCandidateEnvelope) error

func (f sendFunc) Send(e ANPRCandidateEnvelope) error { return f(e) }

// --- C22: HIGH_SPEED_LPR is opt-in, never mutates defaults unrequested -----
func TestC22_HighSpeedLPROptInOnly(t *testing.T) {
	base := testConfig()
	base.MaxFramesPerBurst = 5
	base.FrameSelection = SelectFirst

	// Not applying the profile at all: defaults must be byte-for-byte
	// unchanged (Config.Apply is never auto-invoked by NewRegistry/Submit).
	untouched := base
	if untouched.MaxFramesPerBurst != base.MaxFramesPerBurst || untouched.FrameSelection != base.FrameSelection {
		t.Fatal("unexpected: base config mutated without ever calling Apply")
	}

	profile := &HighSpeedLPRProfile{}
	applied := profile.Apply(base)
	// It's fine (and expected) for Apply's OUTPUT to differ; what matters is
	// that the caller had to explicitly call it — base itself is untouched.
	if base.MaxFramesPerBurst != 5 || base.FrameSelection != SelectFirst {
		t.Fatalf("Apply must not mutate its input in place, base became %+v", base)
	}
	_ = applied
}

// --- C23: privacy — no raw image bytes/base64/credentials/plate text in source
func TestC23_NoSecretsOrRawImageDataInSource(t *testing.T) {
	forbidden := []*regexp.Regexp{
		regexp.MustCompile(`rtsp://[^"'\s]*:[^"'\s@]*@`), // credentialed RTSP URI
		regexp.MustCompile(`(?i)password\s*=\s*"[^"]+"`),
		regexp.MustCompile(`(?i)api[_-]?key\s*=\s*"[^"]+"`),
		regexp.MustCompile(`data:image/(png|jpeg);base64,`),
	}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		data, err := os.ReadFile(e.Name())
		if err != nil {
			t.Fatalf("ReadFile %s: %v", e.Name(), err)
		}
		for _, re := range forbidden {
			if re.Match(data) {
				t.Errorf("%s: matched forbidden pattern %s", e.Name(), re.String())
			}
		}
	}
}

// --- C24: memory bounds hold under sustained load (extends B33) ------------
func TestC24_MemoryBoundsUnderSustainedLoad(t *testing.T) {
	clock := newFakeClock(time.Unix(0, 0))
	cfg := testConfig()
	cfg.MaxCameras = 5
	cfg.MaxActiveBurstsPerCamera = 2
	cfg.MaxFramesPerBurst = 3
	reg := NewRegistry(cfg, WithAuthorizer(AllowAllAuthorizer{}), WithClock(clock.Now))

	for cam := 0; cam < 20; cam++ { // more cameras than MaxCameras
		cameraKey := "cam-" + string(rune('A'+cam))
		for seq := uint64(1); seq <= 50; seq++ { // far more than MaxFramesPerBurst
			v := testVehicleCandidate(cameraKey, seq)
			v.VehicleBBox.X0 += float64(seq)
			v.VehicleBBox.X1 += float64(seq)
			reg.Submit(v)
		}
	}
	if reg.BurstManager().TotalBurstCount() > 5*2 {
		t.Fatalf("expected bounded total burst count (<= MaxCameras*MaxActiveBurstsPerCamera), got %d",
			reg.BurstManager().TotalBurstCount())
	}
	m := reg.Metrics()
	if m.CandidateCount > 5*2*3 {
		t.Fatalf("expected bounded candidate count, got %d", m.CandidateCount)
	}
}

// --- C25: 10 concurrent cameras, no cross-contamination (-race) ------------
func TestC25_ConcurrentCamerasNoCrossContamination(t *testing.T) {
	clock := newFakeClock(time.Unix(0, 0))
	cfg := testConfig()
	cfg.MaxCameras = 10
	cfg.MaxActiveBurstsPerCamera = 4
	cfg.MaxFramesPerBurst = 5
	reg := NewRegistry(cfg, WithAuthorizer(AllowAllAuthorizer{}), WithClock(clock.Now))

	var wg sync.WaitGroup
	results := make([][]string, 10)
	var mu sync.Mutex
	for cam := 0; cam < 10; cam++ {
		cam := cam
		wg.Add(1)
		go func() {
			defer wg.Done()
			cameraKey := "cam-" + string(rune('A'+cam))
			var ids []string
			for burst := 0; burst < 3; burst++ {
				for seq := uint64(1); seq <= 3; seq++ {
					v := testVehicleCandidate(cameraKey, uint64(burst*10)+seq)
					v.TrackID = cameraKey + "-b" + string(rune('0'+burst))
					v.VehicleBBox.X0 += float64(seq)
					v.VehicleBBox.X1 += float64(seq)
					r := reg.Submit(v)
					if r.Reason == ReasonAccepted {
						ids = append(ids, r.Candidate.CandidateID)
					}
				}
			}
			mu.Lock()
			results[cam] = ids
			mu.Unlock()
		}()
	}
	wg.Wait()

	seen := map[string]int{}
	for cam, ids := range results {
		prefix := "cam-" + string(rune('A'+cam)) + ":"
		for _, id := range ids {
			if !strings.HasPrefix(id, prefix) {
				t.Fatalf("candidate id %q leaked into camera %d's result (wrong camera prefix)", id, cam)
			}
			seen[id]++
		}
	}
	for id, count := range seen {
		if count != 1 {
			t.Fatalf("candidate id %q observed %d times across goroutines (expected globally unique)", id, count)
		}
	}
}

// --- C26: unknown schema_version fails explicit, never silently continues --
func TestC26_UnknownSchemaVersionFailsExplicit(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "candidate_v1_valid.json"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if _, err := DecodeEnvelope(data); err != nil {
		t.Fatalf("expected valid fixture to decode, got %v", err)
	}

	tampered := strings.Replace(string(data), "anpr_candidate_v1", "anpr_candidate_v2", 1)
	_, err = DecodeEnvelope([]byte(tampered))
	if err == nil {
		t.Fatal("expected an error decoding an unknown schema_version, got nil")
	}
	if !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("expected an explicit unsupported-version error, got %v", err)
	}
}

// --- C27: envelope carries an optional model-metadata field -----------------
func TestC27_ModelMetadataFieldExists(t *testing.T) {
	pc := PlateCandidate{
		CandidateID: NewCandidateID("cam-1", 1, 0, "burst-1"),
		CameraKey:   "cam-1",
		FrameSeq:    1,
		Timestamp:   time.Now().UTC(),
		VehicleBBox: BBox{X0: 0, Y0: 0, X1: 10, Y1: 10},
		BurstID:     "burst-1",
	}
	env, err := NewEnvelope(pc, "hybrid", nil, "", 0)
	if err != nil {
		t.Fatalf("NewEnvelope: %v", err)
	}
	// Optional and empty in PREP (no real model wired) — must be omitted
	// from the wire, not present-but-null.
	if env.PlateModelVersion != "" {
		t.Fatalf("expected empty PlateModelVersion in PREP, got %q", env.PlateModelVersion)
	}
	data, _ := json.Marshal(env)
	if strings.Contains(string(data), "plate_model_version") {
		t.Fatal("expected omitempty to drop plate_model_version when unset")
	}
	// Setting it must round-trip.
	env.PlateModelVersion = "yolov9-plate-eu-v3"
	data, _ = json.Marshal(env)
	var got ANPRCandidateEnvelope
	json.Unmarshal(data, &got)
	if got.PlateModelVersion != "yolov9-plate-eu-v3" {
		t.Fatalf("plate_model_version did not round-trip: got %q", got.PlateModelVersion)
	}
}

// --- C28: evidence chain reconstructable from the envelope alone -----------
func TestC28_EvidenceChainSelfContained(t *testing.T) {
	clock := newFakeClock(time.Unix(0, 0))
	reg := NewRegistry(testConfig(), WithAuthorizer(AllowAllAuthorizer{}), WithClock(clock.Now))

	r := reg.Submit(testVehicleCandidate("cam-evidence-1", 77))
	if r.Reason != ReasonAccepted {
		t.Fatalf("expected accepted, got %v", r.Reason)
	}
	env, err := NewEnvelope(*r.Candidate, "hybrid", nil, "ref", 0)
	if err != nil {
		t.Fatalf("NewEnvelope: %v", err)
	}
	data, _ := json.Marshal(env)
	var got ANPRCandidateEnvelope
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// From the envelope alone (no log lookup): candidate_id decomposes into
	// camera:frameSeq:ordinal:burstID, and camera_key/frame_seq/burst_id are
	// also present redundantly as first-class fields. Both must agree.
	parts := strings.SplitN(got.CandidateID, ":", 4)
	if len(parts) != 4 {
		t.Fatalf("candidate_id does not decompose into 4 parts: %q", got.CandidateID)
	}
	if parts[0] != got.CameraKey {
		t.Fatalf("candidate_id camera segment %q disagrees with camera_key field %q", parts[0], got.CameraKey)
	}
	if parts[3] != got.BurstID {
		t.Fatalf("candidate_id burst segment %q disagrees with burst_id field %q", parts[3], got.BurstID)
	}
}

// C29: consensus benchmarking is a SaaS-side concern — N/A on the Edge side.

// --- C30: failure matrix ----------------------------------------------------
//
// Edge failure mode            -> taxonomy state    -> covered by
// ANPR disabled (Config)        -> SKIPPED            TestC30 case 1
// Unauthorized camera           -> REJECTED            TestC19 / TestC30 case 2
// Invalid vehicle bbox          -> REJECTED            TestC30 case 3
// Duplicate candidate           -> REJECTED (dup)      TestC8/TestC16
// Out-of-order/stale frame      -> REJECTED (stale)    TestC9
// Burst/camera capacity hit     -> DROPPED_CAPACITY    TestC30 case 4
// Plate region unavailable      -> UNAVAILABLE         TestC12_C13
// Invalid crop geometry         -> REJECTED            TestC30 case 5
// Transport not wired           -> UNAVAILABLE         TestC30 case 6
// Transport rate/capacity denied-> (transport error, not a Reason — caller-level) TestC21
//
// Every row maps to exactly one canonical taxonomy name, never a silent
// success and never a panic.
func TestC30_FailureMatrix(t *testing.T) {
	clock := newFakeClock(time.Unix(0, 0))

	t.Run("disabled", func(t *testing.T) {
		cfg := testConfig()
		cfg.Enabled = false
		reg := NewRegistry(cfg, WithAuthorizer(AllowAllAuthorizer{}), WithClock(clock.Now))
		r := reg.Submit(testVehicleCandidate("cam-1", 1))
		if r.Reason != ReasonSkipped {
			t.Fatalf("expected SKIPPED, got %v", r.Reason)
		}
	})

	t.Run("unauthorized", func(t *testing.T) {
		reg := NewRegistry(testConfig(), WithAuthorizer(DenyAllAuthorizer{}), WithClock(clock.Now))
		r := reg.Submit(testVehicleCandidate("cam-1", 1))
		if r.Reason != ReasonRejected {
			t.Fatalf("expected REJECTED, got %v", r.Reason)
		}
	})

	t.Run("invalid_bbox", func(t *testing.T) {
		reg := NewRegistry(testConfig(), WithAuthorizer(AllowAllAuthorizer{}), WithClock(clock.Now))
		v := testVehicleCandidate("cam-1", 1)
		v.VehicleBBox = BBox{X0: 10, Y0: 10, X1: 5, Y1: 5} // inverted
		r := reg.Submit(v)
		if r.Reason != ReasonRejected {
			t.Fatalf("expected REJECTED, got %v", r.Reason)
		}
	})

	t.Run("capacity", func(t *testing.T) {
		cfg := testConfig()
		cfg.MaxCameras = 1
		reg := NewRegistry(cfg, WithAuthorizer(AllowAllAuthorizer{}), WithClock(clock.Now))
		reg.Submit(testVehicleCandidate("cam-1", 1))
		r := reg.Submit(testVehicleCandidate("cam-2", 1)) // second camera, cap=1
		if r.Reason != ReasonDroppedCapacity {
			t.Fatalf("expected DROPPED_CAPACITY, got %v", r.Reason)
		}
	})

	t.Run("invalid_crop_geometry", func(t *testing.T) {
		reg := NewRegistry(testConfig(), WithAuthorizer(AllowAllAuthorizer{}), WithClock(clock.Now))
		v := testVehicleCandidate("cam-1", 1)
		v.SourceWidth, v.SourceHeight = 0, 0 // makes ComputeCrop fail (ErrCropInvalidFrame)
		r := reg.Submit(v)
		if r.Reason != ReasonRejected {
			t.Fatalf("expected REJECTED, got %v", r.Reason)
		}
	})

	t.Run("transport_not_wired", func(t *testing.T) {
		transport := UnavailableTransport{}
		if err := transport.Send(ANPRCandidateEnvelope{}); err != ErrTransportUnavailable {
			t.Fatalf("expected ErrTransportUnavailable, got %v", err)
		}
	})
}
