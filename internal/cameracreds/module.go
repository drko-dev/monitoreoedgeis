package cameracreds

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// DefaultSyncInterval is the polling period used when Module.Interval is
// unset. Camera credential assignments change rarely, so this deliberately
// is not aggressive.
const DefaultSyncInterval = 5 * time.Minute

// Module periodically calls Syncer.Sync on a fixed interval. It implements
// the same Name/Start/Stop shape as agent.Module (see internal/heartbeat)
// without importing the agent package. It is not wired into the agent's
// module manager in this block — the next block connects a Provider to
// ONVIF, and can decide then whether to start this module automatically or
// trigger Sync explicitly.
type Module struct {
	syncer   *Syncer
	interval time.Duration
	log      *slog.Logger

	cancel context.CancelFunc
	done   chan struct{}
}

// NewModule builds a Module. interval <= 0 selects DefaultSyncInterval.
func NewModule(syncer *Syncer, interval time.Duration, log *slog.Logger) *Module {
	if interval <= 0 {
		interval = DefaultSyncInterval
	}
	if log == nil {
		log = slog.Default()
	}
	return &Module{syncer: syncer, interval: interval, log: log}
}

// Name identifies the module in lifecycle logs.
func (m *Module) Name() string { return "cameracreds" }

// Start runs one immediate sync, then loops on the configured interval
// until Stop. A failed sync is logged (by Syncer) and never stops the loop:
// the last good cache is kept and the next tick tries again.
func (m *Module) Start(_ context.Context) error {
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.done = make(chan struct{})

	go func() {
		defer close(m.done)
		_ = m.syncer.Sync(ctx)

		ticker := time.NewTicker(m.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = m.syncer.Sync(ctx)
			}
		}
	}()
	return nil
}

// Stop cancels the loop and waits for it to finish, bounded by ctx.
func (m *Module) Stop(ctx context.Context) error {
	if m.cancel == nil {
		return nil
	}
	m.cancel()
	select {
	case <-m.done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("cameracreds: shutdown timed out: %w", ctx.Err())
	}
}
