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
	"github.com/drko-dev/monitoreoedgeis/internal/credentials"
	"github.com/drko-dev/monitoreoedgeis/internal/health"
	"github.com/drko-dev/monitoreoedgeis/internal/identity"
	"github.com/drko-dev/monitoreoedgeis/internal/logging"
	"github.com/drko-dev/monitoreoedgeis/internal/platform"
	"github.com/drko-dev/monitoreoedgeis/internal/rtsp"
)

// Agent is the edge agent core.
type Agent struct {
	cfg            *config.Config
	log            *slog.Logger
	identity       identity.Identity
	identityErr    error
	credentials    credentials.Credentials
	credentialsErr error
	platform       platform.Info
	health         *health.Reporter
	heartbeatErr   error
	discoveryErr   error
	rtspManager    *rtsp.Manager
	modules        *moduleManager
}

// New wires the agent from configuration. It performs no network I/O beyond
// resolving/persisting the local identity file.
//
// A failed identity resolution (see internal/identity) is not fatal here:
// the agent still starts so its health surface stays reachable for
// diagnosis, but Run never reaches READY — see logStartup/Run.
func New(cfg *config.Config) *Agent {
	ident, identErr := identity.Load(cfg.DataDir, cfg.EdgeID)
	creds, credErr := credentials.Load(cfg.DataDir)
	host := platform.Detect()

	log := logging.New(cfg.LogLevel, Version, ident.EdgeID, cfg.ProcessingMode.String())
	componentLog := logging.Component(log, "agent")
	reporter := health.New(Version, cfg, ident, host)
	reporter.SetCredentialStatus(creds.Status.String())

	a := &Agent{
		cfg:            cfg,
		log:            componentLog,
		identity:       ident,
		identityErr:    identErr,
		credentials:    creds,
		credentialsErr: credErr,
		platform:       host,
		health:         reporter,
	}
	mods := []Module{
		newHealthServerModule(cfg.HealthAddr, reporter, logging.Component(log, "health-http")),
	}

	// The heartbeat module is skipped for an unenrolled Edge and its
	// construction errors are non-fatal: a misconfigured SaaS URL must not
	// take down the local health surface that would let an operator diagnose
	// it. Either way the agent does not reach READY (see Run).
	hb, hbErr := newHeartbeatModule(cfg, ident, creds, reporter, logging.Component(log, "heartbeat"))
	if hb != nil {
		mods = append(mods, hb)
	}

	disc, discErr := newDiscoveryModule(cfg, creds, reporter, logging.Component(log, "discovery"))
	if disc != nil {
		mods = append(mods, disc)
	}

	if cfg.ConnectivityEnabled {
		rtspCfg := rtsp.Config{
			StreamRole:     cfg.StreamRole,
			PacketTimeout:  cfg.StreamTimeout,
			InitialBackoff: 1 * time.Second,
			MaxBackoff:     60 * time.Second,
			DialTimeout:    cfg.StreamTimeout,
			Enabled:        true,
		}
		rtspMgr := rtsp.NewManager(rtspCfg, reporter, logging.Component(log, "rtsp"))
		mods = append(mods, rtspMgr)
		a.rtspManager = rtspMgr
	}

	a.heartbeatErr = hbErr
	a.discoveryErr = discErr
	a.modules = newModuleManager(reporter.SetModuleState, mods...)
	return a
}

// Health exposes the health reporter (used by tests and future endpoints).
func (a *Agent) Health() *health.Reporter { return a.health }

// RTSPManager exposes the RTSP connectivity manager (nil if connectivity disabled).
func (a *Agent) RTSPManager() *rtsp.Manager { return a.rtspManager }

// Run starts the agent and blocks until ctx is cancelled, then shuts down
// gracefully. A cancelled context is a clean stop, not an error.
//
// The agent only reaches READY when identity resolved cleanly and every
// module started. Otherwise it stays DEGRADED but keeps running: the health
// server module still starts so /status and `geocam-edge check` can report
// the problem instead of the process going dark.
func (a *Agent) Run(ctx context.Context) error {
	a.logStartup()

	moduleErr := a.modules.Start(ctx)

	switch {
	case a.identityErr != nil:
		a.log.Error("agent will not become ready: identity error", slog.Any("error", a.identityErr))
		a.health.Set(health.StateDegraded)
	case a.credentialsErr != nil:
		a.log.Error("agent will not become ready: credentials error", slog.Any("error", a.credentialsErr))
		a.health.Set(health.StateDegraded)
	case moduleErr != nil:
		a.log.Error("agent will not become ready: module startup failed", slog.Any("error", moduleErr))
		a.health.Set(health.StateDegraded)
	case a.heartbeatErr != nil:
		a.log.Error("agent will not become ready: heartbeat module could not be built",
			slog.Any("error", a.heartbeatErr))
		a.health.Set(health.StateDegraded)
	case a.discoveryErr != nil:
		a.log.Error("agent will not become ready: discovery module could not be built",
			slog.Any("error", a.discoveryErr))
		a.health.Set(health.StateDegraded)
	default:
		a.health.Set(health.StateReady)
		a.log.Info("agent ready", slog.String("status", health.StateReady.String()))
	}

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
	if a.identityErr != nil {
		a.log.Error("identity resolution failed", slog.Any("error", a.identityErr))
	} else {
		a.log.Info("identity resolved",
			slog.String("enrollment_status", a.identity.Status.String()),
			slog.String("edge_id", a.identity.EdgeID),
			slog.String("source", string(a.identity.Source)),
		)
	}
	if a.credentialsErr != nil {
		a.log.Error("credentials resolution failed", slog.Any("error", a.credentialsErr))
	} else {
		// Never log a.credentials.Credential or any Authorization header.
		a.log.Info("credentials resolved",
			slog.String("credential_status", a.credentials.Status.String()),
		)
	}
}

func (a *Agent) shutdown() error {
	a.health.Set(health.StateStopping)
	a.log.Info("shutdown signal received",
		slog.String("status", health.StateStopping.String()),
		slog.String("uptime", a.health.Uptime().Round(time.Second).String()),
	)

	stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.modules.Stop(stopCtx); err != nil {
		a.log.Error("module shutdown reported errors", slog.Any("error", err))
	}

	a.log.Info("agent stopped cleanly")
	return nil
}

// String renders a short one-line description of the agent.
func (a *Agent) String() string {
	return fmt.Sprintf("geocam-edge %s (%s/%s, mode=%s, %s)",
		Version, a.platform.GOOS, a.platform.GOARCH,
		a.cfg.ProcessingMode, a.identity.Status)
}
