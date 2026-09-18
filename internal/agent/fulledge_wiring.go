package agent

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/drko-dev/monitoreoedgeis/internal/edgebacklog"
	"github.com/drko-dev/monitoreoedgeis/internal/evidence"
	"github.com/drko-dev/monitoreoedgeis/internal/fulledge"
	"github.com/drko-dev/monitoreoedgeis/internal/processing"
	"github.com/drko-dev/monitoreoedgeis/internal/transport"
	"github.com/drko-dev/monitoreoedgeis/internal/vision"
)

// FrameHistoryProvider supplies decoded-frame history for a candidate camera.
type FrameHistoryProvider interface {
	FrameHistory(candidateKey string) processing.FrameHistory
}

// fullEdgeEventConsumer implements vision.EventConsumer: it is the one real
// place K1's local YOLO output becomes a K5-K8 LocalEvent and, when this
// Edge is enrolled, a K9-K12 backlog submission. Before this integration
// pass, internal/vision and internal/fulledge/internal/edgebacklog were
// three disconnected trees that only met in tests.
type fullEdgeEventConsumer struct {
	svc             *fulledge.Service
	producer        edgebacklog.Producer // nil when unenrolled — see newFullEdgeEventConsumer
	clipper         *evidence.Clipper
	historyProvider FrameHistoryProvider
	dataDir         string
	logger          *slog.Logger
}

func newFullEdgeEventConsumer(svc *fulledge.Service, producer edgebacklog.Producer, clipper *evidence.Clipper, history FrameHistoryProvider, dataDir string, logger *slog.Logger) *fullEdgeEventConsumer {
	return &fullEdgeEventConsumer{
		svc:             svc,
		producer:        producer,
		clipper:         clipper,
		historyProvider: history,
		dataDir:         dataDir,
		logger:          logger,
	}
}

func (c *fullEdgeEventConsumer) SetHistoryProvider(hp FrameHistoryProvider) {
	c.historyProvider = hp
}

// ConsumeInference implements vision.EventConsumer.
func (c *fullEdgeEventConsumer) ConsumeInference(result vision.InferenceResult, jpeg []byte) {
	limits := c.svc.Limits()
	// K6 reconciliation: this is the one call site that ever claims a
	// concurrency slot, so EdgeMaxConcurrentInference (default 1, matching
	// processing.Router's real one-worker-goroutine-per-sink invariant) is
	// now a true bound, not a disconnected counter. It always succeeds in
	// practice for the same reason — Route() already serializes callers —
	// but if that invariant is ever violated, RecordQueueDrop makes it
	// visible instead of silently over-processing.
	if !limits.TryAcquireInference() {
		limits.RecordQueueDrop()
		return
	}
	defer limits.ReleaseInference()

	fres := fulledge.InferenceResult{
		CandidateKey:       result.CandidateKey,
		FrameSeq:           result.FrameSeq,
		CorrelationID:      result.CandidateKey + fmt.Sprintf("-%d", result.FrameSeq),
		FrameTimestamp:     result.Timestamp,
		InferenceTimestamp: result.Timestamp,
		InferenceMs:        result.InferenceMS,
		Device:             result.Device, // the worker's own confirmed device (K3) — never asserted by Go-side capability detection alone
		Detections:         make([]fulledge.LocalDetection, 0, len(result.Detections)),
	}
	for _, det := range result.Detections {
		fres.Detections = append(fres.Detections, fulledge.LocalDetection{
			ClassID:    det.ClassID,
			Label:      det.Label,
			Tipo:       det.Type,
			Confidence: det.Confidence,
			// Pixel x1,y1,x2,y2 — the exact semantics the worker produces
			// (K2), preserved unchanged through fulledge (K2/integration
			// item #2's bbox unification).
			BBox: fulledge.BoundingBox{X1: det.BBox[0], Y1: det.BBox[1], X2: det.BBox[2], Y2: det.BBox[3]},
		})
	}

	events, err := c.svc.ProcessInferenceWithJPEG(fres, jpeg)
	if err != nil {
		if c.logger != nil {
			c.logger.Warn("full edge: failed to process inference result", slog.Any("error", err))
		}
		return
	}
	if c.producer == nil {
		return // unenrolled: events/evidence are still persisted locally (K7/K8), just not queued for sync yet
	}
	for _, evt := range events {
		c.enqueue(evt)
	}
}

// enqueue maps one persisted fulledge.LocalEvent onto the K9-K12 sync
// contract and hands it to the backlog. It never sends organization_id,
// camera_id, or a local filesystem path (item #5) — only the fields
// transport.LocalEvent actually declares.
func (c *fullEdgeEventConsumer) enqueue(evt *fulledge.LocalEvent) {
	sub := edgebacklog.Submission{
		Event: transport.LocalEvent{
			EventUUID:    evt.EventUUID,
			CandidateKey: evt.CandidateKey,
			Class:        evt.Tipo,
			Confidence:   evt.Confidence,
			// The one and only pixel->x/y/width/height conversion in Full
			// Edge (item #2): everywhere else, including fulledge's own
			// on-disk record, keeps the worker's native x1,y1,x2,y2.
			BBox: map[string]float64{
				"x":      evt.BBox.X1,
				"y":      evt.BBox.Y1,
				"width":  evt.BBox.X2 - evt.BBox.X1,
				"height": evt.BBox.Y2 - evt.BBox.Y1,
			},
			Timestamp:     evt.SourceTimestamp.UTC().Format(rfc3339Milli),
			CorrelationID: evt.CorrelationID,
		},
	}
	if evt.Evidence != nil && evt.Evidence.ErrorMessage == "" {
		if abs, ok := c.resolveEvidencePath(evt.Evidence.Path); ok {
			sub.Capture = &edgebacklog.Evidence{Path: abs, SHA256: evt.Evidence.SHA256, Size: evt.Evidence.SizeBytes}
		} else if c.logger != nil {
			c.logger.Warn("full edge: evidence path failed containment check, submitting event without capture",
				slog.String("event_uuid", evt.EventUUID))
		}
	}

	// M8: attempt clip using existing FrameHistory ring buffer.
	if c.clipper != nil && c.historyProvider != nil {
		if history := c.historyProvider.FrameHistory(evt.CandidateKey); history != nil {
			clipRec, err := c.clipper.Capture(context.Background(), history, evt.EventUUID, evt.SourceTimestamp)
			if err != nil {
				if c.logger != nil {
					c.logger.Warn("full edge: failed to generate clip from frame history, submitting event without clip",
						slog.String("event_uuid", evt.EventUUID),
						slog.String("candidate_key", evt.CandidateKey),
						slog.Any("error", err))
				}
			} else {
				if abs, ok := c.resolveEvidencePath(clipRec.RelativePath); ok {
					sub.Clip = &edgebacklog.Evidence{
						Path:       abs,
						SHA256:     clipRec.SHA256,
						Size:       clipRec.Size,
						DurationMS: clipRec.DurationMS,
					}
				} else if c.logger != nil {
					c.logger.Warn("full edge: clip path failed containment check, submitting event without clip",
						slog.String("event_uuid", evt.EventUUID))
				}
			}
		} else if c.logger != nil {
			c.logger.Warn("full edge: no frame history for candidate key, submitting event without clip",
				slog.String("event_uuid", evt.EventUUID),
				slog.String("candidate_key", evt.CandidateKey))
		}
	}

	if err := c.producer.Enqueue(sub); err != nil && c.logger != nil {
		c.logger.Warn("full edge: failed to enqueue local event for sync",
			slog.String("event_uuid", evt.EventUUID), slog.Any("error", err))
	}
}

const rfc3339Milli = "2006-01-02T15:04:05.000Z07:00"

// resolveEvidencePath joins relPath (as stored in fulledge.EvidenceRef,
// itself relative to dataDir) against dataDir and refuses anything that
// would resolve outside it — item #4/#5's "nunca aceptar escape fuera de
// GEOCAM_DATA_DIR" — before it ever reaches edgebacklog, which os.Stat's
// this path directly.
func (c *fullEdgeEventConsumer) resolveEvidencePath(relPath string) (string, bool) {
	if relPath == "" {
		return "", false
	}
	abs := filepath.Join(c.dataDir, relPath)
	rootWithSep := c.dataDir
	if !strings.HasSuffix(rootWithSep, string(filepath.Separator)) {
		rootWithSep += string(filepath.Separator)
	}
	if abs != c.dataDir && !strings.HasPrefix(abs, rootWithSep) {
		return "", false
	}
	return abs, true
}
