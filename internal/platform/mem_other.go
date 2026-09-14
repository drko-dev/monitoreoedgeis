//go:build !darwin

package platform

// memTotalFallback is only reached when /proc/meminfo is unreadable, which on
// Linux means procfs is not mounted. There is no second source to consult.
func memTotalFallback() uint64 { return 0 }
