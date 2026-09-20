package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
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

	// probeAddr is the address that actually ended up bound, and is what the
	// watchdog liveness probe dials. It differs from addr whenever the
	// configured address leaves the port (or the host) unspecified -- notably
	// in tests, where GEOCAM_HEALTH_ADDR is "127.0.0.1:0".
	probeAddr string
	// probeClient is dedicated to the liveness probe so a hung probe can never
	// occupy a connection the operator's own /status request would need.
	probeClient *http.Client
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
		probeClient: &http.Client{
			Timeout: probeTimeout,
		},
	}
}

// probeTimeout bounds one liveness check. It is deliberately shorter than the
// watchdog period, so a probe that hangs is observed as a failure in time for
// the ping to be withheld rather than arriving after the deadline.
const probeTimeout = 5 * time.Second

func (m *healthServerModule) Name() string { return "health-http" }

func (m *healthServerModule) Start(_ context.Context) error {
	ln, err := net.Listen("tcp", m.addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", m.addr, err)
	}
	m.probeAddr = probeAddress(ln.Addr().String())
	go func() {
		if err := m.server.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			m.log.Error("health server stopped unexpectedly", slog.Any("error", err))
		}
	}()
	m.log.Info("health server listening", slog.String("addr", m.addr))
	return nil
}

// Probe implements livenessProbe: it performs a real HTTP round trip to this
// process's own /healthz over loopback.
//
// A real round trip rather than an in-process flag is the point. A wedged
// process is usually wedged on a lock or a stuck runtime, and an in-process
// check would happily report "alive" while /healthz hangs -- which is precisely
// the case the systemd watchdog exists to catch.
func (m *healthServerModule) Probe(ctx context.Context) error {
	if m.probeAddr == "" {
		return errors.New("health server is not listening")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+m.probeAddr+"/healthz", nil)
	if err != nil {
		return err
	}
	resp, err := m.probeClient.Do(req)
	if err != nil {
		return fmt.Errorf("health probe: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<10))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health probe: /healthz returned %d", resp.StatusCode)
	}
	return nil
}

// probeAddress turns a bound listen address into one that is dialable from this
// host. An unspecified host (":8091", "0.0.0.0:8091", "[::]:8091") and the
// test-only port-zero form are both normalised to loopback, matching the
// localhost-only convention the health surface is built around.
func probeAddress(bound string) string {
	host, port, err := net.SplitHostPort(bound)
	if err != nil {
		return bound
	}
	if port == "" || port == "0" {
		return ""
	}
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

func (m *healthServerModule) Stop(ctx context.Context) error {
	return m.server.Shutdown(ctx)
}
