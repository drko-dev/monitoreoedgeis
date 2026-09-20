package health

import (
	"testing"

	"github.com/drko-dev/monitoreoedgeis/internal/config"
	"github.com/drko-dev/monitoreoedgeis/internal/fulledge"
	"github.com/drko-dev/monitoreoedgeis/internal/identity"
	"github.com/drko-dev/monitoreoedgeis/internal/platform"
	"github.com/drko-dev/monitoreoedgeis/internal/vision"
)

func reporterFor(t *testing.T, mode config.ProcessingMode, videoPipeline bool) *Reporter {
	t.Helper()
	cfg := &config.Config{ProcessingMode: mode, VideoPipelineEnabled: videoPipeline}
	return New("0.1.0-test", cfg,
		identity.Identity{EdgeID: "edge-profile", Status: identity.StatusEnrolled},
		platform.Info{Hostname: "host-profile", GOARCH: "arm64"})
}

// TestSnapshot_ProfileIsTheEffectiveCommercialProfile pins the /status
// `profile` field to the same derivation the agent logs: ProcessingMode is the
// request, the profile is what the Edge will actually do. This is the field
// the commercial capability matrix in docs/product/COMMERCIAL_MODES.md is
// read from, so it must never simply echo the requested mode.
func TestSnapshot_ProfileIsTheEffectiveCommercialProfile(t *testing.T) {
	cases := []struct {
		name          string
		mode          config.ProcessingMode
		videoPipeline bool
		wantProfile   config.Profile
	}{
		{"gateway (cloud + media path)", config.ModeCloud, true, config.ProfileGateway},
		{"gateway without media (shipped default)", config.ModeCloud, false, config.ProfileGatewayNoMedia},
		{"hybrid", config.ModeHybrid, true, config.ProfileHybrid},
		{"full edge", config.ModeEdge, true, config.ProfileFullEdge},
		{"hybrid requested but not deliverable", config.ModeHybrid, false, config.ProfileGatewayNoMedia},
		{"edge requested but not deliverable", config.ModeEdge, false, config.ProfileGatewayNoMedia},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := reporterFor(t, tc.mode, tc.videoPipeline)
			snap := r.Snapshot()

			if snap.ProcessingMode != tc.mode.String() {
				t.Errorf("ProcessingMode = %q, want %q (the request is still reported verbatim)",
					snap.ProcessingMode, tc.mode)
			}
			if snap.Profile != tc.wantProfile {
				t.Errorf("Profile = %q, want %q", snap.Profile, tc.wantProfile)
			}
		})
	}
}

// TestSnapshot_ProfileFollowsRuntimeModeChange covers the remote-config case:
// the profile is derived from the *live* mode, so a runtime transition must
// move it too — otherwise /status would keep advertising the profile the Edge
// booted with.
func TestSnapshot_ProfileFollowsRuntimeModeChange(t *testing.T) {
	r := reporterFor(t, config.ModeCloud, true)
	if got := r.Snapshot().Profile; got != config.ProfileGateway {
		t.Fatalf("initial Profile = %q, want %q", got, config.ProfileGateway)
	}

	r.SetProcessingMode(config.ModeEdge.String())
	if got := r.Snapshot().Profile; got != config.ProfileFullEdge {
		t.Fatalf("after runtime mode change Profile = %q, want %q", got, config.ProfileFullEdge)
	}

	r.SetProcessingMode(config.ModeHybrid.String())
	if got := r.Snapshot().Profile; got != config.ProfileHybrid {
		t.Fatalf("after runtime mode change Profile = %q, want %q", got, config.ProfileHybrid)
	}
}

// TestEdgeVisionReady_EdgeModeWithoutPipelineIsReady is the regression guard
// for a trap that turned a deliberate configuration into a failed upgrade.
//
// /readyz requires state==READY && EdgeVisionReady(). In edge mode the gate
// waited for a vision worker, but internal/agent only ever builds the
// local-inference Sink inside its VideoPipelineEnabled gate — so with edge
// mode and the pipeline disabled no status could ever be recorded, /readyz
// stayed 503 forever while /status reported READY, and the appliance's
// update.sh would roll the release back on its readiness timeout.
func TestEdgeVisionReady_EdgeModeWithoutPipelineIsReady(t *testing.T) {
	r := reporterFor(t, config.ModeEdge, false)
	r.Set(StateReady)

	if !r.EdgeVisionReady() {
		t.Fatal("edge mode with the video pipeline disabled must not gate readiness on a vision worker that cannot exist")
	}
	if snap := r.Snapshot(); snap.Status != StateReady {
		t.Fatalf("Status = %q, want %q", snap.Status, StateReady)
	}
}

// TestEdgeVisionReady_EdgeModeWithPipelineRequiresAReadyWorker is the positive
// control: the gate the fix above narrows must still hold where a worker can
// actually exist.
func TestEdgeVisionReady_EdgeModeWithPipelineRequiresAReadyWorker(t *testing.T) {
	r := reporterFor(t, config.ModeEdge, true)

	if r.EdgeVisionReady() {
		t.Fatal("edge mode with the video pipeline enabled must not be ready before a worker reports ready")
	}

	r.SetVisionStatus(vision.Status{Worker: vision.WorkerStatus{State: vision.StateReady}})
	if !r.EdgeVisionReady() {
		t.Fatal("edge mode must become ready once the vision worker reports ready")
	}

	r.SetVisionStatus(vision.Status{Worker: vision.WorkerStatus{State: vision.StateError}})
	if r.EdgeVisionReady() {
		t.Fatal("edge mode must not be ready while the vision worker is in the error state")
	}
}

// TestEdgeVisionReady_NonEdgeModesAreUnaffected keeps the surrounding contract
// visible: the local-inference gate only ever applies to edge mode.
func TestEdgeVisionReady_NonEdgeModesAreUnaffected(t *testing.T) {
	for _, mode := range []config.ProcessingMode{config.ModeCloud, config.ModeHybrid} {
		r := reporterFor(t, mode, true)
		if !r.EdgeVisionReady() {
			t.Errorf("mode %q must not be gated on the vision worker", mode)
		}
	}
}

func TestSnapshot_FullEdgeStatusFollowsEffectiveProfile(t *testing.T) {
	r := reporterFor(t, config.ModeCloud, true)
	r.SetFullEdgeStatus(fulledge.Status{LocalDetections: 7})

	if snap := r.Snapshot(); snap.Profile != config.ProfileGateway || snap.FullEdge != nil {
		t.Fatalf("cloud snapshot = profile %q full_edge=%+v, want gateway with no full_edge", snap.Profile, snap.FullEdge)
	}

	r.SetProcessingMode(config.ModeHybrid.String())
	if snap := r.Snapshot(); snap.Profile != config.ProfileHybrid || snap.FullEdge != nil {
		t.Fatalf("hybrid snapshot = profile %q full_edge=%+v, want hybrid with no full_edge", snap.Profile, snap.FullEdge)
	}

	r.SetProcessingMode(config.ModeEdge.String())
	if snap := r.Snapshot(); snap.Profile != config.ProfileFullEdge || snap.FullEdge == nil {
		t.Fatalf("edge snapshot = profile %q full_edge=%+v, want full-edge with full_edge status", snap.Profile, snap.FullEdge)
	}

	r.SetProcessingMode(config.ModeCloud.String())
	if snap := r.Snapshot(); snap.Profile != config.ProfileGateway || snap.FullEdge != nil {
		t.Fatalf("cloud-after-transition snapshot = profile %q full_edge=%+v, want gateway with no stale full_edge", snap.Profile, snap.FullEdge)
	}
}

func TestSnapshot_FullEdgeStatusHiddenWhenPipelineDisabled(t *testing.T) {
	r := reporterFor(t, config.ModeEdge, false)
	r.SetFullEdgeStatus(fulledge.Status{LocalDetections: 7})

	snap := r.Snapshot()
	if snap.Profile != config.ProfileGatewayNoMedia {
		t.Fatalf("Profile = %q, want %q", snap.Profile, config.ProfileGatewayNoMedia)
	}
	if snap.FullEdge != nil {
		t.Fatalf("FullEdge = %+v, want nil when effective profile is not Full Edge", snap.FullEdge)
	}
}
