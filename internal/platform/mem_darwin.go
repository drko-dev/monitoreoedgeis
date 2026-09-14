package platform

import (
	"os/exec"
	"strconv"
	"strings"
	"sync"
)

// memTotalFallback reports total RAM on macOS, where there is no procfs.
//
// It shells out to sysctl because the stdlib syscall package exposes no
// 64-bit sysctl on Darwin, and pulling in golang.org/x/sys — the module has
// zero dependencies today — to read one number on a development host is a
// bad trade. The value is static for the life of the boot, so it is read
// once and cached rather than on every heartbeat.
//
// Used memory is not reported on macOS: deriving it needs mach
// host_statistics, which needs cgo, and the Go core is built CGO_ENABLED=0.
// Linux amd64/arm64 are the deployment targets and get the full figures from
// /proc/meminfo.
var memTotalFallback = sync.OnceValue(func() uint64 {
	out, err := exec.Command("/usr/sbin/sysctl", "-n", "hw.memsize").Output()
	if err != nil {
		return 0
	}
	n, err := strconv.ParseUint(strings.TrimSpace(string(out)), 10, 64)
	if err != nil {
		return 0
	}
	return n
})
