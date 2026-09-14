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
