// Package platform collects host and runtime information.
//
// It is deliberately portable: nothing here is Raspberry-specific and nothing
// requires cgo. Values that cannot be determined on the current host are
// reported as "unknown" (or 0) instead of panicking.
package platform

import (
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
)

// Unknown is the placeholder for values this host does not expose.
const Unknown = "unknown"

// SupportedArchitectures are the deployment targets for GEO CAM Edge.
// Other architectures still run (macOS/arm64 is the dev environment) but are
// reported as unsupported rather than causing a failure.
var SupportedArchitectures = []string{"amd64", "arm64"}

// Info describes the host the agent runs on.
type Info struct {
	Hostname      string `json:"hostname"`
	OS            string `json:"os"`
	GOOS          string `json:"goos"`
	GOARCH        string `json:"goarch"`
	Kernel        string `json:"kernel"`
	CPUCount      int    `json:"cpu_count"`
	MemTotalByte  uint64 `json:"mem_total_bytes"`
	ArchSupported bool   `json:"arch_supported"`
}

// Detect gathers host information. It never panics.
func Detect() Info {
	hostname, err := os.Hostname()
	if err != nil || hostname == "" {
		hostname = Unknown
	}

	return Info{
		Hostname:      hostname,
		OS:            osName(),
		GOOS:          runtime.GOOS,
		GOARCH:        runtime.GOARCH,
		Kernel:        kernelVersion(),
		CPUCount:      runtime.NumCPU(),
		MemTotalByte:  memTotalBytes(),
		ArchSupported: ArchSupported(runtime.GOARCH),
	}
}

// ArchSupported reports whether arch is a supported deployment target.
func ArchSupported(arch string) bool {
	return slices.Contains(SupportedArchitectures, arch)
}

// osName returns the distro pretty name on Linux, falling back to GOOS.
func osName() string {
	data, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return runtime.GOOS
	}
	for _, line := range strings.Split(string(data), "\n") {
		name, value, ok := strings.Cut(line, "=")
		if ok && name == "PRETTY_NAME" {
			if v := strings.Trim(strings.TrimSpace(value), `"`); v != "" {
				return v
			}
		}
	}
	return runtime.GOOS
}

// kernelVersion reads the kernel release where procfs is available.
func kernelVersion() string {
	data, err := os.ReadFile("/proc/sys/kernel/osrelease")
	if err != nil {
		return Unknown
	}
	if v := strings.TrimSpace(string(data)); v != "" {
		return v
	}
	return Unknown
}

// memTotalBytes reads total RAM from procfs. Returns 0 when unavailable.
func memTotalBytes() uint64 {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "MemTotal:") {
			continue
		}
		fields := strings.Fields(line)
		// Expected shape: "MemTotal:  16384000 kB"
		if len(fields) < 2 {
			return 0
		}
		kb, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return 0
		}
		return kb * 1024
	}
	return 0
}
