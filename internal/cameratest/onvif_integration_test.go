//go:build integration

// Package cameratest integration harness: runs the full credential-test flow
// (ONVIF GetDeviceInformation -> GetCapabilities -> GetProfiles ->
// GetStreamUri -> RTSP DESCRIBE with Digest) against a real camera.
//
// Skipped by default (requires the "integration" build tag). To run it
// against the Tapo TC70 used for Hito F Bloque 2 verification:
//
//	source ~/.tapo_test_creds.env   # sets TAPO_ONVIF_USER / TAPO_ONVIF_PASS
//	TAPO_ONVIF_HOST=192.168.0.6:2020 \
//	  go test -tags integration ./internal/cameratest/... -run TestIntegration_TapoONVIFAndRTSP -v
//
// No credential is hardcoded here; the test is skipped entirely when the
// required env vars are unset, and the harness never logs the password.
package cameratest

import (
	"context"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/cameracreds"
	"github.com/drko-dev/monitoreoedgeis/internal/discovery/onvif"
	"github.com/drko-dev/monitoreoedgeis/internal/rtsptest"
)

func TestIntegration_TapoONVIFAndRTSP(t *testing.T) {
	user := os.Getenv("TAPO_ONVIF_USER")
	pass := os.Getenv("TAPO_ONVIF_PASS")
	if user == "" || pass == "" {
		t.Skip("TAPO_ONVIF_USER / TAPO_ONVIF_PASS not set; skipping real-camera integration test")
	}
	host := os.Getenv("TAPO_ONVIF_HOST")
	if host == "" {
		host = "192.168.0.6:2020"
	}

	dir := t.TempDir()
	key := make([]byte, cameracreds.MasterKeySize)
	store, err := cameracreds.OpenStore(dir, key)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	const candidateKey = "integration-test-tapo-tc70"
	if _, err := store.Apply([]cameracreds.Credential{{
		ID:            "integration-cred",
		Scope:         cameracreds.ScopeDevice,
		CandidateKeys: []string{candidateKey},
		Username:      user,
		Password:      pass,
		Revision:      1,
	}}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	provider := cameracreds.NewProvider(store)

	client := onvif.NewClient(5*time.Second, nil)
	client.SetXAddrValidator(func(raw string) (*url.URL, int, error) {
		u, err := url.Parse(raw)
		if err != nil {
			return nil, 0, err
		}
		return u, 2020, nil
	})

	xaddr := "http://" + host + "/onvif/device_service"
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	result := TestONVIFCredential(ctx, client, provider, candidateKey, "", xaddr)
	t.Logf("ONVIF result: state=%s manufacturer=%s model=%s profiles=%d streamURI=%s err=%v",
		result.State, result.Manufacturer, result.Model, result.ProfileCount, result.StreamURI, result.Err)

	if result.State != StateValid || result.StreamURI == "" {
		t.Logf("ONVIF credential test did not reach a sanitized stream URI (state=%s); skipping RTSP DESCRIBE step", result.State)
		return
	}

	addr, path, err := rtsptest.ParseTarget(result.StreamURI)
	if err != nil {
		t.Fatalf("ParseTarget(%q): %v", result.StreamURI, err)
	}

	rtspResult := rtsptest.TestDescribe(ctx, addr, path, user, pass, 5*time.Second)
	t.Logf("RTSP DESCRIBE result: state=%s err=%v", rtspResult.State, rtspResult.Err)
}
