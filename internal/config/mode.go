package config

import "fmt"

// ProcessingMode selects where inference/video work happens. This milestone
// models the three modes but implements no functional difference between them.
type ProcessingMode string

const (
	// ModeCloud does everything in the SaaS; the edge agent stays minimal.
	ModeCloud ProcessingMode = "cloud"
	// ModeHybrid splits light local processing with the SaaS.
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
