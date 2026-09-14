package agent

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/config"
	"github.com/drko-dev/monitoreoedgeis/internal/credentials"
	"github.com/drko-dev/monitoreoedgeis/internal/health"
	"github.com/drko-dev/monitoreoedgeis/internal/heartbeat"
	"github.com/drko-dev/monitoreoedgeis/internal/identity"
	"github.com/drko-dev/monitoreoedgeis/internal/platform"
	"github.com/drko-dev/monitoreoedgeis/internal/transport"
)

// newHeartbeatModule builds the SaaS heartbeat module, or returns (nil, nil)
// when this agent has nothing to beat with.
//
// The module is skipped — not failed — when no SaaS URL is configured or the
// Edge holds no credential. An Edge that has never enrolled is a legitimate
// state (it can still serve /status and be enrolled later), and refusing to
// start over it would take the whole agent down for a condition an operator
// is about to fix. This is also why STARTING never sends a heartbeat: the
// module simply does not exist until identity and credential are both
// resolved.
func newHeartbeatModule(
	cfg *config.Config,
	ident identity.Identity,
	creds credentials.Credentials,
	reporter *health.Reporter,
	log *slog.Logger,
) (*heartbeat.Module, error) {
	if cfg.SaaSURL == "" {
		log.Info("heartbeat disabled: no GEOCAM_SAAS_URL configured")
		return nil, nil
	}
	if !creds.IsEnrolled() || creds.Credential == "" || creds.DeviceID == "" {
		log.Info("heartbeat disabled: edge is not enrolled",
			slog.String("credential_status", creds.Status.String()))
		return nil, nil
	}

	client, err := transport.New(cfg.SaaSURL, cfg.AllowInsecureHTTP, cfg.SaaSTimeout, Version)
	if err != nil {
		return nil, fmt.Errorf("heartbeat transport: %w", err)
	}

	// One CPU sampler for the life of the module: utilisation is a delta
	// between consecutive readings, so it must not be rebuilt per heartbeat.
	cpu := &platform.CPUSampler{}

	// A boot ID identifies this *process run*, so it must be regenerated on
	// every start — the Edge ID is stable across restarts and would make the
	// field meaningless. SequenceNumber restarts at 1 with it, so the pair
	// lets the SaaS discard a stale snapshot that overtakes a newer one
	// without mistaking a freshly restarted Edge (sequence 1, new boot) for
	// an old one. Getting this wrong would leave a recreated K3s pod stuck
	// OFFLINE behind its predecessor's higher sequence numbers.
	//
	// Entropy failure is not fatal: an empty boot_id is omitted from the
	// payload and the heartbeat still reports liveness.
	bootID, err := identity.NewUUIDv4()
	if err != nil {
		log.Warn("heartbeat: could not generate a boot id; continuing without one",
			slog.Any("error", err))
		bootID = ""
	}
	var sequence int64

	build := func() transport.HeartbeatRequest {
		sample := platform.Collect(cfg.DataDir, cpu)
		sequence++
		snap := reporter.Snapshot()
		return transport.HeartbeatRequest{
			EdgeID:         ident.EdgeID,
			EdgeVersion:    Version,
			UptimeSeconds:  int64(reporter.Uptime().Seconds()),
			Architecture:   snap.Architecture,
			ProcessingMode: cfg.ProcessingMode.String(),
			HealthStatus:   snap.Status.String(),
			BootID:         bootID,
			SequenceNumber: sequence,
			EdgeTimestamp:  time.Now().UTC().Format(time.RFC3339),
			System: transport.HeartbeatSystem{
				CPUPercent:       sample.CPUPercent,
				MemoryTotalBytes: sample.MemTotalBytes,
				MemoryUsedBytes:  sample.MemUsedBytes,
				DiskTotalBytes:   sample.DiskTotalBytes,
				DiskUsedBytes:    sample.DiskUsedBytes,
				TemperatureC:     sample.TemperatureC,
			},
		}
	}

	return heartbeat.New(heartbeat.Options{
		Sender:     client,
		DeviceID:   creds.DeviceID,
		Credential: creds.Credential,
		Build:      build,
		Interval:   cfg.HeartbeatInterval,
		Log:        log,
		OnStatus:   reporter.SetHeartbeatStatus,
		OnUnauthorized: func() {
			// A revoked or disabled Edge is genuinely not doing its job, so
			// the whole agent goes DEGRADED — unlike a plain SaaS outage,
			// which leaves the agent READY. The credential itself is left
			// untouched; clearing it is an operator decision.
			reporter.Set(health.StateDegraded)
		},
	})
}
