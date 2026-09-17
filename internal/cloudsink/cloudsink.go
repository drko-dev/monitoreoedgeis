// Package cloudsink implements processing.Sink to push sampled video frames
// to the SaaS's Cloud Vision Worker (Milestone I). It reuses
// internal/transport for auth/HTTP against the SaaS's existing endpoints —
// never a second RTSP client, never duplicated credentials/reconnect
// handling, and never talking to the Cloud Vision Worker directly (that
// stays behind the SaaS, reachable only over its own internal IPC).
package cloudsink

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"log/slog"
	"sync"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/processing"
	"github.com/drko-dev/monitoreoedgeis/internal/transport"
)

// FrameSender is the subset of *transport.Client this sink needs, kept as an
// interface so tests never spin up a real HTTP client.
type FrameSender interface {
	PostFrame(ctx context.Context, deviceID, credential, candidateKey string, seq uint64, capturedAt time.Time, jpeg []byte) error
}

// JPEGQuality is fixed, not configurable: the frame is already
// sampled/resized by the Hito H pipeline, so quality tuning belongs to a
// later bandwidth-control milestone (I7), not this first slice.
const JPEGQuality = 85

// RequestTimeout bounds a single frame upload. Router runs exactly one
// worker goroutine per sink (see internal/processing/router.go), so a stuck
// upload only ever delays this sink's own queue — it must still be bounded,
// or a dead SaaS would grow this sink's drop count without ever recovering.
const RequestTimeout = 5 * time.Second

// Drain backoff bounds for replaying buffered frames (Milestone I6). Capped
// exponential backoff, same shape as internal/heartbeat/backoff.go — kept as
// a private duplicate here rather than exported from that package, since
// its type is deliberately unexported (its retry policy is heartbeat-only).
//
// drainPollInterval is a var, not a const, solely so cloudsink_test.go can
// shrink it — production code never changes it.
var drainPollInterval = 2 * time.Second

const (
	minDrainBackoff = 2 * time.Second
	maxDrainBackoff = 60 * time.Second
)

// CloudSink implements processing.Sink. Route itself holds no per-frame
// state: Router guarantees Route is called by a single goroutine per sink,
// so nothing there needs a mutex. buffer (I6, optional) is written from
// that same Route goroutine and drained by its own background goroutine —
// Buffer itself is safe for that concurrent access.
type CloudSink struct {
	sender     FrameSender
	deviceID   string
	credential string
	logger     *slog.Logger

	buffer *Buffer
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// Option configures optional CloudSink behavior at construction time.
type Option func(*CloudSink)

// WithBuffer enables Milestone I6 offline buffering: a Cloud upload that
// fails for a recoverable reason (timeout, SaaS unavailable, or an
// unexpected HTTP status) is spooled under dir instead of being dropped,
// and a background goroutine drains it with backoff once uploads start
// succeeding again. maxBytes and maxFrames bound the spool and must both be
// positive, or buffering stays disabled (logged, not a hard error — an
// Edge with no buffer limits configured must still be able to start and
// upload frames directly, exactly as before I6). maxAge <= 0 disables
// age-based eviction.
func WithBuffer(dir string, maxBytes int64, maxFrames int, maxAge time.Duration) Option {
	return func(s *CloudSink) {
		if maxBytes <= 0 || maxFrames <= 0 {
			s.logger.Warn("cloud buffer disabled: max bytes and max frames must both be positive")
			return
		}
		buf, err := OpenBuffer(dir, maxBytes, maxFrames, maxAge)
		if err != nil {
			s.logger.Error("cloud buffer disabled: recovery failed", slog.Any("error", err))
			return
		}
		s.buffer = buf
	}
}

// New creates a CloudSink. deviceID/credential are the Edge's own enrolled
// identity (internal/credentials) — the same ones the heartbeat module uses,
// never a separate credential.
func New(sender FrameSender, deviceID, credential string, logger *slog.Logger, opts ...Option) *CloudSink {
	if logger == nil {
		logger = slog.Default()
	}
	s := &CloudSink{sender: sender, deviceID: deviceID, credential: credential, logger: logger}
	for _, opt := range opts {
		opt(s)
	}
	if s.buffer != nil {
		ctx, cancel := context.WithCancel(context.Background())
		s.cancel = cancel
		s.wg.Add(1)
		go s.drainLoop(ctx)
	}
	return s
}

// Name implements processing.Sink.
func (s *CloudSink) Name() string { return "cloud" }

// Close implements the Router's optional sinkCloser hook: it stops the
// drain goroutine and waits for it to exit, so shutdown never leaves it
// running against a torn-down agent. A no-op when buffering is disabled.
func (s *CloudSink) Close() {
	if s.cancel == nil {
		return
	}
	s.cancel()
	s.wg.Wait()
}

// CloudBufferStats implements processing.CloudBufferReporter. Returns the
// zero value when buffering is disabled.
func (s *CloudSink) CloudBufferStats() processing.CloudBufferStats {
	if s.buffer == nil {
		return processing.CloudBufferStats{}
	}
	st := s.buffer.Stats()
	return processing.CloudBufferStats{
		BufferedFrames: st.BufferedFrames,
		BufferedBytes:  st.BufferedBytes,
		ReplayedFrames: st.ReplayedFrames,
		DroppedFull:    st.DroppedFull,
		CorruptEntries: st.CorruptEntries,
	}
}

// Route implements processing.Sink: encodes f (yuv420p) to JPEG and uploads
// it directly. If buffering is disabled, a failed attempt is simply
// dropped — the pre-I6 behavior. If enabled: a camera that already has
// frames waiting in the buffer keeps queuing behind them (sending this one
// directly first would replay it out of order), and a direct upload that
// fails for a recoverable reason is spooled for later retry instead of
// dropped outright.
func (s *CloudSink) Route(f processing.Frame) error {
	img, err := yuv420pToImage(f.Data, f.OutputWidth, f.OutputHeight)
	if err != nil {
		return fmt.Errorf("cloudsink: %w", err)
	}

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: JPEGQuality}); err != nil {
		return fmt.Errorf("cloudsink: encode jpeg: %w", err)
	}
	jpegBytes := buf.Bytes()

	if s.buffer != nil && s.buffer.HasPending(f.CandidateKey) {
		return s.enqueue(f, jpegBytes)
	}

	uploadErr := s.upload(f, jpegBytes)
	if uploadErr == nil {
		return nil
	}
	if s.buffer != nil && isRecoverable(uploadErr) {
		if err := s.enqueue(f, jpegBytes); err != nil {
			return fmt.Errorf("%w (buffer: %v)", uploadErr, err)
		}
		s.logger.Debug("frame buffered for retry",
			"candidate_key", f.CandidateKey, "seq", f.Seq, "error", uploadErr)
		return nil
	}
	return uploadErr
}

func (s *CloudSink) upload(f processing.Frame, jpegBytes []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), RequestTimeout)
	defer cancel()
	if err := s.sender.PostFrame(ctx, s.deviceID, s.credential, f.CandidateKey, f.Seq, f.Timestamp, jpegBytes); err != nil {
		return fmt.Errorf("cloudsink: upload frame: %w", err)
	}
	s.logger.Debug("frame uploaded", "candidate_key", f.CandidateKey, "seq", f.Seq, "bytes", len(jpegBytes))
	return nil
}

func (s *CloudSink) enqueue(f processing.Frame, jpegBytes []byte) error {
	err := s.buffer.Enqueue(BufferedFrame{
		CandidateKey: f.CandidateKey,
		Seq:          f.Seq,
		Timestamp:    f.Timestamp,
		JPEG:         jpegBytes,
	})
	if err != nil {
		return fmt.Errorf("cloudsink: %w", err)
	}
	return nil
}

// isRecoverable decides whether a PostFrame error is worth buffering for
// retry. ErrUnauthorized (revoked/invalid credential) is deliberately
// excluded: retrying it won't succeed until an operator fixes the
// credential out of band, and repeatedly retrying it would just be a
// request storm against a SaaS that is already rejecting this Edge. Every
// other transport error PostFrame can return (timeout, SaaS unavailable, or
// any non-2xx/401/403 status — see internal/transport/client.go) is treated
// as recoverable. A plain, unclassified error (e.g. from a fake sender in a
// test) is also never buffered — only errors this package can actually
// name as transient are.
func isRecoverable(err error) bool {
	return errors.Is(err, transport.ErrTimeout) ||
		errors.Is(err, transport.ErrSaaSUnavailable) ||
		errors.Is(err, transport.ErrUnexpectedStatus)
}

// drainLoop replays buffered frames in FIFO order, one at a time, backing
// off exponentially between failed attempts so a still-down SaaS never
// turns into a request storm. It never blocks Route: they only ever touch
// the buffer's own mutex briefly, not each other.
func (s *CloudSink) drainLoop(ctx context.Context) {
	defer s.wg.Done()
	var backoff time.Duration
	for {
		wait := drainPollInterval
		if backoff > 0 {
			wait = backoff
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}

		f, ok, err := s.buffer.Peek()
		if err != nil {
			s.logger.Warn("cloud buffer: peek failed", slog.Any("error", err))
			continue
		}
		if !ok {
			backoff = 0
			continue
		}

		uploadCtx, cancel := context.WithTimeout(ctx, RequestTimeout)
		err = s.sender.PostFrame(uploadCtx, s.deviceID, s.credential, f.CandidateKey, f.Seq, f.Timestamp, f.JPEG)
		cancel()

		switch {
		case err == nil:
			s.buffer.Advance()
			backoff = 0
			s.logger.Debug("buffered frame replayed", "candidate_key", f.CandidateKey, "seq", f.Seq)
		case errors.Is(err, context.Canceled):
			return
		case !isRecoverable(err):
			s.logger.Warn("cloud buffer: dropping frame after non-recoverable replay error",
				"candidate_key", f.CandidateKey, "seq", f.Seq, slog.Any("error", err))
			s.buffer.Discard()
			backoff = 0
		default:
			if backoff == 0 {
				backoff = minDrainBackoff
			} else if backoff *= 2; backoff > maxDrainBackoff {
				backoff = maxDrainBackoff
			}
			s.logger.Debug("cloud buffer: replay failed, backing off",
				slog.Any("error", err), slog.Duration("backoff", backoff))
		}
	}
}

// yuv420pToImage wraps a packed yuv420p buffer (no row padding — exactly
// what ffmpeg_decoder.go's rawvideo output and resizer.go's resize produce)
// as an *image.YCbCr without copying pixel data. jpeg.Encode accepts
// *image.YCbCr directly, so this never round-trips through RGB.
func yuv420pToImage(data []byte, width, height int) (*image.YCbCr, error) {
	if width <= 0 || height <= 0 || width%2 != 0 || height%2 != 0 {
		return nil, fmt.Errorf("invalid frame dimensions %dx%d", width, height)
	}
	ySize := width * height
	cSize := (width / 2) * (height / 2)
	want := ySize + 2*cSize
	if len(data) != want {
		return nil, fmt.Errorf("frame data length %d does not match %dx%d yuv420p (want %d)", len(data), width, height, want)
	}
	return &image.YCbCr{
		Y:              data[:ySize],
		Cb:             data[ySize : ySize+cSize],
		Cr:             data[ySize+cSize : ySize+2*cSize],
		YStride:        width,
		CStride:        width / 2,
		SubsampleRatio: image.YCbCrSubsampleRatio420,
		Rect:           image.Rect(0, 0, width, height),
	}, nil
}
