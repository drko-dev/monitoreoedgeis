package main

import (
	"context"
	"errors"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/installer"
)

// NewAppWithPinnedRuntimePack provides the explicit, release-owned wiring
// boundary for H5A/Wails -> H5B -> H6B. Production NewApp stays fail-closed.
// The embedding release must supply a trusted digest from outside packDir.
func NewAppWithPinnedRuntimePack(options installer.RuntimePackProvisionerOptions) (*App, error) {
	if !options.Enabled {
		return nil, installer.ErrRuntimePackProvisioningDisabled
	}
	if options.ExpectedManifestSHA256 == "" {
		return nil, installer.ErrRuntimePackManifestPinRequired
	}
	if options.PackDir == "" || options.DataDir == "" {
		return nil, errors.New("release-owned pack and persistent data locations are required")
	}
	backend, err := installer.NewTransactionalRuntimePackProvisioner(options)
	if err != nil {
		return nil, err
	}
	adapter, err := installer.NewRuntimePackPreparationAdapter(backend, true)
	if err != nil {
		return nil, err
	}
	// Bootstrap from transactional evidence, not H5B's in-memory default.
	// An absent/corrupt active pack remains FAILED (never READY); the
	// operation error is exposed through Status/Diagnostic, not ignored.
	// Bound cooperative recovery and lock acquisition to a 15-second context.
	// This is not a hard deadline for non-cancellable filesystem syscalls.
	// Timeout never implies successful recovery or readiness.
	recoveryCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, _ = adapter.RestoreFromTransaction(recoveryCtx)
	app := NewApp()
	app.preparation = adapter
	return app, nil
}
