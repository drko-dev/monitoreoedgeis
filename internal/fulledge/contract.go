package fulledge

import (
	"errors"
	"fmt"
	"math"
	"time"
)

// Sentinel errors for Full Edge operations.
var (
	ErrZeroDetections        = errors.New("fulledge: inference result has zero detections")
	ErrDiskSpaceBelowMinimum = errors.New("fulledge: free disk space below minimum threshold")
	ErrQueueFull             = errors.New("fulledge: inference queue capacity reached")
	ErrEvidenceAlreadyExists = errors.New("fulledge: evidence file already exists for event")
	ErrEvidenceConflict      = errors.New("fulledge: divergent evidence content for event")
	ErrEventConflict         = errors.New("fulledge: divergent event content for event")
	ErrInvalidDevice         = errors.New("fulledge: invalid inference device configured")
	ErrEventCorrupt          = errors.New("fulledge: persisted event file is corrupt or invalid")
)

// BoundingBox is pixel coordinates in the processed frame — the exact
// semantics the local YOLO worker itself produces (internal/vision.
// Detection.BBox: x1,y1,x2,y2). This is the one bbox representation used
// throughout Full Edge; conversion to x/y/width/height happens only once,
// at the SaaS wire boundary (see the transport.LocalEvent mapping in
// internal/agent) — never a pixel->normalized->pixel round trip internally.
type BoundingBox struct {
	X1 float64 `json:"x1"`
	Y1 float64 `json:"y1"`
	X2 float64 `json:"x2"`
	Y2 float64 `json:"y2"`
}

// Validate checks the box is well-formed (x2>x1, y2>y1, all finite) and,
// when frameWidth/frameHeight are both > 0, that it falls within the frame.
func (b BoundingBox) Validate(frameWidth, frameHeight int) error {
	for _, v := range []float64{b.X1, b.Y1, b.X2, b.Y2} {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return fmt.Errorf("fulledge: bbox coordinate is not finite: %+v", b)
		}
	}
	if b.X2 <= b.X1 || b.Y2 <= b.Y1 {
		return fmt.Errorf("fulledge: bbox is degenerate (x2<=x1 or y2<=y1): %+v", b)
	}
	if frameWidth > 0 && frameHeight > 0 {
		if b.X1 < 0 || b.Y1 < 0 || b.X2 > float64(frameWidth) || b.Y2 > float64(frameHeight) {
			return fmt.Errorf("fulledge: bbox %+v outside frame %dx%d", b, frameWidth, frameHeight)
		}
	}
	return nil
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
	CorrelationID      string           `json:"correlation_id,omitempty"`
	Detections         []LocalDetection `json:"detections"`
}
