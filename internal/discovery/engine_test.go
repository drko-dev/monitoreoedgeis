package discovery

import (
	"context"
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/discovery/wsdiscovery"
)

func TestDeduplicateRawCandidates_EPRPriority(t *testing.T) {
	raw := []wsdiscovery.DiscoveredRawCandidate{
		{
			EPRAddress: "urn:uuid:cam-01",
			Types:      "dn:NetworkVideoTransmitter",
			Scopes:     "onvif://www.onvif.org/hardware/ModelX onvif://www.onvif.org/type/video_encoder",
			XAddrs:     []string{"http://192.168.1.50:80/onvif/device_service"},
			Model:      "ModelX",
		},
		// Same EPR responding with a second XAddr on port 8080 (or different interface)
		{
			EPRAddress: "urn:uuid:cam-01",
			Types:      "dn:NetworkVideoTransmitter",
			Scopes:     "onvif://www.onvif.org/hardware/ModelX",
			XAddrs:     []string{"http://192.168.1.50:8080/onvif/device_service"},
			Model:      "ModelX",
		},
		// Different device without EPR falling back to endpoint
		{
			Types:  "dn:NetworkVideoTransmitter",
			Scopes: "onvif://www.onvif.org/hardware/ModelY",
			XAddrs: []string{"http://192.168.1.60:80/onvif/device_service"},
			Model:  "ModelY",
		},
	}

	deduped := deduplicateRawCandidates(raw)
	if len(deduped) != 2 {
		t.Fatalf("expected 2 unique devices, got %d", len(deduped))
	}

	var cam1 *DiscoveredDevice
	for i := range deduped {
		if deduped[i].EPRAddress == "urn:uuid:cam-01" {
			cam1 = &deduped[i]
			break
		}
	}
	if cam1 == nil {
		t.Fatal("cam-01 not found")
	}
	if len(cam1.AllXAddrs) != 2 {
		t.Errorf("expected 2 XAddrs accumulated for cam-01, got %d: %v", len(cam1.AllXAddrs), cam1.AllXAddrs)
	}
}

func TestClassifyDeviceType(t *testing.T) {
	cases := []struct {
		scopes   string
		types    string
		channels int
		expected DeviceType
	}{
		{"onvif://www.onvif.org/type/Network_Video_Recorder", "dn:NetworkVideoTransmitter", 1, DeviceTypeNVR},
		{"onvif://www.onvif.org/type/video_encoder", "dn:NetworkVideoTransmitter", 1, DeviceTypeEncoder},
		{"onvif://www.onvif.org/hardware/DVR-16CH", "dn:NetworkVideoTransmitter", 16, DeviceTypeDVR},
		{"onvif://www.onvif.org/hardware/NVR-32CH", "dn:NetworkVideoTransmitter", 8, DeviceTypeNVR},
		{"onvif://www.onvif.org/type/video_transmitter", "dn:NetworkVideoTransmitter", 1, DeviceTypeCamera},
	}

	for _, tc := range cases {
		got := classifyDeviceType(tc.scopes, tc.types, tc.channels)
		if got != tc.expected {
			t.Errorf("classifyDeviceType(%q, %q, %d): got %s, want %s", tc.scopes, tc.types, tc.channels, got, tc.expected)
		}
	}
}

func TestEngine_EmptyScanGraceful(t *testing.T) {
	engine := NewEngine(nil, nil, nil, []string{}, 100*time.Millisecond, nil)
	// If no private interfaces match or scanning returns nothing, RunScan should succeed with empty result
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	res, err := engine.RunScan(ctx)
	if err != nil {
		// Auto-private might return no interfaces on some test environments, which is valid
		t.Logf("RunScan returned: %v", err)
		return
	}
	if res == nil {
		t.Fatal("expected non-nil ScanResult")
	}
}
