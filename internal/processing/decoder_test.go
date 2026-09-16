package processing

import (
	"bytes"
	"context"
	"os/exec"
	"testing"
	"time"
)

// requireFFmpeg skips the test if no ffmpeg binary is on PATH — these tests
// exercise the real subprocess decoder path (decoder.go's fake covers the
// pipeline without it). CI/dev environments without ffmpeg installed still
// get every other processing test.
func requireFFmpeg(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not found on PATH, skipping ffmpeg-gated test")
	}
}

// generateAnnexB uses ffmpeg itself to synthesize a tiny H.264 Annex-B
// baseline-profile clip (no external test fixture files needed).
func generateAnnexB(t *testing.T, width, height int, frames int) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc=size="+itoa(width)+"x"+itoa(height)+":rate=5",
		"-frames:v", itoa(frames),
		"-c:v", "libx264", "-profile:v", "baseline", "-pix_fmt", "yuv420p",
		"-f", "h264", "-",
	)
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("ffmpeg encode fixture failed: %v: %s", err, stderr.String())
	}
	if out.Len() == 0 {
		t.Fatal("ffmpeg produced no Annex-B output")
	}
	return out.Bytes()
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// splitAnnexB splits a raw Annex-B byte stream into individual NAL units
// (start codes stripped), for feeding one at a time to FFmpegDecoder.Push.
func splitAnnexB(data []byte) [][]byte {
	var nalus [][]byte
	starts := findStartCodes(data)
	for i, s := range starts {
		end := len(data)
		if i+1 < len(starts) {
			end = starts[i+1].codeStart
		}
		nalus = append(nalus, data[s.nalStart:end])
	}
	return nalus
}

type startCode struct {
	codeStart int
	nalStart  int
}

func findStartCodes(data []byte) []startCode {
	var out []startCode
	for i := 0; i+3 <= len(data); i++ {
		if data[i] == 0 && data[i+1] == 0 && data[i+2] == 1 {
			out = append(out, startCode{codeStart: i, nalStart: i + 3})
			i += 2
		}
	}
	return out
}

func TestFFmpegDecoder_DecodesRealClip(t *testing.T) {
	requireFFmpeg(t)

	const w, h = 64, 64
	annexB := generateAnnexB(t, w, h, 5)
	nalus := splitAnnexB(annexB)
	if len(nalus) == 0 {
		t.Fatal("no NAL units parsed from generated fixture")
	}

	dec, err := NewFFmpegDecoder(FFmpegDecoderConfig{}, w, h, nil, nil)
	if err != nil {
		t.Fatalf("NewFFmpegDecoder: %v", err)
	}
	defer dec.Close()

	for _, nalu := range nalus {
		if err := dec.Push(AccessUnit{NALUs: [][]byte{nalu}, ReceivedAt: time.Now()}); err != nil {
			t.Fatalf("Push: %v", err)
		}
	}
	// A real camera stream never ends, so ffmpeg can hold the last decoded
	// frame back waiting for the next one's data before flushing it. This
	// bounded test has no more data to send, so close stdin to signal EOF
	// and force ffmpeg to flush whatever it already decoded.
	_ = dec.stdin.Close()

	select {
	case frame := <-dec.Frames():
		if frame.Width != w || frame.Height != h {
			t.Fatalf("decoded frame dims = %dx%d, want %dx%d", frame.Width, frame.Height, w, h)
		}
		if len(frame.Data) != w*h*3/2 {
			t.Fatalf("decoded frame data len = %d, want %d", len(frame.Data), w*h*3/2)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for a decoded frame from a real ffmpeg process")
	}

	if dec.DecodedCount() == 0 {
		t.Fatal("DecodedCount() = 0 after receiving a frame")
	}
}

func TestFFmpegDecoder_DonesOnProcessDeath(t *testing.T) {
	requireFFmpeg(t)

	dec, err := NewFFmpegDecoder(FFmpegDecoderConfig{}, 32, 32, nil, nil)
	if err != nil {
		t.Fatalf("NewFFmpegDecoder: %v", err)
	}
	defer dec.Close()

	if dec.cmd.Process == nil {
		t.Fatal("expected ffmpeg process to be started")
	}
	if err := dec.cmd.Process.Kill(); err != nil {
		t.Fatalf("failed to kill ffmpeg process: %v", err)
	}

	select {
	case <-dec.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("Done() did not close after the ffmpeg process was killed")
	}
}

func TestFFmpegDecoder_QueueDepthConfigured(t *testing.T) {
	requireFFmpeg(t)
	dec, err := NewFFmpegDecoder(FFmpegDecoderConfig{QueueDepth: 7}, 32, 32, nil, nil)
	if err != nil {
		t.Fatalf("NewFFmpegDecoder: %v", err)
	}
	defer dec.Close()
	if cap(dec.frames) != 7 {
		t.Fatalf("frames channel capacity = %d, want the configured 7 (GEOCAM_VIDEO_DECODE_QUEUE_DEPTH must actually control this)", cap(dec.frames))
	}
}

func TestFFmpegDecoder_QueueDepthDefaultsWhenUnset(t *testing.T) {
	requireFFmpeg(t)
	dec, err := NewFFmpegDecoder(FFmpegDecoderConfig{}, 32, 32, nil, nil)
	if err != nil {
		t.Fatalf("NewFFmpegDecoder: %v", err)
	}
	defer dec.Close()
	if cap(dec.frames) != 4 {
		t.Fatalf("frames channel capacity = %d, want default 4", cap(dec.frames))
	}
}

func TestFFmpegDecoder_PendingTimesBounded(t *testing.T) {
	requireFFmpeg(t)
	dec, err := NewFFmpegDecoder(FFmpegDecoderConfig{}, 32, 32, nil, nil)
	if err != nil {
		t.Fatalf("NewFFmpegDecoder: %v", err)
	}
	defer dec.Close()

	for i := 0; i < maxPendingTimes*3; i++ {
		_ = dec.Push(AccessUnit{NALUs: [][]byte{{0x01, 0xAA}}, ReceivedAt: time.Now()})
	}

	dec.mu.Lock()
	n := len(dec.pendingTimes)
	dec.mu.Unlock()
	if n > maxPendingTimes {
		t.Fatalf("pendingTimes len = %d, want <= %d (must never grow unbounded)", n, maxPendingTimes)
	}
}

func TestFFmpegDecoder_RejectsInvalidDimensions(t *testing.T) {
	requireFFmpeg(t)

	if _, err := NewFFmpegDecoder(FFmpegDecoderConfig{}, 0, 64, nil, nil); err == nil {
		t.Fatal("expected error for zero width")
	}
	if _, err := NewFFmpegDecoder(FFmpegDecoderConfig{}, 65, 64, nil, nil); err == nil {
		t.Fatal("expected error for odd width")
	}
}
