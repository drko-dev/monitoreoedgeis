//go:build linux || darwin

package platform

import (
	"errors"
	"syscall"
)

// isNoSpaceErrno reports whether err carries one of the two errnos the kernel
// uses for "this writer cannot allocate more space": ENOSPC (filesystem full,
// including the tmpfs/overlayfs case on the appliance) and EDQUOT (the writer
// is over a per-user or per-project quota). EDQUOT is included deliberately:
// on an appliance with a quota-managed data partition it is the errno a full
// disk actually produces, and treating it as a generic I/O error would hide
// the one condition operators can fix by freeing space.
func isNoSpaceErrno(err error) bool {
	return errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.EDQUOT)
}
