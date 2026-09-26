package agent

import (
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/anpr"
	"github.com/drko-dev/monitoreoedgeis/internal/remoteconfig"
)

// defaultAnprConfig is the production Hito J6 tuning. Bounds chosen
// conservatively (a handful of concurrent bursts/cameras, short TTLs) --
// real per-deployment tuning is a documented future improvement, not
// something this integration invents numbers for beyond a safe default.
func defaultAnprConfig() anpr.Config {
	return anpr.Config{
		Enabled:                  true, // gated per-camera by the Authorizer below, never globally bypassed
		MaxActiveBurstsPerCamera: 4,
		MaxFramesPerBurst:        5,
		MaxCandidateBytes:        2 * 1024 * 1024,
		MaxContextFrames:         5,
		MaxCameras:               64,
		BurstTTL:                 3 * time.Second,
		CropPolicy:               anpr.CropVehicleContext,
		FrameSelection:           anpr.SelectTopNQuality,
		MaxEncodedCropBytes:      2 * 1024 * 1024,
		DedupeCacheSize:          512,
	}
}

// remoteConfigAnprAuthorizer implements anpr.Authorizer by reading the
// per-camera ANPR desired state SaaS already resolved, out of the live
// RuntimeConfig snapshot (item 37/38). Fail-closed: no live config yet, no
// entry for this camera, or Enabled=false all deny -- exactly
// anpr.DenyAllAuthorizer's own default, just backed by a real source
// instead of a hardcoded stub.
type remoteConfigAnprAuthorizer struct {
	current func() remoteconfig.RuntimeConfig
}

func (a *remoteConfigAnprAuthorizer) ANPRAllowed(cameraKey string) bool {
	if a.current == nil {
		return false
	}
	cfg := a.current()
	cam, ok := cfg.Cameras[cameraKey]
	if !ok || cam.ANPR == nil {
		return false
	}
	return cam.ANPR.Enabled
}

// newAnprRegistry builds the process-wide anpr.Registry. currentConfig is
// deferred (called lazily on every ANPRAllowed check, never memoized here)
// so it safely observes a.runtimeApplier even though that field is only
// assigned later in Agent construction -- Submit() is never called before
// the agent finishes bootstrapping and starts receiving real frames.
func newAnprRegistry(currentConfig func() remoteconfig.RuntimeConfig) *anpr.Registry {
	return anpr.NewRegistry(
		defaultAnprConfig(),
		anpr.WithAuthorizer(&remoteConfigAnprAuthorizer{current: currentConfig}),
	)
}
