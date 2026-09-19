package perf

import (
	"math"
	"sort"
	"time"
)

// LatencyStats is a small, explicit summary of a duration sample. It reports
// the sample count alongside every percentile on purpose: a p95 computed from
// a handful of samples is an estimate, and a reader must be able to see how
// much data stands behind the number.
//
// Percentiles use the nearest-rank definition (the smallest observed value
// whose rank reaches p), with no interpolation — the reported p95 is always
// an actually observed duration, never a value invented between two
// observations.
type LatencyStats struct {
	Count  int     `json:"count"`
	MinMS  float64 `json:"min_ms"`
	MaxMS  float64 `json:"max_ms"`
	MeanMS float64 `json:"mean_ms"`
	P50MS  float64 `json:"p50_ms"`
	P95MS  float64 `json:"p95_ms"`
	P99MS  float64 `json:"p99_ms"`
}

// SummarizeMS builds a LatencyStats from durations in milliseconds.
func SummarizeMS(values []float64) LatencyStats {
	if len(values) == 0 {
		return LatencyStats{}
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)

	var sum float64
	for _, v := range sorted {
		sum += v
	}
	return LatencyStats{
		Count:  len(sorted),
		MinMS:  sorted[0],
		MaxMS:  sorted[len(sorted)-1],
		MeanMS: sum / float64(len(sorted)),
		P50MS:  nearestRank(sorted, 50),
		P95MS:  nearestRank(sorted, 95),
		P99MS:  nearestRank(sorted, 99),
	}
}

// nearestRank implements the nearest-rank percentile on an already sorted
// slice.
func nearestRank(sorted []float64, p float64) float64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	rank := int(math.Ceil(p / 100 * float64(n)))
	if rank < 1 {
		rank = 1
	}
	if rank > n {
		rank = n
	}
	return sorted[rank-1]
}

// FPS returns values-per-second, guarding against a zero elapsed window (a
// zero denominator would otherwise surface as +Inf in a report).
func FPS(count int64, elapsed time.Duration) float64 {
	if elapsed <= 0 {
		return 0
	}
	return float64(count) / elapsed.Seconds()
}

// MS converts a duration to milliseconds.
func MS(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

// RoundMS rounds for readability in reports without losing precision that
// matters (sub-millisecond decode latencies are not meaningful at this
// scale).
func RoundMS(v float64) float64 { return math.Round(v*1000) / 1000 }
