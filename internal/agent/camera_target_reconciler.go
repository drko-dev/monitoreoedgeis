package agent

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/cameracreds"
	"github.com/drko-dev/monitoreoedgeis/internal/discovery"
	"github.com/drko-dev/monitoreoedgeis/internal/rtsp"
)

// rediscoveryTimeout bounds the background rediscovery
// cameraTargetReconciler.maybeTriggerRediscovery starts after a camera
// credential sync makes a previously auth-required, unenriched device
// resolvable. It is independent of discovery's own configured scan
// timeout: this is a best-effort, one-shot catch-up scan, not the regular
// periodic one.
const rediscoveryTimeout = 30 * time.Second

// cameraTargetReconciler is Hito Z G1-B's reconciliation layer (section
// 10): a thin, stateless-between-calls glue that reads the current
// discovery Inventory snapshot and resolved camera credentials, builds
// targets with the pure buildCameraTargets, and hands them to
// rtsp.Manager.SetTargets — which already owns idempotent add/remove/
// rotation/shutdown-safety on its own. reconcile keeps no other mutable
// copy of the targets and takes no lock of its own around SetTargets.
type cameraTargetReconciler struct {
	disc        *discovery.Module
	provider    *cameracreds.Provider
	rtspMgr     *rtsp.Manager
	desiredRole string
	log         *slog.Logger

	// rediscovering guards against stacking background rediscoveries (see
	// maybeTriggerRediscovery) if several sync successes land in quick
	// succession; it does not replace Rediscover's own internal
	// serialization (scanMu), which still applies across every caller.
	rediscovering atomic.Bool
}

// newCameraTargetReconciler builds a reconciler. provider may be nil (an
// Edge with no camera-credentials subsystem, e.g. unenrolled) — reconcile
// then simply resolves no credentials, which is exactly correct for a
// deployment where every camera is either unauthenticated or permanently
// unreachable, matching the previous behavior.
func newCameraTargetReconciler(
	disc *discovery.Module,
	provider *cameracreds.Provider,
	rtspMgr *rtsp.Manager,
	desiredRole string,
	log *slog.Logger,
) *cameraTargetReconciler {
	if log == nil {
		log = slog.Default()
	}
	return &cameraTargetReconciler{
		disc:        disc,
		provider:    provider,
		rtspMgr:     rtspMgr,
		desiredRole: desiredRole,
		log:         log,
	}
}

// resolve adapts cameracreds.Provider.Resolve to discovery.CredentialResolver's
// shape. It returns ok=false, never a guessed default, when no credential
// resolves — including when the reconciler has no provider at all.
func (r *cameraTargetReconciler) resolve(candidateKey string) (username, password string, ok bool) {
	if r.provider == nil {
		return "", "", false
	}
	cred, ok := r.provider.Resolve(candidateKey)
	if !ok {
		return "", "", false
	}
	return cred.Username, cred.Password, true
}

// reconcile reads the Inventory snapshot, resolves credentials, builds
// targets, and applies them via rtsp.Manager.SetTargets. It is idempotent
// and safe to call repeatedly and concurrently: SetTargets is itself
// serialized and shutdown-safe.
//
// This single method is what both discovery's OnScanSuccess and
// cameracreds' OnSuccess call — ADD, REMOVE, ROTATION and REVOKE (sections
// 11.F-11.I) all fall out of re-running it against whatever changed,
// because SetTargets already diffs the desired set against the running
// one.
func (r *cameraTargetReconciler) reconcile() {
	if r.disc == nil || r.rtspMgr == nil {
		return
	}
	devices := r.disc.Engine().Inventory().List()
	targets, skips := buildCameraTargets(devices, r.resolve, r.desiredRole)

	for _, s := range skips {
		r.log.Debug("camera target reconciler: device skipped",
			slog.String("candidate_key", s.CandidateKey),
			slog.String("reason", string(s.Reason)),
		)
	}
	r.log.Info("camera target reconciler: applying targets",
		slog.Int("device_count", len(devices)),
		slog.Int("target_count", len(targets)),
		slog.Int("skipped_count", len(skips)),
	)

	r.rtspMgr.SetTargets(targets)
}

// onDiscoverySuccess is discovery.ModuleOptions.OnScanSuccess.
func (r *cameraTargetReconciler) onDiscoverySuccess() {
	r.reconcile()
}

// onCredentialsSynced is cameracreds.SyncOptions.OnSuccess (section 4/6).
//
// It always reconciles immediately against the already-known Inventory:
// that alone is enough to pick up a credential ROTATION or REVOKE for a
// device that was already successfully enriched (its profiles/StreamURI
// don't change, only which Username/Password the target carries).
//
// It additionally triggers a one-shot background rediscovery when — and
// only when — Inventory holds an auth-required device that has never been
// successfully authenticated-enriched (no VideoSources yet) and a
// credential now resolves for it. That is the real race from section 6:
// discovery ran first, the camera rejected the anonymous probe, and only
// later did a credential arrive. Reconciling alone cannot help such a
// device, because it has no profiles/StreamURI recorded at all yet — only
// a fresh scan can retry it over WS-Security. Once that catch-up scan
// succeeds, the device has VideoSources and this condition goes false for
// it, so a later sync tick does not keep re-triggering rediscovery
// (no infinite rediscovery loop).
func (r *cameraTargetReconciler) onCredentialsSynced() {
	r.reconcile()
	r.maybeTriggerRediscovery()
}

func (r *cameraTargetReconciler) maybeTriggerRediscovery() {
	if r.disc == nil {
		return
	}
	if !r.hasNewlyResolvableAuthDevice() {
		return
	}
	if !r.rediscovering.CompareAndSwap(false, true) {
		// A previous catch-up rediscovery is already in flight; do not
		// stack another background goroutine on top of it.
		return
	}

	go func() {
		defer r.rediscovering.Store(false)
		ctx, cancel := context.WithTimeout(context.Background(), rediscoveryTimeout)
		defer cancel()
		if err := r.disc.Rediscover(ctx); err != nil && !errors.Is(err, context.Canceled) {
			r.log.Warn("camera target reconciler: catch-up rediscovery failed",
				slog.Any("error", err))
			return
		}
		// Rediscover's own success already invoked OnScanSuccess
		// (onDiscoverySuccess -> reconcile), so nothing further is needed
		// here: the newly authenticated device's target, if any, is
		// already applied.
	}()
}

// hasNewlyResolvableAuthDevice reports whether Inventory holds at least one
// device that (a) requires authentication, (b) has NEVER been successfully
// authenticated-enriched — zero VideoSources recorded, meaning the
// anonymous probe failed and no authenticated retry has ever gotten past
// GetDeviceInformationAuth for it — and (c) a credential now resolves for
// it.
//
// The check is deliberately "zero VideoSources", not "not exactly one":
// once any authenticated enrichment attempt succeeds, VideoSources is
// populated (with exactly one entry for a real camera, or more than one for
// a genuine multichannel device G1 cannot use). Either outcome must never
// re-trigger rediscovery again on a later sync tick — a permanently
// multichannel authenticated NVR would otherwise never satisfy "== 1" and
// cause exactly the infinite rediscovery loop this method exists to avoid.
func (r *cameraTargetReconciler) hasNewlyResolvableAuthDevice() bool {
	if r.provider == nil {
		return false
	}
	for _, dev := range r.disc.Engine().Inventory().List() {
		if !dev.AuthRequired || len(dev.VideoSources) != 0 {
			continue
		}
		if _, _, ok := r.resolve(dev.StableIdentity); ok {
			return true
		}
	}
	return false
}
