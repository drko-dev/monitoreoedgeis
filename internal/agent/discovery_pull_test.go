package agent

import (
	"testing"

	"github.com/drko-dev/monitoreoedgeis/internal/config"
	"github.com/drko-dev/monitoreoedgeis/internal/credentials"
)

// The SaaS discovery-run queue is Gateway-only (403 for device_kind=edge):
// a Full Edge must never poll it, a Gateway must.
func TestSaaSDiscoveryPullIsGatewayOnly(t *testing.T) {
	creds := credentials.Credentials{Status: credentials.StatusEnrolled, DeviceID: "edg_x", Credential: "secret"}
	for mode, want := range map[config.ProcessingMode]bool{
		config.ModeEdge:   false,
		config.ModeCloud:  true,
		config.ModeHybrid: true,
	} {
		cfg := &config.Config{SaaSURL: "https://saas.example", ProcessingMode: mode}
		if got := saasDiscoveryPullEnabled(cfg, creds); got != want {
			t.Errorf("mode %q: pull enabled = %v, want %v", mode, got, want)
		}
	}
	if saasDiscoveryPullEnabled(&config.Config{SaaSURL: "https://saas.example", ProcessingMode: config.ModeCloud}, credentials.Credentials{}) {
		t.Error("an unenrolled Edge must not poll")
	}
}
