//go:build !darwin && !linux && !windows

package installer

import (
	"context"
	"fmt"
)

func auditInstalledPackOwnerBoundary(_ context.Context, _ string, report SignedPackOwnershipAudit) (SignedPackOwnershipAudit, error) {
	return report, fmt.Errorf("%w: platform ownership audit unsupported", ErrRuntimePackOwnershipNotSeparated)
}
