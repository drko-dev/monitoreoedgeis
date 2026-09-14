package main

import (
	"strings"
	"testing"

	"github.com/drko-dev/monitoreoedgeis/internal/config"
	"github.com/drko-dev/monitoreoedgeis/internal/health"
	"github.com/drko-dev/monitoreoedgeis/internal/identity"
	"github.com/drko-dev/monitoreoedgeis/internal/platform"
)

func TestIdentityReportNoSecrets(t *testing.T) {
	ident := identity.Identity{EdgeID: "11111111-1111-4111-8111-111111111111", Source: identity.SourcePersisted}
	cfg := &config.Config{ProcessingMode: config.ModeCloud, DataDir: "/var/lib/geocam-edge"}
	host := platform.Info{GOARCH: "arm64"}

	report := identityReport(ident, cfg, host)

	for _, want := range []string{ident.EdgeID, string(ident.Source), "arm64", string(cfg.ProcessingMode), cfg.DataDir} {
		if !strings.Contains(report, want) {
			t.Errorf("report missing %q:\n%s", want, report)
		}
	}
}

func TestCheckReportReady(t *testing.T) {
	snap := health.Snapshot{Status: health.StateReady, EdgeID: "edge-1", Version: "0.1.0"}

	report, ready := checkReport(snap)

	if !ready {
		t.Error("ready = false, want true for StateReady")
	}
	if !strings.Contains(report, "READY") {
		t.Errorf("report missing status:\n%s", report)
	}
}

func TestCheckReportNotReady(t *testing.T) {
	for _, s := range []health.State{health.StateStarting, health.StateDegraded, health.StateStopping} {
		_, ready := checkReport(health.Snapshot{Status: s})
		if ready {
			t.Errorf("ready = true for state %q, want false", s)
		}
	}
}
