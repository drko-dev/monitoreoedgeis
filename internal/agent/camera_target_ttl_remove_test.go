package agent

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/cameracreds"
	"github.com/drko-dev/monitoreoedgeis/internal/discovery"
	"github.com/drko-dev/monitoreoedgeis/internal/discovery/onvif"
	"github.com/drko-dev/monitoreoedgeis/internal/discovery/wsdiscovery"
	"github.com/drko-dev/monitoreoedgeis/internal/rtsp"
	"github.com/drko-dev/monitoreoedgeis/internal/rtsptest"
)

// newG1BSilentEngine builds a second Engine sharing inv but whose
// WS-Discovery scanner finds nothing — standing in for the same camera
// having gone silent on the LAN, as opposed to a fresh scan that would just
// re-Upsert it and refresh LastSeen. It reuses the same fake ONVIF
// roundtripper the camera would otherwise be enriched through, which is
// simply never invoked because there are no raw candidates to enrich.
func newG1BSilentEngine(t *testing.T, inv *discovery.Inventory) *discovery.Engine {
	t.Helper()
	silentConn := func(net.IP) (wsdiscovery.PacketConn, error) {
		return &g1bFakePacketConn{}, nil // no packets: ReadFrom returns io.EOF immediately
	}
	scanner := wsdiscovery.NewScanner(silentConn)
	onvifClient := onvif.NewClient(2*time.Second, nil)
	return discovery.NewEngine(scanner, onvifClient, inv, nil, 200*time.Millisecond, nil)
}

// TestG1B_RemoveByInventoryTTL is the productive REMOVE-by-disappearance
// path the gate asked for, distinct from both G1-A's isolated
// Inventory.PruneExpired unit tests and G1-B's revoke test: a camera that
// simply stops responding on the LAN (not a credential problem) must still
// end up with its target removed and its Supervisor stopped, once ordinary
// TTL expiry catches up with it —
//
//	device disappears -> next successful scan's PruneExpired removes it
//	-> OnScanSuccess -> reconcile -> KnownCameras drops it -> Supervisor stops
//
// without waiting out the real 24h DeviceTTL.
func TestG1B_RemoveByInventoryTTL(t *testing.T) {
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
	newDiscModFor := func(e *discovery.Engine) *discovery.Module {
		mod, err := discovery.NewModule(discovery.ModuleOptions{
			Engine:   e,
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
		return mod
	}

	discMod := newDiscModFor(engine)
	engine.SetCredentialResolver(func(candidateKey string) (string, string, bool) {
		if reconciler == nil {
			return "", "", false
		}
		return reconciler.resolve(candidateKey)
	})
	reconciler = newCameraTargetReconciler(discMod, provider, rtspMgr, "sub", nil)

	// --- Setup: reach the precondition (a live, targeted camera)
	// deterministically, via manual reconcile() calls after each setup
	// scan — never via onCredentialsSynced's catch-up-rediscovery path,
	// which is late-credential convergence, a different guard already
	// covered by TestG1B_FullPipeline_LateCredentialConvergence. Doing it
	// that way here would make this test's setup itself depend on
	// OnScanSuccess, which is exactly what must NOT be true before the
	// removal phase below. -------------------------------------------
	if err := discMod.Rediscover(context.Background()); err != nil {
		t.Fatalf("Rediscover (learn candidate key, no credential yet): %v", err)
	}
	reconciler.reconcile() // no-op: still auth-required, no credential
	candidateKey := discMod.Engine().Inventory().List()[0].StableIdentity

	if _, err := store.Apply([]cameracreds.Credential{{
		ID:            "1",
		Scope:         cameracreds.ScopeDevice,
		CandidateKeys: []string{candidateKey},
		Username:      "admin",
		Password:      "s3cret",
		Revision:      1,
	}}); err != nil {
		t.Fatalf("Store.Apply: %v", err)
	}
	// An ordinary scan, now that a credential resolves: enrichSingleDevice
	// succeeds over WS-Security within this single scan (no catch-up
	// rediscovery needed), so the manual reconcile() right after it is
	// enough to deterministically populate the target.
	if err := discMod.Rediscover(context.Background()); err != nil {
		t.Fatalf("Rediscover (with credential): %v", err)
	}
	reconciler.reconcile()

	if got := rtspMgr.KnownCameras(); len(got) != 1 || got[0] != candidateKey {
		t.Fatalf("setup failed: KnownCameras = %v, want exactly [%s]", got, candidateKey)
	}

	// A single miss before TTL must NOT remove the camera: back-date it by
	// less than DeviceTTL, run a normal (still-responding) scan, and
	// confirm it survives. This scan naturally refreshes LastSeen too
	// (the camera answers again), which is itself part of the point: real
	// TTL expiry requires SUSTAINED absence, not one missed probe.
	inv := discMod.Engine().Inventory()
	inv.SetLastSeenForTests(candidateKey, time.Now().UTC().Add(-discovery.DeviceTTL+time.Hour))
	if err := discMod.Rediscover(context.Background()); err != nil {
		t.Fatalf("Rediscover (still within TTL): %v", err)
	}
	reconciler.reconcile()
	if got := rtspMgr.KnownCameras(); len(got) != 1 {
		t.Fatalf("KnownCameras = %v after a scan still within DeviceTTL, want the camera to survive", got)
	}

	// --- Now the camera actually goes silent and ages past DeviceTTL ----
	inv.SetLastSeenForTests(candidateKey, time.Now().UTC().Add(-discovery.DeviceTTL-time.Hour))

	silentEngine := newG1BSilentEngine(t, inv) // shares inv; scanner finds nothing
	discModSilent := newDiscModFor(silentEngine)
	reconciler = newCameraTargetReconciler(discModSilent, provider, rtspMgr, "sub", nil)

	// Precondition for the removal phase under test: the camera is still
	// live in rtsp.Manager right before the silent scan runs.
	if got := rtspMgr.KnownCameras(); len(got) != 1 || got[0] != candidateKey {
		t.Fatalf("precondition failed: KnownCameras = %v before the silent scan, want exactly [%s]", got, candidateKey)
	}

	// Deliberately no manual reconciler.reconcile() call here. The only
	// thing that may remove the camera from this point on is the real
	// production path: PruneExpired (inside RunScan) -> OnScanSuccess ->
	// reconcile -> SetTargets -> Supervisor stopped. A manual reconcile
	// call after Rediscover would make this pass even if OnScanSuccess
	// were silently broken — see the sensitivity check below, which
	// exists specifically to catch that.
	if err := discModSilent.Rediscover(context.Background()); err != nil {
		t.Fatalf("Rediscover (silent, past TTL): %v", err)
	}

	g1bWaitFor(t, "camera target to be removed after TTL expiry via OnScanSuccess alone", 3*time.Second, func() bool {
		return len(rtspMgr.KnownCameras()) == 0
	})
	if d := inv.Get(candidateKey); d != nil {
		t.Errorf("Inventory still holds %s after it should have been pruned by TTL", candidateKey)
	}
}
