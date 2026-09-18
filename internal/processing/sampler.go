package processing

import "time"

// Sampler gates decoded frames down to a target FPS by enforcing a minimum
// inter-frame interval. It is a synchronous, allocation-free check — no
// goroutine, no timer — so callers stay in full control of drop accounting.
//
// Milestone J5 extends it (without changing NewSampler's behavior at all)
// with an optional idle/active adaptive mode: see NewAdaptiveSampler.
type Sampler struct {
	interval time.Duration

	// idleInterval <= 0 means adaptive sampling is off: ShouldEmit behaves
	// exactly as it did before Milestone J5, using interval alone. Set
	// only by NewAdaptiveSampler, never by NewSampler.
	idleInterval time.Duration
	idleAfter    time.Duration
	lastMotion   time.Time
	hasMotion    bool

	lastEmit time.Time
	hasEmit  bool
}

// NewSampler creates a Sampler for targetFPS. targetFPS<=0 disables
// sampling: ShouldEmit always returns true. Unaffected by Milestone J5 —
// this constructor never enables adaptive sampling.
func NewSampler(targetFPS float64) *Sampler {
	s := &Sampler{}
	if targetFPS > 0 {
		s.interval = time.Duration(float64(time.Second) / targetFPS)
	}
	return s
}

// NewAdaptiveSampler creates a Sampler that emits at activeFPS while motion
// has been noted (via NoteMotion) within idleAfter, and drops to idleFPS
// once idleAfter has elapsed without a motion candidate (Milestone J5).
// activeFPS remains the ceiling — idleFPS is never allowed above it.
//
// idleFPS<=0 or idleFPS>=activeFPS disables adaptive behavior: the
// returned Sampler behaves exactly like NewSampler(activeFPS), so a hybrid
// pipeline that hasn't opted into GEOCAM_VIDEO_HYBRID_IDLE_FPS sees zero
// behavior change.
func NewAdaptiveSampler(activeFPS, idleFPS float64, idleAfter time.Duration) *Sampler {
	s := NewSampler(activeFPS)
	if idleFPS > 0 && idleFPS < activeFPS {
		s.idleInterval = time.Duration(float64(time.Second) / idleFPS)
		s.idleAfter = idleAfter
	}
	return s
}

// NoteMotion records the evaluator's most recent motion-candidate decision
// (Milestone J5's feedback signal into adaptive sampling). Only the last
// call's timestamp matters — no history is retained. A no-op when adaptive
// sampling is disabled.
func (s *Sampler) NoteMotion(t time.Time, candidate bool) {
	if s.idleInterval <= 0 || !candidate {
		return
	}
	s.lastMotion = t
	s.hasMotion = true
}

// IsIdle reports whether, as of t, the Sampler is currently in its idle
// adaptive state (always false when adaptive sampling is disabled).
func (s *Sampler) IsIdle(t time.Time) bool {
	if s.idleInterval <= 0 {
		return false
	}
	return !s.hasMotion || t.Sub(s.lastMotion) >= s.idleAfter
}

// currentInterval returns the minimum inter-frame interval that applies at
// t: the fixed interval when adaptive sampling is off, or the
// active/idle interval depending on IsIdle when it's on.
func (s *Sampler) currentInterval(t time.Time) time.Duration {
	if s.idleInterval <= 0 {
		return s.interval
	}
	if s.IsIdle(t) {
		return s.idleInterval
	}
	return s.interval
}

// ShouldEmit reports whether a frame arriving at t should pass through,
// given the minimum interval since the last emitted frame (fixed, or
// adaptive per NewAdaptiveSampler/NoteMotion). It never emits duplicates
// faster than the currently applicable interval.
func (s *Sampler) ShouldEmit(t time.Time) bool {
	interval := s.currentInterval(t)
	if interval <= 0 {
		return true
	}
	if !s.hasEmit || t.Sub(s.lastEmit) >= interval {
		s.lastEmit = t
		s.hasEmit = true
		return true
	}
	return false
}
