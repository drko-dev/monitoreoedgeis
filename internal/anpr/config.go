package anpr

import "time"

// FrameSelectionPolicy configures how a burst picks which of its arriving
// candidate frames to actually keep, once MaxFramesPerBurst is reached
// (spec item 13). Configurable, never a single hardcoded strategy.
type FrameSelectionPolicy string

const (
	// SelectFirst keeps the first MaxFramesPerBurst candidates seen and
	// ignores the rest — cheapest, fully order-preserving.
	SelectFirst FrameSelectionPolicy = "first"
	// SelectTopNQuality retains all candidates until the burst closes, then
	// keeps only the top MaxFramesPerBurst by QualityHints score (see
	// quality.go). Requires bounding intermediate storage too (see
	// burst.go's overflow cap), never unlimited memory while "waiting to
	// rank".
	SelectTopNQuality FrameSelectionPolicy = "top_n_quality"
)

// Config holds every J6-B ANPR PREP tunable. The zero value is
// Enabled==false with every bound at 0, which — per spec item 20/25/26/27 —
// must be a strict no-op: nothing in this package mutates state, no burst
// opens, no crop happens, no envelope is built when Enabled is false. Every
// existing pipeline default (sampler, hybrid) is untouched by this package
// entirely; anpr only ever reads processing.Frame, never writes any
// processing/vision/cloudsink config.
type Config struct {
	Enabled bool

	// Bounds (spec item 20). All must be positive for anpr to do anything;
	// a non-positive bound with Enabled==true is treated as "0 capacity",
	// never "unlimited" (fail-closed default, not fail-open).
	MaxActiveBurstsPerCamera int
	MaxFramesPerBurst        int
	MaxCandidateBytes        int64
	MaxContextFrames         int
	MaxCameras               int

	// BurstTTL bounds how long a burst may stay OPEN with no new frame
	// before it expires (spec item 15). No sleeps: the burst manager checks
	// TTL against an injectable clock on every call, never a background
	// timer per burst.
	BurstTTL time.Duration

	// CropPolicy/Padding: spec items 8-9.
	CropPolicy CropPolicy
	Padding    Padding

	// FrameSelection: spec item 13.
	FrameSelection FrameSelectionPolicy

	// MaxEncodedCropBytes bounds a single crop's encoded payload (spec item
	// 31). A crop encoding larger than this is rejected explicitly, never
	// silently truncated or sent oversized.
	MaxEncodedCropBytes int64

	// DedupeCacheSize bounds the idempotency set used to detect duplicate
	// (camera, frame_seq, vehicle bbox) candidates (spec item 23/20) — a
	// map, but a bounded one (oldest-evicted), never unbounded.
	DedupeCacheSize int

	// HighSpeedLPR is Milestone J6's opt-in profile (spec item 25): nil
	// (the default) changes nothing. A non-nil value only ever takes
	// effect where the caller explicitly applies it (see
	// HighSpeedLPRProfile.Apply) — this package itself never auto-detects
	// or auto-activates it.
	HighSpeedLPR *HighSpeedLPRProfile
}

// HighSpeedLPRProfile adjusts burst/crop tunables for fast-moving-vehicle
// scenarios. It is a plain data overlay, not a second code path: Apply
// returns a new Config with the overridden fields set, leaving the base
// Config (and therefore every existing default) untouched unless a caller
// explicitly opts in by calling Apply.
type HighSpeedLPRProfile struct {
	// BurstTTL/MaxFramesPerBurst/Padding override the base Config's values
	// when set (zero value = "don't override that field").
	BurstTTL          time.Duration
	MaxFramesPerBurst int
	Padding           *Padding
	// SamplingFPSHint is surfaced to the sampler via BurstSamplingHint
	// (see sampling.go) — this package never reaches into
	// processing.Sampler directly.
	SamplingFPSHint float64
}

// Apply returns a copy of base with the profile's non-zero overrides
// applied. base is never mutated.
func (p *HighSpeedLPRProfile) Apply(base Config) Config {
	if p == nil {
		return base
	}
	out := base
	if p.BurstTTL > 0 {
		out.BurstTTL = p.BurstTTL
	}
	if p.MaxFramesPerBurst > 0 {
		out.MaxFramesPerBurst = p.MaxFramesPerBurst
	}
	if p.Padding != nil {
		out.Padding = *p.Padding
	}
	return out
}
