package config

// Profile is the *effective* commercial profile of this Edge: what the agent
// is actually going to do, as opposed to what its configuration asked for.
//
// It exists because ProcessingMode alone does not describe the product. Two
// independent, pre-existing knobs decide the profile:
//
//   - ProcessingMode (GEOCAM_PROCESSING_MODE) selects where inference runs.
//   - VideoPipelineEnabled (GEOCAM_VIDEO_PIPELINE_ENABLED, default false)
//     decides whether the local video pipeline is constructed at all.
//     internal/agent/agent.go only builds the ffmpeg decode stages, the
//     processing.Manager, the Cloud sink and the local-inference Sink inside
//     `if cfg.VideoPipelineEnabled`, and the RTSP manager is the only video
//     subsystem built outside it.
//
// Consequently, with the pipeline disabled, *no* local video work happens —
// no decode, no sampling, no motion gating, no frame upload, no local
// inference — whatever ProcessingMode says. An Edge configured
// GEOCAM_PROCESSING_MODE=hybrid or =edge with the pipeline off still reports
// processing_mode=hybrid/edge on /status, which reads as a false claim about
// the product actually running.
//
// Deriving the profile makes that explicit without introducing a second
// agent, a second daemon, a new mode or a new environment variable: the
// profile is a pure function of the two knobs that already exist, so it can
// never disagree with what the agent builds. It is deliberately *reported*,
// never *enforced*: rejecting ProcessingMode=hybrid with the pipeline off
// would change the meaning of two knobs that have always been independent
// (see internal/config/config_test.go's TestLoadOverrides, which loads
// mode=EDGE without enabling the pipeline and expects success).
type Profile string

const (
	// ProfileGatewayNoMedia is the media-less Gateway: no video pipeline is
	// constructed, so there is no ffmpeg decode, no sampling, no gating, no
	// frame upload and no local inference. Everything that does not need the
	// media path still runs — ONVIF/WS-Discovery, camera inventory, RTSP
	// connectivity and per-camera health, SaaS heartbeat and enrollment,
	// the SaaS control channel, remote configuration and OTA.
	//
	// This is what a default appliance install runs: ProcessingMode=cloud
	// with an unset GEOCAM_VIDEO_PIPELINE_ENABLED.
	ProfileGatewayNoMedia Profile = "gateway-no-media"
	// ProfileGateway is the full commercial Gateway: the lightweight local
	// media path (decode + resize + sample) plus frame upload, with inference
	// left entirely to the Cloud. It is ProcessingMode=cloud with the video
	// pipeline enabled, and it runs no local YOLO.
	ProfileGateway Profile = "gateway"
	// ProfileHybrid adds the local motion evaluator ahead of the upload: only
	// frames classified as motion candidates are dispatched, and the Cloud
	// remains the sole inference engine. It is ProcessingMode=hybrid with the
	// video pipeline enabled.
	ProfileHybrid Profile = "hybrid"
	// ProfileFullEdge runs local inference through the out-of-process Python
	// Vision Worker and does not upload video frames for Cloud inference. It
	// is ProcessingMode=edge with the video pipeline enabled.
	ProfileFullEdge Profile = "full-edge"
	// ProfileUnknown is returned for a ProcessingMode this package does not
	// recognize while the video pipeline is enabled. Load() can never produce
	// it (ParseProcessingMode rejects unknown modes), but the health reporter
	// can be handed a runtime mode string, so the derivation stays total
	// rather than silently claiming one of the real profiles.
	ProfileUnknown Profile = "unknown"
)

// ProfileFor derives the effective commercial profile from the two knobs that
// actually determine the running product. mode is the *requested* processing
// mode; videoPipelineEnabled is the effective value of
// GEOCAM_VIDEO_PIPELINE_ENABLED.
func ProfileFor(mode ProcessingMode, videoPipelineEnabled bool) Profile {
	if !videoPipelineEnabled {
		return ProfileGatewayNoMedia
	}
	switch mode {
	case ModeCloud:
		return ProfileGateway
	case ModeHybrid:
		return ProfileHybrid
	case ModeEdge:
		return ProfileFullEdge
	default:
		return ProfileUnknown
	}
}

// Profile returns the effective commercial profile of this configuration.
func (c *Config) Profile() Profile {
	if c == nil {
		return ProfileGatewayNoMedia
	}
	return ProfileFor(c.ProcessingMode, c.VideoPipelineEnabled)
}

// String renders the profile for logs and /status.
func (p Profile) String() string { return string(p) }

// Honored reports whether the requested ProcessingMode can actually be carried
// out under this configuration.
//
// It is false exactly when the configuration asks for a mode whose defining
// work lives in the local video pipeline, while the pipeline is disabled:
// hybrid asks for local motion gating and edge asks for local inference, and
// neither exists without it. cloud is always honored — "no local YOLO" is
// true with or without the pipeline, the pipeline only decides whether frames
// reach the Cloud at all.
//
// Callers use this to surface the contradiction. Nothing in this package
// rejects it: an operator may legitimately stage a Full Edge appliance before
// provisioning the Vision Worker.
func (c *Config) Honored() bool {
	if c == nil {
		return false
	}
	if c.VideoPipelineEnabled {
		return true
	}
	return c.ProcessingMode == ModeCloud
}
