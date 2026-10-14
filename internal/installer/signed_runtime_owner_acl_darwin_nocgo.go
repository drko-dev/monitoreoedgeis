//go:build darwin && !cgo

package installer

import "fmt"

// A shipping macOS strict owner audit cannot claim ACL protection when
// compiled without native ACL inspection support.
func auditInstalledPackAccessACL(path string) error {
	return fmt.Errorf("%w: native macOS ACL validation unavailable (CGO disabled) for %q", ErrRuntimePackOwnershipNotSeparated, path)
}
