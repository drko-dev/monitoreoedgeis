package main

import (
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
	// The startup reconciliation is likewise cryptographically gated. It
	// re-hashes the whole installed pack (~20s for 1.1 GB), so it runs in the
	// background via restoreInBackground once the caller has set its hooks;
	// meanwhile Status reports CHECKING, never READY.
	app := NewApp()
	app.preparation = installer.NewWailsSafeFullEdgePreparationAPI(adapter)
	app.restore = adapter.RestoreFromTransaction
	return app, nil
}
