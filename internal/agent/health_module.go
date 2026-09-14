package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/health"
)

// healthServerModule runs the local health HTTP surface (/healthz, /readyz,
// /status) described in docs/ARCHITECTURE.md. It binds to a localhost-only
// address by default (GEOCAM_HEALTH_ADDR).
type healthServerModule struct {
	addr   string
	server *http.Server
	log    *slog.Logger
}

func newHealthServerModule(addr string, reporter *health.Reporter, log *slog.Logger) *healthServerModule {
	return &healthServerModule{
		addr: addr,
		server: &http.Server{
			Addr:              addr,
			Handler:           health.Handler(reporter),
			ReadHeaderTimeout: 5 * time.Second,
		},
		log: log,
	}
}

func (m *healthServerModule) Name() string { return "health-http" }

func (m *healthServerModule) Start(_ context.Context) error {
	ln, err := net.Listen("tcp", m.addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", m.addr, err)
	}
	go func() {
		if err := m.server.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			m.log.Error("health server stopped unexpectedly", slog.Any("error", err))
		}
	}()
	m.log.Info("health server listening", slog.String("addr", m.addr))
	return nil
}

func (m *healthServerModule) Stop(ctx context.Context) error {
	return m.server.Shutdown(ctx)
}
