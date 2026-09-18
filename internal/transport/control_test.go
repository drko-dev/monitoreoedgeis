package transport

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestClaimNextControlCommand_NoContent(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != ControlNextPath {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("X-Device-Id") != "dev-1" || r.Header.Get("Authorization") != "Bearer cred-1" {
			t.Errorf("auth headers = %v", r.Header)
		}
		w.WriteHeader(http.StatusNoContent)
	})

	c, err := New(srv.URL, true, 2*time.Second, "test")
	if err != nil {
		t.Fatal(err)
	}

	cmd, err := c.ClaimNextControlCommand(context.Background(), "dev-1", "cred-1")
	if err != nil {
		t.Fatalf("ClaimNextControlCommand() error = %v", err)
	}
	if cmd != nil {
		t.Fatalf("cmd = %+v, want nil", cmd)
	}
}

func TestClaimNextControlCommand_Success(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"command": map[string]any{
				"id":           "11111111-1111-1111-1111-111111111111",
				"command_type": "request_status",
				"payload":      map[string]any{},
				"expires_at":   "2026-09-18T15:00:00Z",
			},
		})
	})

	c, err := New(srv.URL, true, 2*time.Second, "test")
	if err != nil {
		t.Fatal(err)
	}

	cmd, err := c.ClaimNextControlCommand(context.Background(), "dev-1", "cred-1")
	if err != nil {
		t.Fatalf("ClaimNextControlCommand() error = %v", err)
	}
	if cmd == nil || cmd.ID != "11111111-1111-1111-1111-111111111111" || cmd.CommandType != "request_status" {
		t.Fatalf("unexpected cmd = %+v", cmd)
	}
}

func TestClaimNextControlCommand_Errors(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		header     http.Header
		wantErr    error
		checkRetry bool
	}{
		{"unauthorized", http.StatusUnauthorized, nil, ErrUnauthorized, false},
		{"forbidden", http.StatusForbidden, nil, ErrUnauthorized, false},
		{"rate_limited", http.StatusTooManyRequests, http.Header{"Retry-After": []string{"25"}}, ErrRateLimited, true},
		{"service_unavailable", http.StatusServiceUnavailable, nil, ErrRetryableStatus, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				if tc.header != nil {
					for k, v := range tc.header {
						for _, val := range v {
							w.Header().Add(k, val)
						}
					}
				}
				w.WriteHeader(tc.status)
			})

			c, _ := New(srv.URL, true, 2*time.Second, "test")
			cmd, err := c.ClaimNextControlCommand(context.Background(), "dev-1", "cred-1")
			if cmd != nil {
				t.Fatalf("expected nil cmd, got %+v", cmd)
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want error wrapping %v", err, tc.wantErr)
			}
			if tc.checkRetry {
				var rle *RateLimitError
				if !errors.As(err, &rle) || rle.RetryAfter != 25*time.Second {
					t.Fatalf("RateLimitError = %+v, want 25s", rle)
				}
			}
		})
	}
}

func TestReportControlCommand_SuccessAndErrors(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/edge/control/11111111-1111-1111-1111-111111111111/report" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	})

	c, _ := New(srv.URL, true, 2*time.Second, "test")
	err := c.ReportControlCommand(context.Background(), "dev-1", "cred-1", "11111111-1111-1111-1111-111111111111", "succeeded", map[string]any{"health": "READY"}, "")
	if err != nil {
		t.Fatalf("ReportControlCommand() error = %v", err)
	}
}
