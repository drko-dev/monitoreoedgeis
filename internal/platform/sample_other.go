//go:build !linux && !darwin

package platform

// diskBytes has no portable statfs(2) on this platform. Disk telemetry is
// simply omitted rather than reported as a guess; availableKnown is always
// false here.
func diskBytes(_ string) (total, used, available uint64, availableKnown bool) {
	return 0, 0, 0, false
}
