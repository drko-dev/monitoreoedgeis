package platform

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestIsDiskFullNil(t *testing.T) {
	if IsDiskFull(nil) {
		t.Fatal("IsDiskFull(nil) = true, want false")
	}
}

func TestIsDiskFullUnrelatedErrors(t *testing.T) {
	for _, err := range []error{
		errors.New("some unrelated failure"),
		os.ErrNotExist,
		os.ErrPermission,
		syscall.EACCES,
		syscall.EIO,
	} {
		if IsDiskFull(err) {
			t.Errorf("IsDiskFull(%v) = true, want false", err)
		}
	}
}

// TestIsDiskFullRawErrnos covers the two errnos the kernel uses for "this
// writer cannot allocate space". They are injected directly rather than
// produced by a real full filesystem so the assertion is deterministic on
// every host; TestIsDiskFullRealKernelNoSpace covers the real-filesystem case
// where the kernel is available.
func TestIsDiskFullRawErrnos(t *testing.T) {
	for _, errno := range []error{syscall.ENOSPC, syscall.EDQUOT} {
		if !IsDiskFull(errno) {
			t.Errorf("IsDiskFull(%v) = false, want true", errno)
		}
	}
}

// TestIsDiskFullThroughWrapping is the property that actually matters at the
// call sites: every write in this repository returns an error wrapped in
// *os.PathError and often in another fmt.Errorf layer, and the classification
// must survive all of it.
func TestIsDiskFullThroughWrapping(t *testing.T) {
	base := &os.PathError{Op: "write", Path: "/data/evidence/clips/x.mp4.tmp", Err: syscall.ENOSPC}
	wrapped := fmt.Errorf("evidence: encode clip: %w", base)

	if !IsDiskFull(base) {
		t.Error("IsDiskFull(*os.PathError{ENOSPC}) = false, want true")
	}
	if !IsDiskFull(wrapped) {
		t.Error("IsDiskFull(fmt.Errorf-wrapped ENOSPC) = false, want true")
	}
	if !errors.Is(wrapped, syscall.ENOSPC) {
		t.Error("the raw errno must stay reachable by errors.Is after classification")
	}
}

func TestWrapDiskErrorClassifiesNoSpace(t *testing.T) {
	base := &os.PathError{Op: "write", Path: "/data/pending/00000000000000000001.json.tmp", Err: syscall.ENOSPC}

	got := WrapDiskError(base)
	if !errors.Is(got, ErrDiskFull) {
		t.Fatalf("errors.Is(WrapDiskError(ENOSPC), ErrDiskFull) = false, want true")
	}
	// The original error must stay reachable, so an operator still sees the
	// failing path and the raw errno.
	if !errors.Is(got, syscall.ENOSPC) {
		t.Error("the raw errno must stay reachable through the classification")
	}
	var pathErr *os.PathError
	if !errors.As(got, &pathErr) || pathErr.Path != base.Path {
		t.Errorf("errors.As must recover the original *os.PathError, got %v", got)
	}
	// The message must not be doubled: these strings are also copied into
	// bounded, operator-visible status fields.
	if got.Error() != base.Error() {
		t.Errorf("classified message = %q, want the original %q unchanged", got.Error(), base.Error())
	}
}

func TestWrapDiskErrorLeavesOtherErrorsAlone(t *testing.T) {
	for _, err := range []error{
		nil,
		errors.New("unrelated"),
		os.ErrPermission,
		syscall.EIO,
	} {
		if got := WrapDiskError(err); got != err {
			t.Errorf("WrapDiskError(%v) = %v, want the same error unchanged", err, got)
		}
	}
}

func TestWrapDiskErrorIsIdempotent(t *testing.T) {
	once := WrapDiskError(&os.PathError{Op: "write", Path: "p", Err: syscall.ENOSPC})
	twice := WrapDiskError(once)
	if twice != once {
		t.Errorf("WrapDiskError must not re-wrap an already classified error")
	}
	if !errors.Is(twice, ErrDiskFull) {
		t.Error("idempotent wrapping must keep the sentinel")
	}
}

// TestWrapDiskErrorAlreadySentinel covers a caller that constructed ErrDiskFull
// itself (or wrapped it) with no underlying errno at all — the case the
// non-linux/non-darwin build tag has to rely on.
func TestWrapDiskErrorAlreadySentinel(t *testing.T) {
	err := fmt.Errorf("edgebacklog: persist: %w", ErrDiskFull)
	if !IsDiskFull(err) {
		t.Fatal("IsDiskFull(ErrDiskFull-wrapped) = false, want true")
	}
	if got := WrapDiskError(err); got != err {
		t.Error("an error already carrying ErrDiskFull must be returned unchanged")
	}
}

// TestIsDiskFullRealKernelNoSpace validates the classification against a real
// kernel ENOSPC rather than a hand-built errno. /dev/full is a Linux device
// whose every write fails with ENOSPC; it does not exist on darwin, where the
// test skips instead of pretending. This is the one assertion in the set that
// proves the errno really is what the kernel produces for a full device.
func TestIsDiskFullRealKernelNoSpace(t *testing.T) {
	const devFull = "/dev/full"
	f, err := os.OpenFile(devFull, os.O_WRONLY, 0)
	if err != nil {
		t.Skipf("%s is not available on this host (%v); the deterministic errno cases above still cover the classification", devFull, err)
	}
	defer f.Close()

	_, err = f.Write([]byte("x"))
	if err == nil {
		t.Skipf("%s accepted a write on this host; there is no real ENOSPC to assert against", devFull)
	}
	if !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("write to %s returned %v, which does not carry ENOSPC; the premise of this test is wrong", devFull, err)
	}
	if !IsDiskFull(err) {
		t.Errorf("IsDiskFull(real kernel ENOSPC from %s) = false, want true", devFull)
	}
	if got := WrapDiskError(err); !errors.Is(got, ErrDiskFull) {
		t.Errorf("WrapDiskError(real kernel ENOSPC) did not classify: %v", got)
	}
}

// TestIsDiskFullRealFilesystemWriteFailure exercises the same classification
// through a genuine os write against a directory that cannot be written, to
// confirm the classifier does not simply return true for every write error.
func TestIsDiskFullRealFilesystemWriteFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: a read-only directory is still writable, so this case cannot be produced")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	err := os.WriteFile(filepath.Join(dir, "x"), []byte("x"), 0o600)
	if err == nil {
		t.Skip("the directory remained writable on this host; skipping rather than asserting a false premise")
	}
	if IsDiskFull(err) {
		t.Errorf("IsDiskFull(%v) = true, want false: a permission failure is not a full disk", err)
	}
	if got := WrapDiskError(err); got != err {
		t.Errorf("WrapDiskError(%v) must return a permission failure unchanged, got %v", err, got)
	}
}
