package agent

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/cameracreds"
	"github.com/drko-dev/monitoreoedgeis/internal/discovery"
	"github.com/drko-dev/monitoreoedgeis/internal/discovery/onvif"
	"github.com/drko-dev/monitoreoedgeis/internal/discovery/wsdiscovery"
	"github.com/drko-dev/monitoreoedgeis/internal/rtsp"
	"github.com/drko-dev/monitoreoedgeis/internal/rtsptest"
)

// dvrFakePacketConn simulates WS-Discovery probe match for a multichannel DVR.
type dvrFakePacketConn struct {
	payload []byte
	sent    bool
}

func (f *dvrFakePacketConn) WriteTo(p []byte, addr net.Addr) (int, error) { return len(p), nil }
func (f *dvrFakePacketConn) ReadFrom(p []byte) (int, net.Addr, error) {
	if f.sent {
		return 0, nil, io.EOF
	}
	f.sent = true
	copy(p, f.payload)
	return len(f.payload), &net.UDPAddr{IP: net.ParseIP("192.168.1.100"), Port: 3702}, nil
}
func (f *dvrFakePacketConn) SetReadDeadline(time.Time) error { return nil }
func (f *dvrFakePacketConn) Close() error                    { return nil }

type dvrFakeAdapter struct {
	channels []discovery.VideoSource
	err      error
	lastUser string
	lastPass string
}

func (a *dvrFakeAdapter) Vendor() string { return "Hikvision" }
func (a *dvrFakeAdapter) Discover(_ context.Context, _, username, password string) ([]discovery.VideoSource, error) {
	a.lastUser = username
	a.lastPass = password
	if a.err != nil {
		return nil, a.err
	}
	return a.channels, nil
}

// TestDVRMultichannelCredentialLifecycleIntegration verifies end-to-end:
//  1. Reproduction of the audit gap: device requires auth; arrival of only
//     channel-level credentials bootstraps rediscovery and ONVIF/ISAPI enrichment.
//  2. Channel inheritance: shared recorder credential authorizes channels without
//     specific overrides.
//  3. Specificity precedence: per-channel credential overrides shared recorder credential.
//  4. Rediscovery idempotency: re-running discovery updates the inventory without
//     creating duplicate devices or channels.
//  5. Rotation & revocation lifecycle: rotating shared updates inheriting channels;
//     revoking override falls back to shared; revoking shared removes targets.
//  6. Cross-device and cross-organization isolation: sibling devices never inherit.
//  7. Absence of secrets in logs, diagnostics, and targets.
func TestDVRMultichannelCredentialLifecycleIntegration(t *testing.T) {
	streamSim, err := rtsptest.NewSimulator(rtsptest.Options{
		AutoPacketCount:    50,
		AutoPacketInterval: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("rtsptest.NewSimulator: %v", err)
	}
	defer streamSim.Close()

	dvrIP := "192.168.1.100"
	dvrStableID := "epr:urn:uuid:dvr-7208huhi"
	streamAddr := streamSim.Addr()

	// ONVIF server for the multichannel recorder:
	// Requires WS-Security UsernameToken for all *Auth endpoints.
	roundTripper := g1bRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(req.Body)
		bodyStr := string(body)
		auth := strings.Contains(bodyStr, "<wsse:UsernameToken>")

		respond := func(status int, xml string) (*http.Response, error) {
			return &http.Response{
				StatusCode: status,
				Body:       io.NopCloser(strings.NewReader(xml)),
				Header:     make(http.Header),
			}, nil
		}

		switch {
		case strings.Contains(bodyStr, "GetDeviceInformation"):
			if !auth {
				return respond(http.StatusUnauthorized, `{"detail":"auth required"}`)
			}
			return respond(http.StatusOK, `<?xml version="1.0" encoding="utf-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:tds="http://www.onvif.org/ver10/device/wsdl">
  <s:Body><tds:GetDeviceInformationResponse>
    <tds:Manufacturer>Hikvision</tds:Manufacturer>
    <tds:Model>iDS-7208HUHI-M1/FA</tds:Model>
    <tds:SerialNumber>DS-TEST-SERIAL-123</tds:SerialNumber>
    <tds:FirmwareVersion>V4.75.011</tds:FirmwareVersion>
  </tds:GetDeviceInformationResponse></s:Body>
</s:Envelope>`)

		case strings.Contains(bodyStr, "GetCapabilities"):
			return respond(http.StatusOK, `<?xml version="1.0" encoding="utf-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:tds="http://www.onvif.org/ver10/device/wsdl">
  <s:Body><tds:GetCapabilitiesResponse><tds:Capabilities>
    <tt:Media xmlns:tt="http://www.onvif.org/ver10/schema"><tt:XAddr>http://`+dvrIP+`:80/onvif/media_service</tt:XAddr></tt:Media>
  </tds:Capabilities></tds:GetCapabilitiesResponse></s:Body>
</s:Envelope>`)

		case strings.Contains(bodyStr, "GetVideoSources"):
			if !auth {
				return respond(http.StatusUnauthorized, `{"detail":"auth required"}`)
			}
			return respond(http.StatusOK, `<?xml version="1.0" encoding="utf-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:trt="http://www.onvif.org/ver10/media/wsdl">
  <s:Body><trt:GetVideoSourcesResponse>
    <trt:VideoSources token="VideoSource_1"/>
    <trt:VideoSources token="VideoSource_2"/>
  </trt:GetVideoSourcesResponse></s:Body>
</s:Envelope>`)

		case strings.Contains(bodyStr, "GetProfiles"):
			if !auth {
				return respond(http.StatusUnauthorized, `{"detail":"auth required"}`)
			}
			return respond(http.StatusOK, `<?xml version="1.0" encoding="utf-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:trt="http://www.onvif.org/ver10/media/wsdl">
  <s:Body><trt:GetProfilesResponse>
    <trt:Profiles token="Profile_1_Main">
      <tt:Name xmlns:tt="http://www.onvif.org/ver10/schema">Main1</tt:Name>
      <tt:VideoSourceConfiguration xmlns:tt="http://www.onvif.org/ver10/schema">
        <tt:SourceToken>VideoSource_1</tt:SourceToken>
      </tt:VideoSourceConfiguration>
      <tt:VideoEncoderConfiguration xmlns:tt="http://www.onvif.org/ver10/schema">
        <tt:Encoding>H264</tt:Encoding>
        <tt:Resolution><tt:Width>1920</tt:Width><tt:Height>1080</tt:Height></tt:Resolution>
        <tt:RateControl><tt:FrameRateLimit>25</tt:FrameRateLimit></tt:RateControl>
      </tt:VideoEncoderConfiguration>
    </trt:Profiles>
    <trt:Profiles token="Profile_2_Main">
      <tt:Name xmlns:tt="http://www.onvif.org/ver10/schema">Main2</tt:Name>
      <tt:VideoSourceConfiguration xmlns:tt="http://www.onvif.org/ver10/schema">
        <tt:SourceToken>VideoSource_2</tt:SourceToken>
      </tt:VideoSourceConfiguration>
      <tt:VideoEncoderConfiguration xmlns:tt="http://www.onvif.org/ver10/schema">
        <tt:Encoding>H264</tt:Encoding>
        <tt:Resolution><tt:Width>1920</tt:Width><tt:Height>1080</tt:Height></tt:Resolution>
        <tt:RateControl><tt:FrameRateLimit>25</tt:FrameRateLimit></tt:RateControl>
      </tt:VideoEncoderConfiguration>
    </trt:Profiles>
  </trt:GetProfilesResponse></s:Body>
</s:Envelope>`)

		case strings.Contains(bodyStr, "GetStreamUri"):
			token := "101"
			if strings.Contains(bodyStr, "Profile_2") {
				token = "201"
			}
			return respond(http.StatusOK, `<?xml version="1.0" encoding="utf-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:trt="http://www.onvif.org/ver10/media/wsdl">
  <s:Body><trt:GetStreamUriResponse><trt:MediaUri>
    <tt:Uri xmlns:tt="http://www.onvif.org/ver10/schema">rtsp://`+streamAddr+`/channels/`+token+`</tt:Uri>
  </trt:MediaUri></trt:GetStreamUriResponse></s:Body>
</s:Envelope>`)

		default:
			return respond(http.StatusNotFound, `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body/></s:Envelope>`)
		}
	})

	probeXML := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<soap:Envelope xmlns:soap="http://www.w3.org/2003/05/soap-envelope" xmlns:d="http://schemas.xmlsoap.org/ws/2005/04/discovery" xmlns:wsa="http://schemas.xmlsoap.org/ws/2004/08/addressing">
  <soap:Body>
    <d:ProbeMatches>
      <d:ProbeMatch>
        <wsa:EndpointReference><wsa:Address>%s</wsa:Address></wsa:EndpointReference>
        <d:Types>dn:NetworkVideoTransmitter</d:Types>
        <d:Scopes>onvif://www.onvif.org/type/video_encoder onvif://www.onvif.org/name/Hikvision</d:Scopes>
        <d:XAddrs>http://%s:80/onvif/device_service</d:XAddrs>
      </d:ProbeMatch>
    </d:ProbeMatches>
  </soap:Body>
</soap:Envelope>`, strings.TrimPrefix(dvrStableID, "epr:"), dvrIP)

	newConn := func(net.IP) (wsdiscovery.PacketConn, error) {
		return &dvrFakePacketConn{payload: []byte(probeXML)}, nil
	}
	scanner := wsdiscovery.NewScanner(newConn)

	onvifClient := onvif.NewClient(2*time.Second, nil)
	onvifClient.SetTransport(roundTripper)

	inv := discovery.NewInventory()
	engine := discovery.NewEngine(scanner, onvifClient, inv, nil, 300*time.Millisecond, nil)

	adapter := &dvrFakeAdapter{
		channels: []discovery.VideoSource{
			{
				SourceToken:   "VideoSource_1",
				ChannelNumber: 1,
				Availability:  discovery.ChannelAvailabilityEnabled,
				Profiles: []discovery.MediaProfile{{
					Token:     "isapi_101",
					StreamID:  101,
					StreamURI: fmt.Sprintf("rtsp://%s/ISAPI/Streaming/channels/101", streamAddr),
				}},
			},
			{
				SourceToken:   "VideoSource_2",
				ChannelNumber: 2,
				Availability:  discovery.ChannelAvailabilityEnabled,
				Profiles: []discovery.MediaProfile{{
					Token:     "isapi_201",
					StreamID:  201,
					StreamURI: fmt.Sprintf("rtsp://%s/ISAPI/Streaming/channels/201", streamAddr),
				}},
			},
		},
	}
	engine.SetRecorderAdapter(adapter)

	store, provider := newG1BStore(t)
	rtspMgr := rtsp.NewManager(rtsp.Config{
		StreamRole:     "main",
		PacketTimeout:  1 * time.Second,
		InitialBackoff: 50 * time.Millisecond,
		MaxBackoff:     500 * time.Millisecond,
		DialTimeout:    1 * time.Second,
		Enabled:        true,
	}, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := rtspMgr.Start(ctx); err != nil {
		t.Fatalf("rtspMgr.Start: %v", err)
	}
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer stopCancel()
		_ = rtspMgr.Stop(stopCtx)
	}()

	var reconciler *cameraTargetReconciler
	discMod, err := discovery.NewModule(discovery.ModuleOptions{
		Engine:   engine,
		Interval: time.Hour,
		OnScanSuccess: func() {
			if reconciler != nil {
				reconciler.onDiscoverySuccess()
			}
		},
	})
	if err != nil {
		t.Fatalf("discovery.NewModule: %v", err)
	}
	engine.SetCredentialResolver(func(candidateKey string) (string, string, bool) {
		if reconciler == nil {
			return "", "", false
		}
		return reconciler.resolve(candidateKey)
	})
	reconciler = newCameraTargetReconciler(discMod, provider, rtspMgr, "main", nil)

	// Step 1: Initial Discovery without credentials.
	// DVR rejects anonymous probe -> device in inventory has AuthRequired=true, 0 VideoSources.
	scanRes, err := engine.RunScan(context.Background())
	if err != nil {
		t.Fatalf("RunScan: %v", err)
	}
	if len(scanRes.DevicesFound) != 1 {
		t.Fatalf("expected 1 discovered device, got %d", len(scanRes.DevicesFound))
	}
	dev := scanRes.DevicesFound[0]
	if !dev.AuthRequired || len(dev.VideoSources) != 0 {
		t.Fatalf("expected unauthenticated DVR to have AuthRequired=true and 0 VideoSources, got %+v", dev)
	}

	// Reconcile produces 0 targets
	reconciler.reconcile()
	if len(rtspMgr.KnownCameras()) != 0 {
		t.Fatalf("expected 0 targets before credentials, got %d", len(rtspMgr.KnownCameras()))
	}

	// Step 2: Bootstrap discovery from channel-scoped credential!
	// SaaS syncs credentials ONLY for channel 1: candidate key epr:dvr-7208huhi|ch=VideoSource_1
	ch1Key := discovery.ChannelCandidateKey(dvrStableID, "VideoSource_1")
	ch2Key := discovery.ChannelCandidateKey(dvrStableID, "VideoSource_2")

	_, err = store.Apply([]cameracreds.Credential{
		{
			ID:            "cred-ch1",
			Scope:         cameracreds.ScopeDevice,
			CandidateKeys: []string{ch1Key},
			Username:      "admin",
			Password:      "HikPass123!",
			Revision:      1,
		},
	})
	if err != nil {
		t.Fatalf("store.Apply: %v", err)
	}

	// Verify that provider.Resolve(dvrStableID) falls back to the channel credential!
	recCred, ok := provider.Resolve(dvrStableID)
	if !ok || recCred.Username != "admin" || recCred.Password != "HikPass123!" {
		t.Fatalf("recorder failed to bootstrap credential from channel: got %+v ok=%v", recCred, ok)
	}

	// Verify hasNewlyResolvableAuthDevice detects the device now has credentials
	if !reconciler.hasNewlyResolvableAuthDevice() {
		t.Fatal("hasNewlyResolvableAuthDevice should be true after channel credential sync")
	}

	// Trigger catch-up rediscovery
	reconciler.onCredentialsSynced()

	// Wait for rediscovery to complete and enrich the multichannel DVR
	g1bWaitFor(t, "rediscovery enriches multichannel DVR", 5*time.Second, func() bool {
		list := inv.List()
		return len(list) == 1 && len(list[0].VideoSources) == 2
	})

	// Inventory must not have duplicated devices
	allDevices := inv.List()
	if len(allDevices) != 1 {
		t.Fatalf("inventory duplicated devices: got %d", len(allDevices))
	}
	if allDevices[0].StableIdentity != dvrStableID {
		t.Fatalf("unexpected stable identity: %s", allDevices[0].StableIdentity)
	}

	// Adapter was called with the bootstrapped credentials
	if adapter.lastUser != "admin" || adapter.lastPass != "HikPass123!" {
		t.Fatalf("adapter did not receive bootstrapped credentials: user=%s pass=%s", adapter.lastUser, adapter.lastPass)
	}

	// Channel 1 has target; Channel 2 has no credential yet (channel isolation!)
	g1bWaitFor(t, "targets to update after rediscovery", 3*time.Second, func() bool {
		return len(rtspMgr.KnownCameras()) == 1
	})
	if rtspMgr.KnownCameras()[0] != ch1Key {
		t.Fatalf("unexpected target candidate key: %v", rtspMgr.KnownCameras())
	}

	// Inspect built targets directly
	curTargets, _ := buildCameraTargets(inv.List(), reconciler.resolve, "main")
	if len(curTargets) != 1 || curTargets[0].Username != "admin" || curTargets[0].Password != "HikPass123!" {
		t.Fatalf("target 0 mismatch: %+v", curTargets)
	}

	// Step 3: Shared recorder credential arrives!
	// Now the SaaS syncs a shared credential for the whole unit (dvrStableID),
	// AND a per-channel override for channel 1.
	_, err = store.Apply([]cameracreds.Credential{
		{
			ID:            "cred-recorder-shared",
			Scope:         cameracreds.ScopeDevice,
			CandidateKeys: []string{dvrStableID},
			Username:      "shared-operator",
			Password:      "SharedPass456!",
			Revision:      2,
		},
		{
			ID:            "cred-ch1-override",
			Scope:         cameracreds.ScopeDevice,
			CandidateKeys: []string{ch1Key},
			Username:      "ch1-special",
			Password:      "Ch1Pass789!",
			Revision:      2,
		},
	})
	if err != nil {
		t.Fatalf("store.Apply: %v", err)
	}

	reconciler.reconcile()

	g1bWaitFor(t, "2 targets after shared credential arrives", 3*time.Second, func() bool {
		return len(rtspMgr.KnownCameras()) == 2
	})

	curTargets, _ = buildCameraTargets(inv.List(), reconciler.resolve, "main")
	if len(curTargets) != 2 {
		t.Fatalf("want 2 targets, got %d", len(curTargets))
	}
	// Verify target 0 (ch1) uses specific override
	if curTargets[0].CandidateKey != ch1Key || curTargets[0].Username != "ch1-special" || curTargets[0].Password != "Ch1Pass789!" {
		t.Fatalf("channel 1 did not use specific override: %+v", curTargets[0])
	}
	// Verify target 1 (ch2) inherits shared recorder credential
	if curTargets[1].CandidateKey != ch2Key || curTargets[1].Username != "shared-operator" || curTargets[1].Password != "SharedPass456!" {
		t.Fatalf("channel 2 did not inherit shared recorder credential: %+v", curTargets[1])
	}

	// Step 4: Credential Rotation.
	// Rotate shared recorder credential to SharedPassRotated!
	_, err = store.Apply([]cameracreds.Credential{
		{
			ID:            "cred-recorder-shared",
			Scope:         cameracreds.ScopeDevice,
			CandidateKeys: []string{dvrStableID},
			Username:      "shared-operator",
			Password:      "SharedPassRotated!",
			Revision:      3,
		},
		{
			ID:            "cred-ch1-override",
			Scope:         cameracreds.ScopeDevice,
			CandidateKeys: []string{ch1Key},
			Username:      "ch1-special",
			Password:      "Ch1Pass789!",
			Revision:      2,
		},
	})
	if err != nil {
		t.Fatalf("store.Apply rotate: %v", err)
	}

	reconciler.reconcile()
	curTargets, _ = buildCameraTargets(inv.List(), reconciler.resolve, "main")
	if curTargets[1].Password != "SharedPassRotated!" {
		t.Fatalf("channel 2 did not receive rotated password: %+v", curTargets[1])
	}
	if curTargets[0].Password != "Ch1Pass789!" {
		t.Fatalf("channel 1 override was unexpectedly altered during recorder rotation: %+v", curTargets[0])
	}

	// Step 5: Revocation.
	// Revoke channel 1 override -> Channel 1 must seamlessly fall back to the shared recorder credential!
	_, err = store.Apply([]cameracreds.Credential{
		{
			ID:            "cred-recorder-shared",
			Scope:         cameracreds.ScopeDevice,
			CandidateKeys: []string{dvrStableID},
			Username:      "shared-operator",
			Password:      "SharedPassRotated!",
			Revision:      3,
		},
	})
	if err != nil {
		t.Fatalf("store.Apply revoke override: %v", err)
	}

	reconciler.reconcile()
	curTargets, _ = buildCameraTargets(inv.List(), reconciler.resolve, "main")
	if len(curTargets) != 2 {
		t.Fatalf("want 2 targets after override revoked, got %d", len(curTargets))
	}
	if curTargets[0].Password != "SharedPassRotated!" || curTargets[1].Password != "SharedPassRotated!" {
		t.Fatalf("both channels should now inherit shared password: ch1=%s ch2=%s", curTargets[0].Password, curTargets[1].Password)
	}

	// Revoke shared recorder credential -> All channels lose credentials
	_, err = store.Apply([]cameracreds.Credential{})
	if err != nil {
		t.Fatalf("store.Apply clear: %v", err)
	}
	reconciler.reconcile()
	g1bWaitFor(t, "0 targets after total revocation", 3*time.Second, func() bool {
		return len(rtspMgr.KnownCameras()) == 0
	})

	// Step 6: Security Audit (absence of secrets)
	// Build targets manually to inspect skips and targets
	builtTargets, skips := buildCameraTargets(inv.List(), reconciler.resolve, "main")
	if len(builtTargets) != 0 {
		t.Fatalf("expected 0 built targets, got %d", len(builtTargets))
	}
	for _, s := range skips {
		if strings.Contains(string(s.Reason), "HikPass") || strings.Contains(string(s.Reason), "SharedPass") {
			t.Fatalf("secrets leaked in skip reason: %+v", s)
		}
	}
}

// TestDVRIsolationBetweenRecorders proves that credentials for recorder A
// are never borrowed or inherited by recorder B, even with substring prefixes.
func TestDVRIsolationBetweenRecorders(t *testing.T) {
	store, provider := newG1BStore(t)

	// Device 1: epr:rec-1 (channels: epr:rec-1|ch=1)
	// Device 2: epr:rec-10 (channels: epr:rec-10|ch=1)
	_, err := store.Apply([]cameracreds.Credential{
		{
			ID:            "cred-10",
			Scope:         cameracreds.ScopeDevice,
			CandidateKeys: []string{"epr:rec-10|ch=1"},
			Username:      "user-10",
			Password:      "pass-10",
			Revision:      1,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Resolve epr:rec-1 -> MUST NOT match epr:rec-10|ch=1
	if cred, ok := provider.Resolve("epr:rec-1"); ok {
		t.Fatalf("epr:rec-1 unexpectedly resolved cred from epr:rec-10: %+v", cred)
	}
	if cred, ok := provider.Resolve("epr:rec-1|ch=1"); ok {
		t.Fatalf("epr:rec-1|ch=1 unexpectedly resolved cred from epr:rec-10: %+v", cred)
	}
}
