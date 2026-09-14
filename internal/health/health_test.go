package health

import (
	"testing"

	"github.com/drko-dev/monitoreoedgeis/internal/config"
	"github.com/drko-dev/monitoreoedgeis/internal/identity"
	"github.com/drko-dev/monitoreoedgeis/internal/platform"
)

func newTestReporter() *Reporter {
	cfg := &config.Config{ProcessingMode: config.ModeHybrid, LogLevel: "info"}
	return New("0.1.0", cfg, identity.New("edge-42"), platform.Info{
		Hostname: "test-host",
		OS:       "linux",
		GOARCH:   "arm64",
	})
}

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
	r := newTestReporter()
	r.Set(StateReady)

	got := r.Snapshot()
	want := Snapshot{
		Status:           StateReady,
		Version:          "0.1.0",
		EdgeID:           "edge-42",
		EnrollmentStatus: string(identity.StatusEnrolled),
		Hostname:         "test-host",
		OS:               "linux",
		Architecture:     "arm64",
		ProcessingMode:   string(config.ModeHybrid),
	}
	// Uptime is time-dependent; compare everything else structurally.
	got.UptimeSeconds, got.Uptime = 0, ""

	if got != want {
		t.Errorf("Snapshot() = %+v, want %+v", got, want)
	}
	if r.Snapshot().UptimeSeconds < 0 {
		t.Error("UptimeSeconds is negative")
	}
}

func TestSnapshotUnenrolled(t *testing.T) {
	cfg := &config.Config{ProcessingMode: config.ModeCloud}
	r := New("0.1.0", cfg, identity.New(""), platform.Info{})

	snap := r.Snapshot()
	if snap.EnrollmentStatus != string(identity.StatusUnenrolled) {
		t.Errorf("EnrollmentStatus = %q, want %q", snap.EnrollmentStatus, identity.StatusUnenrolled)
	}
	if snap.EdgeID != "" {
		t.Errorf("EdgeID = %q, want empty", snap.EdgeID)
	}
}
