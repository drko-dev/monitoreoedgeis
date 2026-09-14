package agent

import (
	"fmt"
	"log/slog"

	"github.com/drko-dev/monitoreoedgeis/internal/config"
	"github.com/drko-dev/monitoreoedgeis/internal/credentials"
	"github.com/drko-dev/monitoreoedgeis/internal/discovery"
	"github.com/drko-dev/monitoreoedgeis/internal/health"
	"github.com/drko-dev/monitoreoedgeis/internal/transport"
)

// newDiscoveryModule constructs the discovery agent module according to configuration.
// If discovery is disabled via GEOCAM_DISCOVERY_ENABLED=false, it returns (nil, nil).
func newDiscoveryModule(
	cfg *config.Config,
	creds credentials.Credentials,
	reporter *health.Reporter,
	log *slog.Logger,
) (*discovery.Module, error) {
	if !cfg.DiscoveryEnabled {
		log.Info("discovery disabled: GEOCAM_DISCOVERY_ENABLED is false")
		return nil, nil
	}

	engine := discovery.NewEngine(
		nil, // Default wsdiscovery.Scanner
		nil, // Default onvif.Client
		nil, // New local Inventory
		cfg.DiscoveryInterfaces,
		cfg.DiscoveryTimeout,
		log,
	)

	var client discovery.TransportClient
	if cfg.SaaSURL != "" && creds.IsEnrolled() && creds.Credential != "" && creds.DeviceID != "" {
		c, err := transport.New(cfg.SaaSURL, cfg.AllowInsecureHTTP, cfg.SaaSTimeout, Version)
		if err != nil {
			return nil, fmt.Errorf("discovery transport: %w", err)
		}
		client = c
	}

	return discovery.NewModule(discovery.ModuleOptions{
		Engine:       engine,
		Client:       client,
		DeviceID:     creds.DeviceID,
		Credential:   creds.Credential,
		Interval:     cfg.DiscoveryInterval,
		PullInterval: discovery.DefaultPullInterval,
		Log:          log,
		OnStatus:     reporter.SetDiscoveryStatus,
	})
}
