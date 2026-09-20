package fulledge

import (
	"fmt"
	"log/slog"
	"sync/atomic"

	"github.com/drko-dev/monitoreoedgeis/internal/platform"
)

// LimitsConfig configures resource bounds for Full Edge operations.
type LimitsConfig struct {
	MaxConcurrentInference int // Bounds concurrent inference workers (semaphore)
	// QueueDepth is the configured value of
	// GEOCAM_EDGE_INFERENCE_QUEUE_DEPTH. It is deliberately reported as
	// QueueDepth in LimitsStatus for config visibility, but it is NOT an
	// enforced bound and must never be presented as one: the only admission
	// gate on the inference path is the MaxConcurrentInference semaphore
	// (TryAcquireInference), and the only real frame-level queue for the
	// vision sink is processing.Router's per-sink channel, sized by
	// GEOCAM_VIDEO_QUEUE_DEPTH. A future change should either wire this knob
	// to a real queue or remove it; until then /status reports the semaphore's
	// real capacity against InFlightInference (see internal/health).
	QueueDepth       int     // Reported for config visibility only — not enforced
	MinFreeDiskBytes uint64  // Minimum free disk bytes required to persist evidence
	MaxMemoryPercent float64 // If > 0, marks memory pressure when host memory exceeds this %
}

// LimitsStatus exposes runtime resource limits and saturation metrics.
type LimitsStatus struct {
	MaxConcurrentInference int     `json:"max_concurrent_inference"`
	InFlightInference      int     `json:"in_flight_inference"`
	QueueDepth             int     `json:"queue_depth"`
	QueueDropped           int64   `json:"queue_dropped"`
	MinFreeDiskBytes       uint64  `json:"min_free_disk_bytes"`
	DiskFreeBytes          uint64  `json:"disk_free_bytes"`
	DiskSaturated          bool    `json:"disk_saturated"`
	MemoryPercent          float64 `json:"memory_percent"`
	MemoryPressure         bool    `json:"memory_pressure"`
}

// DiskChecker inspects filesystem capacity.
type DiskChecker interface {
	FreeBytes(dataDir string) (uint64, error)
}

// PlatformDiskChecker uses the real host telemetry collected by internal/platform.
type PlatformDiskChecker struct{}

// FreeBytes returns available bytes on the filesystem hosting dataDir.
func (PlatformDiskChecker) FreeBytes(dataDir string) (uint64, error) {
	s := platform.Collect(dataDir, nil)
	if s.DiskTotalBytes == 0 {
		return 0, fmt.Errorf("disk capacity cannot be determined for %s", dataDir)
	}
	// A known available figure wins even when it is genuinely 0 (disk full
	// for an unprivileged writer) — only fall back to total-used when the
	// metric truly could not be read.
	if s.DiskAvailableKnown {
		return s.DiskAvailableBytes, nil
	}
	if s.DiskTotalBytes < s.DiskUsedBytes {
		return 0, nil
	}
	return s.DiskTotalBytes - s.DiskUsedBytes, nil
}

// MemoryChecker inspects whole-host memory utilisation.
type MemoryChecker interface {
	MemoryUsagePercent() (float64, bool)
}

// PlatformMemoryChecker reads host memory metrics from internal/platform.
type PlatformMemoryChecker struct{}

// MemoryUsagePercent returns whole-host RAM utilisation in [0..100].
func (PlatformMemoryChecker) MemoryUsagePercent() (float64, bool) {
	s := platform.Collect("", nil)
	if s.MemTotalBytes == 0 {
		return 0, false
	}
	used := float64(s.MemUsedBytes)
	total := float64(s.MemTotalBytes)
	pct := (used / total) * 100.0
	if pct < 0 {
		pct = 0
	} else if pct > 100 {
		pct = 100
	}
	return pct, true
}

// LimitsManager enforces runtime hardware safety and resource bounds.
type LimitsManager struct {
	cfg           LimitsConfig
	diskChecker   DiskChecker
	memoryChecker MemoryChecker
	inFlight      atomic.Int32
	queueDropped  atomic.Int64
	sem           chan struct{}
	logger        *slog.Logger
}

// NewLimitsManager constructs a LimitsManager with default or injected checkers.
func NewLimitsManager(cfg LimitsConfig, dc DiskChecker, mc MemoryChecker, logger *slog.Logger) *LimitsManager {
	if cfg.MaxConcurrentInference <= 0 {
		cfg.MaxConcurrentInference = 1
	}
	if cfg.QueueDepth <= 0 {
		cfg.QueueDepth = 32
	}
	if dc == nil {
		dc = PlatformDiskChecker{}
	}
	if mc == nil {
		mc = PlatformMemoryChecker{}
	}
	return &LimitsManager{
		cfg:           cfg,
		diskChecker:   dc,
		memoryChecker: mc,
		sem:           make(chan struct{}, cfg.MaxConcurrentInference),
		logger:        logger,
	}
}

// TryAcquireInference attempts to claim a concurrent inference slot without blocking.
func (lm *LimitsManager) TryAcquireInference() bool {
	select {
	case lm.sem <- struct{}{}:
		lm.inFlight.Add(1)
		return true
	default:
		return false
	}
}

// ReleaseInference releases a claimed inference slot.
func (lm *LimitsManager) ReleaseInference() {
	select {
	case <-lm.sem:
		lm.inFlight.Add(-1)
	default:
	}
}

// RecordQueueDrop increments the counter when a frame/result is discarded due to queue depth saturation.
func (lm *LimitsManager) RecordQueueDrop() {
	lm.queueDropped.Add(1)
	if lm.logger != nil {
		lm.logger.Warn("inference queue capacity reached, dropping task",
			slog.Int("queue_depth", lm.cfg.QueueDepth),
			slog.Int64("total_dropped", lm.queueDropped.Load()))
	}
}

// CanWriteEvidence checks whether disk space is above the minimum required threshold.
// Returns (ok, freeBytes, error). If freeBytes < MinFreeDiskBytes, returns ErrDiskSpaceBelowMinimum.
func (lm *LimitsManager) CanWriteEvidence(dataDir string) (bool, uint64, error) {
	if lm.cfg.MinFreeDiskBytes == 0 {
		return true, 0, nil
	}
	free, err := lm.diskChecker.FreeBytes(dataDir)
	if err != nil {
		// Degrade safely rather than panicking or blocking permanently
		if lm.logger != nil {
			lm.logger.Warn("unable to read free disk bytes, proceeding conservatively", slog.Any("error", err))
		}
		return true, 0, nil
	}
	if free < lm.cfg.MinFreeDiskBytes {
		return false, free, fmt.Errorf("%w: free=%d min_required=%d",
			ErrDiskSpaceBelowMinimum, free, lm.cfg.MinFreeDiskBytes)
	}
	return true, free, nil
}

// CheckMemoryPressure reports whether host memory usage exceeds the configured threshold.
func (lm *LimitsManager) CheckMemoryPressure() (bool, float64) {
	if lm.cfg.MaxMemoryPercent <= 0 {
		return false, 0
	}
	pct, ok := lm.memoryChecker.MemoryUsagePercent()
	if !ok {
		return false, 0
	}
	return pct >= lm.cfg.MaxMemoryPercent, pct
}

// Status returns a point-in-time snapshot of limits and current resource saturation.
func (lm *LimitsManager) Status(dataDir string) LimitsStatus {
	var freeBytes uint64
	var diskSaturated bool
	if lm.cfg.MinFreeDiskBytes > 0 {
		if free, err := lm.diskChecker.FreeBytes(dataDir); err == nil {
			freeBytes = free
			diskSaturated = free < lm.cfg.MinFreeDiskBytes
		}
	}

	memPressure, memPct := lm.CheckMemoryPressure()

	return LimitsStatus{
		MaxConcurrentInference: lm.cfg.MaxConcurrentInference,
		InFlightInference:      int(lm.inFlight.Load()),
		QueueDepth:             lm.cfg.QueueDepth,
		QueueDropped:           lm.queueDropped.Load(),
		MinFreeDiskBytes:       lm.cfg.MinFreeDiskBytes,
		DiskFreeBytes:          freeBytes,
		DiskSaturated:          diskSaturated,
		MemoryPercent:          memPct,
		MemoryPressure:         memPressure,
	}
}
