package transport

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOnboardCamera_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != CameraOnboardingPath {
			t.Errorf("unexpected method/path: %s %s", r.Method, r.URL.Path)
		}
		var req CameraOnboardingRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed decoding request: %v", err)
		}
		if req.CandidateKey != "cand-1" {
			t.Errorf("unexpected candidate key: %s", req.CandidateKey)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{
			"onboarding": {
				"operation_id": 10,
				"camera_id": 42,
				"credential_id": 7,
				"candidate_key": "cand-1",
				"status": "created"
			}
		}`))
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	res, err := client.OnboardCamera(context.Background(), "dev-1", "secret", CameraOnboardingRequest{
		CandidateKey: "cand-1",
		CameraName:   "Cam 1",
		Username:     "admin",
		Password:     "pass",
	})
	if err != nil {
		t.Fatalf("OnboardCamera failed: %v", err)
	}
	if res.CameraID != 42 || res.Status != "created" {
		t.Errorf("unexpected result: %+v", res)
	}
}

func TestCancelCameraOnboarding_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/api/v1/edge/camera-onboarding/10" {
			t.Errorf("unexpected method/path: %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	if err := client.CancelCameraOnboarding(context.Background(), "dev-1", "secret", 10); err != nil {
		t.Fatalf("CancelCameraOnboarding failed: %v", err)
	}
}

func TestReconcileCamera_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/edge/camera-onboarding/reconcile" {
			t.Errorf("unexpected method/path: %s %s", r.Method, r.URL.Path)
		}
		var rep DiscoveredCameraReport
		if err := json.NewDecoder(r.Body).Decode(&rep); err != nil {
			t.Fatalf("failed decoding request: %v", err)
		}
		if rep.CandidateKey != "epr-uuid-1" {
			t.Errorf("unexpected candidate key: %s", rep.CandidateKey)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"reconciliation": {
				"result": "reconciled",
				"action": "reconciled",
				"camera_id": 55,
				"binding_id": 12,
				"status": "active"
			}
		}`))
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	res, err := client.ReconcileCamera(context.Background(), "dev-1", "secret", DiscoveredCameraReport{
		CandidateKey: "epr-uuid-1",
		StreamURI:    "rtsp://192.168.1.100:554/live",
	})
	if err != nil {
		t.Fatalf("ReconcileCamera failed: %v", err)
	}
	if res.CameraID != 55 || res.Result != "reconciled" {
		t.Errorf("unexpected result: %+v", res)
	}
}

func TestReportDiscoveredBatch_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/edge/camera-onboarding/discovered/batch" {
			t.Errorf("unexpected method/path: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"reconciliations": [
				{
					"result": "reconciled",
					"action": "reconciled",
					"camera_id": 55,
					"binding_id": 12,
					"status": "active"
				},
				{
					"result": "unbound_discovered",
					"action": "discovered_unbound",
					"camera_id": 0,
					"binding_id": 0,
					"status": "pending_binding"
				}
			]
		}`))
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	results, err := client.ReportDiscoveredBatch(context.Background(), "dev-1", "secret", []DiscoveredCameraReport{
		{CandidateKey: "cand-1"},
		{CandidateKey: "cand-2"},
	})
	if err != nil {
		t.Fatalf("ReportDiscoveredBatch failed: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].CameraID != 55 || results[1].Status != "pending_binding" {
		t.Errorf("unexpected batch results: %+v", results)
	}
}
