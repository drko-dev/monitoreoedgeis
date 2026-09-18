package remoteconfig

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/config"
	"github.com/drko-dev/monitoreoedgeis/internal/processing"
	"github.com/drko-dev/monitoreoedgeis/internal/rtsp"
	"github.com/drko-dev/monitoreoedgeis/internal/vision"
)

// NoopRuntimeAdapter is a safe, do-nothing Adapter: it accepts any
// well-formed Config and never touches real runtime state. It lets the
// engine run in tests where no real runtime knobs are wired.
type NoopRuntimeAdapter struct{}

func (NoopRuntimeAdapter) ValidateRuntimeConfig(context.Context, Config) error { return nil }
func (NoopRuntimeAdapter) ApplyRuntimeConfig(context.Context, Config) error    { return nil }
func (NoopRuntimeAdapter) RollbackRuntimeConfig(context.Context, Config) error { return nil }

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
	startTimeout        time.Duration

	initialMode config.ProcessingMode
	initialCfg  RuntimeConfig
	currentCfg  RuntimeConfig
	previousCfg RuntimeConfig
	hasPrevious bool

	currentMode config.ProcessingMode
	activeSinks []processing.Sink
}

// AdapterOption configures RuntimeAdapter.
type AdapterOption func(*RuntimeAdapter)

// WithCloudSinkFactory injects the factory to build/acquire the Cloud video sink.
func WithCloudSinkFactory(fn func() processing.Sink) AdapterOption {
	return func(a *RuntimeAdapter) {
		a.cloudSinkFn = fn
	}
}

// WithVisionSinkFactory injects the factory to build/acquire the Vision video sink and worker lifecycle.
func WithVisionSinkFactory(fn func() (processing.Sink, func(ctx context.Context) error, func(ctx context.Context) error)) AdapterOption {
	return func(a *RuntimeAdapter) {
		a.visionSinkFn = fn
	}
}

// WithInitialVisionStop sets the stop function for an initial vision worker running at startup.
func WithInitialVisionStop(fn func(ctx context.Context) error) AdapterOption {
	return func(a *RuntimeAdapter) {
		a.currentVisionStopFn = fn
	}
}

// WithStartTimeout configures the timeout when waiting for vision worker ready state during mode transition.
func WithStartTimeout(d time.Duration) AdapterOption {
	return func(a *RuntimeAdapter) {
		a.startTimeout = d
	}
}

// WithModeChangeCallback registers a callback invoked when processing mode is committed or rolled back.
func WithModeChangeCallback(fn func(mode string)) AdapterOption {
	return func(a *RuntimeAdapter) {
		a.onModeChange = fn
	}
}

// WithHealthCheck overrides or complements the default pipeline health check.
func WithHealthCheck(fn func(ctx context.Context) error) AdapterOption {
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
	opts ...AdapterOption,
) *RuntimeAdapter {
	if logger == nil {
		logger = slog.Default()
	}
	adapter := &RuntimeAdapter{
		initialMode:  initialMode,
		currentMode:  initialMode,
		videoManager: videoMgr,
		rtspManager:  rtspMgr,
		modelManager: modelMgr,
		logger:       logger,
	}
	for _, opt := range opts {
		opt(adapter)
	}

	adapter.initialCfg = adapter.snapshotCurrentConfig()
	adapter.currentCfg = copyConfig(adapter.initialCfg)
	return adapter
}

func (a *RuntimeAdapter) snapshotCurrentConfig() RuntimeConfig {
	cfg := RuntimeConfig{
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
func (a *RuntimeAdapter) CurrentConfig() RuntimeConfig {
	a.mu.Lock()
	defer a.mu.Unlock()
	return copyConfig(a.currentCfg)
}

// Validate verifies patch merged against current configuration without modifying runtime.
func (a *RuntimeAdapter) Validate(patch RuntimeConfig) error {
	a.mu.Lock()
	effective := MergeConfig(a.currentCfg, patch)
	cameras := a.knownCameras()
	a.mu.Unlock()
	return Validate(effective, cameras)
}

// Apply atomically validates and applies the new remote configuration to the Edge runtime.
// It returns an error without internal rollback, leaving rollback authority to Engine
// (or explicit Rollback calls in standalone testing).
func (a *RuntimeAdapter) Apply(ctx context.Context, patch RuntimeConfig) error {
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
	a.previousCfg = oldCfg

	// 4. Apply changes to runtime
	if err := a.applyState(ctx, effective); err != nil {
		return fmt.Errorf("remoteconfig: apply transition failed: %w", err)
	}

	// 5. Health check
	if err := a.runHealthCheck(ctx); err != nil {
		a.logger.Warn("remoteconfig: health check failed after apply", "error", err)
		return fmt.Errorf("remoteconfig: post-apply health check failed: %w", err)
	}

	// 6. Commit state
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

// Rollback restores the configuration that was active prior to the last Apply attempt.
func (a *RuntimeAdapter) Rollback(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	targetCfg := a.previousCfg
	if !a.hasPrevious && targetCfg.ProcessingMode == nil {
		targetCfg = a.initialCfg
	}

	if err := a.applyState(ctx, targetCfg); err != nil {
		return fmt.Errorf("remoteconfig: rollback apply failed: %w", err)
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

// ValidateRuntimeConfig implements Adapter for Engine.
func (a *RuntimeAdapter) ValidateRuntimeConfig(ctx context.Context, cfg Config) error {
	parsed, err := ParseAndValidateJSON(cfg.Payload, a.knownCameras())
	if err != nil {
		return err
	}
	return a.Validate(parsed)
}

// ApplyRuntimeConfig implements Adapter for Engine.
func (a *RuntimeAdapter) ApplyRuntimeConfig(ctx context.Context, cfg Config) error {
	parsed, err := ParseAndValidateJSON(cfg.Payload, a.knownCameras())
	if err != nil {
		return err
	}
	return a.Apply(ctx, parsed)
}

// RollbackRuntimeConfig implements Adapter for Engine.
func (a *RuntimeAdapter) RollbackRuntimeConfig(ctx context.Context, previous Config) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	var target RuntimeConfig
	if len(previous.Payload) > 0 && previous.Version > 0 {
		var err error
		target, err = ParseAndValidateJSON(previous.Payload, a.knownCameras())
		if err != nil {
			target = copyConfig(a.initialCfg)
		}
	} else {
		target = copyConfig(a.initialCfg)
	}

	if err := a.applyState(ctx, target); err != nil {
		return fmt.Errorf("remoteconfig: rollback apply failed: %w", err)
	}

	a.currentCfg = target
	if target.ProcessingMode != nil {
		a.currentMode = *target.ProcessingMode
		if a.onModeChange != nil {
			a.onModeChange(string(a.currentMode))
		}
	}
	a.hasPrevious = false

	a.logger.Info("remoteconfig: successfully restored configuration on rollback")
	return nil
}

// applyState applies cfg to live runtime components. Must be called with a.mu held.
func (a *RuntimeAdapter) applyState(ctx context.Context, cfg RuntimeConfig) error {
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
				_ = a.videoManager.SetTargetFPS(candidateKey, *camOverride.TargetFPS)
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

	var newSinks []processing.Sink
	var newVisionStop func(ctx context.Context) error

	switch to {
	case config.ModeCloud, config.ModeHybrid:
		if a.cloudSinkFn != nil {
			cs := a.cloudSinkFn()
			if cs == nil {
				return fmt.Errorf("cloud sink factory returned nil; target mode %s is not operational", to)
			}
			newSinks = append(newSinks, cs)
		} else {
			return fmt.Errorf("cloud sink factory not configured; target mode %s is not operational", to)
		}

	case config.ModeEdge:
		if a.visionSinkFn != nil {
			vs, startFn, stopFn := a.visionSinkFn()
			if vs == nil || startFn == nil || stopFn == nil {
				return fmt.Errorf("vision sink factory returned incomplete components; target mode edge is not operational")
			}
			// Start vision worker and wait for ready state
			if err := startFn(ctx); err != nil {
				return fmt.Errorf("start vision worker: %w", err)
			}
			timeout := a.startTimeout
			if timeout <= 0 {
				timeout = 5 * time.Second
			}
			waitCtx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()

			ready := false
			if r, ok := vs.(interface{ WaitForReady(context.Context) error }); ok {
				if err := r.WaitForReady(waitCtx); err != nil {
					_ = stopFn(ctx)
					return fmt.Errorf("vision worker not ready: %w", err)
				}
				ready = true
			} else if r, ok := vs.(interface{ Ready() bool }); ok {
				ticker := time.NewTicker(50 * time.Millisecond)
				defer ticker.Stop()
			loop:
				for {
					if r.Ready() {
						ready = true
						break loop
					}
					select {
					case <-waitCtx.Done():
						break loop
					case <-ticker.C:
					}
				}
			} else {
				ready = true
			}

			if !ready {
				_ = stopFn(ctx)
				return fmt.Errorf("vision worker failed to achieve ready state within %v; target mode edge is not operational", timeout)
			}

			newSinks = append(newSinks, vs)
			newVisionStop = stopFn
		} else {
			return fmt.Errorf("vision sink factory not configured; target mode edge is not operational")
		}

	default:
		return fmt.Errorf("unsupported target processing mode: %s", to)
	}

	// Target verified operational: safely stop previous vision worker if leaving edge mode
	if from == config.ModeEdge && a.currentVisionStopFn != nil {
		if err := a.currentVisionStopFn(ctx); err != nil {
			a.logger.Warn("remoteconfig: error stopping previous vision worker", "error", err)
		}
		a.currentVisionStopFn = nil
	}

	// Update active sinks on the video router
	if a.videoManager != nil {
		a.videoManager.UpdateRouterSinks(newSinks...)
	}
	a.activeSinks = newSinks
	if newVisionStop != nil {
		a.currentVisionStopFn = newVisionStop
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

func copyConfig(c RuntimeConfig) RuntimeConfig {
	out := RuntimeConfig{}
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
func MergeConfig(base, patch RuntimeConfig) RuntimeConfig {
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
		for k, v := range patch.Cameras {
			res.Cameras[k] = v
		}
	}

	return res
}
