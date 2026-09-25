package anpr

import "testing"

// B1: candidate contract — VehicleCandidate/PlateCandidate carry the fields
// this package's registry depends on and nothing sensitive (no RTSP
// credential/URI field exists on either type at all).
func TestB1_CandidateContract(t *testing.T) {
	v := VehicleCandidate{
		CameraKey:         "cam-1",
		FrameSeq:          10,
		VehicleClassID:    2,
		VehicleConfidence: 0.9,
		VehicleBBox:       BBox{X0: 10, Y0: 10, X1: 50, Y1: 60},
		CorrelationID:     "cam-1-10",
		ProcessingMode:    "hybrid",
	}
	if err := ValidateVehicleCandidate(v); err != nil {
		t.Fatalf("expected valid candidate, got %v", err)
	}

	pc := PlateCandidate{
		CandidateID: NewCandidateID(v.CameraKey, v.FrameSeq, 0, "burst-1"),
		CameraKey:   v.CameraKey,
		FrameSeq:    v.FrameSeq,
		VehicleBBox: v.VehicleBBox,
		BurstID:     "burst-1",
	}
	if pc.CandidateID == "" {
		t.Fatal("expected non-empty candidate id")
	}
}

// B2: deterministic candidate ID — identical inputs always produce the
// identical id; no timestamp involved.
func TestB2_DeterministicCandidateID(t *testing.T) {
	id1 := NewCandidateID("cam-1", 42, 0, "burst-7")
	id2 := NewCandidateID("cam-1", 42, 0, "burst-7")
	if id1 != id2 {
		t.Fatalf("expected deterministic id, got %q vs %q", id1, id2)
	}

	idDifferentOrdinal := NewCandidateID("cam-1", 42, 1, "burst-7")
	if idDifferentOrdinal == id1 {
		t.Fatal("expected different ordinal to change the id")
	}

	idDifferentBurst := NewCandidateID("cam-1", 42, 0, "burst-8")
	if idDifferentBurst == id1 {
		t.Fatal("expected different burst id to change the candidate id")
	}
}

// B3: vehicle bbox valid.
func TestB3_VehicleBBoxValid(t *testing.T) {
	b := BBox{X0: 0, Y0: 0, X1: 100, Y1: 200}
	if !b.Valid() {
		t.Fatal("expected valid bbox")
	}
}

// B4: invalid bbox rejected — inverted and zero-area cases both fail
// ValidateVehicleCandidate, never silently accepted.
func TestB4_InvalidBBoxRejected(t *testing.T) {
	cases := []BBox{
		{X0: 100, Y0: 100, X1: 10, Y1: 200}, // inverted X
		{X0: 10, Y0: 200, X1: 100, Y1: 10},  // inverted Y
		{X0: 10, Y0: 10, X1: 10, Y1: 200},   // zero width
		{X0: 10, Y0: 10, X1: 100, Y1: 10},   // zero height
	}
	for _, b := range cases {
		v := VehicleCandidate{CameraKey: "cam-1", VehicleBBox: b}
		if err := ValidateVehicleCandidate(v); err == nil {
			t.Fatalf("expected rejection for bbox %+v", b)
		}
		if b.Valid() {
			t.Fatalf("expected BBox.Valid() false for %+v", b)
		}
	}
}

// B29: correlation ID preserved end to end: VehicleCandidate ->
// PlateCandidate (via Registry.Submit) -> ANPRCandidateEnvelope.
func TestB29_CorrelationIDPreserved(t *testing.T) {
	cfg := testConfig()
	reg := NewRegistry(cfg, WithAuthorizer(AllowAllAuthorizer{}))

	v := testVehicleCandidate("cam-1", 1)
	v.CorrelationID = "cam-1-1"
	res := reg.Submit(v)
	if res.Reason != ReasonAccepted {
		t.Fatalf("expected accepted, got %v (%s)", res.Reason, res.Detail)
	}
	if res.Candidate.CorrelationID != "cam-1-1" {
		t.Fatalf("expected correlation id preserved on candidate, got %q", res.Candidate.CorrelationID)
	}

	env, err := NewEnvelope(*res.Candidate, v.ProcessingMode, nil, "", 0)
	if err != nil {
		t.Fatalf("NewEnvelope: %v", err)
	}
	if env.CorrelationID != "cam-1-1" {
		t.Fatalf("expected correlation id preserved on envelope, got %q", env.CorrelationID)
	}
}

// B30: processing mode preserved end to end into the envelope, never
// altered by this package.
func TestB30_ProcessingModePreserved(t *testing.T) {
	cfg := testConfig()
	reg := NewRegistry(cfg, WithAuthorizer(AllowAllAuthorizer{}))

	v := testVehicleCandidate("cam-1", 1)
	v.ProcessingMode = "hybrid"
	res := reg.Submit(v)
	if res.Reason != ReasonAccepted {
		t.Fatalf("expected accepted, got %v", res.Reason)
	}
	env, err := NewEnvelope(*res.Candidate, v.ProcessingMode, nil, "", 0)
	if err != nil {
		t.Fatalf("NewEnvelope: %v", err)
	}
	if env.ProcessingMode != "hybrid" {
		t.Fatalf("expected processing mode preserved, got %q", env.ProcessingMode)
	}
}
