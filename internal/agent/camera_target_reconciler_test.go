package agent

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/cameracreds"
	"github.com/drko-dev/monitoreoedgeis/internal/discovery"
	"github.com/drko-dev/monitoreoedgeis/internal/discovery/onvif"
	"github.com/drko-dev/monitoreoedgeis/internal/discovery/wsdiscovery"
	"github.com/drko-dev/monitoreoedgeis/internal/rtsp"
	"github.com/drko-dev/monitoreoedgeis/internal/rtsptest"
)

// --- fakes shared by this file's tests -------------------------------------
//
// These mirror internal/discovery/module_test.go's own fakePacketConn and
// roundTripFunc exactly (same technique, package-private on both sides —
// there is nothing to import, only to reproduce), so the WS-Discovery
// UDP layer and the ONVIF HTTP layer both stay entirely in-process.

type g1bFakePacketConn struct {
	mu      sync.Mutex
	packets [][]byte
	index   int
}

func (f *g1bFakePacketConn) WriteTo(p []byte, addr net.Addr) (int, error) { return len(p), nil }

func (f *g1bFakePacketConn) ReadFrom(p []byte) (int, net.Addr, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.index >= len(f.packets) {
		return 0, nil, io.EOF
	}
	pkt := f.packets[f.index]
	f.index++
	copy(p, pkt)
	return len(pkt), &net.UDPAddr{IP: net.ParseIP("192.168.1.50"), Port: 3702}, nil
}

func (f *g1bFakePacketConn) SetReadDeadline(time.Time) error { return nil }
func (f *g1bFakePacketConn) Close() error                    { return nil }

type g1bRoundTripFunc func(req *http.Request) (*http.Response, error)

func (f g1bRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

const g1bProbeMatchXML = `<?xml version="1.0" encoding="UTF-8"?>
<soap:Envelope xmlns:soap="http://www.w3.org/2003/05/soap-envelope" xmlns:d="http://schemas.xmlsoap.org/ws/2005/04/discovery">
  <soap:Body>
    <d:ProbeMatches>
      <d:ProbeMatch>
        <d:XAddrs>http://192.168.1.50:80/onvif/device_service</d:XAddrs>
      </d:ProbeMatch>
    </d:ProbeMatches>
  </soap:Body>
</soap:Envelope>`

// newG1BFakeCamera builds a discovery.Engine wired to a fake single-source
// ONVIF camera at http://192.168.1.50:80/onvif/device_service:
//
//   - GetDeviceInformation (anonymous) always returns ErrAuthRequired — this
//     camera never allows anonymous access, matching section 5's premise.
//   - Every *Auth call succeeds once a WS-Security UsernameToken is present
//     in the request body, regardless of which username/password was used
//     (a real device would reject a wrong password; this fake only needs to
//     prove the wiring calls the right methods in the right order — the
//     cameracreds/onvif packages' own tests already cover wrong-password
//     rejection at the transport layer).
//   - GetStreamUriAuth returns rtsp://<streamAddr>/live, so the resulting
//     CameraTarget points at a real internal/rtsptest.Simulator.
func newG1BFakeCamera(t *testing.T, streamAddr string) *discovery.Engine {
	t.Helper()

	newConn := func(net.IP) (wsdiscovery.PacketConn, error) {
		// A fresh conn per scan, each replaying the same single
		// ProbeMatch — real WS-Discovery re-announces on every probe.
		return &g1bFakePacketConn{packets: [][]byte{[]byte(g1bProbeMatchXML)}}, nil
	}
	scanner := wsdiscovery.NewScanner(newConn)

	roundTripper := g1bRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(req.Body)
		bodyStr := string(body)
		authenticated := strings.Contains(bodyStr, "<wsse:UsernameToken>")

		respond := func(status int, xml string) (*http.Response, error) {
			return &http.Response{
				StatusCode: status,
				Body:       io.NopCloser(strings.NewReader(xml)),
				Header:     make(http.Header),
			}, nil
		}

		switch {
		case strings.Contains(bodyStr, "GetDeviceInformation"):
			if !authenticated {
				return respond(http.StatusUnauthorized, `{"detail":"auth required"}`)
			}
			return respond(http.StatusOK, `<?xml version="1.0" encoding="utf-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:tds="http://www.onvif.org/ver10/device/wsdl">
  <s:Body><tds:GetDeviceInformationResponse>
    <tds:Manufacturer>G1BFake</tds:Manufacturer>
    <tds:Model>CamOne</tds:Model>
  </tds:GetDeviceInformationResponse></s:Body>
</s:Envelope>`)
		case strings.Contains(bodyStr, "GetCapabilities"):
			return respond(http.StatusOK, `<?xml version="1.0" encoding="utf-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:tds="http://www.onvif.org/ver10/device/wsdl">
  <s:Body><tds:GetCapabilitiesResponse><tds:Capabilities>
    <tt:Media xmlns:tt="http://www.onvif.org/ver10/schema"><tt:XAddr>http://192.168.1.50:80/onvif/media_service</tt:XAddr></tt:Media>
  </tds:Capabilities></tds:GetCapabilitiesResponse></s:Body>
</s:Envelope>`)
		case strings.Contains(bodyStr, "GetVideoSources"):
			return respond(http.StatusOK, `<?xml version="1.0" encoding="utf-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:trt="http://www.onvif.org/ver10/media/wsdl">
  <s:Body><trt:GetVideoSourcesResponse>
    <trt:VideoSources token="vs0"/>
  </trt:GetVideoSourcesResponse></s:Body>
</s:Envelope>`)
		case strings.Contains(bodyStr, "GetProfiles"):
			return respond(http.StatusOK, `<?xml version="1.0" encoding="utf-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:trt="http://www.onvif.org/ver10/media/wsdl">
  <s:Body><trt:GetProfilesResponse>
    <trt:Profiles token="profile_sub">
      <tt:Name xmlns:tt="http://www.onvif.org/ver10/schema">SubStream</tt:Name>
      <tt:VideoEncoderConfiguration xmlns:tt="http://www.onvif.org/ver10/schema">
        <tt:Encoding>H264</tt:Encoding>
        <tt:Resolution><tt:Width>640</tt:Width><tt:Height>360</tt:Height></tt:Resolution>
        <tt:RateControl><tt:FrameRateLimit>15</tt:FrameRateLimit></tt:RateControl>
      </tt:VideoEncoderConfiguration>
    </trt:Profiles>
  </trt:GetProfilesResponse></s:Body>
</s:Envelope>`)
		case strings.Contains(bodyStr, "GetStreamUri"):
			return respond(http.StatusOK, `<?xml version="1.0" encoding="utf-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:trt="http://www.onvif.org/ver10/media/wsdl">
  <s:Body><trt:GetStreamUriResponse><trt:MediaUri>
    <tt:Uri xmlns:tt="http://www.onvif.org/ver10/schema">rtsp://`+streamAddr+`/live</tt:Uri>
  </trt:MediaUri></trt:GetStreamUriResponse></s:Body>
</s:Envelope>`)
		default:
			return respond(http.StatusNotFound, `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body/></s:Envelope>`)
		}
	})

	onvifClient := onvif.NewClient(2*time.Second, nil)
	onvifClient.SetTransport(roundTripper)

	inv := discovery.NewInventory()
	return discovery.NewEngine(scanner, onvifClient, inv, nil, 200*time.Millisecond, nil)
}

// newG1BStore builds a real cameracreds.Store/Provider pair in a temp dir,
// so credential rotation/revoke can be exercised via Store.Apply directly —
// deliberately bypassing Syncer/network, since this suite is testing the
// reconciler's own wiring, not the SaaS fetch path (already covered
// elsewhere in internal/cameracreds).
func newG1BStore(t *testing.T) (*cameracreds.Store, *cameracreds.Provider) {
	t.Helper()
	dir := t.TempDir()
	key, err := cameracreds.LoadOrCreateMasterKey(dir)
	if err != nil {
		t.Fatalf("LoadOrCreateMasterKey: %v", err)
	}
	store, err := cameracreds.OpenStore(dir, key)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	return store, cameracreds.NewProvider(store)
}

func g1bWaitFor(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", what)
}

// TestG1B_FullPipeline_LateCredentialConvergence exercises, against real
// (not stubbed-out) internal/discovery, internal/cameracreds and
// internal/rtsp code, the exact race section 6 describes:
//
//  1. Discovery finds a camera that rejects anonymous ONVIF access. No
//     credential exists yet, so it stays AuthRequired with no target (C).
//  2. A camera credential becomes available (Store.Apply, standing in for
//     a successful SaaS sync) for that device's candidate key.
//  3. Without any Agent restart, the reconciler's OnSuccess handler
//     detects the now-resolvable auth-required device and triggers a
//     catch-up rediscovery (E), which retries the camera over WS-Security
//     (D) and gets real profiles/StreamURI this time.
//  4. The resulting CameraTarget reaches rtsp.Manager.SetTargets, and a
//     real internal/rtsptest.Simulator on the other end proves the whole
//     chain — discovery -> builder -> SetTargets -> supervisor -> RTSP ->
//     PacketSink — actually moves packets (J).
//
// It then continues from that converged state to exercise credential
// ROTATION (H) and REVOKE (I) against the same live target.
func TestG1B_FullPipeline_LateCredentialConvergence(t *testing.T) {
	sim, err := rtsptest.NewSimulator(rtsptest.Options{
		AutoPacketCount:    50,
		AutoPacketInterval: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("rtsptest.NewSimulator: %v", err)
	}
	defer sim.Close()

	engine := newG1BFakeCamera(t, sim.Addr())
	store, provider := newG1BStore(t)

	rtspMgr := rtsp.NewManager(rtsp.Config{
		StreamRole:     "sub",
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
		Interval: time.Hour, // no periodic loop; this test drives scans explicitly
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
	reconciler = newCameraTargetReconciler(discMod, provider, rtspMgr, "sub", nil)

	// --- Phase 1: first scan, no credential yet (section 14.C) ------------
	if err := discMod.Rediscover(context.Background()); err != nil {
		t.Fatalf("Rediscover (phase 1): %v", err)
	}
	reconciler.reconcile()

	devices := discMod.Engine().Inventory().List()
	if len(devices) != 1 {
		t.Fatalf("expected 1 discovered device, got %d", len(devices))
	}
	candidateKey := devices[0].StableIdentity
	if !devices[0].AuthRequired {
		t.Fatal("expected the fake camera to be AuthRequired after an anonymous-only scan")
	}
	if len(devices[0].VideoSources) != 0 {
		t.Fatalf("expected no VideoSources before any credential exists, got %+v", devices[0].VideoSources)
	}
	if got := rtspMgr.KnownCameras(); len(got) != 0 {
		t.Fatalf("expected no camera targets yet, got %v", got)
	}

	// --- Phase 2: credential becomes available; must converge without a
	// restart (sections 6 and 14.E) -----------------------------------
	if _, err := store.Apply([]cameracreds.Credential{{
		ID:            "1",
		Scope:         cameracreds.ScopeDevice,
		CandidateKeys: []string{candidateKey},
		Username:      "admin",
		Password:      "s3cret-v1",
		Revision:      1,
	}}); err != nil {
		t.Fatalf("Store.Apply (initial credential): %v", err)
	}
	reconciler.onCredentialsSynced() // == cameracreds.SyncOptions.OnSuccess

	g1bWaitFor(t, "camera target to appear after late credential convergence", 3*time.Second, func() bool {
		return len(rtspMgr.KnownCameras()) == 1
	})

	dev := discMod.Engine().Inventory().Get(candidateKey)
	if dev == nil || len(dev.VideoSources) != 1 {
		t.Fatalf("expected the catch-up rediscovery to populate exactly 1 VideoSource, got %+v", dev)
	}

	// --- Phase 3 (section 14.J): the target actually streams -------------
	var finalSnap rtsp.CameraStreamStatus
	g1bWaitFor(t, "RTSP supervisor to reach ONLINE with packets", 3*time.Second, func() bool {
		for _, s := range rtspMgr.Snapshot() {
			if s.CandidateKey == candidateKey && s.Status == rtsp.StateOnline && s.PacketsReceived > 0 {
				finalSnap = s
				return true
			}
		}
		return false
	})
	if finalSnap.PacketsReceived == 0 {
		t.Fatal("expected packets to have reached the processing-facing PacketSink path via the real Supervisor")
	}

	// A later reconcile must be a no-op for "needs rediscovery": the device
	// is fully enriched now, so it never re-triggers rediscovery again
	// (no infinite rediscovery loop).
	if reconciler.hasNewlyResolvableAuthDevice() {
		t.Error("hasNewlyResolvableAuthDevice() = true after successful convergence, want false (would cause repeated rediscovery)")
	}

	// --- Phase 4 (section 14.H): credential ROTATION ----------------------
	packetsBeforeRotation := finalSnap.PacketsReceived
	if _, err := store.Apply([]cameracreds.Credential{{
		ID:            "1",
		Scope:         cameracreds.ScopeDevice,
		CandidateKeys: []string{candidateKey},
		Username:      "admin",
		Password:      "s3cret-v2-rotated",
		Revision:      2,
	}}); err != nil {
		t.Fatalf("Store.Apply (rotation): %v", err)
	}
	reconciler.onCredentialsSynced()

	// Proof the supervisor actually RESTARTED with the new credential, not
	// just that Store/Provider changed underneath an untouched connection:
	// a fresh Supervisor's PacketsReceived starts over from 0, so it must
	// dip below the pre-rotation peak before climbing again. Asserting only
	// KnownCameras/Provider (as an earlier version of this test did) cannot
	// tell "reconcile ran" apart from "nothing happened but the store
	// changed" — this is why the rotation sensitivity check in
	// G1_CAMERA_TARGET_WIRING.md §11 needed this strengthened.
	g1bWaitFor(t, "supervisor to restart with a reset packet counter after rotation", 3*time.Second, func() bool {
		for _, s := range rtspMgr.Snapshot() {
			if s.CandidateKey == candidateKey && s.PacketsReceived < packetsBeforeRotation {
				return true
			}
		}
		return false
	})
	g1bWaitFor(t, "rotated supervisor to reconnect and reach ONLINE again", 3*time.Second, func() bool {
		for _, s := range rtspMgr.Snapshot() {
			if s.CandidateKey == candidateKey && s.Status == rtsp.StateOnline && s.PacketsReceived > 0 {
				return true
			}
		}
		return false
	})

	// Rotation must replace the one supervisor in place, never duplicate
	// or remove it.
	if got := rtspMgr.KnownCameras(); len(got) != 1 || got[0] != candidateKey {
		t.Fatalf("after rotation, KnownCameras = %v, want exactly [%s]", got, candidateKey)
	}
	cred, ok := provider.Resolve(candidateKey)
	if !ok || cred.Password != "s3cret-v2-rotated" {
		t.Fatalf("Provider.Resolve after rotation = %+v, ok=%v, want the rotated password", cred, ok)
	}

	// --- Phase 5 (section 14.I): credential REVOKE ------------------------
	if _, err := store.Apply(nil); err != nil {
		t.Fatalf("Store.Apply (revoke): %v", err)
	}
	reconciler.onCredentialsSynced()

	g1bWaitFor(t, "camera target to be removed after credential revoke", 3*time.Second, func() bool {
		return len(rtspMgr.KnownCameras()) == 0
	})
	if _, ok := provider.Resolve(candidateKey); ok {
		t.Fatal("Provider.Resolve still returns a credential after revoke")
	}
}
