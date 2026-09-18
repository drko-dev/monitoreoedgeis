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
	"github.com/drko-dev/monitoreoedgeis/internal/transport"
)

type controlExecutor struct {
	reporter  *health.Reporter
	discovery *discovery.Module
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

func newControlModule(cfg *config.Config, creds credentials.Credentials, reporter *health.Reporter, discoveryModule *discovery.Module, log *slog.Logger) (*control.Module, error) {
	if cfg.SaaSURL == "" || !creds.IsEnrolled() || creds.DeviceID == "" || creds.Credential == "" {
		return nil, nil
	}
	client, err := transport.New(cfg.SaaSURL, cfg.AllowInsecureHTTP, cfg.SaaSTimeout, Version)
	if err != nil {
		return nil, fmt.Errorf("control transport: %w", err)
	}
	return control.New(client, controlExecutor{reporter: reporter, discovery: discoveryModule}, creds.DeviceID, creds.Credential), nil
}
