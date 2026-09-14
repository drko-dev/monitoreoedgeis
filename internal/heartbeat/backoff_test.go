package heartbeat

import (
	"testing"
	"time"
)

func TestBackoffDoublesAndCaps(t *testing.T) {
	b := newBackoff(time.Second, 8*time.Second)

	want := []time.Duration{
		1 * time.Second,
		2 * time.Second,
		4 * time.Second,
		8 * time.Second,
		8 * time.Second, // capped
		8 * time.Second,
	}
	for i, w := range want {
		if got := b.next(); got != w {
			t.Errorf("attempt %d: got %s, want %s", i, got, w)
		}
	}
}

// A long outage must not overflow the duration arithmetic: the doubling loop
// has to stop at the ceiling rather than keep multiplying.
func TestBackoffSurvivesLongOutage(t *testing.T) {
	b := newBackoff(BaseBackoff, MaxBackoff)
	for i := 0; i < 10_000; i++ {
		if d := b.next(); d <= 0 || d > MaxBackoff {
			t.Fatalf("attempt %d produced out-of-range delay %s", i, d)
		}
	}
	if b.attempt() != 10_000 {
		t.Errorf("attempt counter stalled at %d, want 10000", b.attempt())
	}
}

func TestBackoffResetReturnsToBase(t *testing.T) {
	b := newBackoff(time.Second, time.Minute)
	b.next()
	b.next()
	b.next()
	if b.attempt() == 0 {
		t.Fatal("precondition: expected attempts to have accumulated")
	}

	b.reset()

	if got := b.attempt(); got != 0 {
		t.Errorf("attempt after reset = %d, want 0", got)
	}
	if got := b.next(); got != time.Second {
		t.Errorf("first delay after reset = %s, want 1s", got)
	}
}

func TestNewBackoffNormalisesBadLimits(t *testing.T) {
	b := newBackoff(0, 0)
	if got := b.next(); got != BaseBackoff {
		t.Errorf("zero base: got %s, want %s", got, BaseBackoff)
	}

	// A max below base must not produce a delay shorter than base.
	b2 := newBackoff(10*time.Second, time.Second)
	if got := b2.next(); got != 10*time.Second {
		t.Errorf("max<base: got %s, want 10s", got)
	}
}
