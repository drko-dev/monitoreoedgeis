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
