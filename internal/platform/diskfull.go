package platform

import "errors"

// ErrDiskFull reports that a filesystem operation failed because the
// filesystem had no space left for this writer (or the writer is over its
// disk quota). It is a stable, platform-independent sentinel: callers test
// for it with errors.Is, and that works whether the underlying error came
// from a real ENOSPC/EDQUOT or from an error this package already classified.
//
// It exists because the raw errno is not a usable contract on its own. Every
// write site in this repository returns *os.PathError-wrapped errors, and a
// caller that must degrade explicitly ("stop persisting, keep reporting")
// needs to tell "the disk is full" apart from "permission denied" or "the
// stored file is corrupt" without re-implementing errno matching per
// platform. The Edge is built for linux/amd64 and linux/arm64 but is also
// developed and tested on darwin, so the errno set lives behind a build tag
// (diskfull_unix.go / diskfull_other.go) exactly like diskBytes does.
var ErrDiskFull = errors.New("platform: no space left on device")

// IsDiskFull reports whether err was caused by the filesystem being out of
// space or by the writer exceeding its disk quota. It reports true for a raw
// ENOSPC/EDQUOT error, for an error already classified by WrapDiskError, and
// for either of those wrapped by any number of layers of fmt.Errorf or
// *os.PathError. It is safe to call with nil.
func IsDiskFull(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrDiskFull) {
		return true
	}
	return isNoSpaceErrno(err)
}

// WrapDiskError classifies a filesystem error for later inspection. An
// out-of-space error is returned wrapped so that errors.Is(err, ErrDiskFull)
// is true, while the original error (and therefore its path and raw errno)
// stays reachable through Unwrap and its message is preserved byte for byte.
// Every other error — including nil — is returned unchanged, so this is safe
// to apply unconditionally to the result of any write.
func WrapDiskError(err error) error {
	if err == nil || errors.Is(err, ErrDiskFull) || !isNoSpaceErrno(err) {
		return err
	}
	return &diskFullError{err: err}
}

// diskFullError adds the ErrDiskFull sentinel to an existing error without
// inventing a second message: Error() stays exactly the underlying error's
// text (so a log line still names the failing path), while Is reports the
// classification. That matters because these errors are also written into
// bounded, operator-visible status fields (edgebacklog.Status.LastError), and
// a doubled message there would waste the 255-byte budget the same way the
// RTSP safeError path avoids.
type diskFullError struct{ err error }

func (e *diskFullError) Error() string { return e.err.Error() }

func (e *diskFullError) Unwrap() error { return e.err }

func (e *diskFullError) Is(target error) bool { return target == ErrDiskFull }
