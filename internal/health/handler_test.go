package health

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/drko-dev/monitoreoedgeis/internal/config"
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
