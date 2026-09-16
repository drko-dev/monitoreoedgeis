package processing

import "time"

// Sampler gates decoded frames down to a target FPS by enforcing a minimum
// inter-frame interval. It is a synchronous, allocation-free check — no
// goroutine, no timer — so callers stay in full control of drop accounting.
type Sampler struct {
	interval time.Duration
	lastEmit time.Time
	hasEmit  bool
}

// NewSampler creates a Sampler for targetFPS. targetFPS<=0 disables
// sampling: ShouldEmit always returns true.
func NewSampler(targetFPS float64) *Sampler {
	s := &Sampler{}
	if targetFPS > 0 {
		s.interval = time.Duration(float64(time.Second) / targetFPS)
	}
	return s
}

// ShouldEmit reports whether a frame arriving at t should pass through,
// given the minimum interval since the last emitted frame. It never emits
// duplicates faster than the configured interval.
func (s *Sampler) ShouldEmit(t time.Time) bool {
	if s.interval <= 0 {
		return true
	}
	if !s.hasEmit || t.Sub(s.lastEmit) >= s.interval {
		s.lastEmit = t
		s.hasEmit = true
		return true
	}
	return false
}
