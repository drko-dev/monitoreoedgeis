//go:build linux

package installer

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

// Both POSIX access and directory default ACLs can grant rights beyond
// simple chmod bits. If the filesystem does not let us inspect xattrs,
// the strict release-owned launch mode must fail closed.
func auditInstalledPackAccessACL(path string) error {
	for _, attr := range []string{"system.posix_acl_access", "system.posix_acl_default"} {
		_, err := unix.Getxattr(path, attr, nil)
		switch {
		case err == nil:
			return fmt.Errorf("%w: extended Linux ACL %s on %q", ErrRuntimePackOwnershipNotSeparated, attr, path)
		case errors.Is(err, unix.ENODATA):
			continue
		default:
			return fmt.Errorf("%w: cannot inspect Linux ACL %s on %q: %v", ErrRuntimePackOwnershipNotSeparated, attr, path, err)
		}
	}
	return nil
}
