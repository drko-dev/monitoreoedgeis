package installer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ErrRuntimePackNotReadOnly is an advisory launch boundary: writable pack
// files/directories are never accepted by the opt-in signed launch contract.
// File mode bits alone are NOT an immutable-ownership security boundary:
// same-user owners/root may chmod, replace files or swap the parent directory.
var ErrRuntimePackNotReadOnly = errors.New("signed installed Runtime Pack is not mode-read-only")

// SignedInstalledRuntimeWorkerLaunchSpec is a non-executing contract for the
// INSTALLED Runtime Pack. It carries an independent signer recheck that a
// future explicitly authorized Worker must invoke before every subprocess
// spawn/restart. It never grants model redistribution or inference rights.
type SignedInstalledRuntimeWorkerLaunchSpec struct {
	// Do not anonymously embed RuntimePackWorkerLaunchSpec: its exported
	// Command/RuntimePackRoot/Environment fields are freely mutable by the
	// consumer AFTER an authenticated launch spec was constructed.
	launch                      RuntimePackWorkerLaunchSpec
	signedBackend               *SignedRuntimePackPreparationBackend
	enforceIndependentOwnership bool
}

// The release-owned signed contract intentionally exposes only read-only
// accessor methods, returning defensive copies for slices. This prevents
// accidental redirection to a different executable or environment after
// signing; it is not a substitute for a trusted process boundary.
func (s SignedInstalledRuntimeWorkerLaunchSpec) ArtifactsVerified() bool {
	return s.launch.ArtifactsVerified()
}
func (s SignedInstalledRuntimeWorkerLaunchSpec) ModelsAuthorized() bool {
	return s.launch.ModelsAuthorized()
}
func (s SignedInstalledRuntimeWorkerLaunchSpec) RuntimePackRootPath() string {
	return s.launch.RuntimePackRootPath()
}
func (s SignedInstalledRuntimeWorkerLaunchSpec) CommandPath() string {
	return s.launch.CommandPath()
}
func (s SignedInstalledRuntimeWorkerLaunchSpec) CommandArgs() []string {
	return s.launch.CommandArgs()
}
func (s SignedInstalledRuntimeWorkerLaunchSpec) ProcessEnvironment() []string {
	return s.launch.ProcessEnvironment()
}
func (s SignedInstalledRuntimeWorkerLaunchSpec) PersistentModelsDir() string {
	return s.launch.PersistentModelsDir()
}
func (s SignedInstalledRuntimeWorkerLaunchSpec) ProcessStartTimeout() time.Duration {
	return s.launch.ProcessStartTimeout()
}

// VerifyBeforeSpawn authenticates the installed, independently pinned pack
// on EVERY call (including a Worker restart); it must not be cached as a
// permanent trust decision. This does not eliminate an OS-level TOCTOU race.
func (s SignedInstalledRuntimeWorkerLaunchSpec) VerifyBeforeSpawn(ctx context.Context) error {
	if s.signedBackend == nil {
		return ErrRuntimePackSignatureRequired
	}
	if s.enforceIndependentOwnership {
		if _, err := s.signedBackend.AuditIndependentInstalledPackOwnership(ctx); err != nil {
			return fmt.Errorf("installed owner-separated Runtime Pack rejected before Worker spawn: %w", err)
		}
		return nil
	}
	if err := s.signedBackend.verifyReadOnlyInstalledRuntimePack(ctx); err != nil {
		return fmt.Errorf("installed signed Runtime Pack rejected before Worker spawn: %w", err)
	}
	return nil
}

// verifyReadOnlyInstalledRuntimePack requires a fully signed, pinned installed
// tree AND no write permission bits on any installed entry. This is a modest
// fail-closed check against accidentally mutable releases, NOT proof of
// immutable ownership or an atomic fexecve/dyld trust chain.
//
// It is read-only: it never chmods source or installed lab media.
func (p *SignedRuntimePackPreparationBackend) verifyReadOnlyInstalledRuntimePack(ctx context.Context) error {
	if err := p.VerifyInstalledRuntimePack(ctx); err != nil {
		return err
	}
	o := p.backend.options
	root, err := canonicalDirectory(filepath.Join(o.DataDir, o.TargetName))
	if err != nil {
		return err
	}
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 ||
			(!info.IsDir() && !info.Mode().IsRegular()) {
			return fmt.Errorf("%w: unexpected filesystem entry %q", ErrRuntimePackNotReadOnly, path)
		}
		if info.Mode().Perm()&0o222 != 0 {
			return fmt.Errorf("%w: writable installed entry %q (mode %04o)",
				ErrRuntimePackNotReadOnly, path, info.Mode().Perm())
		}
		return nil
	})
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
	if err := p.verifyReadOnlyInstalledRuntimePack(ctx); err != nil {
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
	if err := p.verifyReadOnlyInstalledRuntimePack(ctx); err != nil {
		return empty, err
	}
	return SignedInstalledRuntimeWorkerLaunchSpec{
		launch:        spec,
		signedBackend: p,
	}, nil
}
