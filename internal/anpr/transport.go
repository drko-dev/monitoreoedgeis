package anpr

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// SchemaVersionV1 is the ANPRCandidateEnvelope wire schema version (spec
// item 30). It is carried as an explicit field precisely so the SaaS never
// has to infer version from field presence/order — a genuinely new,
// non-backward-compatible envelope shape bumps this string, an additive
// change does not need to.
const SchemaVersionV1 = "anpr_candidate_v1"

// ErrPayloadTooLarge is returned when a crop's encoded bytes exceed
// Config.MaxEncodedCropBytes (spec item 31). No silent truncation, no
// oversized upload.
var ErrPayloadTooLarge = errors.New("anpr: encoded crop payload exceeds configured bound")

// ANPRCandidateEnvelope is the pure transport contract for one plate
// candidate: metadata and payload are kept as separate fields on purpose
// (spec item 29) so a future transport can send the payload by reference
// (object storage key, io.Reader-backed streaming) without reshaping the
// metadata at all.
//
// This is NOT the SaaS's real ingest API — see doc.go's field mapping.
// Wiring this envelope onto an actual HTTP call is explicitly out of scope
// for J6-B (transport connects to the SaaS's definitive contract later).
type ANPRCandidateEnvelope struct {
	SchemaVersion string `json:"schema_version"`

	CameraKey     string    `json:"camera_key"`
	CandidateID   string    `json:"candidate_id"`
	BurstID       string    `json:"burst_id"`
	FrameSeq      uint64    `json:"frame_seq"`
	Timestamp     time.Time `json:"timestamp"`
	CorrelationID string    `json:"correlation_id"`

	VehicleBBox BBox  `json:"vehicle_bbox"`
	PlateBBox   *BBox `json:"plate_bbox,omitempty"`

	QualityHints QualityHints `json:"quality_hints"`

	// ProcessingMode mirrors processing.Frame.ProcessingMode — this package
	// only forwards it, never changes it (spec item 34).
	ProcessingMode string `json:"processing_mode"`
	// Source is always "edge" for anything this package produces.
	Source string `json:"source"`

	// Payload is the crop's encoded bytes (see ExtractJPEG), bounded by
	// NewEnvelope's MaxEncodedCropBytes check. Never populated when a
	// caller intends reference-based delivery instead (PayloadRef).
	Payload []byte `json:"payload,omitempty"`
	// PayloadRef is an opaque reference to a payload stored elsewhere
	// (spec item 29's "reference" option) — mutually exclusive with
	// Payload in practice, though the type does not enforce that since no
	// concrete reference-store integration exists yet in PREP.
	PayloadRef string `json:"payload_ref,omitempty"`
	// PayloadContentType/PayloadSizeBytes are payload metadata (cross-repo
	// contract item: "content-type, size in bytes, hash optional"),
	// auto-populated by NewEnvelope whenever Payload is set. PayloadHash is
	// left empty by PREP (optional per contract; no hashing implemented yet).
	PayloadContentType string `json:"payload_content_type,omitempty"`
	PayloadSizeBytes   int64  `json:"payload_size_bytes,omitempty"`
	PayloadHash        string `json:"payload_hash,omitempty"`

	// PlateModelVersion identifies the plate-detection model/version that
	// produced PlateBBox, when one exists. PREP ships no real model (see
	// provider.go), so this is always empty today — the field exists purely
	// so a future PlateRegionProvider has somewhere to report it without an
	// envelope shape change (cross-repo contract item 27).
	PlateModelVersion string `json:"plate_model_version,omitempty"`
}

// NewEnvelope builds an ANPRCandidateEnvelope from a PlateCandidate and its
// crop payload, enforcing maxPayloadBytes explicitly (spec item 31: fail
// explicit, never silently truncate/oversize). payload may be nil/empty for
// a reference-only envelope (pass payloadRef instead).
func NewEnvelope(pc PlateCandidate, processingMode string, payload []byte, payloadRef string, maxPayloadBytes int64) (ANPRCandidateEnvelope, error) {
	if maxPayloadBytes > 0 && int64(len(payload)) > maxPayloadBytes {
		return ANPRCandidateEnvelope{}, ErrPayloadTooLarge
	}
	env := ANPRCandidateEnvelope{
		SchemaVersion:  SchemaVersionV1,
		CameraKey:      pc.CameraKey,
		CandidateID:    pc.CandidateID,
		BurstID:        pc.BurstID,
		FrameSeq:       pc.FrameSeq,
		Timestamp:      pc.Timestamp,
		CorrelationID:  pc.CorrelationID,
		VehicleBBox:    pc.VehicleBBox,
		PlateBBox:      pc.PlateBBox,
		QualityHints:   pc.QualityHints,
		ProcessingMode: processingMode,
		Source:         "edge",
		Payload:        payload,
		PayloadRef:     payloadRef,
	}
	if len(payload) > 0 {
		env.PayloadContentType = "image/jpeg"
		env.PayloadSizeBytes = int64(len(payload))
	}
	return env, nil
}

// ErrUnsupportedSchemaVersion is returned when an envelope's schema_version
// does not match a version this package knows how to handle — an explicit
// fail, never a best-effort parse of an unknown shape (cross-repo contract
// item, C26).
var ErrUnsupportedSchemaVersion = errors.New("anpr: unsupported envelope schema_version")

// DecodeEnvelope unmarshals data into an ANPRCandidateEnvelope and validates
// its schema_version explicitly. Edge does not currently need to parse its
// own envelopes in production (they are only ever built, never read back),
// but this exists so that any future path that does (retries, local
// replay/inspection) fails closed on an unknown version instead of silently
// continuing.
func DecodeEnvelope(data []byte) (ANPRCandidateEnvelope, error) {
	var env ANPRCandidateEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return ANPRCandidateEnvelope{}, fmt.Errorf("anpr: decode envelope: %w", err)
	}
	if env.SchemaVersion != SchemaVersionV1 {
		return ANPRCandidateEnvelope{}, fmt.Errorf("%w: got %q, want %q", ErrUnsupportedSchemaVersion, env.SchemaVersion, SchemaVersionV1)
	}
	return env, nil
}

// ErrTransportUnavailable is returned by an EnvelopeTransport that cannot
// currently accept envelopes (e.g. no sink wired yet in PREP) — an explicit
// UNAVAILABLE outcome, never a fabricated success (spec item 36).
var ErrTransportUnavailable = errors.New("anpr: envelope transport unavailable")

// EnvelopeTransport sends a built envelope onward. PREP ships no real
// implementation that reaches the SaaS — see
// docs/integrations/J6B_EDGE_ANPR_CANDIDATE_PREP.md for the documented
// trade-off on reusing internal/cloudsink's existing offline buffer
// (spec item 28) versus a future dedicated evidence transport, which this
// package deliberately does not decide or implement in PREP.
type EnvelopeTransport interface {
	Send(ANPRCandidateEnvelope) error
}

// UnavailableTransport implements EnvelopeTransport by always failing
// explicitly — the safe PREP default when no real transport is wired
// (B24).
type UnavailableTransport struct{}

func (UnavailableTransport) Send(ANPRCandidateEnvelope) error { return ErrTransportUnavailable }

// RateLimitedTransport wraps a real EnvelopeTransport with the SAME rate
// bound a real cloudsink.TokenBucket enforces, WITHOUT bypassing that
// limiter (spec item 27): a caller wires the actual limiter's Allow method
// in as AllowFunc. When AllowFunc denies, Send returns ErrCapacityExceeded
// rather than sending anyway — this package never has a code path that
// skips the check.
type RateLimitedTransport struct {
	Inner     EnvelopeTransport
	AllowFunc func(bytes int64) bool
}

// ErrCapacityExceeded is returned when AllowFunc denies an envelope — the
// explicit "drop lower quality / queue bounded / fail explicit" decision
// point spec item 27 requires; RateLimitedTransport's own policy is "fail
// explicit" (the caller decides whether to retry a lower-quality candidate
// instead — this type never silently bypasses the limiter to force delivery).
var ErrCapacityExceeded = errors.New("anpr: transport capacity exceeded (rate/bandwidth limiter denied)")

func (t RateLimitedTransport) Send(env ANPRCandidateEnvelope) error {
	if t.AllowFunc != nil && !t.AllowFunc(int64(len(env.Payload))) {
		return ErrCapacityExceeded
	}
	if t.Inner == nil {
		return ErrTransportUnavailable
	}
	return t.Inner.Send(env)
}
