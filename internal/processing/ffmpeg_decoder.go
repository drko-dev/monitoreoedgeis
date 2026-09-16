package processing

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// FFmpegDecoderConfig configures the ffmpeg subprocess used to decode H.264.
type FFmpegDecoderConfig struct {
	// BinaryPath is the ffmpeg executable, resolved via exec.LookPath.
	// Empty defaults to "ffmpeg" (PATH lookup).
	BinaryPath string
}

// annexBPrefix is the Annex-B NAL unit start code.
var annexBPrefix = []byte{0, 0, 0, 1}

// FFmpegDecoder decodes one camera's H.264 access units by running ffmpeg as
// an OS subprocess (os/exec, no cgo): Annex-B access units are written to
// its stdin, raw yuv420p frames are read back from its stdout.
//
// Why a subprocess instead of cgo bindings or a pure-Go decoder: the repo's
// build is CGO_ENABLED=0 targeting a distroless static image; a cgo ffmpeg
// binding would require CGO_ENABLED=1 and libav* headers, breaking that
// build. No pure-Go H.264 decoder implementation is production-grade for
// main/high profile. A hand-written decoder is explicitly out of scope.
// Running ffmpeg as a subprocess keeps the Go build 100% cgo-free — the only
// new dependency is a runtime one: the ffmpeg binary must be present in the
// container/host (see Dockerfile and docs/ARCHITECTURE.md).
//
// One process per camera pipeline (never shared/pooled): a crash or hang is
// isolated to that one camera. Restart-on-crash is the caller's
// responsibility (see pipeline.go), because a fresh process needs SPS/PPS
// re-injected, which only the pipeline that owns the StreamDescriptor knows.
//
// rawvideo over stdout does not carry the original RTP timestamp — decoded
// frames only get a best-effort FIFO-correlated SourceReceivedAt (see
// DecodedFrame's doc comment) plus the real DecodedAt wall-clock time.
type FFmpegDecoder struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	frames chan DecodedFrame
	done   chan struct{}
	logger *slog.Logger

	width, height, frameSize int

	mu           sync.Mutex
	pendingTimes []time.Time
	seq          uint64
	closed       bool

	decodedCount atomic.Int64
	droppedCount atomic.Int64
}

// NewFFmpegDecoder starts an ffmpeg subprocess decoding H.264 at the given
// (even) width/height, injecting spropParameterSets (if any) before any real
// access unit so the decoder has SPS/PPS even if the camera doesn't repeat
// them in-band.
func NewFFmpegDecoder(cfg FFmpegDecoderConfig, width, height int, spropParameterSets [][]byte, logger *slog.Logger) (*FFmpegDecoder, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if width <= 0 || height <= 0 {
		return nil, fmt.Errorf("processing: ffmpeg decoder requires known width/height, got %dx%d", width, height)
	}
	if width%2 != 0 || height%2 != 0 {
		return nil, fmt.Errorf("processing: ffmpeg decoder requires even width/height for yuv420p, got %dx%d", width, height)
	}

	bin := cfg.BinaryPath
	if bin == "" {
		bin = "ffmpeg"
	}
	resolved, err := exec.LookPath(bin)
	if err != nil {
		return nil, fmt.Errorf("processing: ffmpeg binary %q not found: %w", bin, err)
	}

	cmd := exec.Command(resolved,
		"-hide_banner", "-loglevel", "error",
		"-f", "h264", "-i", "pipe:0",
		"-f", "rawvideo", "-pix_fmt", "yuv420p",
		"-an", "-sn", "pipe:1",
	)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("processing: ffmpeg stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("processing: ffmpeg stdout pipe: %w", err)
	}
	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("processing: ffmpeg start: %w", err)
	}

	d := &FFmpegDecoder{
		cmd:    cmd,
		stdin:  stdin,
		frames: make(chan DecodedFrame, 4), // deliberately small — see doc comment
		done:   make(chan struct{}),
		logger: logger,
		width:  width,
		height: height,
		// yuv420p: full-resolution Y plane + two quarter-resolution
		// chroma planes.
		frameSize: width*height + 2*((width/2)*(height/2)),
	}

	for _, nalu := range spropParameterSets {
		if _, werr := stdin.Write(annexBFrame(nalu)); werr != nil {
			logger.Warn("failed to write sprop-parameter-sets to ffmpeg stdin", "error", truncateErr(werr))
			break
		}
	}

	go d.readLoop(stdout)
	go d.waitLoop(&stderrBuf)

	return d, nil
}

func annexBFrame(nalu []byte) []byte {
	out := make([]byte, 0, len(annexBPrefix)+len(nalu))
	out = append(out, annexBPrefix...)
	out = append(out, nalu...)
	return out
}

// Push implements VideoDecoder.
func (d *FFmpegDecoder) Push(au AccessUnit) error {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return errDecoderClosed
	}
	d.pendingTimes = append(d.pendingTimes, au.ReceivedAt)
	d.mu.Unlock()

	for _, nalu := range au.NALUs {
		if _, err := d.stdin.Write(annexBFrame(nalu)); err != nil {
			return fmt.Errorf("processing: ffmpeg stdin write: %w", err)
		}
	}
	return nil
}

// Frames implements VideoDecoder.
func (d *FFmpegDecoder) Frames() <-chan DecodedFrame { return d.frames }

// Done implements VideoDecoder.
func (d *FFmpegDecoder) Done() <-chan struct{} { return d.done }

// DecodedCount returns the number of frames successfully decoded and
// delivered (or attempted for delivery) so far.
func (d *FFmpegDecoder) DecodedCount() int64 { return d.decodedCount.Load() }

// DroppedCount returns the number of decoded frames dropped because Frames()
// was not drained in time — a bounded-queue backpressure drop, not a decode
// failure.
func (d *FFmpegDecoder) DroppedCount() int64 { return d.droppedCount.Load() }

func (d *FFmpegDecoder) readLoop(stdout io.Reader) {
	buf := make([]byte, d.frameSize)
	for {
		if _, err := io.ReadFull(stdout, buf); err != nil {
			return
		}
		now := time.Now()

		d.mu.Lock()
		var srcAt time.Time
		if len(d.pendingTimes) > 0 {
			srcAt = d.pendingTimes[0]
			d.pendingTimes = d.pendingTimes[1:]
		} else {
			srcAt = now
		}
		d.seq++
		seq := d.seq
		d.mu.Unlock()

		d.decodedCount.Add(1)
		frame := DecodedFrame{
			Data:             append([]byte(nil), buf...),
			Width:            d.width,
			Height:           d.height,
			PipelineSeq:      seq,
			SourceReceivedAt: srcAt,
			DecodedAt:        now,
		}

		select {
		case d.frames <- frame:
		default:
			// Downstream isn't draining fast enough: drop this frame
			// rather than block stdout, which would back-pressure into
			// stdin and stall this camera's decode entirely.
			d.droppedCount.Add(1)
		}
	}
}

func (d *FFmpegDecoder) waitLoop(stderrBuf *bytes.Buffer) {
	err := d.cmd.Wait()

	d.mu.Lock()
	wasClosed := d.closed
	d.mu.Unlock()

	if !wasClosed {
		msg := stderrBuf.String()
		if len(msg) > 500 {
			msg = msg[:500]
		}
		d.logger.Warn("ffmpeg process exited unexpectedly", "error", truncateErr(err), "stderr_tail", msg)
	}
	close(d.done)
}

// Close implements VideoDecoder. It signals the process to terminate,
// waits briefly, and force-kills it if it doesn't exit in time — mirroring
// internal/rtsp.Session.Teardown's bounded-wait shutdown pattern.
func (d *FFmpegDecoder) Close() error {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return nil
	}
	d.closed = true
	d.mu.Unlock()

	_ = d.stdin.Close()
	if d.cmd.Process != nil {
		_ = d.cmd.Process.Signal(syscall.SIGTERM)
	}

	select {
	case <-d.done:
	case <-time.After(2 * time.Second):
		if d.cmd.Process != nil {
			_ = d.cmd.Process.Kill()
		}
		<-d.done
	}
	return nil
}

func truncateErr(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	if len(msg) > 255 {
		msg = msg[:255]
	}
	return msg
}
