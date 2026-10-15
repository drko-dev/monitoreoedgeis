package installer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ErrRuntimePackOwnershipNotSeparated is a negative security assertion:
// a process sharing the installed pack owner cannot rely on chmod as an
// immutable update boundary.
var ErrRuntimePackOwnershipNotSeparated = errors.New("signed installed Runtime Pack is not independently owned")

// SignedPackOwnershipAudit is diagnostic metadata ONLY: it is not a release
// certificate, code signature, privilege grant or proof against ACLs/races.
type SignedPackOwnershipAudit struct {
	InstalledRoot string
	ProcessUID    uint32
	PackOwnerUID  uint32
	Entries       int
	Ancestors     int
}

// AuditIndependentInstalledPackOwnership checks filesystem ownership and
// unsafe group/world write bits on the installed tree and every parent up
// to /, without mutating permissions or the host. An owner distinct from the
// current effective UID is mandatory. Windows deliberately fails closed.
func (p *SignedRuntimePackPreparationBackend) AuditIndependentInstalledPackOwnership(ctx context.Context) (SignedPackOwnershipAudit, error) {
	var report SignedPackOwnershipAudit
	if p == nil || p.backend == nil {
		return report, ErrRuntimePackSignatureRequired
	}
	if err := p.verifyReadOnlyInstalledRuntimePack(ctx); err != nil {
		return report, err
	}
	installed := filepath.Join(p.backend.options.DataDir, p.backend.options.TargetName)
	root, err := filepath.EvalSymlinks(installed)
	if err != nil {
		return report, err
	}
	if !filepath.IsAbs(root) {
		return report, fmt.Errorf("%w: installed path not absolute", ErrRuntimePackOwnershipNotSeparated)
	}
	report.InstalledRoot = root
	return auditInstalledPackOwnerBoundary(ctx, root, report)
}

// BuildOwnershipSeparatedSignedWorkerLaunchSpec is a stricter NON-EXECUTING
// option for a future release-owned installer. Existing lab-only signed launch
// contracts are intentionally unchanged and not considered owner-separated.
func (p *SignedRuntimePackPreparationBackend) BuildOwnershipSeparatedSignedWorkerLaunchSpec(
	ctx context.Context, modelsDir, stateDir string,
) (SignedInstalledRuntimeWorkerLaunchSpec, error) {
	var empty SignedInstalledRuntimeWorkerLaunchSpec
	spec, err := p.BuildSignedInstalledWorkerLaunchSpec(ctx, modelsDir, stateDir)
	if err != nil {
		return empty, err
	}
	if _, err := p.AuditIndependentInstalledPackOwnership(ctx); err != nil {
		return empty, err
	}
	spec.enforceIndependentOwnership = true
	return spec, nil
}

// evaluateSignedPackOwnerAttrs is pure and testable without root/chown.
// ownerUID must match the installed release's independently-owned tree; the
// current process UID must be different. Group/world writable directories
// make ancestor rename/swap possible and are always rejected.
func evaluateSignedPackOwnerAttrs(path string, ownerUID, processUID, packOwnerUID uint32,
	mode os.FileMode, isPack bool) error {
	if ownerUID == processUID {
		return fmt.Errorf("%w: process owns %s", ErrRuntimePackOwnershipNotSeparated, path)
	}
	if isPack && ownerUID != packOwnerUID {
		return fmt.Errorf("%w: mixed pack ownership: %s", ErrRuntimePackOwnershipNotSeparated, path)
	}
	if mode&os.ModeSymlink != 0 || (!mode.IsDir() && !mode.IsRegular()) {
		return fmt.Errorf("%w: unsafe object: %s", ErrRuntimePackOwnershipNotSeparated, path)
	}
	if isPack && mode.Perm()&0o222 != 0 {
		return fmt.Errorf("%w: writable pack entry: %s", ErrRuntimePackOwnershipNotSeparated, path)
	}
	if !isPack && mode.Perm()&0o022 != 0 {
		return fmt.Errorf("%w: group/other writable ancestor: %s", ErrRuntimePackOwnershipNotSeparated, path)
	}
	return nil
}
