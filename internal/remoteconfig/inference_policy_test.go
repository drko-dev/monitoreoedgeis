package remoteconfig

import (
	"context"
	"testing"

	"github.com/drko-dev/monitoreoedgeis/internal/config"
	"github.com/drko-dev/monitoreoedgeis/internal/inference"
)

type fakePolicy struct {
	calls  int
	global *float64
	cams   map[string]inference.CameraConfig
}

func (p *fakePolicy) ApplyRemote(global *float64, cams map[string]inference.CameraConfig) error {
	p.calls++
	p.global, p.cams = global, cams
	return nil
}

func TestEdgeModeTargetFPSGoesToInferencePolicy(t *testing.T) {
	adapter, videoMgr, _, _, _ := setupTestRuntime(t, config.ModeEdge)
	policy := &fakePolicy{}
	adapter.inference = policy
	before := videoMgr.Pipeline("cam-front").Sampler().TargetFPS()

	g, c := 7.0, 4.0
	if err := adapter.Apply(context.Background(), RuntimeConfig{TargetFPS: &g, Cameras: map[string]CameraConfig{"cam-front": {TargetFPS: &c}}}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if policy.calls == 0 || policy.global == nil || *policy.global != 7 || policy.cams["cam-front"].TargetFPS != 4 {
		t.Fatalf("policy got calls=%d global=%v cams=%+v", policy.calls, policy.global, policy.cams)
	}
	if got := videoMgr.Pipeline("cam-front").Sampler().TargetFPS(); got != before {
		t.Fatalf("adapter set the sampler directly (%v -> %v); the policy owns it in edge mode", before, got)
	}
	if adapter.CurrentMode() != config.ModeEdge {
		t.Fatalf("CurrentMode = %q", adapter.CurrentMode())
	}
}

func TestCloudModeIgnoresInferencePolicy(t *testing.T) {
	adapter, videoMgr, _, _, _ := setupTestRuntime(t, config.ModeCloud)
	policy := &fakePolicy{}
	adapter.inference = policy
	c := 4.0
	if err := adapter.Apply(context.Background(), RuntimeConfig{Cameras: map[string]CameraConfig{"cam-front": {TargetFPS: &c}}}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if policy.calls != 0 {
		t.Fatal("cloud mode must not use the Full Edge inference policy")
	}
	if got := videoMgr.Pipeline("cam-front").Sampler().TargetFPS(); got != 4 {
		t.Fatalf("cloud sampler = %v, want 4 (unchanged behavior)", got)
	}
}

func TestParseInferenceBlockStrict(t *testing.T) {
	known := []string{"cam-front"}
	ok := `{"cameras":{"cam-front":{"inference":{"mode":"adaptive","idle_fps":2,"active_fps":10,"max_fps":15,"motion_sensitivity":"medium","idle_timeout_s":10,"priority":"normal"}}}}`
	cfg, err := ParseAndValidateJSON([]byte(ok), known)
	if err != nil {
		t.Fatalf("valid inference block rejected: %v", err)
	}
	p := cfg.Cameras["cam-front"].Inference.Policy(nil)
	if p.Mode != inference.ModeAdaptive || p.ActiveFPS != 10 || p.IdleTimeout.Seconds() != 10 {
		t.Fatalf("policy = %+v", p)
	}
	for name, bad := range map[string]string{
		"unknown field":     `{"cameras":{"cam-front":{"inference":{"mode":"adaptive","turbo":true}}}}`,
		"missing mode":      `{"cameras":{"cam-front":{"inference":{"idle_fps":2}}}}`,
		"bad mode":          `{"cameras":{"cam-front":{"inference":{"mode":"auto"}}}}`,
		"idle above active": `{"cameras":{"cam-front":{"inference":{"mode":"adaptive","idle_fps":12,"active_fps":10}}}}`,
		"max out of range":  `{"cameras":{"cam-front":{"inference":{"mode":"adaptive","max_fps":45}}}}`,
		"bad sensitivity":   `{"cameras":{"cam-front":{"inference":{"mode":"adaptive","motion_sensitivity":"max"}}}}`,
		"bad priority":      `{"cameras":{"cam-front":{"inference":{"mode":"adaptive","priority":"urgent"}}}}`,
		"timeout too short": `{"cameras":{"cam-front":{"inference":{"mode":"adaptive","idle_timeout_s":0.5}}}}`,
		"negative fps":      `{"cameras":{"cam-front":{"inference":{"mode":"fixed","idle_fps":-1}}}}`,
	} {
		if _, err := ParseAndValidateJSON([]byte(bad), known); err == nil {
			t.Errorf("%s accepted: %s", name, bad)
		}
	}
	// Backward compatible: no inference block, target_fps only.
	if _, err := ParseAndValidateJSON([]byte(`{"cameras":{"cam-front":{"target_fps":5}}}`), known); err != nil {
		t.Fatalf("legacy config rejected: %v", err)
	}
}

func TestEdgeModeInferenceBlockReachesPolicyAndSurvivesCopy(t *testing.T) {
	adapter, _, _, _, _ := setupTestRuntime(t, config.ModeEdge)
	policy := &fakePolicy{}
	adapter.inference = policy
	fps := 3.0
	cfg := RuntimeConfig{Cameras: map[string]CameraConfig{"cam-front": {TargetFPS: &fps, Inference: &CameraInferenceConfig{Mode: "adaptive", ActiveFPS: 8, Priority: "high"}}}}
	if err := adapter.Apply(context.Background(), cfg); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	got := policy.cams["cam-front"]
	if got.Mode != inference.ModeAdaptive || got.ActiveFPS != 8 || got.Priority != "high" || got.TargetFPS != 3 {
		t.Fatalf("policy got %+v", got)
	}
	if snap := adapter.CurrentConfig(); snap.Cameras["cam-front"].Inference == nil || snap.Cameras["cam-front"].Inference.ActiveFPS != 8 {
		t.Fatalf("inference block lost in config snapshot: %+v", snap.Cameras["cam-front"])
	}
}
