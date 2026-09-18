package transport

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestLocalEventContractAndEvidenceHeaders(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			if r.Method != http.MethodPost || r.URL.Path != LocalEventsPath || r.Header.Get("Authorization") != "Bearer credential" || r.Header.Get("X-Device-Id") != "device" {
				t.Errorf("metadata request = %s %s headers=%v", r.Method, r.URL.Path, r.Header)
			}
			body, _ := io.ReadAll(r.Body)
			var payload map[string]any
			if err := json.Unmarshal(body, &payload); err != nil {
				t.Fatalf("decode metadata body: %v", err)
			}
			if payload["occurred_at"] != "2026-01-01T00:00:00Z" || payload["class"] != "person" || payload["confidence"] != 0.9 {
				t.Errorf("metadata payload=%v", payload)
			}
			if _, ok := payload["evidence"]; ok {
				t.Errorf("metadata must not declare evidence: %v", payload)
			}
			w.WriteHeader(http.StatusCreated)
			return
		}
		if r.Method != http.MethodPut || r.URL.Path != "/api/v1/edge/local-events/event-1/evidence/capture" {
			t.Errorf("evidence request=%s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("X-Candidate-Key") != "camera" || r.Header.Get("X-Evidence-SHA256") != "abc" || r.Header.Get("X-Evidence-Size") != "3" {
			t.Errorf("integrity headers=%v", r.Header)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	c, err := New(srv.URL, true, 0, "test")
	if err != nil {
		t.Fatal(err)
	}
	e := LocalEvent{EventUUID: "event-1", CandidateKey: "camera", Class: "person", Confidence: 0.9, BBox: map[string]float64{"x": 1, "y": 2, "width": 3, "height": 4}, Timestamp: "2026-01-01T00:00:00Z"}
	if err := c.PostLocalEvent(context.Background(), "device", "credential", e); err != nil {
		t.Fatal(err)
	}
	if err := c.PutLocalEventEvidence(context.Background(), "device", "credential", "event-1", "camera", "capture", []byte("jpg"), "abc", 3); err != nil {
		t.Fatal(err)
	}
}

func TestClassifyLocalEventStatus(t *testing.T) {
	tests := []struct {
		name   string
		status int
		header http.Header
		want   error
	}{
		{name: "created", status: http.StatusCreated},
		{name: "idempotent", status: http.StatusOK},
		{name: "unauthorized is auth error", status: http.StatusUnauthorized, want: ErrUnauthorized},
		{name: "conflict is permanent", status: http.StatusConflict, want: ErrInvalidRequest},
		{name: "service unavailable is retryable", status: http.StatusServiceUnavailable, want: ErrRetryableStatus},
		{name: "rate limited with header", status: http.StatusTooManyRequests, header: http.Header{"Retry-After": []string{"20"}}, want: ErrRateLimited},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := classifyLocalEventStatus(tt.status, tt.header)
			if !errors.Is(err, tt.want) {
				t.Errorf("classifyLocalEventStatus(%d) = %v, want error wrapping %v", tt.status, err, tt.want)
			}
			if tt.status == http.StatusTooManyRequests {
				var rle *RateLimitError
				if !errors.As(err, &rle) || rle.RetryAfter != 20*time.Second {
					t.Errorf("RateLimitError = %+v, want RetryAfter 20s", rle)
				}
			}
		})
	}
}
