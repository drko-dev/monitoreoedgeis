//go:build integration

package cameratest

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/rtsp"
)

func TestIntegration_TapoRTSPConnectivity(t *testing.T) {
	user := os.Getenv("TAPO_ONVIF_USER")
	pass := os.Getenv("TAPO_ONVIF_PASS")
	if user == "" || pass == "" {
		t.Skip("TAPO_ONVIF_USER / TAPO_ONVIF_PASS not set; skipping real-camera integration test")
	}

	addr := "192.168.0.6:554"
	rtspPath := "/stream2" // Substream 640x360 H.264

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	t.Logf("Connecting to RTSP %s%s with user %s...", addr, rtspPath, user)
	session, err := rtsp.Dial(ctx, addr, rtspPath, user, pass, 5*time.Second)
	if err != nil {
		t.Fatalf("rtsp.Dial failed: %v", err)
	}
	defer session.Teardown(2 * time.Second)

	t.Log("RTSP session established! Reading packets...")
	packetsRead := 0
	bytesRead := 0

	for i := 0; i < 20; i++ {
		channel, payload, err := session.ReadPacket(5 * time.Second)
		if err != nil {
			t.Fatalf("ReadPacket failed at packet %d: %v", i, err)
		}
		packetsRead++
		bytesRead += len(payload)
		t.Logf("Packet %d: channel=%d len=%d", i+1, channel, len(payload))
	}

	t.Logf("Successfully read %d packets (%d bytes) from live Tapo TC70 camera!", packetsRead, bytesRead)
	if packetsRead < 20 {
		t.Errorf("Expected 20 packets, got %d", packetsRead)
	}
}
