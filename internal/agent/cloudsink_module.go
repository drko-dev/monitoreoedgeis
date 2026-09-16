package agent

import (
	"log/slog"

	"github.com/drko-dev/monitoreoedgeis/internal/cloudsink"
	"github.com/drko-dev/monitoreoedgeis/internal/config"
	"github.com/drko-dev/monitoreoedgeis/internal/credentials"
	"github.com/drko-dev/monitoreoedgeis/internal/logging"
	"github.com/drko-dev/monitoreoedgeis/internal/processing"
	"github.com/drko-dev/monitoreoedgeis/internal/transport"
)

// newCloudSink builds the Milestone I video-frame-upload sink, or returns nil
// when this Edge has nothing to push frames to or isn't configured for cloud
// processing.
//
// Gated on cfg.ProcessingMode == config.ModeCloud (not a separate env var):
// this reuses the mode the Edge already reports in every heartbeat, rather
// than adding a second knob that could disagree with it. A camera the SaaS
// has linked to this device (edge_device_cameras) only receives Edge-push
// frames while this Edge itself is configured for cloud processing — never
// both RTSP-pull (SaaS-side, legacy) and Edge-push for the same camera.
func newCloudSink(cfg *config.Config, creds credentials.Credentials, log *slog.Logger) processing.Sink {
	if cfg.ProcessingMode != config.ModeCloud {
		return nil
	}
	if cfg.SaaSURL == "" {
		log.Info("cloud video sink disabled: no GEOCAM_SAAS_URL configured")
		return nil
	}
	if !creds.IsEnrolled() || creds.Credential == "" || creds.DeviceID == "" {
		log.Info("cloud video sink disabled: edge is not enrolled",
			slog.String("credential_status", creds.Status.String()))
		return nil
	}

	client, err := transport.New(cfg.SaaSURL, cfg.AllowInsecureHTTP, cfg.SaaSTimeout, Version)
	if err != nil {
		log.Warn("cloud video sink disabled: transport client error", slog.Any("error", err))
		return nil
	}

	return cloudsink.New(client, creds.DeviceID, creds.Credential, logging.Component(log, "cloud-sink"))
}
