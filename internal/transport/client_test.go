package transport

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func newTestServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

const validHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestEnrollSuccess(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != EnrollPath {
			t.Errorf("path = %q, want %q", r.URL.Path, EnrollPath)
		}
		var req EnrollRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.EnrollmentToken != "gw_enroll_valid" {
			t.Errorf("token = %q", req.EnrollmentToken)
		}
		if req.DeviceKeyHash != validHash {
			t.Errorf("device_key_hash = %q, want %q", req.DeviceKeyHash, validHash)
		}
		if req.GatewayInstanceID == "" {
			t.Error("gateway_instance_id is empty, want required field sent")
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(EnrollResponse{DeviceID: "device-1", DeviceKind: "gateway"})
	})

	c, err := New(srv.URL, true, 2*time.Second, "test")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	resp, err := c.Enroll(context.Background(), EnrollRequest{
		EnrollmentToken:   "gw_enroll_valid",
		GatewayInstanceID: "edge-1",
		DeviceKeyHash:     validHash,
		EdgeID:            "edge-1",
	})
	if err != nil {
		t.Fatalf("Enroll() error = %v", err)
	}
	if resp.DeviceID != "device-1" || resp.DeviceKind != "gateway" {
		t.Errorf("resp = %+v", resp)
	}
}

func TestEnrollTokenInvalid(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]string{"detail": "invalid token"})
	})
	c, _ := New(srv.URL, true, 2*time.Second, "test")

	_, err := c.Enroll(context.Background(), EnrollRequest{EnrollmentToken: "bad", GatewayInstanceID: "edge-1"})
	if !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("err = %v, want ErrTokenInvalid", err)
	}
}

// TestEnroll401IsAlwaysTokenInvalid asserts the anti-enumeration contract:
// every 401 cause — invalid, expired, used, mismatched — maps to the same
// ErrTokenInvalid, never inferred from response text.
func TestEnroll401IsAlwaysTokenInvalid(t *testing.T) {
	for _, detail := range []string{"invalid token", "token expired", "token already used", "mismatch"} {
		srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{"detail": detail})
		})
		c, _ := New(srv.URL, true, 2*time.Second, "test")

		_, err := c.Enroll(context.Background(), EnrollRequest{EnrollmentToken: "tok", GatewayInstanceID: "edge-1"})
		if !errors.Is(err, ErrTokenInvalid) {
			t.Errorf("detail=%q: err = %v, want ErrTokenInvalid", detail, err)
		}
	}
}

func TestEnrollAlreadyEnrolled(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]string{"detail": "edge_id already enrolled in another device"})
	})
	c, _ := New(srv.URL, true, 2*time.Second, "test")

	_, err := c.Enroll(context.Background(), EnrollRequest{EnrollmentToken: "tok", GatewayInstanceID: "edge-dupe"})
	if !errors.Is(err, ErrAlreadyEnrolled) {
		t.Fatalf("err = %v, want ErrAlreadyEnrolled", err)
	}
}

func TestEnrollInvalidRequest(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_ = json.NewEncoder(w).Encode(map[string]any{"detail": []map[string]string{{"msg": "field required"}}})
	})
	c, _ := New(srv.URL, true, 2*time.Second, "test")

	_, err := c.Enroll(context.Background(), EnrollRequest{EnrollmentToken: "tok"})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("err = %v, want ErrInvalidRequest", err)
	}
}

func TestEnrollTimeout(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	})
	c, _ := New(srv.URL, true, 10*time.Millisecond, "test")

	_, err := c.Enroll(context.Background(), EnrollRequest{EnrollmentToken: "tok"})
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout", err)
	}
}

func TestEnrollSaaSUnavailable(t *testing.T) {
	// Bind and immediately close to get a port nothing is listening on.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close()

	c, err := New("http://"+addr, true, 500*time.Millisecond, "test")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	_, err = c.Enroll(context.Background(), EnrollRequest{EnrollmentToken: "tok"})
	if !errors.Is(err, ErrSaaSUnavailable) {
		t.Fatalf("err = %v, want ErrSaaSUnavailable", err)
	}
}

func TestNewRejectsInsecureHTTPByDefault(t *testing.T) {
	_, err := New("http://saas.example.test", false, 0, "test")
	if !errors.Is(err, ErrInsecureURL) {
		t.Fatalf("err = %v, want ErrInsecureURL", err)
	}
}

func TestNewAllowsInsecureHTTPWhenExplicit(t *testing.T) {
	_, err := New("http://saas.example.test", true, 0, "test")
	if err != nil {
		t.Fatalf("New() error = %v, want nil", err)
	}
}

func TestMeSendsDeviceIDAndBearer(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Device-Id") != "device-1" {
			t.Errorf("X-Device-Id = %q, want %q", r.Header.Get("X-Device-Id"), "device-1")
		}
		if r.Header.Get("Authorization") != "Bearer bad-cred" {
			t.Errorf("Authorization header = %q", r.Header.Get("Authorization"))
		}
		w.WriteHeader(http.StatusForbidden)
	})
	c, _ := New(srv.URL, true, 2*time.Second, "test")

	_, err := c.Me(context.Background(), "device-1", "bad-cred")
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
}

func TestMeSuccess(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		// Literal wire shape, NOT json.Marshal(MeResponse{}): organization_id
		// and site_id are JSON *numbers* on the real SaaS (derived from int
		// columns). Encoding through the struct would make this mock agree
		// with whatever the struct happens to say and hide a type mismatch.
		_, _ = io.WriteString(w, `{"device_id":"device-1","edge_id":"edge-1","device_kind":"gateway",
			"status":"active","name":"Edge 1","organization_id":7,"organization_name":"Acme",
			"site_id":3,"site_name":"HQ"}`)
	})
	c, _ := New(srv.URL, true, 2*time.Second, "test")

	resp, err := c.Me(context.Background(), "device-1", "good-cred")
	if err != nil {
		t.Fatalf("Me() error = %v", err)
	}
	if resp.EdgeID != "edge-1" || resp.OrganizationName != "Acme" || resp.SiteName != "HQ" {
		t.Errorf("resp = %+v", resp)
	}
	if resp.OrganizationID.String() != "7" || resp.SiteID.String() != "3" {
		t.Errorf("org/site = %q/%q, want \"7\"/\"3\"", resp.OrganizationID, resp.SiteID)
	}
}

// TestMeNullSiteID: an unassigned site comes back as JSON null and must
// decode to the empty string, not fail the whole /edge/me call.
func TestMeNullSiteID(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"device_id":"device-1","edge_id":"edge-1","organization_id":7,"site_id":null}`)
	})
	c, _ := New(srv.URL, true, 2*time.Second, "test")

	resp, err := c.Me(context.Background(), "device-1", "good-cred")
	if err != nil {
		t.Fatalf("Me() error = %v", err)
	}
	if resp.SiteID.String() != "" {
		t.Errorf("SiteID = %q, want empty", resp.SiteID)
	}
}

func TestRotateKeySuccessAndUnauthorized(t *testing.T) {
	rotated := false
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != RotateKeyPath {
			t.Errorf("path = %q, want %q", r.URL.Path, RotateKeyPath)
		}
		if r.Header.Get("Authorization") == "Bearer revoked" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var req RotateKeyRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.DeviceKeyHash != validHash || req.RotationID != "rot-1" {
			t.Errorf("req = %+v", req)
		}
		rotated = true
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(RotateResponse{DeviceID: "device-1", EdgeID: "edge-1", RotatedAt: "2024-01-01T00:00:00Z"})
	})
	c, _ := New(srv.URL, true, 2*time.Second, "test")

	if _, err := c.RotateKey(context.Background(), "device-1", "revoked", RotateKeyRequest{DeviceKeyHash: validHash, RotationID: "rot-1"}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}

	resp, err := c.RotateKey(context.Background(), "device-1", "good-cred", RotateKeyRequest{DeviceKeyHash: validHash, RotationID: "rot-1"})
	if err != nil {
		t.Fatalf("RotateKey() error = %v", err)
	}
	if !rotated || resp.DeviceID != "device-1" || resp.EdgeID != "edge-1" {
		t.Errorf("resp = %+v", resp)
	}
}

func TestRequestNeverLogsCredentialInError(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	c, _ := New(srv.URL, true, 2*time.Second, "test")

	_, err := c.Me(context.Background(), "device-1", "super-secret-credential-value")
	if err == nil {
		t.Fatal("expected error")
	}
	if got := err.Error(); contains(got, "super-secret-credential-value") {
		t.Errorf("error message leaked credential: %q", got)
	}
}

func contains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestPostFrameSuccess(t *testing.T) {
	frameBody := []byte{0xFF, 0xD8, 0xFF, 0xD9} // not a real jpeg, just distinguishable bytes
	capturedAt := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)

	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != FramesPath {
			t.Errorf("path = %q, want %q", r.URL.Path, FramesPath)
		}
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "image/jpeg" {
			t.Errorf("Content-Type = %q, want image/jpeg", ct)
		}
		if got := r.Header.Get("X-Device-Id"); got != "device-1" {
			t.Errorf("X-Device-Id = %q", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer cred-1" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("X-Candidate-Key"); got != "cam-1" {
			t.Errorf("X-Candidate-Key = %q", got)
		}
		if got := r.Header.Get("X-Frame-Seq"); got != "7" {
			t.Errorf("X-Frame-Seq = %q, want 7", got)
		}
		if got := r.Header.Get("X-Frame-Timestamp"); got != capturedAt.Format(time.RFC3339Nano) {
			t.Errorf("X-Frame-Timestamp = %q", got)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		if string(body) != string(frameBody) {
			t.Errorf("body = %v, want %v", body, frameBody)
		}
		w.WriteHeader(http.StatusAccepted)
	})

	c, err := New(srv.URL, true, 2*time.Second, "test")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	if err := c.PostFrame(context.Background(), "device-1", "cred-1", "cam-1", 7, capturedAt, frameBody); err != nil {
		t.Fatalf("PostFrame() error = %v", err)
	}
}

func TestPostFrameUnauthorized(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	c, _ := New(srv.URL, true, 2*time.Second, "test")

	err := c.PostFrame(context.Background(), "device-1", "cred-1", "cam-1", 1, time.Now(), []byte{1})
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("PostFrame() error = %v, want ErrUnauthorized", err)
	}
}

// TestPostFrameStatusClassification pins the exact status->sentinel mapping
// I6's offline buffer depends on to decide what is worth retrying:
// classification is by HTTP status alone, never inferred from the response
// body (see classifyFrameStatus).
func TestPostFrameStatusClassification(t *testing.T) {
	tests := []struct {
		status int
		want   error
	}{
		// Retryable: the SaaS is transiently failing or asking to slow down.
		{http.StatusRequestTimeout, ErrRetryableStatus},
		{http.StatusTooManyRequests, ErrRetryableStatus},
		{http.StatusInternalServerError, ErrRetryableStatus},
		{http.StatusBadGateway, ErrRetryableStatus},
		{http.StatusServiceUnavailable, ErrRetryableStatus},
		{http.StatusGatewayTimeout, ErrRetryableStatus},
		// Never retryable: the credential is rejected.
		{http.StatusUnauthorized, ErrUnauthorized},
		{http.StatusForbidden, ErrUnauthorized},
		// Never retryable: the request itself is permanently wrong.
		{http.StatusBadRequest, ErrInvalidRequest},
		{http.StatusNotFound, ErrInvalidRequest},
		{http.StatusConflict, ErrInvalidRequest},
		{http.StatusRequestEntityTooLarge, ErrInvalidRequest},
		{http.StatusUnprocessableEntity, ErrInvalidRequest},
	}

	for _, tt := range tests {
		t.Run(http.StatusText(tt.status), func(t *testing.T) {
			srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
			})
			c, _ := New(srv.URL, true, 2*time.Second, "test")

			err := c.PostFrame(context.Background(), "device-1", "cred-1", "cam-1", 1, time.Now(), []byte{1})
			if !errors.Is(err, tt.want) {
				t.Fatalf("PostFrame() [status %d] error = %v, want wrapping %v", tt.status, err, tt.want)
			}
		})
	}
}

func TestPostFrameSaaSUnavailable(t *testing.T) {
	// A closed connection (no listener) makes the client's Do() fail before
	// any status code exists.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close()

	c, err := New("http://"+addr, true, 500*time.Millisecond, "test")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	err = c.PostFrame(context.Background(), "device-1", "cred-1", "cam-1", 1, time.Now(), []byte{1})
	if !errors.Is(err, ErrSaaSUnavailable) {
		t.Fatalf("PostFrame() error = %v, want ErrSaaSUnavailable", err)
	}
}

// TestPostFrameContextCanceledIsPreserved pins the fix for a caller (e.g.
// cloudsink's I6 drain loop) that needs to distinguish "my own shutdown
// cancelled this request" from "the SaaS is unreachable" — both used to
// collapse into ErrSaaSUnavailable because the underlying error was
// wrapped with %v instead of %w, which silently drops it from the
// errors.Is chain.
func TestPostFrameContextCanceledIsPreserved(t *testing.T) {
	release := make(chan struct{})
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		<-release // hold the request open until the client cancels
	})
	defer close(release)

	c, err := New(srv.URL, true, 5*time.Second, "test")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	err = c.PostFrame(ctx, "device-1", "cred-1", "cam-1", 1, time.Now(), []byte{1})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("PostFrame() error = %v, want errors.Is(err, context.Canceled) == true", err)
	}
	// The existing classification must still work alongside it (Go
	// supports multiple %w verbs in one Errorf).
	if !errors.Is(err, ErrSaaSUnavailable) {
		t.Fatalf("PostFrame() error = %v, want errors.Is(err, ErrSaaSUnavailable) == true too", err)
	}
}
