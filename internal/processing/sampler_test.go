package processing

import (
	"testing"
	"time"
)

func TestSampler_GatesToTargetInterval(t *testing.T) {
	s := NewSampler(2) // 2 FPS -> 500ms interval
	t0 := time.Unix(0, 0)

	if !s.ShouldEmit(t0) {
		t.Fatal("first frame should always emit")
	}
	if s.ShouldEmit(t0.Add(100 * time.Millisecond)) {
		t.Fatal("frame within interval must not emit (would duplicate)")
	}
	if !s.ShouldEmit(t0.Add(500 * time.Millisecond)) {
		t.Fatal("frame exactly at interval should emit")
	}
	if !s.ShouldEmit(t0.Add(1100 * time.Millisecond)) {
		t.Fatal("frame past interval should emit")
	}
}

func TestSampler_ZeroFPSPassesEverything(t *testing.T) {
	s := NewSampler(0)
	t0 := time.Unix(0, 0)
	for i := 0; i < 5; i++ {
		if !s.ShouldEmit(t0.Add(time.Duration(i) * time.Millisecond)) {
			t.Fatalf("targetFPS<=0 must never gate, frame %d rejected", i)
		}
	}
}

func TestSampler_NeverEmitsDuplicateForSameTimestamp(t *testing.T) {
	s := NewSampler(1)
	t0 := time.Unix(5, 0)
	if !s.ShouldEmit(t0) {
		t.Fatal("first frame should emit")
	}
	if s.ShouldEmit(t0) {
		t.Fatal("identical timestamp must not re-emit")
	}
}

// TestNewAdaptiveSampler_DisabledMatchesNewSampler is the J5 no-regression
// acceptance test: idleFPS<=0 (the config default) must behave IDENTICALLY
// to NewSampler(activeFPS), whether or not the mode is hybrid.
func TestNewAdaptiveSampler_DisabledMatchesNewSampler(t *testing.T) {
	s := NewAdaptiveSampler(2, 0, 5*time.Second) // idleFPS=0 disables adaptation
	t0 := time.Unix(0, 0)

	if !s.ShouldEmit(t0) {
		t.Fatal("first frame should always emit")
	}
	if s.ShouldEmit(t0.Add(100 * time.Millisecond)) {
		t.Fatal("frame within interval must not emit")
	}
	if !s.ShouldEmit(t0.Add(500 * time.Millisecond)) {
		t.Fatal("frame exactly at interval should emit")
	}
	if s.IsIdle(t0) {
		t.Fatal("adaptive sampling disabled: IsIdle must always be false")
	}
}

// TestSampler_IdleReducesEmitRate is the J5/J8 acceptance test: once
// idleAfter has elapsed with no motion candidate noted, the Sampler must
// gate to the (slower) idle interval instead of the active one.
func TestSampler_IdleReducesEmitRate(t *testing.T) {
	// active=10fps (100ms interval), idle=2fps (500ms interval), idle
	// kicks in after 1s with no motion.
	s := NewAdaptiveSampler(10, 2, 1*time.Second)
	t0 := time.Unix(0, 0)

	if !s.ShouldEmit(t0) {
		t.Fatal("first frame should emit")
	}
	s.NoteMotion(t0, true)

	// Still within idleAfter of the last motion: active interval applies.
	if s.IsIdle(t0.Add(200 * time.Millisecond)) {
		t.Fatal("must still be active shortly after motion")
	}
	if !s.ShouldEmit(t0.Add(200 * time.Millisecond)) {
		t.Fatal("active interval (100ms) elapsed: should emit")
	}

	// 1.5s after the last motion candidate: idleAfter (1s) has elapsed,
	// so the sampler must be idle and use the slower 500ms interval.
	tIdle := t0.Add(1500 * time.Millisecond)
	if !s.IsIdle(tIdle) {
		t.Fatal("expected idle state after idleAfter with no motion")
	}
	if !s.ShouldEmit(tIdle) {
		t.Fatal("idle-interval boundary frame should emit")
	}
	if s.ShouldEmit(tIdle.Add(200 * time.Millisecond)) {
		t.Fatal("200ms after an idle emit must not re-emit at idle rate (500ms interval)")
	}
	if !s.ShouldEmit(tIdle.Add(500 * time.Millisecond)) {
		t.Fatal("500ms after an idle emit should emit at idle rate")
	}
}

// TestSampler_ActiveIdleTransitionsWithHysteresis is the J5/J8 acceptance
// test for both transition directions: active->idle only after idleAfter
// with no motion, and idle->active immediately once motion resumes.
func TestSampler_ActiveIdleTransitionsWithHysteresis(t *testing.T) {
	s := NewAdaptiveSampler(10, 2, 2*time.Second)
	t0 := time.Unix(100, 0)

	s.NoteMotion(t0, true)
	if s.IsIdle(t0.Add(1 * time.Second)) {
		t.Fatal("must not go idle before idleAfter elapses")
	}
	if !s.IsIdle(t0.Add(3 * time.Second)) {
		t.Fatal("must go idle once idleAfter has elapsed with no motion")
	}

	// Motion resumes: must go active again immediately (no hysteresis on
	// the idle->active edge -- a real event should never be delayed).
	s.NoteMotion(t0.Add(3*time.Second), true)
	if s.IsIdle(t0.Add(3 * time.Second)) {
		t.Fatal("must return to active immediately when motion resumes")
	}
}
