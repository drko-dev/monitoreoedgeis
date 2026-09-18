package evidence

import (
	"context"
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/processing"
)

type fixedHistory struct{ frames []processing.Frame }

func (h fixedHistory) Snapshot() []processing.Frame { return h.frames }

func TestSelectFramesBoundsDeduplicatesAndOrders(t *testing.T) {
	at := time.Now()
	frames := []processing.Frame{{Seq: 2, Timestamp: at.Add(time.Second)}, {Seq: 1, Timestamp: at}, {Seq: 2, Timestamp: at.Add(time.Second)}, {Seq: 3, Timestamp: at.Add(2 * time.Second)}}
	got := selectFrames(frames, at, at.Add(2*time.Second), 2)
	if len(got) != 2 || got[0].Seq != 2 || got[1].Seq != 3 {
		t.Fatalf("frames=%+v", got)
	}
}
func TestNewClipperRejectsUnboundedConfig(t *testing.T) {
	if _, err := NewClipper(ClipConfig{DataDir: t.TempDir()}); err == nil {
		t.Fatal("want max frames error")
	}
}

func TestCaptureFailureDoesNotCreateClip(t *testing.T) {
	dir := t.TempDir()
	c, err := NewClipper(ClipConfig{DataDir: dir, FFmpegPath: "definitely-not-ffmpeg", MaxFrames: 2})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now()
	h := fixedHistory{frames: []processing.Frame{{Seq: 1, Timestamp: at, OutputWidth: 2, OutputHeight: 2, Data: make([]byte, 6)}}}
	if _, err := c.Capture(context.Background(), h, "event-1", at); err == nil {
		t.Fatal("want encoder failure")
	}
}
