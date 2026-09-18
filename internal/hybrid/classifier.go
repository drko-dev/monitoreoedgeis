// Package hybrid implements the Edge side of Milestone J (J6–J9): candidate
// detection, lightweight classifier abstraction, and candidate metadata.
//
// Candidate frames flagged here or in upstream motion/ROI filters are routed
// to Cloud for second-stage heavy inference (e.g. YOLOv8). Heavy inference
// is never run directly on the appliance under Milestone J.
package hybrid

import (
	"context"
	"errors"
	"fmt"

	"github.com/drko-dev/monitoreoedgeis/internal/processing"
)

// Sentinel errors.
var (
	ErrClassifierDisabled    = errors.New("hybrid: classifier disabled")
	ErrModelNotFound         = errors.New("hybrid: classifier model file not found")
	ErrUnsupportedClassifier = errors.New("hybrid: unsupported classifier engine")
)

// ClassificationResult encapsulates the candidate decision from a local model.
type ClassificationResult struct {
	IsCandidate bool     `json:"is_candidate"`
	Score       float64  `json:"score"`
	Reason      string   `json:"reason"`
	Labels      []string `json:"labels,omitempty"`
}

// CandidateClassifier is the decoupled interface for optional lightweight
// edge models (Milestone J6).
//
// Production status: ADAPTER DONE / MODEL REAL OPTIONAL PENDING.
// Default mode is disabled (NoopClassifier).
type CandidateClassifier interface {
	Classify(ctx context.Context, f *processing.Frame) (ClassificationResult, error)
	Close() error
}

// Config defines tunables for the optional local candidate classifier.
type Config struct {
	Enabled   bool    `json:"enabled"`
	ModelPath string  `json:"model_path"`
	Threshold float64 `json:"threshold"`
}

// NoopClassifier is an inert implementation used when the classifier is
// disabled (the default).
type NoopClassifier struct{}

// Classify always returns a non-candidate result.
func (n *NoopClassifier) Classify(ctx context.Context, f *processing.Frame) (ClassificationResult, error) {
	return ClassificationResult{
		IsCandidate: false,
		Score:       0.0,
		Reason:      "noop_disabled",
	}, nil
}

// Close is a no-op.
func (n *NoopClassifier) Close() error {
	return nil
}

// NewClassifier constructs a CandidateClassifier according to cfg.
// If cfg.Enabled is false or cfg.ModelPath is empty, it returns a NoopClassifier.
func NewClassifier(cfg Config) (CandidateClassifier, error) {
	if !cfg.Enabled || cfg.ModelPath == "" {
		return &NoopClassifier{}, nil
	}
	// For now, only NoopClassifier is provided in this milestone.
	// When real lightweight models (e.g. MobileNet/TFLite/ONNX) are integrated,
	// their adapters will plug in here without altering CandidateClassifier.
	return nil, fmt.Errorf("%w: model_path=%s", ErrUnsupportedClassifier, cfg.ModelPath)
}
