package evidence

import (
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/processing"
)

// A clip must play in real time whatever rate the camera was sampled at.
func TestClipFrameRateFromTimestamps(t *testing.T) {
	start := time.Unix(1000, 0)
	frames := make([]processing.Frame, 46) // 3 s at 15 FPS
	for i := range frames {
		frames[i].Timestamp = start.Add(time.Duration(i) * time.Second / 15)
	}
	if got := clipFrameRate(frames, 2); got < 14.99 || got > 15.01 {
		t.Fatalf("rate = %v, want 15 (not the 2 FPS global fallback)", got)
	}
	if got := clipFrameRate(frames[:1], 2); got != 2 {
		t.Fatalf("single frame rate = %v, want fallback 2", got)
	}
}
