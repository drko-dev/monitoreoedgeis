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
