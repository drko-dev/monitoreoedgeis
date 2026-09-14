package transport

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newTestClient(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	c, err := New(srv.URL, true, 2*time.Second, "9.9.9")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func sampleRequest() HeartbeatRequest {
	cpu := 12.5
	temp := 47.25
	return HeartbeatRequest{
		EdgeID:         "edge-abc",
		AgentVersion:   "1.2.3",
		UptimeSeconds:  3600,
		Architecture:   "arm64",
		ProcessingMode: "cloud",
		HealthStatus:   "READY",
		BootID:         "edge-abc",
		SequenceNumber: 7,
		EdgeTimestamp:  "2026-01-01T00:00:00Z",
		TemperatureC:   &temp,
		System: HeartbeatSystem{
			CPUPercent:       &cpu,
			MemoryTotalBytes: 8 << 30,
			MemoryUsedBytes:  2 << 30,
			DiskTotalBytes:   64 << 30,
			DiskUsedBytes:    10 << 30,
		},
	}
}

// --- D2: authentication -----------------------------------------------------

func TestHeartbeatSendsBearerCredentialAndDeviceID(t *testing.T) {
	var gotAuth, gotDevice, gotQuery, gotContentType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotDevice = r.Header.Get("X-Device-Id")
		gotQuery = r.URL.RawQuery
		gotContentType = r.Header.Get("Content-Type")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if err := newTestClient(t, srv).Heartbeat(context.Background(), "dev-9", "cred-xyz", sampleRequest()); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}

	if gotAuth != "Bearer cred-xyz" {
		t.Errorf("Authorization = %q, want Bearer cred-xyz", gotAuth)
	}
	if gotDevice != "dev-9" {
		t.Errorf("X-Device-Id = %q, want dev-9", gotDevice)
	}
	// A credential in a URL ends up in access logs and proxies. It must never
	// leave the header.
	if gotQuery != "" {
		t.Errorf("query string = %q, want empty: credentials never go in the URL", gotQuery)
	}
	if gotContentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotContentType)
	}
}

func TestHeartbeatPostsToTheSharedEndpoint(t *testing.T) {
	var gotPath, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if err := newTestClient(t, srv).Heartbeat(context.Background(), "d", "c", sampleRequest()); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	if gotPath != HeartbeatPath {
		t.Errorf("path = %q, want %q", gotPath, HeartbeatPath)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
}

// --- D4/D5: payload shape ---------------------------------------------------

func TestHeartbeatPayloadMatchesTheServerModel(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if err := newTestClient(t, srv).Heartbeat(context.Background(), "d", "c", sampleRequest()); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}

	for key, want := range map[string]any{
		"edge_id":         "edge-abc",
		"agent_version":   "1.2.3",
		"uptime_seconds":  float64(3600),
		"architecture":    "arm64",
		"processing_mode": "cloud",
		"health_status":   "READY",
		"temperature_c":   47.25,
	} {
		if got := body[key]; got != want {
			t.Errorf("%s = %#v, want %#v", key, got, want)
		}
	}

	system, ok := body["system"].(map[string]any)
	if !ok {
		t.Fatalf("system = %#v, want a nested object", body["system"])
	}
	for key, want := range map[string]any{
		"cpu_percent":        12.5,
		"memory_total_bytes": float64(8 << 30),
		"memory_used_bytes":  float64(2 << 30),
		"disk_total_bytes":   float64(64 << 30),
		"disk_used_bytes":    float64(10 << 30),
	} {
		if got := system[key]; got != want {
			t.Errorf("system.%s = %#v, want %#v", key, got, want)
		}
	}
}

// The SaaS derives tenant, site and organization from the authenticated
// credential. The Edge must not get a vote, so it must not send them at all.
func TestHeartbeatNeverClaimsTenantOrSite(t *testing.T) {
	var raw string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		raw = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if err := newTestClient(t, srv).Heartbeat(context.Background(), "d", "c", sampleRequest()); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	for _, forbidden := range []string{"tenant", "site_id", "organization", "device_key", "credential"} {
		if strings.Contains(raw, forbidden) {
			t.Errorf("payload contains %q, which the SaaS must derive from the credential: %s", forbidden, raw)
		}
	}
}

// Absent optional readings must be omitted, not sent as a zero that the SaaS
// would store as a real measurement.
func TestHeartbeatOmitsUnavailableOptionalMetrics(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// A host with no thermal sensor and no measurable CPU.
	bare := HeartbeatRequest{EdgeID: "edge-1", UptimeSeconds: 10}
	if err := newTestClient(t, srv).Heartbeat(context.Background(), "d", "c", bare); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}

	if _, present := body["temperature_c"]; present {
		t.Error("temperature_c must be omitted when no sensor exists, not sent as null or 0")
	}
	system, _ := body["system"].(map[string]any)
	if _, present := system["cpu_percent"]; present {
		t.Error("system.cpu_percent must be omitted when CPU cannot be measured")
	}
	if _, present := system["memory_total_bytes"]; present {
		t.Error("system.memory_total_bytes must be omitted when memory cannot be read")
	}
}

// --- D17: error classification ----------------------------------------------

func TestHeartbeatErrorClassification(t *testing.T) {
	tests := map[string]struct {
		status  int
		want    error
		headers map[string]string
	}{
		"401 -> unauthorized": {http.StatusUnauthorized, ErrUnauthorized, nil},
		"403 -> unauthorized": {http.StatusForbidden, ErrUnauthorized, nil},
		"429 -> rate limited": {http.StatusTooManyRequests, ErrRateLimited, nil},
		"422 -> invalid":      {http.StatusUnprocessableEntity, ErrInvalidRequest, nil},
		"500 -> unexpected":   {http.StatusInternalServerError, ErrUnexpectedStatus, nil},
		"503 -> unexpected":   {http.StatusServiceUnavailable, ErrUnexpectedStatus, nil},
		"413 -> unexpected":   {http.StatusRequestEntityTooLarge, ErrUnexpectedStatus, nil},
		"415 -> unexpected":   {http.StatusUnsupportedMediaType, ErrUnexpectedStatus, nil},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				for k, v := range tc.headers {
					w.Header().Set(k, v)
				}
				w.WriteHeader(tc.status)
			}))
			defer srv.Close()

			err := newTestClient(t, srv).Heartbeat(context.Background(), "d", "c", sampleRequest())
			if !errors.Is(err, tc.want) {
				t.Errorf("error = %v, want one wrapping %v", err, tc.want)
			}
		})
	}
}

func TestHeartbeat200IsSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok","connectivity":"online"}`))
	}))
	defer srv.Close()

	if err := newTestClient(t, srv).Heartbeat(context.Background(), "d", "c", sampleRequest()); err != nil {
		t.Errorf("Heartbeat: %v", err)
	}
}

// --- D17: Retry-After -------------------------------------------------------

func TestHeartbeatSurfacesRetryAfter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "17")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	err := newTestClient(t, srv).Heartbeat(context.Background(), "d", "c", sampleRequest())

	var rl *RateLimitError
	if !errors.As(err, &rl) {
		t.Fatalf("error = %v, want a *RateLimitError", err)
	}
	if rl.RetryAfter != 17*time.Second {
		t.Errorf("RetryAfter = %s, want 17s", rl.RetryAfter)
	}
}

func TestParseRetryAfter(t *testing.T) {
	for name, tc := range map[string]struct {
		value string
		want  time.Duration
	}{
		"absent":      {"", 0},
		"seconds":     {"30", 30 * time.Second},
		"zero":        {"0", 0},
		"negative":    {"-5", 0},
		"http date":   {"Wed, 21 Oct 2026 07:28:00 GMT", 0},
		"non numeric": {"soon", 0},
	} {
		t.Run(name, func(t *testing.T) {
			h := http.Header{}
			if tc.value != "" {
				h.Set("Retry-After", tc.value)
			}
			if got := parseRetryAfter(h); got != tc.want {
				t.Errorf("parseRetryAfter(%q) = %s, want %s", tc.value, got, tc.want)
			}
		})
	}
}

// --- D20: timeouts and cancellation -----------------------------------------

func TestHeartbeatTimesOut(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(500 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c, err := New(srv.URL, true, 50*time.Millisecond, "9.9.9")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := c.Heartbeat(context.Background(), "d", "c", sampleRequest()); !errors.Is(err, ErrTimeout) {
		t.Errorf("error = %v, want one wrapping ErrTimeout", err)
	}
}

func TestHeartbeatHonoursContextCancellation(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release
	}))
	// Deferred calls run LIFO, so release is closed first: Close waits for the
	// in-flight handler, and the handler only returns once release is closed.
	defer srv.Close()
	defer close(release)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	err := newTestClient(t, srv).Heartbeat(ctx, "d", "c", sampleRequest())
	if err == nil {
		t.Fatal("expected an error when the context is cancelled mid-flight")
	}
}

func TestHeartbeatReportsAnUnreachableSaaS(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close() // nothing is listening any more

	c, err := New(url, true, time.Second, "9.9.9")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := c.Heartbeat(context.Background(), "d", "c", sampleRequest()); !errors.Is(err, ErrSaaSUnavailable) {
		t.Errorf("error = %v, want one wrapping ErrSaaSUnavailable", err)
	}
}

// --- secrets never appear in errors -----------------------------------------

func TestHeartbeatErrorsNeverLeakTheCredential(t *testing.T) {
	const credential = "top-secret-credential"
	for _, status := range []int{401, 403, 422, 429, 500} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
		}))
		err := newTestClient(t, srv).Heartbeat(context.Background(), "dev", credential, sampleRequest())
		srv.Close()

		if err == nil {
			t.Fatalf("status %d: expected an error", status)
		}
		if strings.Contains(err.Error(), credential) {
			t.Errorf("status %d: error leaks the credential: %v", status, err)
		}
	}
}
