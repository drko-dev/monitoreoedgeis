package agent

import (
	"fmt"
	"log/slog"

	"github.com/drko-dev/monitoreoedgeis/internal/cameracreds"
	"github.com/drko-dev/monitoreoedgeis/internal/config"
	"github.com/drko-dev/monitoreoedgeis/internal/credentials"
	"github.com/drko-dev/monitoreoedgeis/internal/transport"
)

// newCameraCredsModule wires internal/cameracreds into the production
// Agent (Hito Z G1-B): a local encrypted cache of per-camera ONVIF/RTSP
// credentials, synced from the SaaS on a fixed interval.
//
// It only builds this subsystem when it can actually mean something: a
// configured SaaS URL, an enrolled Edge, and a non-empty DeviceID/enrollment
// credential — the exact same gate newDiscoveryModule already uses for its
// own SaaS transport client. An unenrolled or offline Edge returns
// (nil, nil, nil) and keeps running its local surface exactly as before;
// no camera-credential state is fabricated for it.
//
// A corrupt local master key or credentials cache is a hard, fail-closed
// error returned here — never silently regenerated or deleted, per
// cameracreds' own contract (see LoadOrCreateMasterKey/OpenStore). The
// caller (Agent.New) treats this the same as any other non-fatal module
// build error: logged, surfaced as a degraded startup cause, and the health
// server keeps serving so it stays diagnosable.
//
// New() performs no network I/O: OpenStore only reads the local encrypted
// cache from disk, and the first SaaS fetch only happens once
// cameracreds.Module.Start runs as part of normal module lifecycle.
// onSuccess, if non-nil, is wired as cameracreds.SyncOptions.OnSuccess —
// the caller's camera-target reconciler (Hito Z G1-B section 4/6).
func newCameraCredsModule(
	cfg *config.Config,
	creds credentials.Credentials,
	log *slog.Logger,
	onSuccess func(),
) (*cameracreds.Module, *cameracreds.Provider, error) {
	if cfg.SaaSURL == "" || !creds.IsEnrolled() || creds.Credential == "" || creds.DeviceID == "" {
		log.Info("camera credentials disabled: Edge is unenrolled or SaaS URL is not configured")
		return nil, nil, nil
	}

	masterKey, err := cameracreds.LoadOrCreateMasterKey(cfg.DataDir)
	if err != nil {
		return nil, nil, fmt.Errorf("camera credentials: master key: %w", err)
	}
	store, err := cameracreds.OpenStore(cfg.DataDir, masterKey)
	if err != nil {
		return nil, nil, fmt.Errorf("camera credentials: store: %w", err)
	}
	provider := cameracreds.NewProvider(store)

	client, err := transport.New(cfg.SaaSURL, cfg.AllowInsecureHTTP, cfg.SaaSTimeout, Version)
	if err != nil {
		return nil, nil, fmt.Errorf("camera credentials: transport: %w", err)
	}

	syncer, err := cameracreds.NewSyncer(cameracreds.SyncOptions{
		Client:     client,
		Store:      store,
		DeviceID:   creds.DeviceID,
		Credential: creds.Credential,
		Log:        log,
		OnSuccess:  onSuccess,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("camera credentials: syncer: %w", err)
	}

	// interval <= 0 selects cameracreds.DefaultSyncInterval (5 minutes).
	// This is deliberately not a new configuration knob: camera-credential
	// assignments change rarely, and the reconciler this drives also runs
	// on every successful discovery scan (see camera_target_reconciler.go),
	// so a 5-minute floor is not the only path to convergence.
	mod := cameracreds.NewModule(syncer, 0, log)
	return mod, provider, nil
}
