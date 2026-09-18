package fulledge

import (
	"fmt"
	"log/slog"
	"sync/atomic"

	"github.com/drko-dev/monitoreoedgeis/internal/identity"
	"github.com/drko-dev/monitoreoedgeis/internal/processing"
)

// ServiceConfig contains configuration for the Full Edge service.
type ServiceConfig struct {
	EdgeID      string
	TenantID    string
	SiteID      string
	DataDir     string
	ModelName   string
	DeviceMode  DeviceMode
	Limits      LimitsConfig
	JPEGQuality int
}

// Service coordinates Full Edge local event creation, hardware management, limits, and evidence storage.
type Service struct {
	cfg        ServiceConfig
	hardware   *HardwareManager
	limits     *LimitsManager
	store      *EventStore
	evidence   *EvidenceManager
	healthSink HealthSink
	logger     *slog.Logger

	localDetections    atomic.Int64
	localEventsCreated atomic.Int64
	evidenceSaved      atomic.Int64
	evidenceFailures   atomic.Int64
}

// NewService instantiates and wires a Full Edge Service.
func NewService(cfg ServiceConfig, store *EventStore, evidence *EvidenceManager, hw *HardwareManager, limits *LimitsManager, hs HealthSink, logger *slog.Logger) *Service {
	// No invented default model name: the real production model set is
	// always yolo11s-pose.pt (person) + yolo11n.pt (vehicle) — see
	// internal/vision — and callers must supply that composite ModelName
	// explicitly (see internal/agent's wiring). An empty ModelName here
	// stays empty rather than silently claiming a model ("yolov8n") that
	// was never used.
	if hw == nil {
		hw = NewHardwareManager(cfg.DeviceMode, nil, logger)
	}
	if limits == nil {
		limits = NewLimitsManager(cfg.Limits, nil, nil, logger)
	}
	s := &Service{
		cfg:        cfg,
		hardware:   hw,
		limits:     limits,
		store:      store,
		evidence:   evidence,
		healthSink: hs,
		logger:     logger,
	}
	s.publishStatus()
	return s
}

// Hardware returns the hardware manager.
func (s *Service) Hardware() *HardwareManager {
	return s.hardware
}

// Limits returns the limits manager.
func (s *Service) Limits() *LimitsManager {
	return s.limits
}

// Store returns the underlying EventStore.
func (s *Service) Store() *EventStore {
	return s.store
}

// Evidence returns the underlying EvidenceManager.
func (s *Service) Evidence() *EvidenceManager {
	return s.evidence
}

// ProcessInference evaluates an InferenceResult and creates persisted LocalEvents and evidence if detections are present.
func (s *Service) ProcessInference(res InferenceResult, optFrame *processing.Frame) ([]*LocalEvent, error) {
	if len(res.Detections) == 0 {
		return nil, ErrZeroDetections
	}

	s.localDetections.Add(int64(len(res.Detections)))
	if res.Device == "" {
		res.Device = s.hardware.CurrentDevice()
	}

	var evRef *EvidenceRef
	if optFrame != nil && s.evidence != nil {
		// A fresh random UUID identifies this shared capture on disk — never
		// the raw candidate_key (see internal/fulledge/evidence.go: the
		// evidence path is built from a validated UUID alone).
		captureUUID, err := identity.NewUUIDv4()
		if err != nil {
			return nil, fmt.Errorf("fulledge: generate capture uuid: %w", err)
		}
		ref, err := s.evidence.SaveFrame(captureUUID, *optFrame)
		if err != nil {
			s.evidenceFailures.Add(1)
			if s.logger != nil {
				s.logger.Warn("failed to save evidence frame, degrading safely",
					slog.String("candidate_key", res.CandidateKey),
					slog.Uint64("frame_seq", res.FrameSeq),
					slog.Any("error", err))
			}
			evRef = &EvidenceRef{
				CapturedAt:   optFrame.Timestamp,
				ErrorMessage: err.Error(),
			}
		} else {
			s.evidenceSaved.Add(1)
			evRef = ref
		}
	}

	var created []*LocalEvent
	for _, det := range res.Detections {
		if err := det.BBox.Validate(0, 0); err != nil {
			if s.logger != nil {
				s.logger.Warn("dropping detection with invalid bbox",
					slog.String("candidate_key", res.CandidateKey),
					slog.String("correlation_id", res.CorrelationID),
					slog.Any("error", err))
			}
			continue
		}
		if err := det.ValidateConfidence(); err != nil {
			if s.logger != nil {
				s.logger.Warn("dropping detection with invalid confidence",
					slog.String("candidate_key", res.CandidateKey),
					slog.String("correlation_id", res.CorrelationID),
					slog.Any("error", err))
			}
			continue
		}
		evt, err := NewLocalEvent(s.cfg.EdgeID, s.cfg.TenantID, s.cfg.SiteID, s.cfg.ModelName, res, det, evRef)
		if err != nil {
			return created, fmt.Errorf("fulledge: create local event: %w", err)
		}

		if s.store != nil {
			if err := s.store.Save(evt); err != nil {
				return created, fmt.Errorf("fulledge: persist local event: %w", err)
			}
		}
		s.localEventsCreated.Add(1)
		created = append(created, evt)
	}

	s.publishStatus()
	return created, nil
}

// ProcessInferenceWithJPEG evaluates an InferenceResult and attaches raw JPEG bytes as evidence if provided.
func (s *Service) ProcessInferenceWithJPEG(res InferenceResult, jpegBytes []byte) ([]*LocalEvent, error) {
	if len(res.Detections) == 0 {
		return nil, ErrZeroDetections
	}

	s.localDetections.Add(int64(len(res.Detections)))
	if res.Device == "" {
		res.Device = s.hardware.CurrentDevice()
	}

	var evRef *EvidenceRef
	if len(jpegBytes) > 0 && s.evidence != nil {
		captureUUID, err := identity.NewUUIDv4()
		if err != nil {
			return nil, fmt.Errorf("fulledge: generate capture uuid: %w", err)
		}
		ref, err := s.evidence.SaveJPEG(captureUUID, res.FrameTimestamp, jpegBytes)
		if err != nil {
			s.evidenceFailures.Add(1)
			if s.logger != nil {
				s.logger.Warn("failed to save evidence jpeg, degrading safely",
					slog.String("candidate_key", res.CandidateKey),
					slog.Uint64("frame_seq", res.FrameSeq),
					slog.Any("error", err))
			}
			evRef = &EvidenceRef{
				CapturedAt:   res.FrameTimestamp,
				ErrorMessage: err.Error(),
			}
		} else {
			s.evidenceSaved.Add(1)
			evRef = ref
		}
	}

	var created []*LocalEvent
	for _, det := range res.Detections {
		if err := det.BBox.Validate(0, 0); err != nil {
			if s.logger != nil {
				s.logger.Warn("dropping detection with invalid bbox",
					slog.String("candidate_key", res.CandidateKey),
					slog.String("correlation_id", res.CorrelationID),
					slog.Any("error", err))
			}
			continue
		}
		if err := det.ValidateConfidence(); err != nil {
			if s.logger != nil {
				s.logger.Warn("dropping detection with invalid confidence",
					slog.String("candidate_key", res.CandidateKey),
					slog.String("correlation_id", res.CorrelationID),
					slog.Any("error", err))
			}
			continue
		}
		evt, err := NewLocalEvent(s.cfg.EdgeID, s.cfg.TenantID, s.cfg.SiteID, s.cfg.ModelName, res, det, evRef)
		if err != nil {
			return created, fmt.Errorf("fulledge: create local event: %w", err)
		}

		if s.store != nil {
			if err := s.store.Save(evt); err != nil {
				return created, fmt.Errorf("fulledge: persist local event: %w", err)
			}
		}
		s.localEventsCreated.Add(1)
		created = append(created, evt)
	}

	s.publishStatus()
	return created, nil
}

// Status returns the current Full Edge status snapshot.
func (s *Service) Status() Status {
	var backlog int64
	if s.store != nil {
		backlog = s.store.BacklogCount()
	}

	return Status{
		LocalDetections:        s.localDetections.Load(),
		LocalEventsCreated:     s.localEventsCreated.Load(),
		EvidenceSaved:          s.evidenceSaved.Load(),
		EvidenceFailures:       s.evidenceFailures.Load(),
		LocalEventBacklog:      backlog,
		CurrentInferenceDevice: s.hardware.CurrentDevice(),
		FallbackCPUCount:       s.hardware.FallbackCount(),
		Hardware:               s.hardware.Status(),
		Limits:                 s.limits.Status(s.cfg.DataDir),
	}
}

func (s *Service) publishStatus() {
	if s.healthSink != nil {
		s.healthSink.SetFullEdgeStatus(s.Status())
	}
}
