package anpr

import "time"

// Reason is the outcome category ProcessCandidate/Submit reports for every
// non-accepted candidate — spec item 36 requires distinguishing these
// explicitly rather than a single generic failure.
type Reason string

const (
	// ReasonAccepted means a PlateCandidate (and, if a crop was requested,
	// a CropResult) was produced.
	ReasonAccepted Reason = "ACCEPTED"
	// ReasonSkipped means ANPR is disabled for this call entirely (Config
	// zero value, or Enabled==false) — never touches any state.
	ReasonSkipped Reason = "SKIPPED"
	// ReasonRejected means the input or burst state made this candidate
	// invalid to process (bad bbox, unauthorized camera, duplicate, stale
	// out-of-order frame, crop geometry rejected).
	ReasonRejected Reason = "REJECTED"
	// ReasonUnavailable means a dependency this candidate needed could not
	// answer (plate-region provider required by policy but unavailable,
	// transport unavailable).
	ReasonUnavailable Reason = "UNAVAILABLE"
	// ReasonDroppedCapacity means a configured bound was already exhausted
	// (max active bursts, max frames per burst, max cameras, payload too
	// large for the transport's current limiter).
	ReasonDroppedCapacity Reason = "DROPPED_CAPACITY"
)

// CameraStatus is the small, bounded per-camera status block (spec item
// 38): counters only, no per-frame history.
type CameraStatus struct {
	CameraKey          string     `json:"camera_key"`
	ActiveBursts       int        `json:"active_bursts"`
	CandidatesCreated  int64      `json:"candidates_created"`
	CandidatesRejected int64      `json:"candidates_rejected"`
	CropsCreated       int64      `json:"crops_created"`
	CapacityDrops      int64      `json:"capacity_drops"`
	LastCandidateAt    *time.Time `json:"last_candidate_at,omitempty"`
}

// Metrics is the global, technical-operation-only counter block (spec item
// 39) — no billing, no per-frame history.
type Metrics struct {
	CandidateCount  int64 `json:"candidate_count"`
	CropCount       int64 `json:"crop_count"`
	CandidateBytes  int64 `json:"candidate_bytes"`
	BurstCount      int64 `json:"burst_count"`
	BurstExpired    int64 `json:"burst_expired"`
	CapacityDropped int64 `json:"capacity_dropped"`
	UploadEnvelopes int64 `json:"upload_envelopes"`
}
