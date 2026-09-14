package health

import (
	"testing"

	"github.com/drko-dev/monitoreoedgeis/internal/config"
	"github.com/drko-dev/monitoreoedgeis/internal/identity"
	"github.com/drko-dev/monitoreoedgeis/internal/platform"
)

func TestReporterStartsInStarting(t *testing.T) {
	if got := newTestReporter().State(); got != StateStarting {
		t.Errorf("State() = %q, want %q", got, StateStarting)
	}
}

func TestReporterTransitions(t *testing.T) {
	r := newTestReporter()
	for _, want := range []State{StateReady, StateDegraded, StateStopping} {
		r.Set(want)
		if got := r.State(); got != want {
			t.Errorf("State() = %q, want %q", got, want)
		}
	}
}

func TestSnapshot(t *testing.T) {
	cfg := &config.Config{ProcessingMode: config.ModeHybrid, LogLevel: "info"}
	r := New("0.1.0", cfg, identity.Identity{EdgeID: "edge-42", Status: identity.StatusEnrolled},
		platform.Info{Hostname: "test-host", OS: "linux", GOARCH: "arm64"})
	r.Set(StateReady)

	got := r.Snapshot()
	if got.Status != StateReady {
		t.Errorf("Status = %q, want %q", got.Status, StateReady)
	}
	if got.Version != "0.1.0" {
		t.Errorf("Version = %q, want %q", got.Version, "0.1.0")
	}
	if got.EdgeID != "edge-42" {
		t.Errorf("EdgeID = %q, want %q", got.EdgeID, "edge-42")
	}
	if got.EnrollmentStatus != string(identity.StatusEnrolled) {
		t.Errorf("EnrollmentStatus = %q, want %q", got.EnrollmentStatus, identity.StatusEnrolled)
	}
	if got.Hostname != "test-host" || got.OS != "linux" || got.Architecture != "arm64" {
		t.Errorf("platform fields = %+v", got)
	}
	if got.ProcessingMode != string(config.ModeHybrid) {
		t.Errorf("ProcessingMode = %q, want %q", got.ProcessingMode, config.ModeHybrid)
	}
	if got.UptimeSeconds < 0 {
		t.Error("UptimeSeconds is negative")
	}
	if got.Modules == nil {
		t.Error("Modules is nil, want an (empty) map")
	}
}

func TestSnapshotUnenrolled(t *testing.T) {
	cfg := &config.Config{ProcessingMode: config.ModeCloud}
	r := New("0.1.0", cfg, identity.Identity{}, platform.Info{})

	snap := r.Snapshot()
	if snap.EnrollmentStatus != "" {
		t.Errorf("EnrollmentStatus = %q, want empty", snap.EnrollmentStatus)
	}
	if snap.EdgeID != "" {
		t.Errorf("EdgeID = %q, want empty", snap.EdgeID)
	}
}

func TestReporterCredentialStatus(t *testing.T) {
	r := newTestReporter()
	r.SetCredentialStatus("ENROLLED")

	if got := r.Snapshot().CredentialStatus; got != "ENROLLED" {
		t.Errorf("CredentialStatus = %q, want %q", got, "ENROLLED")
	}
}

func TestReporterModuleStates(t *testing.T) {
	r := newTestReporter()
	r.SetModuleState("health-http", "starting")
	r.SetModuleState("health-http", "running")
	r.SetModuleState("other", "stopped")

	snap := r.Snapshot()
	if snap.Modules["health-http"] != "running" {
		t.Errorf("Modules[health-http] = %q, want %q", snap.Modules["health-http"], "running")
	}
	if snap.Modules["other"] != "stopped" {
		t.Errorf("Modules[other] = %q, want %q", snap.Modules["other"], "stopped")
	}
}
