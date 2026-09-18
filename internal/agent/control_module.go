package agent

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/drko-dev/monitoreoedgeis/internal/config"
	"github.com/drko-dev/monitoreoedgeis/internal/control"
	"github.com/drko-dev/monitoreoedgeis/internal/credentials"
	"github.com/drko-dev/monitoreoedgeis/internal/discovery"
	"github.com/drko-dev/monitoreoedgeis/internal/health"
	"github.com/drko-dev/monitoreoedgeis/internal/remoteconfig"
	"github.com/drko-dev/monitoreoedgeis/internal/transport"
)

type controlExecutor struct {
	reporter     *health.Reporter
	discovery    *discovery.Module
	remoteConfig *remoteconfig.Module
}

func (e controlExecutor) Status() map[string]any {
	result := map[string]any{"health": e.reporter.State().String()}
	if e.discovery != nil {
		result["discovery_state"] = e.discovery.Status().State
	}
	return result
}
func (e controlExecutor) Rediscover(ctx context.Context) error {
	if e.discovery == nil {
		return fmt.Errorf("discovery unavailable")
	}
	return e.discovery.Rediscover(ctx)
}

// ReloadConfig implements control.Executor for the "reload_config"
// command (Hito O): SaaS queues it via the existing outbound control
// channel whenever the desired remote config changes, and Edge fetches +
// applies it here through the same poll cadence -- no second poller.
func (e controlExecutor) ReloadConfig(ctx context.Context) error {
	if e.remoteConfig == nil {
		return fmt.Errorf("remote config unavailable")
	}
	return e.remoteConfig.SyncOnce(ctx)
}

func newControlModule(cfg *config.Config, creds credentials.Credentials, reporter *health.Reporter, discoveryModule *discovery.Module, remoteConfigModule *remoteconfig.Module, log *slog.Logger) (*control.Module, error) {
	if cfg.SaaSURL == "" || !creds.IsEnrolled() || creds.DeviceID == "" || creds.Credential == "" {
		return nil, nil
	}
	client, err := transport.New(cfg.SaaSURL, cfg.AllowInsecureHTTP, cfg.SaaSTimeout, Version)
	if err != nil {
		return nil, fmt.Errorf("control transport: %w", err)
	}
	var opts []control.Option
	if cfg.DataDir != "" {
		ledger, err := control.OpenLedger(cfg.DataDir, 100)
		if err != nil {
			return nil, fmt.Errorf("control: open ledger: %w", err)
		}
		opts = append(opts, control.WithLedger(ledger))
	}
	return control.New(client, controlExecutor{reporter: reporter, discovery: discoveryModule, remoteConfig: remoteConfigModule}, creds.DeviceID, creds.Credential, opts...), nil
}
