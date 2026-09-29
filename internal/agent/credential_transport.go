package agent

import (
	"github.com/drko-dev/monitoreoedgeis/internal/config"
	"github.com/drko-dev/monitoreoedgeis/internal/credentials"
	"github.com/drko-dev/monitoreoedgeis/internal/transport"
)

func newAgentTransport(cfg *config.Config) (*transport.Client, error) {
	client, err := transport.New(cfg.SaaSURL, cfg.AllowInsecureHTTP, cfg.SaaSTimeout, Version)
	if err != nil {
		return nil, err
	}
	client.SetCurrentCredentialSource(func() string {
		current, err := credentials.Load(cfg.DataDir)
		if err != nil {
			return ""
		}
		return current.Credential
	})
	return client, nil
}
