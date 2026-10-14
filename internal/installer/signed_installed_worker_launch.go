package installer

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
)

// SignedInstalledRuntimeWorkerLaunchSpec is a non-executing contract for the
// INSTALLED Runtime Pack. It carries an independent signer recheck that a
// future explicitly authorized Worker must invoke before every subprocess
// spawn/restart. It never grants model redistribution or inference rights.
type SignedInstalledRuntimeWorkerLaunchSpec struct {
	RuntimePackWorkerLaunchSpec
	signedBackend *SignedRuntimePackPreparationBackend
}

// VerifyBeforeSpawn authenticates the installed, independently pinned pack
// on EVERY call (including a Worker restart); it must not be cached as a
// permanent trust decision. This does not eliminate an OS-level TOCTOU race.
func (s SignedInstalledRuntimeWorkerLaunchSpec) VerifyBeforeSpawn(ctx context.Context) error {
	if s.signedBackend == nil {
		return ErrRuntimePackSignatureRequired
	}
	if err := s.signedBackend.VerifyInstalledRuntimePack(ctx); err != nil {
		return fmt.Errorf("installed signed Runtime Pack rejected before Worker spawn: %w", err)
	}
	return nil
}

// BuildSignedInstalledWorkerLaunchSpec creates a launch contract from verified
// installed files and the independently signed release payload. The method
// does not launch Python or authorize model weights, and modelsAuthorized
// remains false until separately approved rights and provenance exist.
func (p *SignedRuntimePackPreparationBackend) BuildSignedInstalledWorkerLaunchSpec(
	ctx context.Context, modelsDir, stateDir string,
) (SignedInstalledRuntimeWorkerLaunchSpec, error) {
	var empty SignedInstalledRuntimeWorkerLaunchSpec
	if p == nil || p.backend == nil || p.signed == nil {
		return empty, ErrRuntimePackSignatureRequired
	}
	if err := p.VerifyInstalledRuntimePack(ctx); err != nil {
		return empty, err
	}
	o := p.backend.options
	if !o.Enabled {
		return empty, ErrRuntimePackProvisioningDisabled
	}
	if !filepath.IsAbs(modelsDir) || !filepath.IsAbs(stateDir) {
		return empty, errors.New("model and worker state directories must be absolute")
	}
	installedDir := filepath.Join(o.DataDir, o.TargetName)
	spec, err := BuildRuntimePackWorkerLaunchSpec(
		&p.signed.Manifest, installedDir, modelsDir, stateDir, o.TargetOS, o.TargetArch,
	)
	if err != nil {
		return empty, err
	}
	// A second check catches changes while the non-executing spec is built.
	if err := p.VerifyInstalledRuntimePack(ctx); err != nil {
		return empty, err
	}
	return SignedInstalledRuntimeWorkerLaunchSpec{
		RuntimePackWorkerLaunchSpec: spec,
		signedBackend:               p,
	}, nil
}
