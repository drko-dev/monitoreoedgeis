//go:build linux || darwin

package platform

import "syscall"

// diskBytes returns (total, used) bytes for the filesystem holding path,
// via statfs(2) — no cgo, no external dependency. An empty path or a failed
// syscall yields (0, 0) rather than an error: disk figures are telemetry,
// not a reason to fail a heartbeat.
//
// "Used" is deliberately total minus *free*, not total minus *available*, so
// it matches what df reports as used rather than silently counting
// root-reserved blocks as consumed.
func diskBytes(path string) (total, used uint64) {
	if path == "" {
		return 0, 0
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0
	}
	// Bsize is int64 on Linux and uint32 on Darwin; widen explicitly.
	bsize := uint64(st.Bsize)
	if bsize == 0 || st.Blocks == 0 {
		return 0, 0
	}
	total = st.Blocks * bsize
	if st.Bfree <= st.Blocks {
		used = (st.Blocks - st.Bfree) * bsize
	}
	return total, used
}
