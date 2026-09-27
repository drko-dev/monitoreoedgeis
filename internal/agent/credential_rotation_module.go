package agent

import (
	"log/slog"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/auditjournal"
	"github.com/drko-dev/monitoreoedgeis/internal/config"
	"github.com/drko-dev/monitoreoedgeis/internal/credentialrotation"
	"github.com/drko-dev/monitoreoedgeis/internal/credentials"
	"github.com/drko-dev/monitoreoedgeis/internal/health"
	"github.com/drko-dev/monitoreoedgeis/internal/logging"
)

func newCredentialRotationModule(cfg *config.Config, creds credentials.Credentials, reporter *health.Reporter, audit *auditjournal.Journal, log *slog.Logger) *credentialrotation.Module {
	var service *credentialrotation.Service
	if cfg.AutoRotationEnabled && creds.IsEnrolled() && cfg.SaaSURL != "" {
		client, err := newAgentTransport(cfg)
		if err == nil {
			delays := []time.Duration{cfg.AutoRotationRetryBase, min(2*cfg.AutoRotationRetryBase, cfg.AutoRotationRetryMax)}
			interval := max(cfg.AutoRotationInterval, cfg.AutoRotationMinimumAge)
			service, err = credentialrotation.New(credentialrotation.Options{
				DataDir: cfg.DataDir, EdgeID: creds.EdgeID, Client: client, Store: credentialrotation.DiskStore{}, Audit: audit,
				Interval: interval, JitterWindow: cfg.AutoRotationJitterWindow, RetryDelays: delays,
			})
		}
		if err != nil {
			log.Error("automatic credential rotation unavailable", "error", "transport configuration failed")
		}
	}
	return credentialrotation.NewModule(credentialrotation.ModuleOptions{
		Service: service, Store: credentialrotation.DiskStore{}, DataDir: cfg.DataDir, Enabled: cfg.AutoRotationEnabled,
		Interval: max(cfg.AutoRotationInterval, cfg.AutoRotationMinimumAge), MinimumAge: cfg.AutoRotationMinimumAge,
		JitterWindow: cfg.AutoRotationJitterWindow, RetryBase: cfg.AutoRotationRetryBase, RetryMax: cfg.AutoRotationRetryMax,
		Log: logging.Component(log, "credential-rotation"),
		OnStatus: func(s credentialrotation.Status) {
			reporter.SetCredentialRotationStatus(health.CredentialRotationStatus{
				Enabled: s.Enabled, LastSuccessAt: s.LastSuccessAt, NextDueAt: s.NextDueAt,
				LastAttemptAt: s.LastAttemptAt, ConsecutiveFailures: s.ConsecutiveFailures, State: s.State,
			})
		},
	})
}
