package anpr

import (
	"errors"
	"fmt"
)

// ErrInvalidVehicleBBox is returned when a VehicleCandidate's bbox is
// structurally invalid (inverted or zero-area) — never silently accepted.
var ErrInvalidVehicleBBox = errors.New("anpr: invalid vehicle bbox")

// ValidateVehicleCandidate checks the fields ProcessCandidate depends on
// before any burst/crop work happens. It never panics; callers get an
// explicit error to reject on (fail-closed, see registry.go).
func ValidateVehicleCandidate(v VehicleCandidate) error {
	if v.CameraKey == "" {
		return errors.New("anpr: empty camera key")
	}
	if !v.VehicleBBox.Valid() {
		return ErrInvalidVehicleBBox
	}
	return nil
}

// NewCandidateID builds a deterministic, reproducible candidate id from
// stable inputs only — camera, frame sequence, an ordinal (this candidate's
// position within its frame/burst) and the burst id it belongs to. Two
// calls with identical inputs always produce identical output (required for
// test reproducibility); no timestamp, random value or counter that isn't
// one of the arguments ever enters the id.
//
// The ordinal exists because a single frame can in principle carry more
// than one plate candidate (multiple vehicles); without it, two candidates
// from the same camera+frame+burst would collide.
func NewCandidateID(cameraKey string, frameSeq uint64, ordinal int, burstID string) PlateCandidateID {
	return fmt.Sprintf("%s:%d:%d:%s", cameraKey, frameSeq, ordinal, burstID)
}

// groupKey returns the isolation key a VehicleCandidate's burst is scoped
// to: camera is always part of it, and TrackID (when present) further
// splits candidates from the same camera into independent groups so two
// different vehicles in view of one camera never share a burst. Empty
// TrackID falls back to CorrelationID (still per-frame-detection, never
// merging unrelated vehicles from different frames into "no track"), and
// finally to the camera alone only when neither is available.
func groupKey(cameraKey, trackID, correlationID string) string {
	switch {
	case trackID != "":
		return cameraKey + "|track:" + trackID
	case correlationID != "":
		return cameraKey + "|corr:" + correlationID
	default:
		return cameraKey + "|notrack"
	}
}

// dedupeKey identifies "the same candidate processed twice": same camera,
// same frame sequence, same vehicle bbox. Re-processing an identical
// (camera, frame_seq, vehicle candidate) tuple must not duplicate
// burst/evidence (B23) — this key is how the registry recognizes that case.
func dedupeKey(v VehicleCandidate) string {
	return fmt.Sprintf("%s:%d:%.2f,%.2f,%.2f,%.2f", v.CameraKey, v.FrameSeq,
		v.VehicleBBox.X0, v.VehicleBBox.Y0, v.VehicleBBox.X1, v.VehicleBBox.Y1)
}
