package agent

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/config"
	"github.com/drko-dev/monitoreoedgeis/internal/credentials"
	"github.com/drko-dev/monitoreoedgeis/internal/health"
	"github.com/drko-dev/monitoreoedgeis/internal/heartbeat"
	"github.com/drko-dev/monitoreoedgeis/internal/identity"
	"github.com/drko-dev/monitoreoedgeis/internal/ota"
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
	gate credentialHealth,
	otaModule *ota.Module,
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
		reporter.SetPlatformSample(sample)
		sequence++
		snap := reporter.Snapshot()
		var cams []transport.CameraStreamStatus
		for _, c := range snap.Cameras {
			var lastPkt *string
			if c.LastPacketAt != nil {
				formatted := c.LastPacketAt.UTC().Format(time.RFC3339)
				lastPkt = &formatted
			}
			cams = append(cams, transport.CameraStreamStatus{
				CandidateKey:    c.CandidateKey,
				Status:          string(c.Status),
				StreamRole:      c.StreamRole,
				Codec:           c.Codec,
				Width:           c.Width,
				Height:          c.Height,
				FPS:             c.FPS,
				ReconnectCount:  c.ReconnectCount,
				PacketsReceived: c.PacketsReceived,
				BytesReceived:   c.BytesReceived,
				LastPacketAt:    lastPkt,
				LastErrorSafe:   c.LastErrorSafe,
			})
		}

		return transport.HeartbeatRequest{
			EdgeID:         ident.EdgeID,
			EdgeVersion:    Version,
			UptimeSeconds:  int64(reporter.Uptime().Seconds()),
			Architecture:   snap.Architecture,
			ProcessingMode: snap.ProcessingMode,
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
			Cameras: cams,
		}
	}

	return heartbeat.New(heartbeat.Options{
		Sender:     client,
		DeviceID:   creds.DeviceID,
		Credential: creds.Credential,
		Build:      build,
		Interval:   cfg.HeartbeatInterval,
		// Zero keeps the module's own five-minute default; a non-zero value
		// comes from GEOCAM_HEARTBEAT_AUTH_FAILURE_INTERVAL.
		AuthFailureInterval: cfg.HeartbeatAuthFailureInterval,
		Log:                 log,
		OnStatus:            reporter.SetHeartbeatStatus,
		OnUnauthorized: func() {
			// A revoked or disabled Edge is genuinely not doing its job, so
			// the whole agent goes DEGRADED — unlike a plain SaaS outage,
			// which leaves the agent READY. The credential itself is left
			// untouched; clearing it is an operator decision.
			if gate != nil {
				gate.MarkCredentialRevoked()
			}
		},
		// The counterpart OnUnauthorized needs. A revoked credential is an
		// administrative state an operator can reverse (re-enable the device,
		// rotate the credential), and once the SaaS accepts it again the Edge
		// is doing its job and must say so. Without this the agent stayed
		// DEGRADED — and /readyz stayed 503 — until the process restarted,
		// which also made the appliance's own post-update readiness gate fail
		// and roll back a good release.
		OnRecovered: func() {
			if gate != nil {
				gate.ClearCredentialRevoked()
			}
		},
		OnSuccess: otaCheckOnSuccess(otaModule, log),
	})
}

// otaCheckOnSuccess returns the heartbeat OnSuccess hook that drives Hito
// T's OTA check off the existing heartbeat cadence (T2: "no second poll
// loop"). It runs CheckOnce in its own goroutine so a slow download never
// delays the next scheduled heartbeat, and its error is only logged — an
// OTA failure must never be mistaken for a heartbeat failure. nil
// otaModule (unenrolled edge, no SaaS URL) yields a no-op hook.
func otaCheckOnSuccess(otaModule *ota.Module, log *slog.Logger) func() {
	if otaModule == nil {
		return nil
	}
	return func() {
		go func() {
			if err := otaModule.CheckOnce(context.Background()); err != nil {
				log.Warn("ota check failed", slog.Any("error", err))
			}
		}()
	}
}
