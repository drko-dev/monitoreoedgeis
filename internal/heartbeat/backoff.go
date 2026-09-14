package heartbeat

import "time"

// Backoff limits for transient SaaS failures. The sequence is 1s, 2s, 4s,
// 8s ... capped at MaxBackoff, so a SaaS outage costs at most one request per
// minute per Edge instead of a request storm.
const (
	BaseBackoff = 1 * time.Second
	MaxBackoff  = 60 * time.Second
)

// backoff is an exponential retry delay with a ceiling. It is not safe for
// concurrent use: the heartbeat module owns one and touches it only from its
// single scheduling goroutine.
type backoff struct {
	base     time.Duration
	max      time.Duration
	attempts int
}

func newBackoff(base, max time.Duration) *backoff {
	if base <= 0 {
		base = BaseBackoff
	}
	if max < base {
		max = base
	}
	return &backoff{base: base, max: max}
}

// next returns the delay for the current attempt and advances the sequence.
func (b *backoff) next() time.Duration {
	d := b.base
	// Stop doubling as soon as the cap is reached: on a long outage
	// attempts grows without bound and continuing would overflow.
	for i := 0; i < b.attempts && d < b.max; i++ {
		d *= 2
	}
	b.attempts++
	if d > b.max {
		return b.max
	}
	return d
}

// reset returns the sequence to its first delay. A single successful
// heartbeat is enough to clear an outage's accumulated backoff.
func (b *backoff) reset() { b.attempts = 0 }

// attempt reports how many delays have been handed out since the last reset.
func (b *backoff) attempt() int { return b.attempts }
