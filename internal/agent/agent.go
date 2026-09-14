// Package agent is the GEO CAM Edge core: it owns startup, runtime context,
// health and graceful shutdown.
//
// Discovery, cameras/RTSP, transport and video processing are deliberately
// absent in this milestone.
package agent

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/config"
	"github.com/drko-dev/monitoreoedgeis/internal/health"
	"github.com/drko-dev/monitoreoedgeis/internal/identity"
	"github.com/drko-dev/monitoreoedgeis/internal/logging"
	"github.com/drko-dev/monitoreoedgeis/internal/platform"
)

// Agent is the edge agent core.
type Agent struct {
	cfg      *config.Config
	log      *slog.Logger
	identity identity.Identity
	platform platform.Info
	health   *health.Reporter
}

// New wires the agent from configuration. It performs no network I/O.
func New(cfg *config.Config) *Agent {
	ident := identity.New(cfg.EdgeID)
	host := platform.Detect()

	log := logging.New(cfg.LogLevel, Version, ident.EdgeID, cfg.ProcessingMode.String())

	return &Agent{
		cfg:      cfg,
		log:      logging.Component(log, "agent"),
		identity: ident,
		platform: host,
		health:   health.New(Version, cfg, ident, host),
	}
}

// Health exposes the health reporter (used by tests and future endpoints).
func (a *Agent) Health() *health.Reporter { return a.health }

// Run starts the agent and blocks until ctx is cancelled, then shuts down
// gracefully. A cancelled context is a clean stop, not an error.
func (a *Agent) Run(ctx context.Context) error {
	a.logStartup()

	a.health.Set(health.StateReady)
	a.log.Info("agent ready", slog.String("status", health.StateReady.String()))

	<-ctx.Done()

	return a.shutdown()
}

func (a *Agent) logStartup() {
	a.log.Info("starting GEO CAM Edge",
		slog.String("commit", Commit),
		slog.String("build_date", BuildDate),
		slog.String("status", health.StateStarting.String()),
	)
	a.log.Info("platform detected",
		slog.String("hostname", a.platform.Hostname),
		slog.String("os", a.platform.OS),
		slog.String("goos", a.platform.GOOS),
		slog.String("architecture", a.platform.GOARCH),
		slog.String("kernel", a.platform.Kernel),
		slog.Int("cpu_count", a.platform.CPUCount),
		slog.Uint64("mem_total_bytes", a.platform.MemTotalByte),
		slog.Bool("arch_supported", a.platform.ArchSupported),
	)
	if !a.platform.ArchSupported {
		a.log.Warn("architecture is not a supported deployment target",
			slog.String("architecture", a.platform.GOARCH),
			slog.Any("supported", platform.SupportedArchitectures),
		)
	}
	a.log.Info("configuration loaded",
		slog.String("log_level", a.cfg.LogLevel),
		slog.String("saas_url", a.cfg.SaaSURL),
		slog.Duration("heartbeat_interval", a.cfg.HeartbeatInterval),
		slog.String("data_dir", a.cfg.DataDir),
	)
	a.log.Info("identity resolved",
		slog.String("enrollment_status", a.identity.Status.String()),
		slog.String("edge_id", a.identity.EdgeID),
	)
	if !a.identity.IsEnrolled() {
		a.log.Info("agent is not enrolled; set GEOCAM_EDGE_ID to assign an identity")
	}
}

func (a *Agent) shutdown() error {
	a.health.Set(health.StateStopping)
	a.log.Info("shutdown signal received",
		slog.String("status", health.StateStopping.String()),
		slog.String("uptime", a.health.Uptime().Round(time.Second).String()),
	)

	// No long-lived subsystems exist yet, so there is nothing to drain.
	// Discovery/transport/video will need a bounded grace window here.
	a.log.Info("agent stopped cleanly")
	return nil
}

// String renders a short one-line description of the agent.
func (a *Agent) String() string {
	return fmt.Sprintf("geocam-edge %s (%s/%s, mode=%s, %s)",
		Version, a.platform.GOOS, a.platform.GOARCH,
		a.cfg.ProcessingMode, a.identity.Status)
}
