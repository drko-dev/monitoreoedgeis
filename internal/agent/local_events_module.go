package agent

import (
	"context"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/config"
	"github.com/drko-dev/monitoreoedgeis/internal/credentials"
	"github.com/drko-dev/monitoreoedgeis/internal/edgebacklog"
	"github.com/drko-dev/monitoreoedgeis/internal/health"
	"github.com/drko-dev/monitoreoedgeis/internal/transport"
)

type localEventsModule struct {
	backlog              *edgebacklog.Backlog
	sender               transport.LocalEventSender
	deviceID, credential string
	reporter             *health.Reporter
	cancel               context.CancelFunc
	wg                   sync.WaitGroup
}

func newLocalEventsModule(cfg *config.Config, creds credentials.Credentials, reporter *health.Reporter, log *slog.Logger) (*localEventsModule, error) {
	if cfg.SaaSURL == "" || !creds.IsEnrolled() {
		return nil, nil
	}
	b, err := edgebacklog.Open(edgebacklog.Config{Dir: filepath.Join(cfg.DataDir, "local-event-backlog"), MaxOperations: cfg.LocalEventBacklogMaxOperations, MaxBytes: cfg.LocalEventBacklogMaxBytes})
	if err != nil {
		return nil, err
	}
	c, err := transport.New(cfg.SaaSURL, cfg.AllowInsecureHTTP, cfg.SaaSTimeout, Version)
	if err != nil {
		return nil, err
	}
	m := &localEventsModule{backlog: b, sender: c, deviceID: creds.DeviceID, credential: creds.Credential, reporter: reporter}
	reporter.SetLocalEventBacklogStatus(b.Status())
	return m, nil
}
func (m *localEventsModule) Name() string { return "local-events" }
func (m *localEventsModule) Start(ctx context.Context) error {
	runCtx, cancel := context.WithCancel(ctx)
	m.cancel = cancel
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		m.backlog.Run(runCtx, m.sender, m.deviceID, m.credential, time.Second)
		m.reporter.SetLocalEventBacklogStatus(m.backlog.Status())
	}()
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				m.reporter.SetLocalEventBacklogStatus(m.backlog.Status())
			}
		}
	}()
	return nil
}
func (m *localEventsModule) Stop(context.Context) error {
	if m.cancel != nil {
		m.cancel()
		m.wg.Wait()
	}
	return nil
}
