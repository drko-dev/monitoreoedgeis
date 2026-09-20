//go:build !linux && !darwin

package platform

// isNoSpaceErrno has no portable errno to match on this platform, so nothing
// is classified from the raw error here. ErrDiskFull itself still works:
// callers can produce it explicitly, and IsDiskFull still honours an error
// that already carries the sentinel. This mirrors diskBytes' behaviour in
// sample_other.go — the unsupported platform degrades to "no classification"
// rather than guessing at a number or an errno.
func isNoSpaceErrno(error) bool { return false }
