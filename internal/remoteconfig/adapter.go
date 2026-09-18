package remoteconfig

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/drko-dev/monitoreoedgeis/internal/config"
	"github.com/drko-dev/monitoreoedgeis/internal/processing"
	"github.com/drko-dev/monitoreoedgeis/internal/rtsp"
	"github.com/drko-dev/monitoreoedgeis/internal/vision"
)

// Applier defines the contract for validating, applying, and rolling back remote configuration.
type Applier interface {
	Validate(cfg Config) error
	Apply(ctx context.Context, cfg Config) error
	Rollback(ctx context.Context) error
	CurrentConfig() Config
}

// RuntimeAdapter connects validated remote configuration to the live Edge runtime.
type RuntimeAdapter struct {
	mu           sync.Mutex
	videoManager *processing.Manager
	rtspManager  *rtsp.Manager
	modelManager *vision.ModelManager
	logger       *slog.Logger

	cloudSinkFn         func() processing.Sink
	visionSinkFn        func() (processing.Sink, func(ctx context.Context) error, func(ctx context.Context) error)
	currentVisionStopFn func(ctx context.Context) error
	healthCheck         func(ctx context.Context) error
	onModeChange        func(string)

	currentCfg  Config
	previousCfg Config
	hasPrevious bool

	currentMode config.ProcessingMode
	activeSinks []processing.Sink
}

// Option configures RuntimeAdapter.
type Option func(*RuntimeAdapter)

// WithCloudSinkFactory injects the factory to build/acquire the Cloud video sink.
func WithCloudSinkFactory(fn func() processing.Sink) Option {
	return func(a *RuntimeAdapter) {
		a.cloudSinkFn = fn
	}
}

// WithVisionSinkFactory injects the factory to build/acquire the Vision video sink and worker lifecycle.
func WithVisionSinkFactory(fn func() (processing.Sink, func(ctx context.Context) error, func(ctx context.Context) error)) Option {
	return func(a *RuntimeAdapter) {
		a.visionSinkFn = fn
	}
}

// WithModeChangeCallback registers a callback invoked when processing mode is committed or rolled back.
func WithModeChangeCallback(fn func(mode string)) Option {
	return func(a *RuntimeAdapter) {
		a.onModeChange = fn
	}
}

// WithHealthCheck overrides or complements the default pipeline health check.
func WithHealthCheck(fn func(ctx context.Context) error) Option {
	return func(a *RuntimeAdapter) {
		a.healthCheck = fn
	}
}

// NewRuntimeAdapter creates a new adapter connected to live runtime subsystems.
func NewRuntimeAdapter(
	initialMode config.ProcessingMode,
	videoMgr *processing.Manager,
	rtspMgr *rtsp.Manager,
	modelMgr *vision.ModelManager,
	logger *slog.Logger,
	opts ...Option,
) *RuntimeAdapter {
	if logger == nil {
		logger = slog.Default()
	}
	adapter := &RuntimeAdapter{
		currentMode:  initialMode,
		videoManager: videoMgr,
		rtspManager:  rtspMgr,
		modelManager: modelMgr,
		logger:       logger,
	}
	for _, opt := range opts {
		opt(adapter)
	}

	// Snapshot initial config from live managers
	adapter.currentCfg = adapter.snapshotCurrentConfig()
	return adapter
}

func (a *RuntimeAdapter) snapshotCurrentConfig() Config {
	cfg := Config{
		ProcessingMode: &a.currentMode,
	}
	if a.videoManager != nil {
		vCfg := a.videoManager.Config()
		fps := vCfg.TargetFPS
		w := vCfg.OutputWidth
		h := vCfg.OutputHeight
		cfg.TargetFPS = &fps
		cfg.OutputWidth = &w
		cfg.OutputHeight = &h
		if len(vCfg.Hybrid.ROIs) > 0 {
			rois := make([]config.HybridROI, len(vCfg.Hybrid.ROIs))
			for i, r := range vCfg.Hybrid.ROIs {
				rois[i] = config.HybridROI{XMin: r.XMin, YMin: r.YMin, XMax: r.XMax, YMax: r.YMax}
			}
			cfg.HybridROIs = rois
		}
	}
	if a.modelManager != nil {
		p, v := a.modelManager.Models()
		if p != "" {
			cfg.PersonModel = &p
		}
		if v != "" {
			cfg.VehicleModel = &v
		}
	}
	return cfg
}

func (a *RuntimeAdapter) knownCameras() []string {
	var cameras []string
	if a.rtspManager != nil {
		cameras = a.rtspManager.KnownCameras()
	}
	if a.videoManager != nil {
		active := a.videoManager.ActivePipelines()
		knownMap := make(map[string]bool, len(cameras)+len(active))
		for _, c := range cameras {
			knownMap[c] = true
		}
		for _, c := range active {
			if !knownMap[c] {
				cameras = append(cameras, c)
				knownMap[c] = true
			}
		}
	}
	return cameras
}

// CurrentConfig returns a copy of the currently active configuration.
func (a *RuntimeAdapter) CurrentConfig() Config {
	a.mu.Lock()
	defer a.mu.Unlock()
	return copyConfig(a.currentCfg)
}

// Validate verifies patch merged against current configuration without modifying runtime.
func (a *RuntimeAdapter) Validate(patch Config) error {
	a.mu.Lock()
	effective := MergeConfig(a.currentCfg, patch)
	cameras := a.knownCameras()
	a.mu.Unlock()
	return Validate(effective, cameras)
}

// Apply atomically validates and applies the new remote configuration to the Edge runtime.
// If applying the configuration causes a health check failure, it automatically rolls back
// to the previous working state and returns a descriptive error.
func (a *RuntimeAdapter) Apply(ctx context.Context, patch Config) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	// 1. Calculate effective configuration
	effective := MergeConfig(a.currentCfg, patch)

	// 2. Validate: all or nothing
	if err := Validate(effective, a.knownCameras()); err != nil {
		return fmt.Errorf("remoteconfig: validation failed: %w", err)
	}

	// 3. Save previous state for rollback
	oldCfg := copyConfig(a.currentCfg)

	// 4. Apply changes to runtime
	if err := a.applyState(ctx, effective); err != nil {
		// Failure during transition: rollback
		_ = a.applyState(ctx, oldCfg)
		return fmt.Errorf("remoteconfig: apply transition failed: %w; rolled back", err)
	}

	// 5. Health check
	if err := a.runHealthCheck(ctx); err != nil {
		a.logger.Warn("remoteconfig: health check failed after apply, rolling back", "error", err)
		_ = a.applyState(ctx, oldCfg)
		return fmt.Errorf("remoteconfig: post-apply health check failed: %w; rolled back to previous configuration", err)
	}

	// 6. Commit state
	a.previousCfg = oldCfg
	a.hasPrevious = true
	a.currentCfg = effective
	if effective.ProcessingMode != nil {
		a.currentMode = *effective.ProcessingMode
		if a.onModeChange != nil {
			a.onModeChange(string(a.currentMode))
		}
	}

	a.logger.Info("remoteconfig: successfully applied new configuration")
	return nil
}

// Rollback restores the configuration that was active prior to the last successful Apply.
func (a *RuntimeAdapter) Rollback(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if !a.hasPrevious {
		return fmt.Errorf("remoteconfig: no previous configuration available to rollback")
	}

	targetCfg := copyConfig(a.previousCfg)
	if err := a.applyState(ctx, targetCfg); err != nil {
		return fmt.Errorf("remoteconfig: rollback apply failed: %w", err)
	}

	if err := a.runHealthCheck(ctx); err != nil {
		return fmt.Errorf("remoteconfig: rollback health check failed: %w", err)
	}

	a.currentCfg = targetCfg
	if targetCfg.ProcessingMode != nil {
		a.currentMode = *targetCfg.ProcessingMode
		if a.onModeChange != nil {
			a.onModeChange(string(a.currentMode))
		}
	}
	a.hasPrevious = false

	a.logger.Info("remoteconfig: successfully rolled back configuration")
	return nil
}

// applyState applies cfg to live runtime components. Must be called with a.mu held.
func (a *RuntimeAdapter) applyState(ctx context.Context, cfg Config) error {
	targetMode := a.currentMode
	if cfg.ProcessingMode != nil {
		targetMode = *cfg.ProcessingMode
	}

	// Mode transition: adjust sinks
	if targetMode != a.currentMode {
		if err := a.transitionMode(ctx, a.currentMode, targetMode); err != nil {
			return fmt.Errorf("mode transition %s -> %s: %w", a.currentMode, targetMode, err)
		}
		a.currentMode = targetMode
	}

	// Model updates
	if a.modelManager != nil {
		person, vehicle := a.modelManager.Models()
		if cfg.PersonModel != nil {
			person = *cfg.PersonModel
		}
		if cfg.VehicleModel != nil {
			vehicle = *cfg.VehicleModel
		}
		a.modelManager.SetModels(person, vehicle)
	}

	if a.videoManager == nil {
		return nil
	}

	// Update default manager config
	mgrCfg := a.videoManager.Config()
	mgrCfg.Hybrid.Enabled = (targetMode == config.ModeHybrid)

	if cfg.TargetFPS != nil {
		mgrCfg.TargetFPS = *cfg.TargetFPS
	}
	if cfg.OutputWidth != nil {
		mgrCfg.OutputWidth = *cfg.OutputWidth
	}
	if cfg.OutputHeight != nil {
		mgrCfg.OutputHeight = *cfg.OutputHeight
	}
	if cfg.HybridROIs != nil {
		mgrCfg.Hybrid.ROIs = convertROIs(cfg.HybridROIs)
	}
	a.videoManager.SetConfig(mgrCfg)

	// Hot-reload global FPS on active pipelines
	if cfg.TargetFPS != nil {
		_ = a.videoManager.SetTargetFPS("", *cfg.TargetFPS)
	}

	// Restart active pipelines if resolution or mode or ROIs require it
	for _, candidateKey := range a.videoManager.ActivePipelines() {
		camPipe := a.videoManager.Pipeline(candidateKey)
		if camPipe == nil {
			continue
		}

		camCfg := camPipe.Config()
		needsRestart := false

		// Check mode change
		if camCfg.Hybrid.Enabled != mgrCfg.Hybrid.Enabled {
			camCfg.Hybrid.Enabled = mgrCfg.Hybrid.Enabled
			needsRestart = true
		}

		// Global resolution change
		if cfg.OutputWidth != nil && (camCfg.OutputWidth != *cfg.OutputWidth || camCfg.OutputHeight != *cfg.OutputHeight) {
			camCfg.OutputWidth = *cfg.OutputWidth
			camCfg.OutputHeight = *cfg.OutputHeight
			needsRestart = true
		}

		// Global ROIs change
		if cfg.HybridROIs != nil {
			camCfg.Hybrid.ROIs = convertROIs(cfg.HybridROIs)
			needsRestart = true
		}

		// Per-camera overrides
		if camOverride, ok := cfg.Cameras[candidateKey]; ok {
			if camOverride.TargetFPS != nil {
				camPipe.SetTargetFPS(*camOverride.TargetFPS)
			}
			if camOverride.OutputWidth != nil && (camCfg.OutputWidth != *camOverride.OutputWidth || camCfg.OutputHeight != *camOverride.OutputHeight) {
				camCfg.OutputWidth = *camOverride.OutputWidth
				camCfg.OutputHeight = *camOverride.OutputHeight
				needsRestart = true
			}
			if camOverride.HybridROIs != nil {
				camCfg.Hybrid.ROIs = convertROIs(camOverride.HybridROIs)
				needsRestart = true
			}
		}

		if needsRestart {
			if err := a.videoManager.RestartCameraPipeline(ctx, candidateKey, camCfg); err != nil {
				return fmt.Errorf("camera %q pipeline restart failed: %w", candidateKey, err)
			}
		}
	}

	return nil
}

func (a *RuntimeAdapter) transitionMode(ctx context.Context, from, to config.ProcessingMode) error {
	a.logger.Info("remoteconfig: transitioning processing mode", "from", from, "to", to)

	if a.videoManager == nil {
		return nil
	}

	switch to {
	case config.ModeCloud, config.ModeHybrid:
		// Stop running vision worker if any
		if a.currentVisionStopFn != nil {
			_ = a.currentVisionStopFn(ctx)
			a.currentVisionStopFn = nil
		}
		// Sinks: CloudSink active, VisionSink stopped
		var cloudSink processing.Sink
		if a.cloudSinkFn != nil {
			cloudSink = a.cloudSinkFn()
		}
		if cloudSink != nil {
			a.videoManager.UpdateRouterSinks(cloudSink)
		} else {
			a.videoManager.UpdateRouterSinks()
		}

	case config.ModeEdge:
		// Sinks: VisionSink active, CloudSink stopped
		var visionSink processing.Sink
		if a.visionSinkFn != nil {
			sink, startFn, stopFn := a.visionSinkFn()
			if startFn != nil {
				if err := startFn(ctx); err != nil {
					return fmt.Errorf("start vision worker: %w", err)
				}
			}
			a.currentVisionStopFn = stopFn
			visionSink = sink
		}
		if visionSink != nil {
			a.videoManager.UpdateRouterSinks(visionSink)
		} else {
			a.videoManager.UpdateRouterSinks()
		}
	}

	return nil
}

func (a *RuntimeAdapter) runHealthCheck(ctx context.Context) error {
	if a.healthCheck != nil {
		if err := a.healthCheck(ctx); err != nil {
			return err
		}
	}

	if a.videoManager != nil {
		for _, key := range a.videoManager.ActivePipelines() {
			p := a.videoManager.Pipeline(key)
			if p != nil && p.State() == "error" {
				return fmt.Errorf("camera pipeline %q is in error state", key)
			}
		}
	}
	return nil
}

func convertROIs(rois []config.HybridROI) []processing.ROI {
	out := make([]processing.ROI, len(rois))
	for i, r := range rois {
		out[i] = processing.ROI{XMin: r.XMin, YMin: r.YMin, XMax: r.XMax, YMax: r.YMax}
	}
	return out
}

func copyConfig(c Config) Config {
	out := Config{}
	if c.ProcessingMode != nil {
		m := *c.ProcessingMode
		out.ProcessingMode = &m
	}
	if c.TargetFPS != nil {
		fps := *c.TargetFPS
		out.TargetFPS = &fps
	}
	if c.OutputWidth != nil {
		w := *c.OutputWidth
		out.OutputWidth = &w
	}
	if c.OutputHeight != nil {
		h := *c.OutputHeight
		out.OutputHeight = &h
	}
	if c.HybridROIs != nil {
		out.HybridROIs = make([]config.HybridROI, len(c.HybridROIs))
		copy(out.HybridROIs, c.HybridROIs)
	}
	if c.PersonModel != nil {
		p := *c.PersonModel
		out.PersonModel = &p
	}
	if c.VehicleModel != nil {
		v := *c.VehicleModel
		out.VehicleModel = &v
	}
	if c.Cameras != nil {
		out.Cameras = make(map[string]CameraConfig, len(c.Cameras))
		for k, v := range c.Cameras {
			cam := CameraConfig{}
			if v.TargetFPS != nil {
				f := *v.TargetFPS
				cam.TargetFPS = &f
			}
			if v.OutputWidth != nil {
				w := *v.OutputWidth
				cam.OutputWidth = &w
			}
			if v.OutputHeight != nil {
				h := *v.OutputHeight
				cam.OutputHeight = &h
			}
			if v.HybridROIs != nil {
				cam.HybridROIs = make([]config.HybridROI, len(v.HybridROIs))
				copy(cam.HybridROIs, v.HybridROIs)
			}
			out.Cameras[k] = cam
		}
	}
	return out
}

// MergeConfig creates an effective configuration by overlaying patch onto base.
// Non-nil scalar fields and non-nil slices in patch overwrite base.
// For Cameras map, camera overrides are merged per-camera key.
func MergeConfig(base, patch Config) Config {
	res := copyConfig(base)

	if patch.ProcessingMode != nil {
		m := *patch.ProcessingMode
		res.ProcessingMode = &m
	}
	if patch.TargetFPS != nil {
		fps := *patch.TargetFPS
		res.TargetFPS = &fps
	}
	if patch.OutputWidth != nil {
		w := *patch.OutputWidth
		res.OutputWidth = &w
	}
	if patch.OutputHeight != nil {
		h := *patch.OutputHeight
		res.OutputHeight = &h
	}
	if patch.HybridROIs != nil {
		res.HybridROIs = make([]config.HybridROI, len(patch.HybridROIs))
		copy(res.HybridROIs, patch.HybridROIs)
	}
	if patch.PersonModel != nil {
		p := *patch.PersonModel
		res.PersonModel = &p
	}
	if patch.VehicleModel != nil {
		v := *patch.VehicleModel
		res.VehicleModel = &v
	}
	if patch.Cameras != nil {
		if res.Cameras == nil {
			res.Cameras = make(map[string]CameraConfig, len(patch.Cameras))
		}
		for camKey, patchCam := range patch.Cameras {
			baseCam, exists := res.Cameras[camKey]
			if !exists {
				baseCam = CameraConfig{}
			}
			if patchCam.TargetFPS != nil {
				f := *patchCam.TargetFPS
				baseCam.TargetFPS = &f
			}
			if patchCam.OutputWidth != nil {
				w := *patchCam.OutputWidth
				baseCam.OutputWidth = &w
			}
			if patchCam.OutputHeight != nil {
				h := *patchCam.OutputHeight
				baseCam.OutputHeight = &h
			}
			if patchCam.HybridROIs != nil {
				baseCam.HybridROIs = make([]config.HybridROI, len(patchCam.HybridROIs))
				copy(baseCam.HybridROIs, patchCam.HybridROIs)
			}
			res.Cameras[camKey] = baseCam
		}
	}

	return res
}
