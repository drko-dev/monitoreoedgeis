package remoteconfig

import (
	"context"
	"log/slog"
	"sync/atomic"

	"github.com/drko-dev/monitoreoedgeis/internal/transport"
)

// Client is the outbound transport SyncOnce talks through -- the same
// Edge-initiated, allowlisted channel internal/control uses (Hito L). No
// second poller, no inbound port, no WebSocket.
type Client interface {
	GetDesiredConfig(ctx context.Context, deviceID, credential string) (*transport.RemoteConfig, error)
	AckRemoteConfig(ctx context.Context, deviceID, credential string, version int64, status, errorCode string) error
}

// Module is the sync component that fetches the desired config from SaaS,
// runs it through Engine, and ACKs the outcome. It owns no poll loop of
// its own: SyncOnce is driven by internal/control's existing outbound poll
// cadence (Hito L) via the allowlisted "reload_config" command -- reusing
// that channel instead of running a second ticker/goroutine.
type Module struct {
	client               Client
	engine               *Engine
	deviceID, credential string
	logger               *slog.Logger
	healthSink           HealthSink

	desiredVersion atomic.Int64
}

// Option configures Module options.
type Option func(*Module)

// WithHealthSink lets Module publish its status into the agent health
// reporter after every sync/apply cycle, mirroring internal/control's
// WithLedger option.
func WithHealthSink(hs HealthSink) Option {
	return func(m *Module) {
		m.healthSink = hs
	}
}

// New builds a Module. Recover must be called once at startup, before any
// SyncOnce call, so a crash-interrupted apply is resolved first.
func New(client Client, engine *Engine, deviceID, credential string, logger *slog.Logger, opts ...Option) *Module {
	if logger == nil {
		logger = slog.Default()
	}
	m := &Module{
		client:     client,
		engine:     engine,
		deviceID:   deviceID,
		credential: credential,
		logger:     logger,
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// Recover resolves any config left mid-apply by a prior crash. Safe to
// call multiple times; a no-op once resolved.
func (m *Module) Recover(ctx context.Context) error {
	err := m.engine.Recover(ctx)
	m.publishStatus()
	return err
}

// SyncOnce performs a single fetch -> apply -> ACK cycle: GET the current
// desired config, run it through Engine, and POST the outcome back to
// SaaS. It does not poll, retry, or back off internally -- it is called
// once per "reload_config" control command, so internal/control's own
// poll cadence and error handling (backoff on claim failures, ledger
// idempotency) already cover retry/backoff; a failed SyncOnce simply
// reports the command as failed, and SaaS can queue reload_config again.
func (m *Module) SyncOnce(ctx context.Context) error {
	cfg, err := m.client.GetDesiredConfig(ctx, m.deviceID, m.credential)
	if err != nil {
		return err
	}
	if cfg == nil {
		return nil // no config currently assigned; nothing to do
	}

	m.desiredVersion.Store(cfg.Version)
	desired := Config{Version: cfg.Version, Payload: cfg.Payload}

	status, code, err := m.engine.ReceiveDesired(ctx, desired)
	if err != nil {
		// Durable persistence failure (or an unresolved prior rollback):
		// do not ACK a status we could not record or that is not final.
		m.publishStatus()
		return err
	}
	m.publishStatus()
	if status == "" {
		return nil
	}
	return m.client.AckRemoteConfig(ctx, m.deviceID, m.credential, desired.Version, string(status), code)
}

// Status returns the current /status snapshot for remote_config.
func (m *Module) Status() Status {
	return m.engine.store.Get().toStatus(m.desiredVersion.Load())
}

func (m *Module) publishStatus() {
	if m.healthSink != nil {
		m.healthSink.SetRemoteConfigStatus(m.Status())
	}
}
