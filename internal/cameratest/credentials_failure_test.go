package cameratest

// Hito W — W7 (bad credentials), camera/ONVIF half.
//
// The point of these tests is not that a wrong password is rejected — that is
// already covered — but that a rejection is (a) classified as an auth denial
// rather than as an unreachable camera, (b) attempted exactly once, with no
// retry loop, and (c) impossible to observe the username or password in.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/cameracreds"
	"github.com/drko-dev/monitoreoedgeis/internal/discovery/onvif"
)

const w7Password = "w7-wrong-password"

// A 401 from the device must be reported as an invalid credential, distinctly
// from an unreachable device, after exactly one attempt.
func TestW7_ONVIFAuthRejectionIsInvalidAndIsNotRetried(t *testing.T) {
	var requests atomic.Int32

	provider := newTestProvider(t, cameracreds.Credential{
		ID:            "cred-1",
		Scope:         cameracreds.ScopeDevice,
		CandidateKeys: []string{"dev-1"},
		Username:      "admin",
		Password:      w7Password,
		Revision:      1,
	})

	client, server := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`<s:Envelope><s:Body><s:Fault><s:Reason><s:Text>NotAuthorized</s:Text></s:Reason></s:Fault></s:Body></s:Envelope>`))
	})
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := TestONVIFCredential(ctx, client, provider, "dev-1", server.URL)

	if result.State != StateInvalid {
		t.Fatalf("state = %q for a 401, want %q (err=%v)", result.State, StateInvalid, result.Err)
	}
	if result.Err == nil {
		t.Fatal("state is INVALID but no error was reported")
	}
	if got := requests.Load(); got != 1 {
		t.Errorf("the device received %d requests for one credential test, want 1 "+
			"(a rejected credential must not be retried in a loop)", got)
	}
	if strings.Contains(result.Err.Error(), w7Password) {
		t.Errorf("the password leaked into the result error: %v", result.Err)
	}
	if strings.Contains(result.StreamURI, w7Password) {
		t.Errorf("the stream URI carries the password: %q", result.StreamURI)
	}
}

// A device that answers with a WP-Security auth fault instead of an HTTP 401
// is the same credential failure, and equally silent about the secret.
func TestW7_ONVIFAuthFaultIsAlsoInvalidAndSanitized(t *testing.T) {
	provider := newTestProvider(t, cameracreds.Credential{
		ID:            "cred-1",
		Scope:         cameracreds.ScopeDevice,
		CandidateKeys: []string{"dev-1"},
		Username:      "admin",
		Password:      w7Password,
		Revision:      1,
	})

	client, server := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/soap+xml")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`<s:Envelope><s:Body><s:Fault>` +
			`<s:Code><s:Value>s:Sender</s:Value></s:Code>` +
			`<s:Reason><s:Text>Sender NotAuthorized</s:Text></s:Reason>` +
			`</s:Fault></s:Body></s:Envelope>`))
	})
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := TestONVIFCredential(ctx, client, provider, "dev-1", server.URL)
	if result.State != StateInvalid {
		t.Fatalf("state = %q for a SOAP auth fault, want %q (err=%v)", result.State, StateInvalid, result.Err)
	}
	if result.Err != nil && strings.Contains(result.Err.Error(), w7Password) {
		t.Errorf("the password leaked into the result error: %v", result.Err)
	}
}

// An unreachable device and a rejected credential must not collapse into the
// same state: that distinction is what lets an operator tell "wrong password"
// from "camera is off".
func TestW7_UnreachableDeviceIsNotReportedAsABadCredential(t *testing.T) {
	provider := newTestProvider(t, cameracreds.Credential{
		ID:            "cred-1",
		Scope:         cameracreds.ScopeDevice,
		CandidateKeys: []string{"dev-1"},
		Username:      "admin",
		Password:      "whatever",
		Revision:      1,
	})

	// A port nothing listens on: a real ECONNREFUSED.
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := srv.URL
	srv.Close()

	client := onvif.NewClient(2*time.Second, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := TestONVIFCredential(ctx, client, provider, "dev-1", deadURL)
	if result.State == StateInvalid {
		t.Fatalf("an unreachable device was reported as an invalid credential (err=%v)", result.Err)
	}
	if result.State != StateUnreachable && result.State != StateError {
		t.Fatalf("state = %q for an unreachable device, want UNREACHABLE or ERROR", result.State)
	}
}
