package cloudsink

import (
	"testing"
	"time"
)

func TestUploadTracker(t *testing.T) {
	tracker := newUploadTracker()

	// Rate when no uploads recorded is 0
	if r := tracker.rate(time.Now()); r != 0 {
		t.Fatalf("rate with no uploads = %g, want 0", r)
	}

	t0 := time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)
	// Record 100,000 bytes at t0
	tracker.record(100000, t0)

	// Immediate rate at t0
	if r := tracker.rate(t0); r != 100000 {
		t.Fatalf("rate at t0 = %g, want 100000", r)
	}

	// Rate after 2 seconds
	t2 := t0.Add(2 * time.Second)
	tracker.record(100000, t2)
	// 200,000 bytes over 2 seconds = 100,000 bytes/s
	if r := tracker.rate(t2); r != 100000 {
		t.Fatalf("rate at t2 = %g, want 100000", r)
	}

	// Rate after 20 seconds of silence -> should be 0
	t20 := t0.Add(20 * time.Second)
	if r := tracker.rate(t20); r != 0 {
		t.Fatalf("rate after 20s of silence = %g, want 0", r)
	}
}
