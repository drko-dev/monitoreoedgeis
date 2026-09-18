package agent

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/drko-dev/monitoreoedgeis/internal/config"
	"github.com/drko-dev/monitoreoedgeis/internal/credentials"
	"github.com/drko-dev/monitoreoedgeis/internal/health"
	"github.com/drko-dev/monitoreoedgeis/internal/remoteconfig"
	"github.com/drko-dev/monitoreoedgeis/internal/transport"
)

// newRemoteConfigModule wires internal/remoteconfig's poll+apply engine
// using the shared outbound transport.Client and a no-op runtime adapter:
// this Hito O slice owns versioning/persistence/rollback only -- the real
// FPS/ROI/model/processing-mode knobs (IA2) plug in later via
// remoteconfig.RuntimeAdapter, replacing remoteconfig.NoopRuntimeAdapter{}
// here.
func newRemoteConfigModule(cfg *config.Config, creds credentials.Credentials, reporter *health.Reporter, log *slog.Logger) (*remoteconfig.Module, error) {
	if cfg.SaaSURL == "" || !creds.IsEnrolled() || creds.DeviceID == "" || creds.Credential == "" || cfg.DataDir == "" {
		return nil, nil
	}
	client, err := transport.New(cfg.SaaSURL, cfg.AllowInsecureHTTP, cfg.SaaSTimeout, Version)
	if err != nil {
		return nil, fmt.Errorf("remote config transport: %w", err)
	}
	store, err := remoteconfig.OpenStore(cfg.DataDir)
	if err != nil {
		return nil, fmt.Errorf("remote config: open store: %w", err)
	}
	engine := remoteconfig.NewEngine(store, remoteconfig.NoopRuntimeAdapter{})
	m := remoteconfig.New(client, engine, creds.DeviceID, creds.Credential, log, remoteconfig.WithHealthSink(reporter))
	if err := m.Recover(context.Background()); err != nil {
		return nil, fmt.Errorf("remote config: recover: %w", err)
	}
	return m, nil
}
