package agent

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/drko-dev/monitoreoedgeis/internal/config"
	"github.com/drko-dev/monitoreoedgeis/internal/credentials"
	"github.com/drko-dev/monitoreoedgeis/internal/health"
	"github.com/drko-dev/monitoreoedgeis/internal/remoteconfig"
)

// newRemoteConfigModule wires internal/remoteconfig's poll+apply engine
// using the shared outbound transport.Client and the real runtime adapter
// (or NoopRuntimeAdapter if no runtime adapter was supplied).
func newRemoteConfigModule(cfg *config.Config, creds credentials.Credentials, reporter *health.Reporter, adapter remoteconfig.Adapter, audit remoteconfig.AuditSink, log *slog.Logger) (*remoteconfig.Module, error) {
	if cfg.SaaSURL == "" || !creds.IsEnrolled() || creds.DeviceID == "" || creds.Credential == "" || cfg.DataDir == "" {
		return nil, nil
	}
	client, err := newAgentTransport(cfg)
	if err != nil {
		return nil, fmt.Errorf("remote config transport: %w", err)
	}
	store, err := remoteconfig.OpenStore(cfg.DataDir)
	if err != nil {
		return nil, fmt.Errorf("remote config: open store: %w", err)
	}
	if adapter == nil {
		adapter = remoteconfig.NoopRuntimeAdapter{}
	}
	var engineOpts []remoteconfig.EngineOption
	if audit != nil {
		engineOpts = append(engineOpts, remoteconfig.WithEngineAuditSink(audit))
	}
	engine := remoteconfig.NewEngine(store, adapter, engineOpts...)
	var modOpts []remoteconfig.Option
	modOpts = append(modOpts, remoteconfig.WithHealthSink(reporter))
	if audit != nil {
		modOpts = append(modOpts, remoteconfig.WithAuditSink(audit))
	}
	m := remoteconfig.New(client, engine, creds.DeviceID, creds.Credential, log, modOpts...)
	if err := m.Recover(context.Background()); err != nil {
		return nil, fmt.Errorf("remote config: recover: %w", err)
	}
	return m, nil
}
