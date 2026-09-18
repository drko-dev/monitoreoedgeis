// Package evidence materializes bounded local evidence from frames that the
// existing video pipeline has already decoded. It never opens RTSP itself.
package evidence

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/processing"
)

// Sentinel errors for clip operations.
var (
	ErrClipConflict = errors.New("evidence: divergent clip content for event")
)

// FrameHistory is implemented by an existing ring buffer or a small adapter
// over a camera pipeline. A future local event producer supplies it; this
// package never decides when an event exists.
type FrameHistory interface{ Snapshot() []processing.Frame }

type ClipConfig struct {
	DataDir      string
	FFmpegPath   string
	PreEvent     time.Duration
	PostEvent    time.Duration
	MaxFrames    int
	FrameRate    float64
	PollInterval time.Duration
	// MaxSizeBytes rejects a clip locally, before it is ever handed to
	// edgebacklog/uploaded, once the encoded file exceeds this size —
	// separate from and unrelated to the SaaS's JPEG-sized
	// MAX_CAPTURE_SIZE_BYTES (integration item #8: GEOCAM_EDGE_MAX_CLIP_SIZE_BYTES
	// / config.DefaultEdgeMaxClipSizeBytes). Zero disables the check.
	MaxSizeBytes int64
}

type ClipRecord struct {
	EventUUID    string `json:"event_uuid"`
	RelativePath string `json:"relative_path"`
	SHA256       string `json:"sha256"`
	Size         int64  `json:"size"`
	DurationMS   int64  `json:"duration_ms,omitempty"`
}

type Clipper struct{ cfg ClipConfig }

func NewClipper(cfg ClipConfig) (*Clipper, error) {
	if cfg.DataDir == "" {
		return nil, fmt.Errorf("evidence: DataDir is required")
	}
	if cfg.MaxFrames <= 0 {
		return nil, fmt.Errorf("evidence: MaxFrames must be positive")
	}
	if cfg.FrameRate <= 0 {
		cfg.FrameRate = 5
	}
	if cfg.PreEvent < 0 || cfg.PostEvent < 0 {
		return nil, fmt.Errorf("evidence: event windows cannot be negative")
	}
	if cfg.FFmpegPath == "" {
		cfg.FFmpegPath = "ffmpeg"
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 100 * time.Millisecond
	}
	return &Clipper{cfg: cfg}, nil
}

// Capture writes GEOCAM_DATA_DIR/evidence/clips/<event_uuid>.mp4 from the
// already-decoded ring-buffer frames surrounding eventAt. A failed encoder
// returns an error but never mutates an event/capture record owned by callers.
func (c *Clipper) Capture(ctx context.Context, history FrameHistory, eventUUID string, eventAt time.Time) (ClipRecord, error) {
	if history == nil || eventUUID == "" {
		return ClipRecord{}, fmt.Errorf("evidence: history and event UUID are required")
	}
	deadline := eventAt.Add(c.cfg.PostEvent)
	frames := history.Snapshot()
	for time.Now().Before(deadline) {
		wait := time.NewTimer(minDuration(c.cfg.PollInterval, time.Until(deadline)))
		select {
		case <-ctx.Done():
			wait.Stop()
			return ClipRecord{}, ctx.Err()
		case <-wait.C:
		}
		frames = history.Snapshot()
	}
	frames = selectFrames(frames, eventAt.Add(-c.cfg.PreEvent), deadline, c.cfg.MaxFrames)
	if len(frames) == 0 {
		return ClipRecord{}, fmt.Errorf("evidence: no frames in configured event window")
	}
	if err := sameShape(frames); err != nil {
		return ClipRecord{}, err
	}
	dir := filepath.Join(c.cfg.DataDir, "evidence", "clips")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return ClipRecord{}, err
	}
	rel := filepath.Join("evidence", "clips", eventUUID+".mp4")
	final := filepath.Join(c.cfg.DataDir, rel)
	tmp := final + ".tmp"
	if err := c.encode(ctx, tmp, frames); err != nil {
		_ = os.Remove(tmp)
		return ClipRecord{}, err
	}
	if c.cfg.MaxSizeBytes > 0 {
		info, err := os.Stat(tmp)
		if err != nil {
			_ = os.Remove(tmp)
			return ClipRecord{}, err
		}
		if info.Size() > c.cfg.MaxSizeBytes {
			_ = os.Remove(tmp)
			return ClipRecord{}, fmt.Errorf("evidence: clip size %d exceeds max %d, rejecting locally rather than uploading a clip the SaaS would reject", info.Size(), c.cfg.MaxSizeBytes)
		}
	}

	tmpData, err := os.ReadFile(tmp)
	if err != nil {
		_ = os.Remove(tmp)
		return ClipRecord{}, err
	}
	tmpSum := sha256.Sum256(tmpData)
	tmpSHA := hex.EncodeToString(tmpSum[:])

	duration := int64(time.Duration(len(frames)-1).Seconds() / c.cfg.FrameRate * 1000)
	if len(frames) > 1 && frames[len(frames)-1].Timestamp.After(frames[0].Timestamp) {
		duration = frames[len(frames)-1].Timestamp.Sub(frames[0].Timestamp).Milliseconds()
	}

	// Guard against overwriting an existing clip file.
	// If identical content already exists, return the existing record idempotently.
	// If divergent content exists for the same eventUUID, return ErrClipConflict without overwriting.
	if existingData, err := os.ReadFile(final); err == nil {
		_ = os.Remove(tmp)
		existingSum := sha256.Sum256(existingData)
		existingSHA := hex.EncodeToString(existingSum[:])
		if existingSHA == tmpSHA {
			return ClipRecord{
				EventUUID:    eventUUID,
				RelativePath: rel,
				SHA256:       existingSHA,
				Size:         int64(len(existingData)),
				DurationMS:   duration,
			}, nil
		}
		return ClipRecord{}, fmt.Errorf("%w: %s", ErrClipConflict, final)
	}

	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return ClipRecord{}, err
	}

	return ClipRecord{
		EventUUID:    eventUUID,
		RelativePath: rel,
		SHA256:       tmpSHA,
		Size:         int64(len(tmpData)),
		DurationMS:   duration,
	}, nil
}

func (c *Clipper) encode(ctx context.Context, output string, frames []processing.Frame) error {
	f := frames[0]
	cmd := exec.CommandContext(ctx, c.cfg.FFmpegPath, "-y", "-f", "rawvideo", "-pix_fmt", "yuv420p", "-s", fmt.Sprintf("%dx%d", f.OutputWidth, f.OutputHeight), "-r", fmt.Sprintf("%.3f", c.cfg.FrameRate), "-i", "pipe:0", "-an", "-c:v", "libx264", "-pix_fmt", "yuv420p", output)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("evidence: start ffmpeg: %w", err)
	}
	for _, frame := range frames {
		if _, err := stdin.Write(frame.Data); err != nil {
			_ = stdin.Close()
			_ = cmd.Wait()
			return fmt.Errorf("evidence: write ffmpeg: %w", err)
		}
	}
	if err := stdin.Close(); err != nil {
		return err
	}
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("evidence: encode clip: %w", err)
	}
	return nil
}
func selectFrames(frames []processing.Frame, from, to time.Time, max int) []processing.Frame {
	seen := map[uint64]bool{}
	out := make([]processing.Frame, 0, len(frames))
	for _, f := range frames {
		if !f.Timestamp.Before(from) && !f.Timestamp.After(to) && !seen[f.Seq] {
			seen[f.Seq] = true
			out = append(out, f)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Timestamp.Before(out[j].Timestamp) })
	if len(out) > max {
		return out[len(out)-max:]
	}
	return out
}
func sameShape(frames []processing.Frame) error {
	for _, f := range frames {
		if f.OutputWidth != frames[0].OutputWidth || f.OutputHeight != frames[0].OutputHeight || len(f.Data) != len(frames[0].Data) {
			return fmt.Errorf("evidence: frame dimensions changed within clip window")
		}
	}
	return nil
}
func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
