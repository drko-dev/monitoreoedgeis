//go:build windows

package installer

import (
	"context"
	"fmt"
)

// Windows needs explicit ACL/owner-token verification and an independently
// approved process identity. Do not silently treat mode bits as that proof.
func auditInstalledPackOwnerBoundary(_ context.Context, _ string, report SignedPackOwnershipAudit) (SignedPackOwnershipAudit, error) {
	return report, fmt.Errorf("%w: Windows ACL owner boundary not implemented", ErrRuntimePackOwnershipNotSeparated)
}
