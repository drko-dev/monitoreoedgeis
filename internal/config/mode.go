package config

import "fmt"

// ProcessingMode selects where inference/video work happens.
type ProcessingMode string

const (
	// ModeCloud does everything in the SaaS; the edge agent stays minimal:
	// every sampled frame is dispatched, unfiltered.
	ModeCloud ProcessingMode = "cloud"
	// ModeHybrid (Milestone J) runs a lightweight local motion evaluator
	// (internal/processing.MotionDetector) ahead of the Router: only frames
	// flagged as motion candidates are dispatched (and therefore reach the
	// Cloud sink), reducing Cloud traffic while Cloud remains the sole
	// inference engine. It is not local YOLO/object detection (Hito K).
	ModeHybrid ProcessingMode = "hybrid"
	// ModeEdge runs local inference via a (future) Python Vision Worker.
	ModeEdge ProcessingMode = "edge"
)

// ParseProcessingMode validates a raw mode string.
func ParseProcessingMode(s string) (ProcessingMode, error) {
	switch ProcessingMode(s) {
	case ModeCloud, ModeHybrid, ModeEdge:
		return ProcessingMode(s), nil
	default:
		return "", fmt.Errorf("invalid processing mode %q: must be one of %s, %s, %s",
			s, ModeCloud, ModeHybrid, ModeEdge)
	}
}

func (m ProcessingMode) String() string { return string(m) }
