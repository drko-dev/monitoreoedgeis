package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/credentials"
	"github.com/drko-dev/monitoreoedgeis/internal/identity"
	"github.com/drko-dev/monitoreoedgeis/internal/platform"
	"github.com/drko-dev/monitoreoedgeis/internal/transport"
)

var hex64 = regexp.MustCompile(`^[0-9a-f]{64}$`)

const testEdgeID = "11111111-1111-4111-8111-111111111111"

func newClient(t *testing.T, url string) *transport.Client {
	t.Helper()
	c, err := transport.New(url, true, 2*time.Second, "test")
	if err != nil {
		t.Fatalf("transport.New() error = %v", err)
	}
	return c
}

// TestRunEnrollGeneratesCredentialLocallyAndSendsOnlyHash verifies the
// zero-knowledge contract end-to-end: the mock SaaS receives a well-formed
// device_key_hash and the raw enroll request body never contains the
// generated plaintext credential anywhere.
func TestRunEnrollGeneratesCredentialLocallyAndSendsOnlyHash(t *testing.T) {
	var rawEnrollBody []byte
	var gotHash string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case transport.EnrollPath:
			var req transport.EnrollRequest
			body := decodeAndCapture(t, r, &rawEnrollBody)
			if err := json.Unmarshal(body, &req); err != nil {
				t.Fatalf("decode enroll request: %v", err)
			}
			gotHash = req.DeviceKeyHash
			if !hex64.MatchString(req.DeviceKeyHash) {
				t.Errorf("device_key_hash = %q, want 64 hex chars", req.DeviceKeyHash)
			}
			if req.GatewayInstanceID != testEdgeID {
				t.Errorf("gateway_instance_id = %q, want %q", req.GatewayInstanceID, testEdgeID)
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(transport.EnrollResponse{DeviceID: "device-1", DeviceKind: "gateway"})
		case transport.MePath:
			if r.Header.Get("X-Device-Id") != "device-1" {
				t.Errorf("X-Device-Id = %q, want %q", r.Header.Get("X-Device-Id"), "device-1")
			}
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer edg_live_") {
				t.Errorf("Authorization = %q, want Bearer edg_live_*", r.Header.Get("Authorization"))
			}
			w.WriteHeader(http.StatusOK)
			// Literal wire shape: organization_id/site_id are JSON numbers
			// on the real SaaS. See TestMeSuccess.
			_, _ = io.WriteString(w, `{"device_id":"device-1","edge_id":"`+testEdgeID+`",
				"organization_id":7,"site_id":3}`)
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	client := newClient(t, srv.URL)
	ident := identity.Identity{EdgeID: testEdgeID}
	host := platform.Info{OS: "linux", GOARCH: "arm64"}

	creds, warning, err := runEnroll(context.Background(), client, ident, host, "gw_enroll_valid")
	if err != nil {
		t.Fatalf("runEnroll() error = %v", err)
	}
	if warning != "" {
		t.Errorf("warning = %q, want empty", warning)
	}
	if creds.Credential == "" || creds.DeviceID == "" {
		t.Fatalf("creds missing credential/device_id: %+v", creds)
	}
	if creds.TenantID != "7" || creds.SiteID != "3" {
		t.Errorf("creds = %+v", creds)
	}

	// The plaintext credential that ended up in creds must never have
	// crossed the wire in the enroll request body.
	if strings.Contains(string(rawEnrollBody), creds.Credential) {
		t.Fatalf("enroll request body leaked the plaintext credential:\n%s", rawEnrollBody)
	}
	if gotHash != credentials.HashCredential(creds.Credential) {
		t.Errorf("device_key_hash %q does not match sha256(persisted credential)", gotHash)
	}
}

// TestRunEnrollMeFailureStillPersistsCredentialWithEmptyOrgSite covers the
// ambiguous case: the claim succeeded server-side (the credential is
// already valid) but the follow-up /edge/me call fails. The credential must
// still be returned for persisting — losing it here would strand the
// device — with empty org/site and a non-empty warning.
func TestRunEnrollMeFailureStillPersistsCredentialWithEmptyOrgSite(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case transport.EnrollPath:
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(transport.EnrollResponse{DeviceID: "device-1", DeviceKind: "gateway"})
		case transport.MePath:
			w.WriteHeader(http.StatusInternalServerError)
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	client := newClient(t, srv.URL)
	ident := identity.Identity{EdgeID: testEdgeID}
	host := platform.Info{OS: "linux", GOARCH: "arm64"}

	creds, warning, err := runEnroll(context.Background(), client, ident, host, "gw_enroll_valid")
	if err != nil {
		t.Fatalf("runEnroll() error = %v, want nil (claim already succeeded)", err)
	}
	if warning == "" {
		t.Error("warning is empty, want a non-empty warning about org/site confirmation")
	}
	if creds.Credential == "" || creds.DeviceID == "" {
		t.Fatalf("credential lost on /edge/me failure: creds = %+v", creds)
	}
	if creds.TenantID != "" || creds.SiteID != "" {
		t.Errorf("creds.TenantID/SiteID = %q/%q, want empty", creds.TenantID, creds.SiteID)
	}
}

// TestRunEnrollClaimFailureDiscardsCredential asserts that when the claim
// itself fails, runEnroll returns an error and no Credentials worth
// persisting — the generated credential is simply discarded in memory.
func TestRunEnrollClaimFailureDiscardsCredential(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]string{"detail": "invalid token"})
	}))
	defer srv.Close()

	client := newClient(t, srv.URL)
	ident := identity.Identity{EdgeID: testEdgeID}
	host := platform.Info{OS: "linux", GOARCH: "arm64"}

	_, _, err := runEnroll(context.Background(), client, ident, host, "bad-token")
	if !errors.Is(err, transport.ErrTokenInvalid) {
		t.Fatalf("err = %v, want ErrTokenInvalid", err)
	}
}

// TestRotateWithRetrySucceedsOnThirdAttempt simulates a mock SaaS that
// drops the connection for the first two rotation attempts and accepts the
// third, asserting the same rotation_id/device_key_hash is resubmitted
// every time and B is only reachable after the ACK.
func TestRotateWithRetrySucceedsOnThirdAttempt(t *testing.T) {
	var attempts atomic.Int32
	var seenHashes, seenRotationIDs []string

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		var req transport.RotateKeyRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode rotate request: %v", err)
		}
		seenHashes = append(seenHashes, req.DeviceKeyHash)
		seenRotationIDs = append(seenRotationIDs, req.RotationID)
		if n < 3 {
			// Simulate a dropped connection: hijack and close without
			// writing a response.
			hj, ok := w.(http.Hijacker)
			if !ok {
				t.Fatal("ResponseWriter does not support hijacking")
			}
			conn, _, err := hj.Hijack()
			if err != nil {
				t.Fatalf("hijack: %v", err)
			}
			conn.Close()
			return
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(transport.RotateResponse{DeviceID: "device-1", EdgeID: testEdgeID, RotatedAt: time.Now().UTC().Format(time.RFC3339)})
	}))
	srv.Listener.Close()
	srv.Listener = ln
	srv.Start()
	defer srv.Close()

	client := newClient(t, srv.URL)
	req := transport.RotateKeyRequest{DeviceKeyHash: strings.Repeat("b", 64), RotationID: "rot-fixed"}

	resp, err := rotateWithRetry(context.Background(), client, "device-1", "current-cred", req, []time.Duration{time.Millisecond, time.Millisecond})
	if err != nil {
		t.Fatalf("rotateWithRetry() error = %v", err)
	}
	if resp.DeviceID != "device-1" {
		t.Errorf("resp = %+v", resp)
	}
	if attempts.Load() != 3 {
		t.Errorf("attempts = %d, want 3", attempts.Load())
	}
	for i, h := range seenHashes {
		if h != req.DeviceKeyHash {
			t.Errorf("attempt %d hash = %q, want %q (same B on every retry)", i, h, req.DeviceKeyHash)
		}
	}
	for i, id := range seenRotationIDs {
		if id != req.RotationID {
			t.Errorf("attempt %d rotation_id = %q, want %q (same idempotency key on every retry)", i, id, req.RotationID)
		}
	}
}

// TestRotateWithRetryExhaustsAfterThreeFailures asserts that after 3 failed
// attempts, rotateWithRetry gives up and returns the last error — the
// caller's contract is then to leave the on-disk credential untouched.
func TestRotateWithRetryExhaustsAfterThreeFailures(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := newClient(t, srv.URL)
	req := transport.RotateKeyRequest{DeviceKeyHash: strings.Repeat("c", 64), RotationID: "rot-fail"}

	_, err := rotateWithRetry(context.Background(), client, "device-1", "current-cred", req, []time.Duration{time.Millisecond, time.Millisecond})
	if err == nil {
		t.Fatal("rotateWithRetry() succeeded, want error after exhausting attempts")
	}
	if attempts.Load() != rotateMaxAttempts {
		t.Errorf("attempts = %d, want %d", attempts.Load(), rotateMaxAttempts)
	}
}

// TestRotateWithRetryStopsImmediatelyOnUnauthorized asserts a revoked
// current credential is not retried (it will never start working) and maps
// to ErrUnauthorized.
func TestRotateWithRetryStopsImmediatelyOnUnauthorized(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	client := newClient(t, srv.URL)
	req := transport.RotateKeyRequest{DeviceKeyHash: strings.Repeat("d", 64), RotationID: "rot-revoked"}

	_, err := rotateWithRetry(context.Background(), client, "device-1", "revoked-cred", req, []time.Duration{time.Second, time.Second})
	if !errors.Is(err, transport.ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
	if attempts.Load() != 1 {
		t.Errorf("attempts = %d, want 1 (no retry on revoked credential)", attempts.Load())
	}
}

func decodeAndCapture(t *testing.T, r *http.Request, dst *[]byte) []byte {
	t.Helper()
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 4096)
	for {
		n, err := r.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err != nil {
			break
		}
	}
	*dst = buf
	return buf
}
