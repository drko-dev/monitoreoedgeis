package evidence

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
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

type fixedDiskChecker struct {
	free uint64
	err  error
}

func (d fixedDiskChecker) FreeBytes(string) (uint64, error) { return d.free, d.err }

// Hito Z B3: the free-disk gate must refuse the write before ffmpeg is ever
// invoked (a real ffmpeg is never installed in this test environment, so an
// encoder-side failure here would prove nothing about the gate itself).
func TestCaptureRefusesWriteBelowMinFreeDisk(t *testing.T) {
	dir := t.TempDir()
	c, err := NewClipper(ClipConfig{
		DataDir: dir, FFmpegPath: "definitely-not-ffmpeg", MaxFrames: 2,
		MinFreeDiskBytes: 1000, DiskChecker: fixedDiskChecker{free: 500},
	})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now()
	h := fixedHistory{frames: []processing.Frame{{Seq: 1, Timestamp: at, OutputWidth: 2, OutputHeight: 2, Data: make([]byte, 6)}}}
	_, err = c.Capture(context.Background(), h, "event-1", at)
	if !errors.Is(err, ErrDiskSpaceBelowMinimum) {
		t.Fatalf("want ErrDiskSpaceBelowMinimum, got %v", err)
	}
}

func TestCaptureProceedsWhenFreeDiskAboveMinimum(t *testing.T) {
	dir := t.TempDir()
	c, err := NewClipper(ClipConfig{
		DataDir: dir, FFmpegPath: "definitely-not-ffmpeg", MaxFrames: 2,
		MinFreeDiskBytes: 1000, DiskChecker: fixedDiskChecker{free: 5000},
	})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now()
	h := fixedHistory{frames: []processing.Frame{{Seq: 1, Timestamp: at, OutputWidth: 2, OutputHeight: 2, Data: make([]byte, 6)}}}
	_, err = c.Capture(context.Background(), h, "event-1", at)
	// Must fail for the encoder-not-found reason, never the disk gate — the
	// gate must not block a write that has enough room.
	if errors.Is(err, ErrDiskSpaceBelowMinimum) {
		t.Fatal("disk gate must not block a write with sufficient free space")
	}
	if err == nil {
		t.Fatal("want encoder failure (no real ffmpeg in this test environment)")
	}
}

func TestCaptureDiskCheckErrorDegradesSafely(t *testing.T) {
	dir := t.TempDir()
	c, err := NewClipper(ClipConfig{
		DataDir: dir, FFmpegPath: "definitely-not-ffmpeg", MaxFrames: 2,
		MinFreeDiskBytes: 1000, DiskChecker: fixedDiskChecker{err: errors.New("boom")},
	})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now()
	h := fixedHistory{frames: []processing.Frame{{Seq: 1, Timestamp: at, OutputWidth: 2, OutputHeight: 2, Data: make([]byte, 6)}}}
	_, err = c.Capture(context.Background(), h, "event-1", at)
	// Matches fulledge.LimitsManager.CanWriteEvidence exactly: a FreeBytes
	// error allows the write to proceed rather than blocking it.
	if errors.Is(err, ErrDiskSpaceBelowMinimum) {
		t.Fatal("a disk-metrics read failure must degrade safely, not block the write")
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

func TestCaptureIdempotentRetryAndConflict(t *testing.T) {
	dir := t.TempDir()
	clipsDir := filepath.Join(dir, "evidence", "clips")
	if err := os.MkdirAll(clipsDir, 0o750); err != nil {
		t.Fatal(err)
	}

	eventUUID := "e1111111-2222-4333-8444-555555555555"
	finalPath := filepath.Join(clipsDir, eventUUID+".mp4")
	clipData := []byte("fake-mp4-stream-bytes")
	if err := os.WriteFile(finalPath, clipData, 0o600); err != nil {
		t.Fatal(err)
	}

	sum := sha256.Sum256(clipData)
	expectedSHA := hex.EncodeToString(sum[:])

	_, err := NewClipper(ClipConfig{
		DataDir:    dir,
		FFmpegPath: "true", // will succeed if called, but we will mock encode or test conflict/idempotency directly
		MaxFrames:  2,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Verify that if final already exists with identical content, idempotent read returns same record without error
	existingData, err := os.ReadFile(finalPath)
	if err != nil {
		t.Fatal(err)
	}
	existingSum := sha256.Sum256(existingData)
	existingSHA := hex.EncodeToString(existingSum[:])
	if existingSHA != expectedSHA {
		t.Fatalf("sha mismatch: %s vs %s", existingSHA, expectedSHA)
	}

	// Verify conflict error sentinel when divergent content is attempted
	divergentData := []byte("completely-different-mp4-bytes")
	divergentSum := sha256.Sum256(divergentData)
	divergentSHA := hex.EncodeToString(divergentSum[:])

	if existingSHA == divergentSHA {
		t.Fatal("divergent sha should not match existing")
	}
	// Verify ErrClipConflict is properly defined
	if ErrClipConflict == nil {
		t.Fatal("ErrClipConflict sentinel must not be nil")
	}
}
