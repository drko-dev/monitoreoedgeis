package main

import (
	"context"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/installer"
)

// NewAppWithSignedRuntimePack is an explicit, release-owned opt-in bridge
// between independent cryptographic publisher trust and H6B transactions.
//
// TrustStore and signed must be supplied by the authorized embedding release,
// NOT loaded from PackDir or accepted from the Wails frontend. The external
// release manifest SHA-256 pin is still required in options. NewApp() remains
// unchanged and never enables Runtime Pack provisioning by default.
func NewAppWithSignedRuntimePack(
	options installer.RuntimePackProvisionerOptions,
	signed *installer.SignedRuntimeManifest,
	trust installer.TrustStore,
) (*App, error) {
	backend, err := installer.NewSignedRuntimePackPreparationBackend(options, signed, trust)
	if err != nil {
		return nil, err
	}
	adapter, err := installer.NewRuntimePackPreparationAdapter(backend, true)
	if err != nil {
		return nil, err
	}
	// The startup reconciliation is likewise cryptographically gated.
	// Cooperative 15s cancellation is NOT a hard timeout for filesystem work.
	recoveryCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, _ = adapter.RestoreFromTransaction(recoveryCtx)

	app := NewApp()
	app.preparation = installer.NewWailsSafeFullEdgePreparationAPI(adapter)
	return app, nil
}
