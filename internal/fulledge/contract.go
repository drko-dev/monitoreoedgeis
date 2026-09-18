package fulledge

import (
	"errors"
	"time"
)

// Sentinel errors for Full Edge operations.
var (
	ErrZeroDetections        = errors.New("fulledge: inference result has zero detections")
	ErrDiskSpaceBelowMinimum = errors.New("fulledge: free disk space below minimum threshold")
	ErrQueueFull             = errors.New("fulledge: inference queue capacity reached")
	ErrEvidenceAlreadyExists = errors.New("fulledge: evidence file already exists for event")
	ErrInvalidDevice         = errors.New("fulledge: invalid inference device configured")
	ErrEventCorrupt          = errors.New("fulledge: persisted event file is corrupt or invalid")
)

// BoundingBox represents the normalized coordinates [0..1] of a detected object.
type BoundingBox struct {
	XMin float64 `json:"xmin"`
	YMin float64 `json:"ymin"`
	XMax float64 `json:"xmax"`
	YMax float64 `json:"ymax"`
}

// LocalDetection is a single detected object emitted by local YOLO inference.
type LocalDetection struct {
	ClassID    int         `json:"class_id"`
	Label      string      `json:"label"`
	Tipo       string      `json:"tipo"`
	Confidence float64     `json:"confidence"`
	BBox       BoundingBox `json:"bbox"`
}

// InferenceResult is the structured payload delivered by local YOLO inference (K1-K4).
type InferenceResult struct {
	CandidateKey       string           `json:"candidate_key"`
	FrameSeq           uint64           `json:"frame_seq"`
	FrameTimestamp     time.Time        `json:"frame_timestamp"`
	InferenceTimestamp time.Time        `json:"inference_timestamp"`
	InferenceMs        float64          `json:"inference_ms"`
	Device             string           `json:"device"`
	Detections         []LocalDetection `json:"detections"`
}
