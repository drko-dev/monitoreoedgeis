package transport

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestClaimNextDiscoveryRun_NoContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if r.URL.Path != DiscoveryNextPath {
			t.Errorf("expected path %s, got %s", DiscoveryNextPath, r.URL.Path)
		}
		if r.Header.Get("X-Device-Id") != "dev-1" {
			t.Errorf("expected X-Device-Id dev-1, got %s", r.Header.Get("X-Device-Id"))
		}
		if r.Header.Get("Authorization") != "Bearer secret-key" {
			t.Errorf("expected Bearer secret-key, got %s", r.Header.Get("Authorization"))
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	runID, err := client.ClaimNextDiscoveryRun(context.Background(), "dev-1", "secret-key")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if runID != nil {
		t.Errorf("expected nil runID, got %v", *runID)
	}
}

func TestClaimNextDiscoveryRun_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"run_id": 99}`))
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	runID, err := client.ClaimNextDiscoveryRun(context.Background(), "dev-1", "secret-key")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if runID == nil || *runID != 99 {
		t.Fatalf("expected runID 99, got %v", runID)
	}
}

func TestClaimNextDiscoveryRun_Errors(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		header     http.Header
		body       string
		wantErr    error
	}{
		{
			name:       "unauthorized",
			statusCode: http.StatusUnauthorized,
			wantErr:    ErrUnauthorized,
		},
		{
			name:       "forbidden",
			statusCode: http.StatusForbidden,
			wantErr:    ErrUnauthorized,
		},
		{
			name:       "rate_limited",
			statusCode: http.StatusTooManyRequests,
			header:     http.Header{"Retry-After": []string{"30"}},
			wantErr:    ErrRateLimited,
		},
		{
			name:       "server_error",
			statusCode: http.StatusInternalServerError,
			wantErr:    ErrUnexpectedStatus,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				for k, v := range tt.header {
					for _, val := range v {
						w.Header().Add(k, val)
					}
				}
				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()

			client := newTestClient(t, srv)
			runID, err := client.ClaimNextDiscoveryRun(context.Background(), "dev-1", "secret-key")
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("expected error %v, got %v", tt.wantErr, err)
			}
			if runID != nil {
				t.Errorf("expected nil runID, got %v", runID)
			}
			if tt.statusCode == http.StatusTooManyRequests {
				var rlErr *RateLimitError
				if errors.As(err, &rlErr) {
					if rlErr.RetryAfter != 30*time.Second {
						t.Errorf("expected RetryAfter 30s, got %v", rlErr.RetryAfter)
					}
				} else {
					t.Errorf("expected *RateLimitError, got %T", err)
				}
			}
		})
	}
}

func TestReportDiscoveryRun_Success(t *testing.T) {
	var receivedReq DiscoveryReportRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/api/v1/gateway/discovery/runs/42/report" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&receivedReq); err != nil {
			t.Fatalf("failed to decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"result": "completed", "candidate_count": 1}`))
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	req := DiscoveryReportRequest{
		Status: "completed",
		Candidates: []DiscoveryCandidatePayload{
			{
				Protocol:     "onvif",
				EndpointHost: "192.168.1.100",
				EndpointPort: 80,
				EndpointPath: "/onvif/device_service",
				DeviceType:   "camera",
				AuthRequired: true,
			},
		},
	}
	err := client.ReportDiscoveryRun(context.Background(), "dev-1", "secret-key", 42, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if receivedReq.Status != "completed" {
		t.Errorf("expected completed, got %s", receivedReq.Status)
	}
	if len(receivedReq.Candidates) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(receivedReq.Candidates))
	}
	c := receivedReq.Candidates[0]
	if c.EndpointHost != "192.168.1.100" || c.EndpointPort != 80 || !c.AuthRequired {
		t.Errorf("candidate fields mismatch: %+v", c)
	}
}

func TestReportDiscoveryRun_Errors(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		header     http.Header
		body       string
		wantErr    error
	}{
		{
			name:       "not_found",
			statusCode: http.StatusNotFound,
			wantErr:    ErrRunNotFound,
		},
		{
			name:       "conflict",
			statusCode: http.StatusConflict,
			wantErr:    ErrRunConflict,
		},
		{
			name:       "unprocessable_entity",
			statusCode: http.StatusUnprocessableEntity,
			body:       "too many candidates",
			wantErr:    ErrInvalidRequest,
		},
		{
			name:       "unauthorized",
			statusCode: http.StatusUnauthorized,
			wantErr:    ErrUnauthorized,
		},
		{
			name:       "rate_limited",
			statusCode: http.StatusTooManyRequests,
			header:     http.Header{"Retry-After": []string{"60"}},
			wantErr:    ErrRateLimited,
		},
		{
			name:       "internal_error",
			statusCode: http.StatusInternalServerError,
			wantErr:    ErrUnexpectedStatus,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				for k, v := range tt.header {
					for _, val := range v {
						w.Header().Add(k, val)
					}
				}
				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()

			client := newTestClient(t, srv)
			err := client.ReportDiscoveryRun(context.Background(), "dev-1", "secret-key", 42, DiscoveryReportRequest{Status: "failed"})
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("expected error %v, got %v", tt.wantErr, err)
			}
		})
	}
}
