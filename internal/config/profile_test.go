package config

import "testing"

// TestProfileFor pins the commercial profile derivation to the two knobs that
// actually determine the running product. This table is the config-level
// source of truth for the capability matrix in
// docs/product/COMMERCIAL_MODES.md — if a profile mapping changes here, that
// document's matrix is wrong and must change with it.
func TestProfileFor(t *testing.T) {
	cases := []struct {
		name         string
		mode         ProcessingMode
		pipeline     bool
		want         Profile
		whyItMatters string
	}{
		{
			name: "cloud without pipeline is the media-less gateway",
			mode: ModeCloud, pipeline: false, want: ProfileGatewayNoMedia,
			whyItMatters: "the shipped appliance default: discovery, inventory, camera health, RTSP, control and OTA, but no frames ever reach the Cloud",
		},
		{
			name: "cloud with pipeline is the commercial Gateway",
			mode: ModeCloud, pipeline: true, want: ProfileGateway,
			whyItMatters: "Z8: the commercial Gateway maps onto the code's existing cloud mode, so no fourth processing mode is needed",
		},
		{
			name: "hybrid with pipeline",
			mode: ModeHybrid, pipeline: true, want: ProfileHybrid,
			whyItMatters: "Z9: local motion gating only; Cloud stays the sole inference engine",
		},
		{
			name: "edge with pipeline",
			mode: ModeEdge, pipeline: true, want: ProfileFullEdge,
			whyItMatters: "Z10: local YOLO through the out-of-process Python Vision Worker",
		},
		{
			name: "hybrid without pipeline does not claim hybrid",
			mode: ModeHybrid, pipeline: false, want: ProfileGatewayNoMedia,
			whyItMatters: "no pipeline means no MotionDetector, so reporting hybrid would be a false claim about the product",
		},
		{
			name: "edge without pipeline does not claim full edge",
			mode: ModeEdge, pipeline: false, want: ProfileGatewayNoMedia,
			whyItMatters: "no pipeline means no local inference at all, so reporting full-edge would be a false claim",
		},
		{
			name: "unrecognized mode with pipeline is unknown, not a real profile",
			mode: ProcessingMode("nonsense"), pipeline: true, want: ProfileUnknown,
			whyItMatters: "the derivation must stay total without inventing a real profile for a mode it does not understand",
		},
		{
			name: "unrecognized mode without pipeline is still media-less",
			mode: ProcessingMode("nonsense"), pipeline: false, want: ProfileGatewayNoMedia,
			whyItMatters: "the pipeline flag decides the media path, so an unknown mode cannot make one appear",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ProfileFor(tc.mode, tc.pipeline)
			if got != tc.want {
				t.Fatalf("ProfileFor(%q, %v) = %q, want %q (%s)",
					tc.mode, tc.pipeline, got, tc.want, tc.whyItMatters)
			}
		})
	}
}

// TestProfileFor_NeverClaimsALocalStageItCannotRun is the regression guard for
// the defect this derivation exists to close: before it, an Edge configured
// GEOCAM_PROCESSING_MODE=hybrid or =edge with GEOCAM_VIDEO_PIPELINE_ENABLED
// left at its false default still reported processing_mode=hybrid/edge on
// /status while building no local video pipeline whatsoever.
//
// It asserts the property, not the implementation: a profile that promises a
// local stage (gating or inference) may only be reported when the video
// pipeline is actually enabled.
func TestProfileFor_NeverClaimsALocalStageItCannotRun(t *testing.T) {
	localStageProfiles := map[Profile]string{
		ProfileHybrid:   "local motion gating",
		ProfileFullEdge: "local YOLO inference",
	}
	for _, mode := range []ProcessingMode{ModeCloud, ModeHybrid, ModeEdge} {
		for _, pipeline := range []bool{true, false} {
			profile := ProfileFor(mode, pipeline)
			stage, promisesLocalStage := localStageProfiles[profile]
			if promisesLocalStage && !pipeline {
				t.Errorf("ProfileFor(%q, false) = %q, which promises %s, but the video pipeline is disabled so it cannot run",
					mode, profile, stage)
			}
		}
	}
}

// TestConfigProfileAndHonored covers the Config-level wrappers, including the
// nil-receiver case: health.Snapshot may be built from a Reporter that was
// constructed without a Config, and it must not panic there.
func TestConfigProfileAndHonored(t *testing.T) {
	var nilCfg *Config
	if got := nilCfg.Profile(); got != ProfileGatewayNoMedia {
		t.Errorf("nil Config Profile() = %q, want %q", got, ProfileGatewayNoMedia)
	}
	if nilCfg.Honored() {
		t.Error("nil Config Honored() = true, want false")
	}

	cases := []struct {
		name     string
		mode     ProcessingMode
		pipeline bool
		want     Profile
		honored  bool
	}{
		{"cloud, no pipeline", ModeCloud, false, ProfileGatewayNoMedia, true},
		{"cloud, pipeline", ModeCloud, true, ProfileGateway, true},
		{"hybrid, pipeline", ModeHybrid, true, ProfileHybrid, true},
		{"edge, pipeline", ModeEdge, true, ProfileFullEdge, true},
		{"hybrid, no pipeline", ModeHybrid, false, ProfileGatewayNoMedia, false},
		{"edge, no pipeline", ModeEdge, false, ProfileGatewayNoMedia, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{ProcessingMode: tc.mode, VideoPipelineEnabled: tc.pipeline}
			if got := cfg.Profile(); got != tc.want {
				t.Errorf("Profile() = %q, want %q", got, tc.want)
			}
			if got := cfg.Honored(); got != tc.honored {
				t.Errorf("Honored() = %v, want %v", got, tc.honored)
			}
		})
	}
}

// TestProfileFor_CloudWithoutPipelineIsStillHonored documents the one
// asymmetry in Honored(): disabling the pipeline does not contradict
// ProcessingMode=cloud, because cloud mode promises "no local YOLO" and that
// remains true. The pipeline knob governs whether frames reach the Cloud, not
// whether the requested mode is coherent — which is exactly why the
// media-less Gateway is reported as its own profile instead of as an error.
func TestProfileFor_CloudWithoutPipelineIsStillHonored(t *testing.T) {
	cfg := &Config{ProcessingMode: ModeCloud, VideoPipelineEnabled: false}
	if !cfg.Honored() {
		t.Fatal("cloud without the video pipeline must be honored: cloud mode never promises a local stage")
	}
	if cfg.Profile() == ProfileGateway {
		t.Fatal("cloud without the video pipeline must not report the media-path Gateway profile")
	}
}
