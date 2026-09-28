package installer_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drko-dev/monitoreoedgeis/internal/credentials"
	"github.com/drko-dev/monitoreoedgeis/internal/identity"
	"github.com/drko-dev/monitoreoedgeis/internal/installer"
)

func TestCrockfordNormalization(t *testing.T) {
	cases := []struct {
		input    string
		expected string
		valid    bool
	}{
		{"K9X4-7MB2", "K9X47MB2", true},
		{"k9x4-7mb2", "K9X47MB2", true},
		{"k9x4 7mb2", "K9X47MB2", true},
		{"O9X4-LMB2", "09X41MB2", true},  // O -> 0, L -> 1
		{"i9x4-7mb2", "19X47MB2", true},  // i -> 1
		{"k9x4", "K9X4", false},          // Too short
		{"k9x4-7mb2-extra", "K9X47MB2EXTRA", false}, // Too long
		{"k9x4-7mb!", "", false},         // Invalid char
	}

	for _, tc := range cases {
		norm := installer.NormalizeEnrollmentCode(tc.input)
		valid := installer.ValidateEnrollmentCode(norm)
		if valid != tc.valid {
			t.Errorf("NormalizeEnrollmentCode(%q) valid = %v, want %v", tc.input, valid, tc.valid)
		}
		if tc.valid && norm != tc.expected {
			t.Errorf("NormalizeEnrollmentCode(%q) = %q, want %q", tc.input, norm, tc.expected)
		}
	}
}

func TestClaimDeviceSuccessAndAtomicPersistence(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "edge-claim-test-*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	defer os.RemoveAll(tempDir)

	var receivedPayload installer.ClaimPayload
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/edge/claim" {
			t.Errorf("Unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Method != http.MethodPost {
			t.Errorf("Unexpected method: %s", r.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		if err := json.NewDecoder(r.Body).Decode(&receivedPayload); err != nil {
			t.Errorf("Decode payload failed: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(installer.ClaimResponse{
			DeviceID:       "edg_test123456",
			DeviceKind:     "edge",
			OrganizationID: 101,
			Status:         "active",
		})
	}))
	defer server.Close()

	svc := installer.NewService(tempDir, filepath.Join(tempDir, "config.env"))
	svc.EnrollmentProvider = installer.NewSaaSEnrollmentProvider(server.Client())

	ctx := context.Background()
	req := installer.ClaimRequest{
		Code:       "k9x4-7mb2",
		DeviceName: "test-device-01",
		SaaSURL:    server.URL,
	}

	result, err := svc.ClaimDevice(ctx, req)
	if err != nil {
		t.Fatalf("ClaimDevice failed: %v", err)
	}

	if result.DeviceID != "edg_test123456" {
		t.Errorf("result.DeviceID = %q, want edg_test123456", result.DeviceID)
	}
	if result.OrganizationID != 101 {
		t.Errorf("result.OrganizationID = %d, want 101", result.OrganizationID)
	}
	if result.DeviceKind != "edge" {
		t.Errorf("result.DeviceKind = %q, want edge", result.DeviceKind)
	}
	if result.Status != "active" {
		t.Errorf("result.Status = %q, want active", result.Status)
	}

	// Verify wire payload details
	if receivedPayload.Code != "K9X47MB2" {
		t.Errorf("receivedPayload.Code = %q, want K9X47MB2", receivedPayload.Code)
	}
	if len(receivedPayload.DeviceKeyHash) != 64 {
		t.Errorf("expected 64-char hex device_key_hash, got len %d", len(receivedPayload.DeviceKeyHash))
	}
	if receivedPayload.ClaimRequestID == "" {
		t.Errorf("expected non-empty claim_request_id")
	}

	// Verify persistence: credentials.json
	creds, err := credentials.Load(tempDir)
	if err != nil {
		t.Fatalf("credentials.Load failed: %v", err)
	}
	if !creds.IsEnrolled() {
		t.Errorf("creds.IsEnrolled() = false, want true")
	}
	if creds.DeviceID != "edg_test123456" {
		t.Errorf("creds.DeviceID = %q, want edg_test123456", creds.DeviceID)
	}
	if creds.TenantID != "101" {
		t.Errorf("creds.TenantID = %q, want 101", creds.TenantID)
	}
	if !strings.HasPrefix(creds.Credential, "edg_live_") {
		t.Errorf("creds.Credential = %q, want edg_live_ prefix", creds.Credential)
	}

	// Verify persistence: identity.json
	id, err := identity.LoadExisting(tempDir)
	if err != nil {
		t.Fatalf("identity.LoadExisting failed: %v", err)
	}
	if !id.IsEnrolled() {
		t.Errorf("id.IsEnrolled() = false, want true")
	}
	if id.EdgeID != result.EdgeID {
		t.Errorf("id.EdgeID = %q, want %q", id.EdgeID, result.EdgeID)
	}

	// Verify installer state transition to ENROLLED
	state, err := svc.GetInstallerState(ctx)
	if err != nil {
		t.Fatalf("GetInstallerState failed: %v", err)
	}
	if state.State != installer.StateEnrolled {
		t.Errorf("state.State = %s, want %s", state.State, installer.StateEnrolled)
	}
}

func TestClaimDeviceErrors(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "edge-claim-err-*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	defer os.RemoveAll(tempDir)

	statusCode := http.StatusUnauthorized
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(statusCode)
		_ = json.NewEncoder(w).Encode(map[string]string{"detail": "Rejected"})
	}))
	defer server.Close()

	svc := installer.NewService(tempDir, filepath.Join(tempDir, "config.env"))
	svc.EnrollmentProvider = installer.NewSaaSEnrollmentProvider(server.Client())

	ctx := context.Background()

	// 1. Invalid format rejected locally before network
	_, err = svc.ClaimDevice(ctx, installer.ClaimRequest{Code: "bad-code", SaaSURL: server.URL})
	if err == nil {
		t.Errorf("expected error on malformed code")
	}
	if safeErr, ok := err.(*installer.SafeError); ok {
		if safeErr.Code != "INVALID_CODE" {
			t.Errorf("safeErr.Code = %q, want INVALID_CODE", safeErr.Code)
		}
	} else {
		t.Errorf("expected *installer.SafeError, got %T", err)
	}

	// 2. 401 Unauthorized from server
	statusCode = http.StatusUnauthorized
	_, err = svc.ClaimDevice(ctx, installer.ClaimRequest{Code: "K9X4-7MB2", SaaSURL: server.URL})
	if safeErr, ok := err.(*installer.SafeError); ok {
		if safeErr.Code != "INVALID_CODE" {
			t.Errorf("safeErr.Code = %q, want INVALID_CODE", safeErr.Code)
		}
	}

	// 3. 409 Conflict from server
	statusCode = http.StatusConflict
	_, err = svc.ClaimDevice(ctx, installer.ClaimRequest{Code: "K9X4-7MB2", SaaSURL: server.URL})
	if safeErr, ok := err.(*installer.SafeError); ok {
		if safeErr.Code != "EDGE_ID_CONFLICT" {
			t.Errorf("safeErr.Code = %q, want EDGE_ID_CONFLICT", safeErr.Code)
		}
	}

	// 4. 429 Rate limited from server
	statusCode = http.StatusTooManyRequests
	_, err = svc.ClaimDevice(ctx, installer.ClaimRequest{Code: "K9X4-7MB2", SaaSURL: server.URL})
	if safeErr, ok := err.(*installer.SafeError); ok {
		if safeErr.Code != "RATE_LIMITED" {
			t.Errorf("safeErr.Code = %q, want RATE_LIMITED", safeErr.Code)
		}
	}
}
