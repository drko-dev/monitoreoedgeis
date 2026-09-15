package cameratest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/cameracreds"
	"github.com/drko-dev/monitoreoedgeis/internal/discovery/onvif"
)

const deviceInfoXML = `<?xml version="1.0" encoding="utf-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:tds="http://www.onvif.org/ver10/device/wsdl">
  <s:Body>
    <tds:GetDeviceInformationResponse>
      <tds:Manufacturer>TP-LINK</tds:Manufacturer>
      <tds:Model>TC70</tds:Model>
    </tds:GetDeviceInformationResponse>
  </s:Body>
</s:Envelope>`

func newTestProvider(t *testing.T, cred cameracreds.Credential) *cameracreds.Provider {
	t.Helper()
	dir := t.TempDir()
	key := make([]byte, cameracreds.MasterKeySize)
	store, err := cameracreds.OpenStore(dir, key)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	if _, err := store.Apply([]cameracreds.Credential{cred}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	return cameracreds.NewProvider(store)
}

func newTestClient(t *testing.T, handler http.HandlerFunc) (*onvif.Client, *httptest.Server) {
	t.Helper()
	ts := httptest.NewServer(handler)
	client := onvif.NewClient(2*time.Second, nil)
	client.SetXAddrValidator(func(raw string) (*url.URL, int, error) {
		u, err := url.Parse(raw)
		return u, 80, err
	})
	return client, ts
}

func TestTestONVIFCredential_NoCredentialIsError(t *testing.T) {
	provider := newTestProvider(t, cameracreds.Credential{
		ID:            "cred-1",
		Scope:         cameracreds.ScopeDevice,
		CandidateKeys: []string{"other-device"},
		Username:      "admin",
		Password:      "pw",
		Revision:      1,
	})
	client, ts := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("ONVIF client must not be called when no credential resolves")
	})
	defer ts.Close()

	result := TestONVIFCredential(context.Background(), client, provider, "this-device", "", ts.URL)
	if result.State != StateError {
		t.Errorf("expected ERROR when no credential resolves, got %v", result.State)
	}
}

func TestTestONVIFCredential_DeviceScopeValid(t *testing.T) {
	provider := newTestProvider(t, cameracreds.Credential{
		ID:            "cred-1",
		Scope:         cameracreds.ScopeDevice,
		CandidateKeys: []string{"stable-id-1"},
		Username:      "admin",
		Password:      "correct-pw",
		Revision:      1,
	})

	step := 0
	client, ts := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		step++
		w.WriteHeader(http.StatusOK)
		if step == 1 {
			_, _ = w.Write([]byte(deviceInfoXML))
			return
		}
		// GetCapabilities / GetProfiles / GetStreamUri: return empty/no-op
		// bodies so the flow completes without extra fields to assert.
		_, _ = w.Write([]byte(`<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body></s:Body></s:Envelope>`))
	})
	defer ts.Close()

	result := TestONVIFCredential(context.Background(), client, provider, "stable-id-1", "", ts.URL)
	if result.State != StateValid {
		t.Fatalf("expected VALID, got %v (err=%v)", result.State, result.Err)
	}
	if result.Manufacturer != "TP-LINK" || result.Model != "TC70" {
		t.Errorf("unexpected device info: %+v", result)
	}
}

func TestTestONVIFCredential_GroupFallback(t *testing.T) {
	provider := newTestProvider(t, cameracreds.Credential{
		ID:            "cred-group",
		Scope:         cameracreds.ScopeGroup,
		CandidateKeys: []string{"group-1"},
		Username:      "admin",
		Password:      "pw",
		Revision:      1,
	})

	client, ts := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(deviceInfoXML))
	})
	defer ts.Close()

	// No DEVICE credential for "stable-id-unassigned"; must fall back to
	// the GROUP credential via groupID.
	result := TestONVIFCredential(context.Background(), client, provider, "stable-id-unassigned", "group-1", ts.URL)
	if result.Err != nil && result.State != StateValid {
		t.Fatalf("expected the group credential to resolve and authenticate, got %v (err=%v)", result.State, result.Err)
	}
}

func TestTestONVIFCredential_InvalidPassword(t *testing.T) {
	provider := newTestProvider(t, cameracreds.Credential{
		ID:            "cred-1",
		Scope:         cameracreds.ScopeDevice,
		CandidateKeys: []string{"stable-id-1"},
		Username:      "admin",
		Password:      "wrong-pw",
		Revision:      1,
	})
	client, ts := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	defer ts.Close()

	result := TestONVIFCredential(context.Background(), client, provider, "stable-id-1", "", ts.URL)
	if result.State != StateInvalid {
		t.Errorf("expected INVALID, got %v", result.State)
	}
}

func TestTestONVIFCredential_Unreachable(t *testing.T) {
	provider := newTestProvider(t, cameracreds.Credential{
		ID:            "cred-1",
		Scope:         cameracreds.ScopeDevice,
		CandidateKeys: []string{"stable-id-1"},
		Username:      "admin",
		Password:      "pw",
		Revision:      1,
	})
	client := onvif.NewClient(2*time.Second, nil)
	client.SetXAddrValidator(func(raw string) (*url.URL, int, error) {
		u, err := url.Parse(raw)
		return u, 80, err
	})

	// Nothing listens on this port.
	result := TestONVIFCredential(context.Background(), client, provider, "stable-id-1", "", "http://127.0.0.1:1")
	if result.State != StateUnreachable {
		t.Errorf("expected UNREACHABLE, got %v (err=%v)", result.State, result.Err)
	}
}
