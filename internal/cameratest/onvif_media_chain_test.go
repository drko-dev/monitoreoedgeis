package cameratest

// Hito W2: exercises the full discovery -> ONVIF metadata/profiles -> RTSP
// URI sanitized flow through TestONVIFCredential (the real production
// function under test), using the reusable onviftest.Simulator fixture
// instead of ad hoc httptest handlers returning empty bodies. Unlike
// TestTestONVIFCredential_DeviceScopeValid in onvif_test.go (which stops
// short of asserting real profile/StreamURI data), this proves the chain
// actually reaches a sanitized RTSP URI backed by real media metadata.

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/cameracreds"
	"github.com/drko-dev/monitoreoedgeis/internal/discovery/onvif"
	"github.com/drko-dev/monitoreoedgeis/internal/onviftest"
)

func TestTestONVIFCredential_FullMediaChainWithSanitizedStreamURI(t *testing.T) {
	device := onviftest.Device{
		Manufacturer:    "Reolink",
		Model:           "RLC-810A",
		FirmwareVersion: "3.0.0.1",
		SerialNumber:    "SN-W2-CHAIN",
		Profiles: []onviftest.Profile{
			{
				Token: "main", Name: "MainStream", Codec: "H264",
				Width: 2560, Height: 1440, FPS: 25,
				StreamURI: "rtsp://camuser:camsecret@203.0.113.5:554/h264Preview_01_main",
			},
		},
	}
	sim := onviftest.NewSimulator(device)
	defer sim.Close()

	provider := newTestProvider(t, cameracreds.Credential{
		ID:            "cred-w2-chain",
		Scope:         cameracreds.ScopeDevice,
		CandidateKeys: []string{"stable-id-w2-chain"},
		Username:      "admin",
		Password:      "correct-pw",
		Revision:      1,
	})

	client := onvif.NewClient(2*time.Second, nil)
	client.SetXAddrValidator(func(raw string) (*url.URL, int, error) {
		u, err := url.Parse(raw)
		return u, 80, err
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := TestONVIFCredential(ctx, client, provider, "stable-id-w2-chain", sim.DeviceXAddr())

	if result.State != StateValid {
		t.Fatalf("expected VALID, got %v (err=%v)", result.State, result.Err)
	}
	if result.Manufacturer != device.Manufacturer || result.Model != device.Model {
		t.Errorf("unexpected device info: %+v", result)
	}
	if result.ProfileCount != 1 {
		t.Fatalf("expected 1 profile, got %d", result.ProfileCount)
	}
	if result.StreamURI == "" {
		t.Fatal("expected a non-empty sanitized StreamURI")
	}
	if strings.Contains(result.StreamURI, "camuser") || strings.Contains(result.StreamURI, "camsecret") {
		t.Fatalf("credentials leaked into sanitized StreamURI: %q", result.StreamURI)
	}
	if result.StreamURI != "rtsp://203.0.113.5:554/h264Preview_01_main" {
		t.Fatalf("unexpected sanitized StreamURI: %q", result.StreamURI)
	}
}
