package anpr

import "time"

// BBox is a rectangle expressed in PIXEL coordinates of the source frame it
// was measured against (never normalized 0..1). This matches
// vision.Detection.BBox and processing.Frame.OutputWidth/Height, so a
// VehicleCandidate built from an existing vision.Detection needs no unit
// conversion.
//
// A valid BBox has X1>X0, Y1>Y0 (non-zero area, non-inverted). Validity
// against a specific frame's bounds (fully or partially inside/outside) is a
// separate concern handled by crop clamping (see crop.go) — a BBox can be
// structurally Valid() yet still extend beyond a given frame's edges.
type BBox struct {
	X0, Y0, X1, Y1 float64
}

// Valid reports whether b is structurally well-formed: non-inverted and
// non-zero-area. It does not check against any frame's bounds.
func (b BBox) Valid() bool {
	return b.X1 > b.X0 && b.Y1 > b.Y0
}

// Width and Height return b's dimensions. Both are <= 0 if !b.Valid().
func (b BBox) Width() float64  { return b.X1 - b.X0 }
func (b BBox) Height() float64 { return b.Y1 - b.Y0 }

// Area returns b's area, 0 if !b.Valid().
func (b BBox) Area() float64 {
	if !b.Valid() {
		return 0
	}
	return b.Width() * b.Height()
}

// VehicleCandidate is a vehicle detection this package can turn into a
// burst/plate-candidate pipeline. It carries no RTSP credential, URI or raw
// pixel data — only the metadata needed to correlate it back to a frame
// already flowing through internal/processing.
//
// It is deliberately NOT a new detector output: the expected source is an
// existing vision.Detection (Type == vision.DetectionTypeVehicle) mapped
// 1:1 by the caller (internal/agent wiring, outside this package's scope in
// PREP).
type VehicleCandidate struct {
	// CameraKey is processing.Frame.CandidateKey (the pipeline's per-camera
	// identity), reused verbatim — never a separate camera identifier.
	CameraKey string
	// FrameSeq is processing.Frame.Seq for the frame this vehicle was
	// detected in.
	FrameSeq uint64
	// Timestamp is the frame's own decode timestamp (processing.Frame.Timestamp),
	// not wall-clock-at-detection.
	Timestamp time.Time
	// VehicleClassID mirrors vision.Detection.ClassID (COCO ids the vision
	// package already defines: ClassIDCar, ClassIDMotor, ClassIDBus,
	// ClassIDTruck).
	VehicleClassID int
	// VehicleConfidence mirrors vision.Detection.Confidence.
	VehicleConfidence float64
	// VehicleBBox mirrors vision.Detection.BBox, reinterpreted as anpr.BBox
	// (identical pixel semantics).
	VehicleBBox BBox
	// SourceWidth/SourceHeight are the frame's pixel dimensions this bbox
	// was measured against (processing.Frame.OutputWidth/OutputHeight) —
	// required to clamp crops and plate-region lookups to actual frame
	// bounds.
	SourceWidth, SourceHeight int
	// TrackID is optional (empty means "no tracker signal available" — J5
	// tracking is out of scope for J6-B). When present it is the grouping
	// key alongside CameraKey (see burst.go groupKey).
	TrackID string
	// CorrelationID is processing.Frame.CorrelationID, carried through
	// verbatim end to end (never re-derived) so the SaaS can correlate
	// candidate/burst/OCR observations without depending on logs.
	CorrelationID string
	// ProcessingMode is processing.Frame.ProcessingMode ("hybrid" for the
	// mode J6 actually targets); this package only consumes it, never
	// changes it.
	ProcessingMode string
	// CandidateReason is a short, human-readable trigger reason (e.g.
	// "vehicle_detection", "explicit_signal"). Free-form but never PII.
	CandidateReason string
}

// PlateCandidateID is a deterministic, reproducible identifier — see
// candidate.go for construction. It is a string, never a raw timestamp.
type PlateCandidateID = string

// PlateCandidate is the Edge-side plate-candidate contract: a single
// frame/region worth sending upstream for the SaaS's OCR/consensus pipeline
// to evaluate. See doc.go for the field-by-field mapping to the SaaS's
// PlateCandidate.
type PlateCandidate struct {
	CandidateID PlateCandidateID
	CameraKey   string
	FrameSeq    uint64
	Timestamp   time.Time
	VehicleBBox BBox
	// PlateBBox is nil unless a PlateRegionProvider actually located a
	// plate region for this candidate (see provider.go). A nil PlateBBox
	// with a non-nil VehicleBBox means "vehicle/context crop only" — never
	// a fabricated plate region.
	PlateBBox *BBox
	// TrackID, BurstID: see VehicleCandidate.TrackID and CandidateBurst.BurstID.
	TrackID       string
	BurstID       string
	CorrelationID string
	QualityHints  QualityHints
	SourceWidth   int
	SourceHeight  int
}
