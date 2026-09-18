package platform

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// --- D9: CPU ----------------------------------------------------------------

func TestParseProcStat(t *testing.T) {
	// user nice system idle iowait irq softirq steal
	const stat = "cpu  100 20 30 700 50 5 5 0 0 0\ncpu0 1 1 1 1 1\nintr 123\n"
	idle, total, ok := parseProcStat(stat)
	if !ok {
		t.Fatal("expected the aggregate cpu line to parse")
	}
	if want := uint64(750); idle != want { // idle 700 + iowait 50
		t.Errorf("idle = %d, want %d", idle, want)
	}
	if want := uint64(910); total != want {
		t.Errorf("total = %d, want %d", total, want)
	}
}

func TestParseProcStatRejectsGarbage(t *testing.T) {
	for name, data := range map[string]string{
		"empty":          "",
		"no cpu line":    "intr 1\nctxt 2\n",
		"too few fields": "cpu  1 2 3\n",
		"non numeric":    "cpu  1 2 3 four 5 6 7 8\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, ok := parseProcStat(data); ok {
				t.Error("expected parse to fail, but it reported success")
			}
		})
	}
}

// The first reading cannot yield a rate: a utilisation figure needs two.
func TestCPUSamplerPrimesBeforeReporting(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("procfs-only: this host has no /proc/stat")
	}
	s := &CPUSampler{}
	if got := s.Percent(); got != nil {
		t.Errorf("first Percent() = %v, want nil while priming", *got)
	}
	// Burn a little CPU so the second reading spans a non-zero interval.
	for i := 0; i < 5_000_000; i++ {
		_ = i * i
	}
	if got := s.Percent(); got != nil && (*got < 0 || *got > 100) {
		t.Errorf("Percent() = %v, outside [0,100]", *got)
	}
}

// A nil sampler is a legitimate configuration (CPU simply not collected) and
// must not panic.
func TestCollectToleratesNilCPUSampler(t *testing.T) {
	s := Collect(t.TempDir(), nil)
	if s.CPUPercent != nil {
		t.Errorf("CPUPercent = %v, want nil when no sampler was supplied", *s.CPUPercent)
	}
}

func TestCPUPercentArithmetic(t *testing.T) {
	// Half the jiffies in the interval were idle -> 50% busy.
	if got := cpuPercent(100, 200, 150, 300); got == nil || *got != 50 {
		t.Errorf("cpuPercent(half idle) = %v, want 50", got)
	}
	// Fully idle interval.
	if got := cpuPercent(100, 200, 200, 300); got == nil || *got != 0 {
		t.Errorf("cpuPercent(all idle) = %v, want 0", got)
	}
	// Fully busy interval.
	if got := cpuPercent(100, 200, 100, 300); got == nil || *got != 100 {
		t.Errorf("cpuPercent(no idle) = %v, want 100", got)
	}
}

// A counter reset (reboot, wrap) must report nil rather than a nonsense spike.
func TestCPUPercentRejectsUnusablePairs(t *testing.T) {
	for name, args := range map[string][4]uint64{
		// prevIdle, prevTotal, idle, total
		"total went backwards": {100, 200, 100, 150},
		"idle went backwards":  {100, 200, 50, 300},
		"no elapsed time":      {100, 200, 100, 200},
		"idle exceeds total":   {100, 200, 250, 300},
	} {
		t.Run(name, func(t *testing.T) {
			if got := cpuPercent(args[0], args[1], args[2], args[3]); got != nil {
				t.Errorf("got %v, want nil", *got)
			}
		})
	}
}

func TestClampPercent(t *testing.T) {
	for _, tc := range []struct{ in, want float64 }{
		{-5, 0}, {0, 0}, {42.5, 42.5}, {100, 100}, {150, 100},
	} {
		if got := clampPercent(tc.in); got != tc.want {
			t.Errorf("clampPercent(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// --- D10: memory ------------------------------------------------------------

func TestParseMeminfoBytes(t *testing.T) {
	for name, tc := range map[string]struct {
		in   string
		want uint64
	}{
		"kB suffix":   {"  16384 kB", 16384 * 1024},
		"no suffix":   {" 4096", 4096},
		"empty":       {"   ", 0},
		"non numeric": {" abc kB", 0},
	} {
		t.Run(name, func(t *testing.T) {
			if got := parseMeminfoBytes(tc.in); got != tc.want {
				t.Errorf("parseMeminfoBytes(%q) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

// D10/D12: a host without procfs must degrade, never panic.
func TestMemoryBytesDegradesWithoutProcfs(t *testing.T) {
	total, used, available := memoryBytes()
	if used > total && total != 0 {
		t.Errorf("used (%d) exceeds total (%d)", used, total)
	}
	if available > total && total != 0 {
		t.Errorf("available (%d) exceeds total (%d)", available, total)
	}
	if runtime.GOOS == "linux" && total == 0 {
		t.Error("expected /proc/meminfo to yield a total on Linux")
	}
}

// --- D11: disk --------------------------------------------------------------

func TestDiskBytesForDataDir(t *testing.T) {
	dir := t.TempDir()
	total, used, available := diskBytes(dir)
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("statfs is only wired for linux and darwin")
	}
	if total == 0 {
		t.Fatal("expected a non-zero total for an existing directory")
	}
	if used > total {
		t.Errorf("used (%d) exceeds total (%d)", used, total)
	}
	if available > total {
		t.Errorf("available (%d) exceeds total (%d)", available, total)
	}
}

func TestDiskBytesOnEmptyPathIsZero(t *testing.T) {
	if total, used, available := diskBytes(""); total != 0 || used != 0 || available != 0 {
		t.Errorf("diskBytes(\"\") = (%d, %d, %d), want (0, 0, 0)", total, used, available)
	}
}

// The data directory may not exist yet on a first boot; the figures must
// still describe the filesystem it will live on.
func TestDiskTargetWalksUpToAnExistingAncestor(t *testing.T) {
	base := t.TempDir()
	missing := filepath.Join(base, "not", "created", "yet")

	if got := diskTarget(missing); got != base {
		t.Errorf("diskTarget(%q) = %q, want %q", missing, got, base)
	}
	if got := diskTarget(base); got != base {
		t.Errorf("diskTarget(%q) = %q, want itself", base, got)
	}
	if got := diskTarget(""); got != "" {
		t.Errorf("diskTarget(\"\") = %q, want empty", got)
	}
}

// --- D12: temperature -------------------------------------------------------

func TestTemperatureReadsHottestZone(t *testing.T) {
	root := t.TempDir()
	writeZone(t, root, "thermal_zone0", "41200")
	writeZone(t, root, "thermal_zone1", "57800")

	restore := swapThermalRoot(t, root)
	defer restore()

	got := temperatureC()
	if got == nil {
		t.Fatal("expected a temperature reading")
	}
	if *got != 57.8 {
		t.Errorf("temperature = %v, want 57.8 (the hottest zone)", *got)
	}
}

// D7/D12: absence of a sensor is normal, not an error, and never a DEGRADED
// signal on its own. The field is simply omitted.
func TestTemperatureIsNilWhenNoSensorExists(t *testing.T) {
	restore := swapThermalRoot(t, t.TempDir())
	defer restore()

	if got := temperatureC(); got != nil {
		t.Errorf("temperature = %v, want nil on a host with no thermal zones", *got)
	}
}

func TestTemperatureSkipsUnreadableAndImplausibleZones(t *testing.T) {
	root := t.TempDir()
	writeZone(t, root, "thermal_zone0", "not-a-number")
	writeZone(t, root, "thermal_zone1", "9000000") // 9000C: a broken sensor
	writeZone(t, root, "thermal_zone2", "-300000") // below absolute zero
	writeZone(t, root, "thermal_zone3", "38500")

	restore := swapThermalRoot(t, root)
	defer restore()

	got := temperatureC()
	if got == nil {
		t.Fatal("expected the one plausible zone to be reported")
	}
	if *got != 38.5 {
		t.Errorf("temperature = %v, want 38.5", *got)
	}
}

func TestTemperatureIsNilWhenEveryZoneIsGarbage(t *testing.T) {
	root := t.TempDir()
	writeZone(t, root, "thermal_zone0", "garbage")

	restore := swapThermalRoot(t, root)
	defer restore()

	if got := temperatureC(); got != nil {
		t.Errorf("temperature = %v, want nil when nothing parsed", *got)
	}
}

// --- Collect ----------------------------------------------------------------

func TestCollectNeverPanicsAndStaysSelfConsistent(t *testing.T) {
	s := Collect(t.TempDir(), &CPUSampler{})

	if s.MemUsedBytes > s.MemTotalBytes && s.MemTotalBytes != 0 {
		t.Errorf("memory used (%d) exceeds total (%d)", s.MemUsedBytes, s.MemTotalBytes)
	}
	if s.DiskUsedBytes > s.DiskTotalBytes && s.DiskTotalBytes != 0 {
		t.Errorf("disk used (%d) exceeds total (%d)", s.DiskUsedBytes, s.DiskTotalBytes)
	}
	if s.CPUPercent != nil {
		t.Error("the first Collect with a fresh sampler cannot report CPU yet")
	}
}

// --- helpers ----------------------------------------------------------------

func swapThermalRoot(t *testing.T, root string) func() {
	t.Helper()
	prev := thermalRoot
	thermalRoot = root
	return func() { thermalRoot = prev }
}

func writeZone(t *testing.T, root, zone, milli string) {
	t.Helper()
	dir := filepath.Join(root, zone)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "temp"), []byte(milli+"\n"), 0o644); err != nil {
		t.Fatalf("write temp: %v", err)
	}
}
