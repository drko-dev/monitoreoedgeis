//go:build integration

// Real end-to-end acceptance test for Hito I's first slice (Edge -> SaaS
// frame push). Reuses the exact TC70 RTSP/RTP/decode/sample/resize chain
// TestIntegration_VideoPipeline (Hito H) already validates, and adds a real
// cloudsink.CloudSink pointed at a locally-run SaaS + Cloud Vision Worker
// stack instead of the DebugSink-only wiring. Never a second RTSP client:
// the SaaS/worker never dial the camera in this test — only this Go
// process does, exactly like Hito H.
//
// Skipped unless TAPO_ONVIF_USER/PASS and GEOCAM_E2E_SAAS_URL /
// GEOCAM_E2E_DEVICE_ID / GEOCAM_E2E_DEVICE_KEY / GEOCAM_E2E_CANDIDATE_KEY
// are all set. Credentials are read only via os.Getenv and never logged.
//
//	source ~/.tapo_test_creds.env
//	export GEOCAM_E2E_SAAS_URL=http://127.0.0.1:8100
//	export GEOCAM_E2E_DEVICE_ID=... GEOCAM_E2E_DEVICE_KEY=... GEOCAM_E2E_CANDIDATE_KEY=...
//	go test -tags integration ./internal/cameratest/... -run TestIntegration_CloudFramePush -v -timeout 90s
package cameratest

import (
	"context"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/cameracreds"
	"github.com/drko-dev/monitoreoedgeis/internal/cloudsink"
	"github.com/drko-dev/monitoreoedgeis/internal/discovery/onvif"
	"github.com/drko-dev/monitoreoedgeis/internal/processing"
	"github.com/drko-dev/monitoreoedgeis/internal/rtsp"
	"github.com/drko-dev/monitoreoedgeis/internal/rtsptest"
	"github.com/drko-dev/monitoreoedgeis/internal/transport"
)

func TestIntegration_CloudFramePush(t *testing.T) {
	user := os.Getenv("TAPO_ONVIF_USER")
	pass := os.Getenv("TAPO_ONVIF_PASS")
	saasURL := os.Getenv("GEOCAM_E2E_SAAS_URL")
	deviceID := os.Getenv("GEOCAM_E2E_DEVICE_ID")
	deviceKey := os.Getenv("GEOCAM_E2E_DEVICE_KEY")
	candidateKey := os.Getenv("GEOCAM_E2E_CANDIDATE_KEY")
	if user == "" || pass == "" || saasURL == "" || deviceID == "" || deviceKey == "" || candidateKey == "" {
		t.Skip("TAPO_ONVIF_USER/PASS and/or GEOCAM_E2E_SAAS_URL/DEVICE_ID/DEVICE_KEY/CANDIDATE_KEY not set; skipping real cloud-push E2E test")
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
	if _, err := store.Apply([]cameracreds.Credential{{
		ID:            "e2e-cred",
		Scope:         cameracreds.ScopeDevice,
		CandidateKeys: []string{candidateKey},
		Username:      user,
		Password:      pass,
		Revision:      1,
	}}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	provider := cameracreds.NewProvider(store)
	cred, ok := provider.Resolve(candidateKey, "")
	if !ok {
		t.Fatal("no credential resolved for test candidate")
	}

	client := onvif.NewClient(5*time.Second, nil)
	client.SetXAddrValidator(func(raw string) (*url.URL, int, error) {
		u, err := url.Parse(raw)
		if err != nil {
			return nil, 0, err
		}
		return u, 2020, nil
	})

	xaddr := "http://" + host + "/onvif/device_service"
	discoverCtx, discoverCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer discoverCancel()

	if _, err := client.GetDeviceInformationAuth(discoverCtx, xaddr, cred.Username, cred.Password); err != nil {
		t.Fatalf("GetDeviceInformationAuth: %v", err)
	}
	mediaXAddr, capErr := client.GetCapabilitiesAuth(discoverCtx, xaddr, cred.Username, cred.Password)
	if capErr != nil || mediaXAddr == "" {
		mediaXAddr = xaddr
	}
	profiles, err := client.GetProfilesAuth(discoverCtx, mediaXAddr, cred.Username, cred.Password)
	if err != nil {
		t.Fatalf("GetProfilesAuth: %v", err)
	}
	if len(profiles) == 0 {
		t.Fatal("camera returned zero media profiles")
	}

	profile := profiles[0]
	for _, p := range profiles {
		if p.Role == "sub" {
			profile = p
			break
		}
	}
	t.Logf("selected profile: role=%s codec=%s %dx%d fps=%g", profile.Role, profile.Codec, profile.Width, profile.Height, profile.FPS)

	streamURI, err := client.GetStreamUriAuth(discoverCtx, mediaXAddr, profile.Token, cred.Username, cred.Password)
	if err != nil {
		t.Fatalf("GetStreamUriAuth: %v", err)
	}
	addr, rtspPath, err := rtsptest.ParseTarget(streamURI)
	if err != nil {
		t.Fatalf("ParseTarget: %v", err)
	}

	rtspCfg := rtsp.Config{
		StreamRole:     "sub",
		PacketTimeout:  5 * time.Second,
		InitialBackoff: 1 * time.Second,
		MaxBackoff:     10 * time.Second,
		DialTimeout:    5 * time.Second,
		Enabled:        true,
	}
	rtspMgr := rtsp.NewManager(rtspCfg, nil, nil)

	// Real transport.Client against the locally-run SaaS, exactly like
	// production (Bearer credential, X-Device-Id header) — never a mock.
	tc, err := transport.New(saasURL, true, 5*time.Second, "e2e-test")
	if err != nil {
		t.Fatalf("transport.New: %v", err)
	}
	cSink := cloudsink.New(tc, deviceID, deviceKey, nil)

	procCfg := processing.Config{
		Enabled:                true,
		TargetFPS:              2,
		OutputWidth:            320,
		OutputHeight:           180,
		RingBufferSize:         10,
		QueueDepth:             64,
		DecodeQueueDepth:       4,
		MaxConcurrentPipelines: 2,
		FFmpegPath:             "ffmpeg",
		DecodeTimeout:          10 * time.Second,
	}
	health := &capturingHealthSink{}
	procMgr := processing.NewManager(procCfg, rtspMgr, health, nil, cSink)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := rtspMgr.Start(ctx); err != nil {
		t.Fatalf("rtspMgr.Start: %v", err)
	}
	if err := procMgr.Start(ctx); err != nil {
		t.Fatalf("procMgr.Start: %v", err)
	}

	rtspMgr.SetTargets([]rtsp.CameraTarget{{
		CandidateKey: candidateKey,
		Addr:         addr,
		RTSPPath:     rtspPath,
		Username:     cred.Username,
		Password:     cred.Password,
		StreamRole:   profile.Role,
		Codec:        profile.Codec,
		Width:        profile.Width,
		Height:       profile.Height,
		FPS:          profile.FPS,
	}})

	const minRunTime = 30 * time.Second
	const maxWait = 45 * time.Second
	deadline := time.Now().Add(maxWait)
	start := time.Now()

	var lastStatus processing.PipelineStatus
	for time.Now().Before(deadline) {
		time.Sleep(2 * time.Second)

		snap := health.last
		for _, s := range snap.Cameras {
			if s.CandidateKey == candidateKey {
				lastStatus = s
			}
		}
		t.Logf("elapsed=%s state=%s frames_decoded=%d frames_sampled=%d frames_dropped=%d",
			time.Since(start).Round(time.Second), lastStatus.State, lastStatus.FramesDecoded, lastStatus.FramesSampled, lastStatus.FramesDropped)

		if time.Since(start) >= minRunTime && lastStatus.FramesSampled > 0 {
			break
		}
	}

	if lastStatus.FramesSampled == 0 {
		t.Fatalf("no frames sampled within %s — cloud push acceptance not met (last status: %+v)", maxWait, lastStatus)
	}
	t.Logf("ACCEPTANCE: ran %s, frames_sampled=%d (each one attempted a POST /api/v1/edge/frames to %s) — check the SaaS/worker logs and DB for delivery/detection results",
		time.Since(start).Round(time.Second), lastStatus.FramesSampled, saasURL)

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopCancel()
	if err := procMgr.Stop(stopCtx); err != nil {
		t.Errorf("procMgr.Stop: %v", err)
	}
	if err := rtspMgr.Stop(stopCtx); err != nil {
		t.Errorf("rtspMgr.Stop: %v", err)
	}
}
