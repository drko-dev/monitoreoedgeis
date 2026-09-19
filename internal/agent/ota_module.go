package agent

import (
	"log/slog"

	"github.com/drko-dev/monitoreoedgeis/internal/config"
	"github.com/drko-dev/monitoreoedgeis/internal/credentials"
	"github.com/drko-dev/monitoreoedgeis/internal/ota"
	"github.com/drko-dev/monitoreoedgeis/internal/platform"
	"github.com/drko-dev/monitoreoedgeis/internal/transport"
)

// newOTAModule builds the Hito T OTA module, or returns (nil, nil) when
// this agent has nothing to check with — same gate as heartbeat: an
// unenrolled Edge or a missing SaaS URL is a legitimate, non-fatal state,
// not an error.
//
// A missing/unreadable GEOCAM_OTA_PUBLIC_KEY_FILE is deliberately NOT
// fatal here either: OTA is best-effort and fails closed at verification
// time (every downloaded release is simply rejected), it must never take
// the rest of the agent down.
func newOTAModule(cfg *config.Config, creds credentials.Credentials, log *slog.Logger) *ota.Module {
	if cfg.SaaSURL == "" {
		log.Info("ota disabled: no GEOCAM_SAAS_URL configured")
		return nil
	}
	if !creds.IsEnrolled() || creds.Credential == "" || creds.DeviceID == "" {
		log.Info("ota disabled: edge is not enrolled")
		return nil
	}

	client, err := transport.New(cfg.SaaSURL, cfg.AllowInsecureHTTP, cfg.SaaSTimeout, Version)
	if err != nil {
		log.Warn("ota disabled: could not build SaaS transport", slog.Any("error", err))
		return nil
	}

	pubKey, err := ota.LoadPublicKey(cfg.OTAPublicKeyFile)
	if err != nil {
		// Logged, not fatal: CheckOnce's downloader will simply reject
		// every release at signature-verification time (fail closed) until
		// an operator provisions GEOCAM_OTA_PUBLIC_KEY_FILE.
		log.Warn("ota: no usable public key configured; releases will be rejected at verification time",
			slog.Any("error", err))
	}

	arch := platform.Detect().GOARCH
	downloader := ota.NewFileDownloader(cfg.DataDir, pubKey, Version, arch, log)

	return ota.New(client, downloader, creds.DeviceID, creds.Credential, Version, log)
}
