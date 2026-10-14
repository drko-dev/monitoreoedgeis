//go:build darwin && cgo

package installer

/*
#include <sys/acl.h>
#include <stdlib.h>
#include <errno.h>

// On Darwin, acl_get_file returns NULL/ENOENT for NO extended ACL,
// and non-NULL for an extended ACL; acl_get_entry returns 0 on success.
// Reject every non-NULL extended ACL (even an empty ACL) conservatively.
// Unknown native errors must fail closed in Go.
static int geocam_has_extended_acl(const char *path) {
	acl_t a = acl_get_file(path, ACL_TYPE_EXTENDED);
	if (a == NULL) return -errno;
	acl_free(a);
	return 1;
}
*/
import "C"

import (
	"fmt"
	"os"
	"unsafe"
)

func auditInstalledPackAccessACL(path string) error {
	cpath := C.CString(path)
	defer C.free(unsafe.Pointer(cpath))
	switch r := C.geocam_has_extended_acl(cpath); {
	case r == 0:
		// Native helper never intentionally returns 0. An unexpected native
		// result must not be silently interpreted as "no ACL".
		return fmt.Errorf("%w: unexpected native macOS ACL result on %q", ErrRuntimePackOwnershipNotSeparated, path)
	case r == -C.int(C.ENOENT):
		// macOS returns ENOENT when *the ACL* is absent. Distinguish this
		// from a concurrently removed path by checking the file again.
		info, err := os.Lstat(path)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: ACL-absent path not verified: %q: %v", ErrRuntimePackOwnershipNotSeparated, path, err)
		}
		return nil
	case r > 0:
		return fmt.Errorf("%w: macOS extended ACL present on %q", ErrRuntimePackOwnershipNotSeparated, path)
	default:
		return fmt.Errorf("%w: macOS extended ACL could not be inspected on %q", ErrRuntimePackOwnershipNotSeparated, path)
	}
}
