package agent

import (
	"log/slog"
	"testing"

	"github.com/drko-dev/monitoreoedgeis/internal/config"
	"github.com/drko-dev/monitoreoedgeis/internal/credentials"
	"github.com/drko-dev/monitoreoedgeis/internal/health"
	"github.com/drko-dev/monitoreoedgeis/internal/identity"
	"github.com/drko-dev/monitoreoedgeis/internal/platform"
)

// TestNewVisionSink_OnlyEdgeMode covers K1: the local-inference Sink/Module
// are only ever constructed for ProcessingMode=edge — cloud and hybrid
// build nothing vision-related at all, not even an idle worker.
func TestNewVisionSink_OnlyEdgeMode(t *testing.T) {
	log := slog.New(slog.DiscardHandler)
	for _, mode := range []config.ProcessingMode{config.ModeCloud, config.ModeHybrid} {
		cfg := testConfig(t)
		cfg.ProcessingMode = mode
		reporter := health.New("test", cfg, identity.Identity{}, platform.Info{})
		sink, mod := newVisionSink(cfg, reporter, nil, log)
		if sink != nil || mod != nil {
			t.Fatalf("mode %s: newVisionSink returned non-nil, want nil (edge-only)", mode)
		}
	}
}

// TestNewVisionSink_EdgeModeBuildsWorker covers the flip side: ModeEdge
// always builds a Sink+Module, even with no GEOCAM_EDGE_YOLO_WORKER_CMD —
// it reports NOT_READY/not_configured (K3) rather than being absent, so
// /status still shows edge mode's real state.
func TestNewVisionSink_EdgeModeBuildsWorker(t *testing.T) {
	log := slog.New(slog.DiscardHandler)
	cfg := testConfig(t)
	cfg.ProcessingMode = config.ModeEdge
	reporter := health.New("test", cfg, identity.Identity{}, platform.Info{})
	sink, mod := newVisionSink(cfg, reporter, nil, log)
	if sink == nil || mod == nil {
		t.Fatal("newVisionSink returned nil in edge mode")
	}
	if mod.Name() != "edge-vision" {
		t.Fatalf("module name = %q, want %q", mod.Name(), "edge-vision")
	}
}

// TestNewCloudSink_NeverBuildsInEdgeMode covers "Edge no usa Cloud inference
// path": newCloudSink must return nil for ModeEdge, so edge mode's Router
// never even has a CloudSink registered to accidentally send frames to.
func TestNewCloudSink_NeverBuildsInEdgeMode(t *testing.T) {
	log := slog.New(slog.DiscardHandler)
	cfg := testConfig(t)
	cfg.ProcessingMode = config.ModeEdge
	cfg.SaaSURL = "https://example.invalid"
	reporter := health.New("test", cfg, identity.Identity{}, platform.Info{})
	creds := credentials.Credentials{DeviceID: "d1", Credential: "c1", Status: credentials.StatusEnrolled}
	if cs := newCloudSink(cfg, creds, reporter, log); cs != nil {
		t.Fatal("newCloudSink returned non-nil for ModeEdge")
	}
}
