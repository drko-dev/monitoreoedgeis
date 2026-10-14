package processing

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// FFmpegDecoderConfig configures the ffmpeg subprocess used to decode H.264.
type FFmpegDecoderConfig struct {
	// Mode selects the legacy PATH-resolved executable or an explicitly
	// verified Runtime Pack executable. The zero value preserves legacy behavior.
	Mode FFmpegExecutionMode
	// BinaryPath is used only in legacy mode. Empty defaults to "ffmpeg".
	BinaryPath string
	// VerifiedFFmpeg is mandatory in hermetic mode and is revalidated before
	// each subprocess start. Its expected digest must originate in a release
	// manifest authenticated by a trusted release pin.
	VerifiedFFmpeg *VerifiedFFmpeg
	// Codec is H264 (default) or H265/HEVC and selects ffmpeg's elementary
	// stream demuxer for pipe:0.
	Codec string
	// OutputWidth/OutputHeight are used only when the source dimensions are
	// unknown. In that case ffmpeg probes the dimensions from the elementary
	// stream and scales to this fixed output so the rawvideo reader still has
	// a deterministic frame size.
	OutputWidth  int
	OutputHeight int
	// QueueDepth bounds the raw decoded-frame output channel
	// (GEOCAM_VIDEO_DECODE_QUEUE_DEPTH) — deliberately small, since each
	// yuv420p frame is large. <=0 defaults to 4.
	QueueDepth int
}

// FFmpegExecutionMode identifies the executable-resolution policy.
type FFmpegExecutionMode uint8

const (
	// FFmpegExecutionLegacy preserves the historical PATH lookup behavior.
	FFmpegExecutionLegacy FFmpegExecutionMode = iota
	// FFmpegExecutionHermetic forbids PATH lookup and requires a verified pack binary.
	FFmpegExecutionHermetic
)

// annexBPrefix is the Annex-B NAL unit start code.
var annexBPrefix = []byte{0, 0, 0, 1}

// maxPendingTimes bounds FFmpegDecoder.pendingTimes: if Push keeps being
// called while the decoder produces no output (e.g. it's stalled or being
// force-fed faster than it can decode), the FIFO used for the best-effort
// SourceReceivedAt correlation must never grow unbounded. On overflow the
// oldest entry is dropped — losing correlation accuracy for it, which is
// already a documented approximation, not a memory-safety concern.
const maxPendingTimes = 64

// stderrTailMax bounds how much of ffmpeg's stderr is retained for
// diagnostics — the last stderrTailMax bytes only, never the full process
// lifetime output. ffmpeg's stdout (the decoded video) is a completely
// separate pipe, never captured here, and no RTSP URL or credential is ever
// passed to this process (only pipe:0/pipe:1), so nothing secret reaches
// this buffer.
const stderrTailMax = 4 * 1024

// tailBuffer is an io.Writer that keeps only the most recent max bytes
// written to it.
type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func newTailBuffer(max int) *tailBuffer { return &tailBuffer{max: max} }

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}

// decodeQueueDepth normalizes a configured queue depth, defaulting to 4
// when unset/invalid.
func decodeQueueDepth(configured int) int {
	if configured <= 0 {
		return 4
	}
	return configured
}

// FFmpegDecoder decodes one camera's H.264 or H.265 access units by running ffmpeg as
// an OS subprocess (os/exec, no cgo): Annex-B access units are written to
// its stdin, raw yuv420p frames are read back from its stdout.
//
// Why a subprocess instead of cgo bindings or a pure-Go decoder: the repo's
// build is CGO_ENABLED=0 targeting a distroless static image; a cgo ffmpeg
// binding would require CGO_ENABLED=1 and libav* headers, breaking that
// build. No pure-Go H.264 decoder implementation is production-grade for
// main/high profile. A hand-written decoder is explicitly out of scope.
// Running ffmpeg as a subprocess keeps the Go build 100% cgo-free. Legacy
// callers resolve it from PATH; explicit hermetic callers provide a SHA-256-
// verified executable confined to their Runtime Pack.
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

// NewFFmpegDecoder starts an ffmpeg subprocess decoding H.264 or H.265 at the given
// (even) width/height, injecting spropParameterSets (if any) before any real
// access unit so the decoder has SPS/PPS even if the camera doesn't repeat
// them in-band.
func NewFFmpegDecoder(cfg FFmpegDecoderConfig, width, height int, spropParameterSets [][]byte, logger *slog.Logger) (*FFmpegDecoder, error) {
	if logger == nil {
		logger = slog.Default()
	}
	decodeWidth, decodeHeight := width, height
	needsScaleProbe := width <= 0 || height <= 0
	if needsScaleProbe {
		decodeWidth, decodeHeight = cfg.OutputWidth, cfg.OutputHeight
		if decodeWidth <= 0 || decodeHeight <= 0 {
			return nil, fmt.Errorf("processing: ffmpeg decoder source dimensions unknown (%dx%d) and no fixed output dimensions configured", width, height)
		}
	}
	if decodeWidth%2 != 0 || decodeHeight%2 != 0 {
		return nil, fmt.Errorf("processing: ffmpeg decoder requires even output width/height for yuv420p, got %dx%d", decodeWidth, decodeHeight)
	}

	resolved, err := resolveFFmpegExecutable(cfg)
	if err != nil {
		return nil, err
	}

	inputFormat := "h264"
	switch {
	case cfg.Codec == "" || strings.EqualFold(cfg.Codec, "H264"):
		inputFormat = "h264"
	case strings.EqualFold(cfg.Codec, "H265"), strings.EqualFold(cfg.Codec, "HEVC"):
		inputFormat = "hevc"
	default:
		return nil, fmt.Errorf("processing: ffmpeg decoder unsupported codec %q", cfg.Codec)
	}

	args := []string{"-hide_banner", "-loglevel", "error", "-f", inputFormat, "-i", "pipe:0"}
	if needsScaleProbe {
		args = append(args, "-vf", fmt.Sprintf("scale=%d:%d", decodeWidth, decodeHeight))
	}
	args = append(args, "-f", "rawvideo", "-pix_fmt", "yuv420p", "-an", "-sn", "pipe:1")
	cmd := exec.Command(resolved, args...)
	if cfg.Mode == FFmpegExecutionHermetic {
		// Do not pass loader injection variables or the host PATH to a bundled
		// executable. The executable itself is already an absolute path.
		cmd.Env = []string{"PATH=", "LANG=C", "LC_ALL=C"}
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("processing: ffmpeg stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("processing: ffmpeg stdout pipe: %w", err)
	}
	stderrBuf := newTailBuffer(stderrTailMax)
	cmd.Stderr = stderrBuf

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("processing: ffmpeg start: %w", err)
	}

	d := &FFmpegDecoder{
		cmd:    cmd,
		stdin:  stdin,
		frames: make(chan DecodedFrame, decodeQueueDepth(cfg.QueueDepth)), // deliberately small — see doc comment
		done:   make(chan struct{}),
		logger: logger,
		width:  decodeWidth,
		height: decodeHeight,
		// yuv420p: full-resolution Y plane + two quarter-resolution
		// chroma planes.
		frameSize: decodeWidth*decodeHeight + 2*((decodeWidth/2)*(decodeHeight/2)),
	}

	for _, nalu := range spropParameterSets {
		if _, werr := stdin.Write(annexBFrame(nalu)); werr != nil {
			logger.Warn("failed to write sprop-parameter-sets to ffmpeg stdin", "error", truncateErr(werr))
			break
		}
	}

	go d.readLoop(stdout)
	go d.waitLoop(stderrBuf)

	return d, nil
}

func resolveFFmpegExecutable(cfg FFmpegDecoderConfig) (string, error) {
	switch cfg.Mode {
	case FFmpegExecutionLegacy:
		bin := cfg.BinaryPath
		if bin == "" {
			bin = "ffmpeg"
		}
		resolved, err := exec.LookPath(bin)
		if err != nil {
			return "", fmt.Errorf("processing: ffmpeg binary %q not found: %w", bin, err)
		}
		return resolved, nil
	case FFmpegExecutionHermetic:
		if cfg.VerifiedFFmpeg == nil {
			return "", ErrHermeticFFmpegRequired
		}
		resolved, err := cfg.VerifiedFFmpeg.Resolve()
		if err != nil {
			return "", fmt.Errorf("processing: hermetic ffmpeg validation failed: %w", err)
		}
		return resolved, nil
	default:
		return "", fmt.Errorf("processing: unsupported ffmpeg execution mode %d", cfg.Mode)
	}
}

// VerifiedFFmpeg is a capability for one executable inside a Runtime Pack.
// It verifies path confinement, file type, executable permissions, and SHA-256.
// The digest is not a release signature: callers must obtain it from a
// manifest authenticated by a trusted release pin.
type VerifiedFFmpeg struct {
	packRoot string
	path     string
	digest   string
}

// VerifyPackFFmpeg creates a capability only when the executable is a regular,
// executable, non-symlink file inside packRoot and matches expectedSHA256.
func VerifyPackFFmpeg(packRoot, executablePath, expectedSHA256 string) (VerifiedFFmpeg, error) {
	var verified VerifiedFFmpeg
	if !filepath.IsAbs(packRoot) || !filepath.IsAbs(executablePath) {
		return verified, fmt.Errorf("Runtime Pack root and FFmpeg path must be absolute")
	}
	if filepath.Clean(executablePath) != executablePath {
		return verified, fmt.Errorf("FFmpeg path must be clean and canonical")
	}
	digest, err := normalizeSHA256(expectedSHA256)
	if err != nil {
		return verified, err
	}
	declaredRoot, err := filepath.Abs(packRoot)
	if err != nil {
		return verified, err
	}
	declaredPath, err := filepath.Abs(executablePath)
	if err != nil {
		return verified, err
	}
	declaredRel, err := filepath.Rel(declaredRoot, declaredPath)
	if err != nil || declaredRel == "." || declaredRel == ".." || filepath.IsAbs(declaredRel) || strings.HasPrefix(declaredRel, ".."+string(filepath.Separator)) {
		return verified, fmt.Errorf("FFmpeg executable escapes Runtime Pack")
	}
	if err := rejectSymlinkComponents(declaredRoot, declaredRel); err != nil {
		return verified, err
	}
	root, err := filepath.EvalSymlinks(declaredRoot)
	if err != nil {
		return verified, fmt.Errorf("resolve Runtime Pack root: %w", err)
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return verified, err
	}
	path, err := filepath.EvalSymlinks(declaredPath)
	if err != nil {
		return verified, fmt.Errorf("resolve bundled FFmpeg path: %w", err)
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return verified, err
	}
	if err := verifyPackExecutable(root, path, digest); err != nil {
		return verified, err
	}
	return VerifiedFFmpeg{packRoot: root, path: path, digest: digest}, nil
}

func rejectSymlinkComponents(root, relativePath string) error {
	current := root
	for _, part := range strings.Split(relativePath, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return fmt.Errorf("inspect bundled FFmpeg: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("Runtime Pack FFmpeg path contains a symlink")
		}
	}
	return nil
}

// Resolve revalidates the capability immediately before process creation.
func (v VerifiedFFmpeg) Resolve() (string, error) {
	if v.packRoot == "" || v.path == "" || v.digest == "" {
		return "", ErrHermeticFFmpegRequired
	}
	if err := verifyPackExecutable(v.packRoot, v.path, v.digest); err != nil {
		return "", err
	}
	return v.path, nil
}

func verifyPackExecutable(root, path, expectedDigest string) error {
	currentRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("resolve Runtime Pack root: %w", err)
	}
	currentRoot, err = filepath.Abs(currentRoot)
	if err != nil || currentRoot != root {
		return fmt.Errorf("Runtime Pack root changed or is not canonical")
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." || rel == ".." || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("FFmpeg executable escapes Runtime Pack")
	}
	rootInfo, err := os.Stat(root)
	if err != nil {
		return fmt.Errorf("Runtime Pack root is not a directory: %w", err)
	}
	if !rootInfo.IsDir() {
		return fmt.Errorf("Runtime Pack root is not a directory")
	}
	current := root
	parts := strings.Split(rel, string(filepath.Separator))
	for i, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return fmt.Errorf("inspect bundled FFmpeg: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("Runtime Pack FFmpeg path contains a symlink")
		}
		if i < len(parts)-1 && !info.IsDir() {
			return fmt.Errorf("Runtime Pack FFmpeg parent is not a directory")
		}
		if i == len(parts)-1 && (!info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0) {
			return fmt.Errorf("Runtime Pack FFmpeg must be a regular executable file")
		}
	}
	before, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect bundled FFmpeg: %w", err)
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open bundled FFmpeg: %w", err)
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return fmt.Errorf("bundled FFmpeg changed during validation")
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("hash bundled FFmpeg: %w", err)
	}
	if hex.EncodeToString(h.Sum(nil)) != expectedDigest {
		return fmt.Errorf("bundled FFmpeg SHA-256 mismatch")
	}
	return nil
}

func normalizeSHA256(value string) (string, error) {
	decoded, err := hex.DecodeString(strings.TrimSpace(value))
	if err != nil || len(decoded) != sha256.Size {
		return "", fmt.Errorf("expected FFmpeg digest must be a SHA-256 hex value")
	}
	return hex.EncodeToString(decoded), nil
}

// ErrHermeticFFmpegRequired indicates that strict execution lacks its capability.
var ErrHermeticFFmpegRequired = errors.New("processing: hermetic mode requires a verified Runtime Pack FFmpeg")

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
	if len(d.pendingTimes) >= maxPendingTimes {
		d.pendingTimes = d.pendingTimes[1:] // drop oldest, bounded
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

func (d *FFmpegDecoder) waitLoop(stderrBuf *tailBuffer) {
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
