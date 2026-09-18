package vision

import (
	"bytes"
	"context"
	"fmt"
	"image/jpeg"
	"log/slog"
	"sync"
	"sync/atomic"

	"github.com/drko-dev/monitoreoedgeis/internal/processing"
)

// jpegQuality is fixed rather than configurable: the worker only ever sees
// this frame to run inference, never to display or store it, so a further
// bandwidth/quality knob (like Milestone I7's CloudJPEGQuality) would be a
// knob nobody needs. Ultralytics' own preprocessing resizes/normalizes
// regardless.
const jpegQuality = 90

// Sink implements processing.Sink: it is the routing destination K1 wires
// in for GEOCAM_PROCESSING_MODE=edge, in place of (never alongside, for
// inference purposes) internal/cloudsink.CloudSink — see
// internal/agent/vision_module.go. Edge mode's Router therefore never sends
// a frame to CloudSink for inference at all: newCloudSink already gates on
// ProcessingMode being ModeCloud/ModeHybrid, and edge mode registers this
// Sink instead.
type Sink struct {
	worker *Worker
	models *ModelManager
	health HealthReporter // optional; nil in tests that don't care
	logger *slog.Logger

	mu   sync.Mutex
	last InferenceResult

	droppedNotReady atomic.Int64
	encodeErrors    atomic.Int64
}

// HealthReporter is implemented by *health.Reporter, satisfied
// structurally so this package never imports internal/health — same
// cycle-avoidance pattern internal/cloudsink already uses for its own
// SetCloudStatus push.
type HealthReporter interface {
	SetVisionStatus(Status)
}

// NewSink wraps worker as a routing Sink. Callers own worker's lifecycle
// only via Sink.Close (implements the router's sinkCloser interface).
// health may be nil (tests, or callers that only read Status() directly).
func NewSink(worker *Worker, models *ModelManager, health HealthReporter, logger *slog.Logger) *Sink {
	if logger == nil {
		logger = slog.Default()
	}
	s := &Sink{worker: worker, models: models, health: health, logger: logger}
	worker.SetOnStateChange(s.publishStatus)
	return s
}

// Name implements processing.Sink.
func (s *Sink) Name() string { return "edge-vision" }

// Route implements processing.Sink: encodes f to JPEG and runs local YOLO
// inference, bounded by the worker's configured InferTimeout. Backpressure
// is Router's job (one bounded queue + one worker goroutine per sink,
// see internal/processing/router.go) — a busy worker simply makes Route
// take longer, which fills that queue and starts dropping frames for this
// sink specifically, never blocking any other sink or the pipeline itself
// (K4: "No permitir cola infinita mientras YOLO está ocupado").
func (s *Sink) Route(f processing.Frame) error {
	defer s.publishStatus()

	if !s.worker.Ready() {
		s.droppedNotReady.Add(1)
		return fmt.Errorf("edge-vision: worker not ready (state=%s)", s.worker.Status().State)
	}

	img, err := processing.YUV420PToImage(f.Data, f.OutputWidth, f.OutputHeight)
	if err != nil {
		s.encodeErrors.Add(1)
		return fmt.Errorf("edge-vision: yuv420p decode: %w", err)
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: jpegQuality}); err != nil {
		s.encodeErrors.Add(1)
		return fmt.Errorf("edge-vision: jpeg encode: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), s.worker.cfg.InferTimeout)
	defer cancel()

	result, err := s.worker.Infer(ctx, InferRequest{
		CandidateKey: f.CandidateKey,
		FrameSeq:     f.Seq,
		Timestamp:    f.Timestamp,
		Width:        f.OutputWidth,
		Height:       f.OutputHeight,
		JPEG:         buf.Bytes(),
	})
	if err != nil {
		return fmt.Errorf("edge-vision: infer: %w", err)
	}

	s.mu.Lock()
	s.last = result
	s.mu.Unlock()
	return nil
}

func (s *Sink) publishStatus() {
	if s.health != nil {
		s.health.SetVisionStatus(s.Status())
	}
}

// Close implements the router's sinkCloser interface: it stops the worker
// subprocess in an orderly, bounded way.
func (s *Sink) Close() { _ = s.worker.Stop(context.Background()) }

// Status is Sink's /status block (K1's LocalDetection/InferenceResult
// authority plus K4's counters): worker lifecycle state, model info, and
// the sink-level counters Route itself owns (dropped-not-ready, encode
// errors) — never a frame or its bytes.
type Status struct {
	Worker             WorkerStatus  `json:"worker"`
	Models             []ModelStatus `json:"models"`
	DroppedNotReady    int64         `json:"dropped_not_ready"`
	EncodeErrors       int64         `json:"encode_errors"`
	LastDetectionCount int           `json:"last_detection_count"`
	LastCandidateKey   string        `json:"last_candidate_key,omitempty"`
}

// Status returns a point-in-time snapshot.
func (s *Sink) Status() Status {
	s.mu.Lock()
	last := s.last
	s.mu.Unlock()
	return Status{
		Worker:             s.worker.Status(),
		Models:             s.models.Status(),
		DroppedNotReady:    s.droppedNotReady.Load(),
		EncodeErrors:       s.encodeErrors.Load(),
		LastDetectionCount: len(last.Detections),
		LastCandidateKey:   last.CandidateKey,
	}
}
