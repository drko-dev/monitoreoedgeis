package onviftest

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/discovery/onvif"
)

func testDevice() Device {
	return Device{
		Manufacturer:    "TP-LINK",
		Model:           "TC70",
		FirmwareVersion: "1.2.3",
		SerialNumber:    "SN12345",
		EPRAddress:      "urn:uuid:onviftest-device-1",
		Types:           "dn:NetworkVideoTransmitter",
		Scopes:          "onvif://www.onvif.org/hardware/TC70",
		Profiles: []Profile{
			{
				Token: "profile_main", Name: "MainStream", Codec: "H264",
				Width: 1920, Height: 1080, FPS: 15,
				StreamURI: "rtsp://admin:s3cr3t@192.0.2.10:554/stream1",
			},
			{
				Token: "profile_sub", Name: "SubStream", Codec: "H264",
				Width: 640, Height: 360, FPS: 15,
				StreamURI: "rtsp://admin:s3cr3t@192.0.2.10:554/stream2",
			},
		},
	}
}

func newRealClient(t *testing.T) *onvif.Client {
	t.Helper()
	client := onvif.NewClient(2*time.Second, nil)
	client.SetXAddrValidator(func(raw string) (*url.URL, int, error) {
		u, err := url.Parse(raw)
		return u, 80, err
	})
	return client
}

func TestSimulator_FullChain_DeviceInfoCapabilitiesProfilesStreamURI(t *testing.T) {
	device := testDevice()
	sim := NewSimulator(device)
	defer sim.Close()

	client := newRealClient(t)
	ctx := context.Background()

	info, err := client.GetDeviceInformation(ctx, sim.DeviceXAddr())
	if err != nil {
		t.Fatalf("GetDeviceInformation: %v", err)
	}
	if info.Manufacturer != device.Manufacturer || info.Model != device.Model {
		t.Fatalf("unexpected device info: %+v", info)
	}

	mediaXAddr, err := client.GetCapabilities(ctx, sim.DeviceXAddr())
	if err != nil {
		t.Fatalf("GetCapabilities: %v", err)
	}
	if mediaXAddr != sim.MediaXAddr() {
		t.Fatalf("expected media xaddr %q, got %q", sim.MediaXAddr(), mediaXAddr)
	}

	profiles, err := client.GetProfiles(ctx, mediaXAddr)
	if err != nil {
		t.Fatalf("GetProfiles: %v", err)
	}
	if len(profiles) != 2 {
		t.Fatalf("expected 2 profiles, got %d", len(profiles))
	}
	if profiles[0].Width != 1920 || profiles[0].Height != 1080 {
		t.Errorf("unexpected profile[0] dimensions: %+v", profiles[0])
	}

	uri, err := client.GetStreamUri(ctx, mediaXAddr, profiles[0].Token)
	if err != nil {
		t.Fatalf("GetStreamUri: %v", err)
	}

	// Security requirement (W4): the simulator's fixture deliberately
	// embeds credentials in StreamURI; production must sanitize them.
	if strings.Contains(uri, "admin") || strings.Contains(uri, "s3cr3t") {
		t.Fatalf("credentials leaked into sanitized stream URI: %q", uri)
	}
	if uri != "rtsp://192.0.2.10:554/stream1" {
		t.Fatalf("unexpected sanitized stream URI: %q", uri)
	}
}

func TestSimulator_RequireAuth_RejectsMissingCredentials(t *testing.T) {
	device := testDevice()
	device.RequireAuth = true
	device.Username = "admin"
	device.Password = "correct-password"
	sim := NewSimulator(device)
	defer sim.Close()

	client := newRealClient(t)
	if _, err := client.GetDeviceInformation(context.Background(), sim.DeviceXAddr()); err == nil {
		t.Fatal("expected error when calling an auth-required device unauthenticated")
	}
}

func TestSimulator_RequireAuth_AcceptsValidCredentials(t *testing.T) {
	device := testDevice()
	device.RequireAuth = true
	device.Username = "admin"
	device.Password = "correct-password"
	sim := NewSimulator(device)
	defer sim.Close()

	client := newRealClient(t)
	info, err := client.GetDeviceInformationAuth(context.Background(), sim.DeviceXAddr(), "admin", "correct-password")
	if err != nil {
		t.Fatalf("GetDeviceInformationAuth: %v", err)
	}
	if info.Manufacturer != device.Manufacturer {
		t.Fatalf("unexpected device info: %+v", info)
	}
}

func TestSimulator_RequireAuth_RejectsWrongPassword(t *testing.T) {
	device := testDevice()
	device.RequireAuth = true
	device.Username = "admin"
	device.Password = "correct-password"
	sim := NewSimulator(device)
	defer sim.Close()

	client := newRealClient(t)
	if _, err := client.GetDeviceInformationAuth(context.Background(), sim.DeviceXAddr(), "admin", "wrong-password"); err == nil {
		t.Fatal("expected error with wrong password")
	}
}
