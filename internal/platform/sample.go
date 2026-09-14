package platform

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Sample is a point-in-time reading of host resource usage, as opposed to
// Info which is static host description gathered once at startup.
//
// Every field degrades rather than failing: a value this host does not
// expose is left at its zero value (or nil for the optional pointers), never
// a panic and never a fabricated number. Pointer fields are optional by
// design so "not available" is distinguishable from "measured zero".
type Sample struct {
	// CPUPercent is whole-host CPU utilisation in [0,100], or nil where it
	// cannot be measured without cgo (see CPUSampler).
	CPUPercent     *float64
	MemTotalBytes  uint64
	MemUsedBytes   uint64
	DiskTotalBytes uint64
	DiskUsedBytes  uint64
	// TemperatureC is the hottest readable thermal zone in degrees Celsius,
	// or nil where the host exposes none. Absence is normal (most VMs and
	// every non-Linux host) and is never on its own a degraded condition.
	TemperatureC *float64
}

// CPUSampler measures whole-host CPU utilisation as the delta between two
// consecutive /proc/stat readings. A single reading cannot yield a rate, so
// the first Percent call after construction always returns nil ("priming");
// every later call reports utilisation over the interval since the previous
// call. It is not safe for concurrent use — the heartbeat module owns one
// and calls it from its single scheduling goroutine.
//
// On hosts without procfs (macOS dev machines, Windows) Percent always
// returns nil: measuring host CPU there needs cgo, and the Go core is built
// CGO_ENABLED=0. Linux amd64/arm64 — the actual deployment targets — are
// fully covered.
type CPUSampler struct {
	prevIdle  uint64
	prevTotal uint64
	primed    bool
}

// Percent returns host CPU utilisation since the previous call, or nil when
// unavailable or still priming.
func (s *CPUSampler) Percent() *float64 {
	idle, total, ok := readProcStat()
	if !ok {
		return nil
	}

	prevIdle, prevTotal, primed := s.prevIdle, s.prevTotal, s.primed
	s.prevIdle, s.prevTotal, s.primed = idle, total, true
	if !primed {
		return nil
	}
	return cpuPercent(prevIdle, prevTotal, idle, total)
}

// cpuPercent converts two /proc/stat readings into a utilisation figure, or
// nil when the pair cannot yield one. Counters are monotonic, so a wrap or a
// reboot between readings shows up as a counter going backwards: report
// nothing rather than a nonsense spike.
func cpuPercent(prevIdle, prevTotal, idle, total uint64) *float64 {
	if total < prevTotal || idle < prevIdle {
		return nil
	}
	deltaTotal := total - prevTotal
	deltaIdle := idle - prevIdle
	if deltaTotal == 0 || deltaIdle > deltaTotal {
		return nil
	}
	pct := clampPercent((1 - float64(deltaIdle)/float64(deltaTotal)) * 100)
	return &pct
}

func clampPercent(v float64) float64 {
	switch {
	case v < 0:
		return 0
	case v > 100:
		return 100
	default:
		return v
	}
}

// readProcStat returns (idle+iowait, total) jiffies from the aggregate "cpu"
// line of /proc/stat.
func readProcStat() (idle, total uint64, ok bool) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, 0, false
	}
	return parseProcStat(string(data))
}

func parseProcStat(data string) (idle, total uint64, ok bool) {
	for _, line := range strings.Split(data, "\n") {
		if !strings.HasPrefix(line, "cpu ") {
			continue
		}
		fields := strings.Fields(line)
		// Shape: cpu user nice system idle iowait irq softirq steal ...
		if len(fields) < 5 {
			return 0, 0, false
		}
		for i, f := range fields[1:] {
			v, err := strconv.ParseUint(f, 10, 64)
			if err != nil {
				return 0, 0, false
			}
			total += v
			// Fields 3 and 4 (0-indexed within fields[1:]) are idle and iowait.
			if i == 3 || i == 4 {
				idle += v
			}
		}
		return idle, total, true
	}
	return 0, 0, false
}

// Collect gathers one resource sample. dataDir selects which filesystem the
// disk figures describe: it is GEOCAM_DATA_DIR, not an arbitrary mount, so
// the reported disk is the one the agent would actually run out of.
//
// cpu may be nil, in which case CPU utilisation is simply omitted.
func Collect(dataDir string, cpu *CPUSampler) Sample {
	s := Sample{}
	if cpu != nil {
		s.CPUPercent = cpu.Percent()
	}
	s.MemTotalBytes, s.MemUsedBytes = memoryBytes()
	s.DiskTotalBytes, s.DiskUsedBytes = diskBytes(diskTarget(dataDir))
	s.TemperatureC = temperatureC()
	return s
}

// diskTarget walks up from dataDir to the nearest existing ancestor. A data
// directory that has not been created yet still yields the figures for the
// filesystem it will live on, instead of no disk metrics at all.
func diskTarget(dataDir string) string {
	dir := strings.TrimSpace(dataDir)
	if dir == "" {
		return ""
	}
	for {
		if _, err := os.Stat(dir); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// memoryBytes returns (total, used) RAM in bytes, or (0, 0) when the host
// does not expose it. "Used" is total minus MemAvailable, which counts
// reclaimable page cache as free — the figure an operator actually cares
// about, not MemFree.
func memoryBytes() (total, used uint64) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		// No procfs (macOS/Windows dev hosts): fall back to whatever the
		// platform-specific file can determine, usually total only.
		return memTotalFallback(), 0
	}
	var available uint64
	var haveAvailable bool
	for _, line := range strings.Split(string(data), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch key {
		case "MemTotal":
			total = parseMeminfoBytes(value)
		case "MemAvailable":
			available = parseMeminfoBytes(value)
			haveAvailable = true
		}
	}
	if total > 0 && haveAvailable && available <= total {
		used = total - available
	}
	return total, used
}

// parseMeminfoBytes converts a /proc/meminfo value ("  16384000 kB") to bytes.
func parseMeminfoBytes(value string) uint64 {
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return 0
	}
	n, err := strconv.ParseUint(fields[0], 10, 64)
	if err != nil {
		return 0
	}
	if len(fields) > 1 && strings.EqualFold(fields[1], "kB") {
		return n * 1024
	}
	return n
}

// thermalRoot is the sysfs thermal directory, overridden in tests.
var thermalRoot = "/sys/class/thermal"

// temperatureC returns the hottest readable thermal zone in degrees Celsius,
// or nil when the host exposes none. This is the generic Linux thermal
// interface, not a Raspberry-specific path: absence is expected on most VMs
// and on every non-Linux host, and is never treated as an error.
func temperatureC() *float64 {
	zones, err := filepath.Glob(filepath.Join(thermalRoot, "thermal_zone*", "temp"))
	if err != nil || len(zones) == 0 {
		return nil
	}
	var hottest float64
	var found bool
	for _, zone := range zones {
		data, err := os.ReadFile(zone)
		if err != nil {
			continue
		}
		milli, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
		if err != nil {
			continue
		}
		c := float64(milli) / 1000
		// Guard against sensors that report obvious garbage rather than
		// surfacing -273C or 5000C as a real reading.
		if c < -50 || c > 200 {
			continue
		}
		if !found || c > hottest {
			hottest, found = c, true
		}
	}
	if !found {
		return nil
	}
	return &hottest
}
