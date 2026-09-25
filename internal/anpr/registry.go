package anpr

import (
	"log/slog"
	"sync"
	"time"
)

// SubmitResult is what Registry.Submit returns for one VehicleCandidate.
type SubmitResult struct {
	Candidate *PlateCandidate
	Crop      *CropResult
	Reason    Reason
	// Detail is a short machine-readable reason code (e.g.
	// "unauthorized", "duplicate", "capacity_bursts",
	// "plate_region_unavailable") for logs/metrics — never PII, never a
	// full plate read or JPEG.
	Detail string
}

// Registry ties burst management, authorization, the plate-region
// provider and crop geometry into one entry point: Submit. It never
// touches internal/rtsp, never opens a second video pipeline, and (per
// Config.Enabled / Authorizer) guarantees zero mutation for a
// disabled/unauthorized camera (spec items 20, 35, 36).
type Registry struct {
	mu       sync.Mutex
	cfg      Config
	burstMgr *BurstManager
	auth     Authorizer
	provider PlateRegionProvider
	logger   *slog.Logger
	now      func() time.Time

	cameras map[string]*CameraStatus // bounded by cfg.MaxCameras
	metrics Metrics
}

// RegistryOption configures optional Registry dependencies, all of which
// have safe fail-closed defaults (a nil Authorizer denies everything; a
// nil PlateRegionProvider behaves like NoneProvider).
type RegistryOption func(*Registry)

func WithAuthorizer(a Authorizer) RegistryOption { return func(r *Registry) { r.auth = a } }
func WithPlateRegionProvider(p PlateRegionProvider) RegistryOption {
	return func(r *Registry) { r.provider = p }
}
func WithLogger(l *slog.Logger) RegistryOption { return func(r *Registry) { r.logger = l } }
func WithClock(now func() time.Time) RegistryOption {
	return func(r *Registry) { r.now = now }
}

// NewRegistry creates a Registry for cfg. cfg.HighSpeedLPR, if set, is
// never auto-applied here — a caller opts in explicitly by pre-applying it
// (cfg = cfg.HighSpeedLPR.Apply(cfg)) before calling NewRegistry (spec item
// 25: opt-in only, never auto-activated).
func NewRegistry(cfg Config, opts ...RegistryOption) *Registry {
	r := &Registry{
		cfg:     cfg,
		auth:    DenyAllAuthorizer{}, // fail-closed default: no wired Authorizer means no camera is entitled.
		logger:  slog.Default(),
		now:     time.Now,
		cameras: make(map[string]*CameraStatus),
	}
	for _, o := range opts {
		o(r)
	}
	if r.provider == nil {
		r.provider = NoneProvider{}
	}
	r.burstMgr = NewBurstManager(cfg, r.now)
	return r
}

// Submit evaluates one VehicleCandidate end to end: authorization, burst
// admission/dedupe/out-of-order handling, plate-region lookup and crop
// geometry (pixel extraction is a separate step — see ExtractJPEG — this
// method never touches frame bytes).
func (r *Registry) Submit(v VehicleCandidate) SubmitResult {
	if !r.cfg.Enabled {
		return SubmitResult{Reason: ReasonSkipped, Detail: "anpr_disabled"}
	}

	if r.auth == nil || !r.auth.ANPRAllowed(v.CameraKey) {
		r.logger.Info("anpr.edge_candidate_rejected", "camera_key", v.CameraKey, "reason", "unauthorized")
		return SubmitResult{Reason: ReasonRejected, Detail: "unauthorized"}
	}

	if err := ValidateVehicleCandidate(v); err != nil {
		r.logger.Info("anpr.edge_candidate_rejected", "camera_key", v.CameraKey, "reason", "invalid_bbox")
		return SubmitResult{Reason: ReasonRejected, Detail: "invalid_bbox"}
	}

	r.mu.Lock()
	cam, exists := r.cameras[v.CameraKey]
	if !exists {
		if r.cfg.MaxCameras > 0 && len(r.cameras) >= r.cfg.MaxCameras {
			r.mu.Unlock()
			r.logger.Warn("anpr.edge_capacity_exceeded", "camera_key", v.CameraKey, "reason", "max_cameras")
			return SubmitResult{Reason: ReasonDroppedCapacity, Detail: "max_cameras"}
		}
		cam = &CameraStatus{CameraKey: v.CameraKey}
		r.cameras[v.CameraKey] = cam
	}
	r.mu.Unlock()

	burst, candidateID, outcome := r.burstMgr.Process(v)
	if outcome != OutcomeAccepted {
		return r.rejectOutcome(cam, v.CameraKey, outcome)
	}

	if burst.FramesAdded == 1 {
		r.mu.Lock()
		r.metrics.BurstCount++
		r.mu.Unlock()
		r.logger.Info("anpr.edge_burst_started", "camera_key", v.CameraKey, "burst_id", burst.BurstID)
	}
	if burst.State == BurstFull {
		r.logger.Info("anpr.edge_burst_full", "camera_key", v.CameraKey, "burst_id", burst.BurstID)
	}

	pc := PlateCandidate{
		CandidateID:   candidateID,
		CameraKey:     v.CameraKey,
		FrameSeq:      v.FrameSeq,
		Timestamp:     v.Timestamp,
		VehicleBBox:   v.VehicleBBox,
		TrackID:       v.TrackID,
		BurstID:       burst.BurstID,
		CorrelationID: v.CorrelationID,
		SourceWidth:   v.SourceWidth,
		SourceHeight:  v.SourceHeight,
	}

	if plate, err := r.provider.LocatePlate(v, v.SourceWidth, v.SourceHeight); err == nil {
		pc.PlateBBox = &plate
	}

	cropBBox, err := SelectCropBBox(r.cfg.CropPolicy, v.VehicleBBox, pc.PlateBBox)
	if err != nil {
		// Only CropPlateOnly can fail here (no plate bbox); this is the
		// documented fail-closed "provider unavailable" path (spec item 23/36).
		r.recordRejected(cam)
		r.logger.Info("anpr.edge_crop_rejected", "camera_key", v.CameraKey, "reason", "plate_region_unavailable")
		return SubmitResult{Reason: ReasonUnavailable, Detail: "plate_region_unavailable"}
	}

	cropResult, err := ComputeCrop(CropSpec{
		RequestedBBox:   cropBBox,
		Padding:         r.cfg.Padding,
		SourceFrameSeq:  v.FrameSeq,
		SourceTimestamp: v.Timestamp,
		SourceWidth:     v.SourceWidth,
		SourceHeight:    v.SourceHeight,
		EncodingIntent:  "jpeg",
	})
	if err != nil {
		r.recordRejected(cam)
		r.logger.Info("anpr.edge_crop_rejected", "camera_key", v.CameraKey, "reason", err.Error())
		return SubmitResult{Reason: ReasonRejected, Detail: "invalid_crop"}
	}

	r.recordAccepted(cam, r.now())
	r.logger.Info("anpr.edge_candidate_created", "camera_key", v.CameraKey, "candidate_id", candidateID, "burst_id", burst.BurstID)
	r.logger.Info("anpr.edge_crop_created", "camera_key", v.CameraKey, "candidate_id", candidateID)
	return SubmitResult{
		Candidate: &pc,
		Crop:      &cropResult,
		Reason:    ReasonAccepted,
	}
}

func (r *Registry) rejectOutcome(cam *CameraStatus, cameraKey string, outcome BurstOutcome) SubmitResult {
	r.recordRejected(cam)
	switch outcome {
	case OutcomeCapacityBursts, OutcomeCapacityFrames:
		r.mu.Lock()
		r.metrics.CapacityDropped++
		r.mu.Unlock()
		if cam != nil {
			r.mu.Lock()
			cam.CapacityDrops++
			r.mu.Unlock()
		}
		r.logger.Warn("anpr.edge_capacity_exceeded", "camera_key", cameraKey, "reason", string(outcome))
		return SubmitResult{Reason: ReasonDroppedCapacity, Detail: string(outcome)}
	default: // duplicate, stale_out_of_order, invalid_bbox
		r.logger.Info("anpr.edge_candidate_rejected", "camera_key", cameraKey, "reason", string(outcome))
		return SubmitResult{Reason: ReasonRejected, Detail: string(outcome)}
	}
}

func (r *Registry) recordRejected(cam *CameraStatus) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if cam != nil {
		cam.CandidatesRejected++
	}
}

func (r *Registry) recordAccepted(cam *CameraStatus, at time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.metrics.CandidateCount++
	r.metrics.CropCount++
	if cam != nil {
		cam.CandidatesCreated++
		cam.CropsCreated++
		t := at
		cam.LastCandidateAt = &t
	}
}

// Status returns a copy of cameraKey's bounded status block, or nil if this
// registry has never seen that camera.
func (r *Registry) Status(cameraKey string) *CameraStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	cam, ok := r.cameras[cameraKey]
	if !ok {
		return nil
	}
	cam.ActiveBursts = r.burstMgr.ActiveBurstCount(cameraKey)
	out := *cam
	return &out
}

// Metrics returns a copy of the global technical-operation metrics.
func (r *Registry) Metrics() Metrics {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.metrics
}

// Cleanup releases memory for terminal (CLOSED/EXPIRED) bursts and updates
// the BurstExpired metric. Call it periodically (e.g. from the same
// housekeeping loop that already drives other pipeline cleanup) — there is
// no internal timer.
func (r *Registry) Cleanup() {
	before := r.burstMgr.TotalBurstCount()
	r.burstMgr.Cleanup()
	after := r.burstMgr.TotalBurstCount()
	if released := before - after; released > 0 {
		r.mu.Lock()
		r.metrics.BurstExpired += int64(released)
		r.mu.Unlock()
	}
}

// BurstManager exposes the underlying BurstManager for callers that need
// direct access (e.g. explicit Close on an external "event ended" signal).
func (r *Registry) BurstManager() *BurstManager { return r.burstMgr }
