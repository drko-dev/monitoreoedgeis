// Package perf provides a deterministic, opt-in harness that measures Edge
// resource consumption (CPU, RSS, goroutines, file descriptors) while
// running a real internal/rtsp.Manager against N simulated cameras
// (internal/rtsptest.Simulator). It does not touch decode/inference FPS or
// any hardware capacity claim — see docs/performance/CAMERA_SCALE_CPU_RAM.md
// for the exact MEASURED / DERIVED / NOT VALIDATED boundaries.
package perf

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/rtsp"
	"github.com/drko-dev/monitoreoedgeis/internal/rtsptest"
)

const (
	envEnabled  = "GEOCAM_PERF"
	envCameras  = "GEOCAM_PERF_CAMERAS"
	envDuration = "GEOCAM_PERF_DURATION"
)

// Config parameterizes a single benchmark run.
type Config struct {
	Cameras  int
	Duration time.Duration
}

// DefaultProfiles returns the camera-count scenarios Hito X asks for.
// Callers choose which ones to actually run (see docs/performance).
func DefaultProfiles() []int { return []int{1, 5, 10, 25, 50} }

// ParseConfig reads GEOCAM_PERF / GEOCAM_PERF_CAMERAS / GEOCAM_PERF_DURATION.
// enabled reports whether GEOCAM_PERF=1 (the manual/full benchmark gate).
// Defaults (1 camera, 10s) apply when the corresponding var is unset, and
// are returned even when enabled is false so callers can smoke-test parsing.
func ParseConfig() (cfg Config, enabled bool, err error) {
	cfg = Config{Cameras: 1, Duration: 10 * time.Second}
	enabled = os.Getenv(envEnabled) == "1"

	if raw := os.Getenv(envCameras); raw != "" {
		n, convErr := strconv.Atoi(raw)
		if convErr != nil || n <= 0 {
			return Config{}, enabled, fmt.Errorf("perf: invalid %s=%q: must be a positive integer", envCameras, raw)
		}
		cfg.Cameras = n
	}
	if raw := os.Getenv(envDuration); raw != "" {
		d, convErr := time.ParseDuration(raw)
		if convErr != nil || d <= 0 {
			return Config{}, enabled, fmt.Errorf("perf: invalid %s=%q: must be a positive duration", envDuration, raw)
		}
		cfg.Duration = d
	}
	return cfg, enabled, nil
}

// Result is the machine-readable outcome of one Run. A -1 value on
// RSS/FD fields means UNAVAILABLE on this platform (see sampleNow), never a
// real zero measurement. CPUSecondsUsed/ThroughputPPS are DERIVED from
// measured counters, not directly sampled.
type Result struct {
	CameraTarget    int     `json:"camera_target"`
	CameraActive    int     `json:"camera_active"`
	DurationSeconds float64 `json:"duration_seconds"`

	CPUAvailable   bool    `json:"cpu_available"`
	CPUSecondsUsed float64 `json:"cpu_seconds_used"` // DERIVED: end-start rusage

	RSSStartBytes int64 `json:"rss_start_bytes"` // -1 = UNAVAILABLE
	RSSPeakBytes  int64 `json:"rss_peak_bytes"`
	RSSEndBytes   int64 `json:"rss_end_bytes"`

	GoroutinesStart int `json:"goroutines_start"`
	GoroutinesPeak  int `json:"goroutines_peak"`
	GoroutinesEnd   int `json:"goroutines_end"`

	FDStart int `json:"fd_start"` // -1 = UNAVAILABLE
	FDPeak  int `json:"fd_peak"`
	FDEnd   int `json:"fd_end"`

	Errors          int64   `json:"errors"` // reconnects + timeouts + stalls, summed
	PacketsReceived int64   `json:"packets_received"`
	BytesReceived   int64   `json:"bytes_received"`
	ThroughputPPS   float64 `json:"throughput_packets_per_second"` // DERIVED

	FinalStateCounts map[string]int `json:"final_state_counts"`
}

// Run starts cfg.Cameras simulated cameras, drives a real rtsp.Manager
// against them for cfg.Duration, and returns measured/derived resource and
// stream metrics. Shutdown (manager stop + simulator close) is always
// attempted, even on error paths, before Run returns.
func Run(ctx context.Context, cfg Config) (Result, error) {
	if cfg.Cameras <= 0 {
		return Result{}, errors.New("perf: cameras must be > 0")
	}
	if cfg.Duration <= 0 {
		return Result{}, errors.New("perf: duration must be > 0")
	}

	const packetInterval = 50 * time.Millisecond
	autoCount := int(cfg.Duration/packetInterval) + 5

	sims := make([]*rtsptest.Simulator, 0, cfg.Cameras)
	defer func() {
		for _, s := range sims {
			s.Close()
		}
	}()

	targets := make([]rtsp.CameraTarget, 0, cfg.Cameras)
	for i := 0; i < cfg.Cameras; i++ {
		sim, err := rtsptest.NewSimulator(rtsptest.Options{
			AutoPacketCount:    autoCount,
			AutoPacketInterval: packetInterval,
		})
		if err != nil {
			return Result{}, fmt.Errorf("perf: starting simulator %d: %w", i, err)
		}
		sims = append(sims, sim)
		targets = append(targets, rtsp.CameraTarget{
			CandidateKey: fmt.Sprintf("perf-cam-%04d", i),
			Addr:         sim.Addr(),
			RTSPPath:     "/stream",
			StreamRole:   rtsp.StreamRoleSub,
		})
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	mgr := rtsp.NewManager(rtsp.DefaultConfig(), nil, logger)

	start := sampleNow()
	cpuStart, cpuOK := cpuSecondsSelf()
	peak := start

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	sampleDone := make(chan struct{})
	go func() {
		defer close(sampleDone)
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				s := sampleNow()
				if s.Goroutines > peak.Goroutines {
					peak.Goroutines = s.Goroutines
				}
				if s.RSSBytes > peak.RSSBytes {
					peak.RSSBytes = s.RSSBytes
				}
				if s.FDCount > peak.FDCount {
					peak.FDCount = s.FDCount
				}
			}
		}
	}()

	if err := mgr.Start(runCtx); err != nil {
		cancel()
		<-sampleDone
		return Result{}, fmt.Errorf("perf: manager start: %w", err)
	}
	mgr.SetTargets(targets)

	runStart := time.Now()
	select {
	case <-time.After(cfg.Duration):
	case <-ctx.Done():
	}
	actualDuration := time.Since(runStart)

	// Snapshot before Stop: Manager.Stop deletes every supervisor from its
	// internal map, so Snapshot() afterwards would report zero cameras.
	snapshot := mgr.Snapshot()

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
	stopErr := mgr.Stop(stopCtx)
	stopCancel()

	for _, s := range sims {
		s.Close()
	}
	cancel()
	<-sampleDone

	if stopErr != nil {
		return Result{}, fmt.Errorf("perf: manager stop: %w", stopErr)
	}

	end := waitGoroutinesSettle(start.Goroutines, 5, 500*time.Millisecond)
	cpuEnd, _ := cpuSecondsSelf()

	return aggregate(cfg, snapshot, start, peak, end, cpuStart, cpuEnd, cpuOK, actualDuration), nil
}

func aggregate(cfg Config, snap []rtsp.CameraStreamStatus, start, peak, end Sample, cpuStart, cpuEnd float64, cpuOK bool, actualDuration time.Duration) Result {
	var active int
	var errorsTotal int64
	var packets, bytesRecv int64
	finalStates := make(map[string]int, len(snap))
	for _, c := range snap {
		if c.PacketsReceived > 0 {
			active++
		}
		errorsTotal += c.ReconnectCount + c.TimeoutCount + c.StallCount
		packets += c.PacketsReceived
		bytesRecv += c.BytesReceived
		finalStates[string(c.Status)]++
	}

	durationSeconds := actualDuration.Seconds()
	var pps float64
	if durationSeconds > 0 {
		pps = float64(packets) / durationSeconds
	}

	cpuUsed := 0.0
	if cpuOK {
		cpuUsed = cpuEnd - cpuStart
	}

	return Result{
		CameraTarget:     cfg.Cameras,
		CameraActive:     active,
		DurationSeconds:  durationSeconds,
		CPUAvailable:     cpuOK,
		CPUSecondsUsed:   cpuUsed,
		RSSStartBytes:    start.RSSBytes,
		RSSPeakBytes:     peak.RSSBytes,
		RSSEndBytes:      end.RSSBytes,
		GoroutinesStart:  start.Goroutines,
		GoroutinesPeak:   peak.Goroutines,
		GoroutinesEnd:    end.Goroutines,
		FDStart:          start.FDCount,
		FDPeak:           peak.FDCount,
		FDEnd:            end.FDCount,
		Errors:           errorsTotal,
		PacketsReceived:  packets,
		BytesReceived:    bytesRecv,
		ThroughputPPS:    pps,
		FinalStateCounts: finalStates,
	}
}

// Sample is a single point-in-time resource reading.
type Sample struct {
	Goroutines int
	RSSBytes   int64 // -1 = UNAVAILABLE
	FDCount    int   // -1 = UNAVAILABLE
}

func sampleNow() Sample {
	rss := int64(-1)
	if v, ok := rssBytesSelf(); ok {
		rss = v
	}
	fd := -1
	if v, ok := fdCountSelf(); ok {
		fd = v
	}
	return Sample{Goroutines: runtime.NumGoroutine(), RSSBytes: rss, FDCount: fd}
}

// waitGoroutinesSettle polls (bounded by timeout) until the goroutine count
// returns near baseline, to avoid measuring an end-state mid-teardown.
func waitGoroutinesSettle(baseline, margin int, timeout time.Duration) Sample {
	deadline := time.Now().Add(timeout)
	last := sampleNow()
	for last.Goroutines > baseline+margin && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		last = sampleNow()
	}
	return last
}

// rssBytesSelf reads resident set size from /proc/self/statm (Linux only).
// RSS (resident memory the OS accounts to the process) is not the same
// thing as the Go heap runtime.MemStats reports.
func rssBytesSelf() (int64, bool) {
	data, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0, false
	}
	fields := strings.Fields(string(data))
	if len(fields) < 2 {
		return 0, false
	}
	pages, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return 0, false
	}
	return pages * int64(os.Getpagesize()), true
}

// fdCountSelf counts open file descriptors via /proc/self/fd (Linux only).
func fdCountSelf() (int, bool) {
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return 0, false
	}
	return len(entries), true
}

// cpuSecondsSelf returns cumulative process user+system CPU time via
// getrusage(RUSAGE_SELF), which is available on every unix target this
// project builds for (Linux amd64/arm64, and macOS during local dev).
func cpuSecondsSelf() (float64, bool) {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0, false
	}
	utime := float64(ru.Utime.Sec) + float64(ru.Utime.Usec)/1e6
	stime := float64(ru.Stime.Sec) + float64(ru.Stime.Usec)/1e6
	return utime + stime, true
}
