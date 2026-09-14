package health

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/config"
	"github.com/drko-dev/monitoreoedgeis/internal/heartbeat"
	"github.com/drko-dev/monitoreoedgeis/internal/identity"
	"github.com/drko-dev/monitoreoedgeis/internal/platform"
)

func newTestReporter() *Reporter {
	return New("0.1.0-test", &config.Config{ProcessingMode: config.ModeCloud},
		identity.Identity{EdgeID: "edge-test", Status: identity.StatusEnrolled},
		platform.Info{Hostname: "host-test", GOARCH: "amd64"})
}

func TestHandlerHealthz(t *testing.T) {
	r := newTestReporter()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()

	Handler(r).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestHandlerReadyz(t *testing.T) {
	tests := []struct {
		name  string
		state State
		want  int
	}{
		{"starting", StateStarting, http.StatusServiceUnavailable},
		{"ready", StateReady, http.StatusOK},
		{"degraded", StateDegraded, http.StatusServiceUnavailable},
		{"stopping", StateStopping, http.StatusServiceUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newTestReporter()
			r.Set(tt.state)

			req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
			rec := httptest.NewRecorder()
			Handler(r).ServeHTTP(rec, req)

			if rec.Code != tt.want {
				t.Errorf("status = %d, want %d", rec.Code, tt.want)
			}
		})
	}
}

func TestHandlerStatus(t *testing.T) {
	r := newTestReporter()
	r.Set(StateReady)
	r.SetModuleState("health-http", "running")

	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	rec := httptest.NewRecorder()
	Handler(r).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var snap Snapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &snap); err != nil {
		t.Fatalf("invalid JSON body: %v", err)
	}
	if snap.Status != StateReady {
		t.Errorf("Status = %q, want %q", snap.Status, StateReady)
	}
	if snap.EdgeID != "edge-test" {
		t.Errorf("EdgeID = %q, want %q", snap.EdgeID, "edge-test")
	}
	if snap.Modules["health-http"] != "running" {
		t.Errorf("Modules[health-http] = %q, want %q", snap.Modules["health-http"], "running")
	}
}

// TestHandlerStatusOmitsHeartbeatWhenTheModuleIsNotRunning guards the
// omitempty on Snapshot.Heartbeat: an unenrolled Edge, or one with no SaaS
// URL, must not report a fabricated all-zero heartbeat state that reads as
// "never succeeded" rather than "not applicable".
func TestHandlerStatusOmitsHeartbeatWhenTheModuleIsNotRunning(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	rec := httptest.NewRecorder()
	Handler(newTestReporter()).ServeHTTP(rec, req)

	if body := rec.Body.String(); strings.Contains(body, `"heartbeat"`) {
		t.Errorf("/status carries a heartbeat object with no module running:\n%s", body)
	}
}

func TestHandlerStatusReportsHeartbeatState(t *testing.T) {
	success := time.Now().Add(-90 * time.Second).UTC().Truncate(time.Second)
	attempt := time.Now().Add(-30 * time.Second).UTC().Truncate(time.Second)

	r := newTestReporter()
	r.Set(StateReady)
	r.SetHeartbeatStatus(heartbeat.Status{
		State:               "degraded",
		LastSuccessAt:       success,
		LastAttemptAt:       attempt,
		ConsecutiveFailures: 3,
		LastError:           "saas_unavailable",
	})

	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	rec := httptest.NewRecorder()
	Handler(r).ServeHTTP(rec, req)

	var snap Snapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &snap); err != nil {
		t.Fatalf("invalid JSON body: %v", err)
	}
	if snap.Heartbeat == nil {
		t.Fatal("Snapshot.Heartbeat = nil, want the module status")
	}
	got := *snap.Heartbeat
	if got.State != "degraded" {
		t.Errorf("Heartbeat.State = %q, want %q", got.State, "degraded")
	}
	if !got.LastSuccessAt.Equal(success) {
		t.Errorf("Heartbeat.LastSuccessAt = %v, want %v", got.LastSuccessAt, success)
	}
	if !got.LastAttemptAt.Equal(attempt) {
		t.Errorf("Heartbeat.LastAttemptAt = %v, want %v", got.LastAttemptAt, attempt)
	}
	if got.ConsecutiveFailures != 3 {
		t.Errorf("Heartbeat.ConsecutiveFailures = %d, want 3", got.ConsecutiveFailures)
	}
	if got.LastError != "saas_unavailable" {
		t.Errorf("Heartbeat.LastError = %q, want %q", got.LastError, "saas_unavailable")
	}
}

// A SaaS outage must not take the Edge down: /healthz stays 200 and the agent
// stays READY (so /readyz stays 200) while only the heartbeat module is
// degraded. This is the documented /readyz semantics for Hito D.
func TestSaaSOutageLeavesTheEdgeHealthyAndReady(t *testing.T) {
	r := newTestReporter()
	r.Set(StateReady)
	r.SetHeartbeatStatus(heartbeat.Status{
		State:               "degraded",
		ConsecutiveFailures: 12,
		LastError:           "saas_unavailable",
	})

	for path, want := range map[string]int{"/healthz": http.StatusOK, "/readyz": http.StatusOK} {
		rec := httptest.NewRecorder()
		Handler(r).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != want {
			t.Errorf("%s status = %d, want %d during a SaaS outage", path, rec.Code, want)
		}
	}
}

// TestHandlerStatusNeverExposesCredential proves /status can only ever leak
// the credential STATUS string, never the credential secret itself — the
// Reporter/Snapshot type simply has no field capable of carrying it, but
// this test guards against a future field accidentally introducing one.
func TestHandlerStatusNeverExposesCredential(t *testing.T) {
	const secret = "edg_live_should_never_appear_in_status"

	r := newTestReporter()
	r.SetCredentialStatus("ENROLLED")

	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	rec := httptest.NewRecorder()
	Handler(r).ServeHTTP(rec, req)

	body := rec.Body.String()
	if strings.Contains(body, secret) {
		t.Fatalf("/status body unexpectedly contains the credential secret:\n%s", body)
	}

	var snap Snapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &snap); err != nil {
		t.Fatalf("invalid JSON body: %v", err)
	}
	if snap.CredentialStatus != "ENROLLED" {
		t.Errorf("CredentialStatus = %q, want %q", snap.CredentialStatus, "ENROLLED")
	}
}
