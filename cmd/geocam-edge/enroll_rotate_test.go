package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
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
