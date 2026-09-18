package remoteconfig

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/transport"
)

const (
	defaultInterval = 15 * time.Second
	maxBackoff      = time.Minute
	authBackoff     = 5 * time.Minute
)

// Client is the outbound transport this Module polls through -- the same
// Edge-initiated, allowlisted channel internal/control uses (Hito L). No
// second poller, no inbound port, no WebSocket.
type Client interface {
	GetDesiredConfig(ctx context.Context, deviceID, credential string) (*transport.RemoteConfig, error)
	AckRemoteConfig(ctx context.Context, deviceID, credential string, version int64, status, errorCode string) error
}

// Module is the poll loop that feeds Engine with desired configs from
// SaaS and ACKs the outcome. Mirrors internal/control.Module's shape.
type Module struct {
	client               Client
	engine               *Engine
	deviceID, credential string
	pollInterval         time.Duration
	logger               *slog.Logger
	healthSink           HealthSink
	cancel               context.CancelFunc
	wg                   sync.WaitGroup

	desiredVersion atomic.Int64
}

// Option configures Module options.
type Option func(*Module)

// WithHealthSink lets Module publish its status into the agent health
// reporter after every poll/apply cycle, mirroring internal/control's
// WithLedger option.
func WithHealthSink(hs HealthSink) Option {
	return func(m *Module) {
		m.healthSink = hs
	}
}

// New builds a Module. engine must already have Recover called on it (or
// the caller must call Module.Recover before Start) so a crash-interrupted
// apply is resolved before polling begins.
func New(client Client, engine *Engine, deviceID, credential string, logger *slog.Logger, opts ...Option) *Module {
	if logger == nil {
		logger = slog.Default()
	}
	m := &Module{
		client:       client,
		engine:       engine,
		deviceID:     deviceID,
		credential:   credential,
		pollInterval: defaultInterval,
		logger:       logger,
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// SetPollInterval sets the interval between polls (useful for testing).
func (m *Module) SetPollInterval(d time.Duration) {
	if d > 0 {
		m.pollInterval = d
	}
}

// Recover resolves any config left mid-apply by a prior crash. Safe to
// call multiple times; a no-op once resolved. Called by agent wiring
// before Start, and exposed here so tests can call it without going
// through Start/Stop.
func (m *Module) Recover(ctx context.Context) error {
	err := m.engine.Recover(ctx)
	m.publishStatus()
	return err
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

func (m *Module) Name() string { return "remote-config" }

func (m *Module) Start(context.Context) error {
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.wg.Add(1)
	go func() { defer m.wg.Done(); m.loop(ctx) }()
	return nil
}

func (m *Module) Stop(ctx context.Context) error {
	if m.cancel == nil {
		return nil
	}
	m.cancel()
	done := make(chan struct{})
	go func() { m.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *Module) loop(ctx context.Context) {
	wait, backoff := m.pollInterval, time.Duration(0)
	for {
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return
		}

		cfg, err := m.client.GetDesiredConfig(ctx, m.deviceID, m.credential)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			if errors.Is(err, transport.ErrUnauthorized) {
				wait = authBackoff
				continue
			}
			var rl *transport.RateLimitError
			if errors.As(err, &rl) && rl.RetryAfter > 0 {
				wait = rl.RetryAfter
				continue
			}
			if backoff == 0 {
				backoff = m.pollInterval
			} else {
				backoff *= 2
				if backoff > maxBackoff {
					backoff = maxBackoff
				}
			}
			wait = backoff
			continue
		}
		backoff, wait = 0, m.pollInterval
		if cfg == nil {
			continue
		}

		m.desiredVersion.Store(cfg.Version)
		desired := Config{Version: cfg.Version, Payload: cfg.Payload}

		status, code, err := m.engine.ReceiveDesired(ctx, desired)
		if err != nil {
			// Durable persistence failure: do not ACK a status we could not
			// record. Degrade and stop polling to avoid unsafe repeated
			// apply attempts with no record of the outcome.
			m.logger.Error("remote config: persistence failure, stopping poll", slog.Any("error", err))
			return
		}
		m.publishStatus()
		if status == "" {
			continue
		}
		if err := m.client.AckRemoteConfig(ctx, m.deviceID, m.credential, desired.Version, string(status), code); err != nil && errors.Is(err, context.Canceled) {
			return
		}
	}
}
