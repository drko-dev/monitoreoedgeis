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
	"sync/atomic"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/processing"
)

// FrameSender is the subset of *transport.Client this sink needs, kept as an
// interface so tests never spin up a real HTTP client.
type FrameSender interface {
	PostFrame(ctx context.Context, deviceID, credential, candidateKey string, seq uint64, capturedAt time.Time, jpeg []byte) error
}

// DefaultJPEGQuality is the standard compression quality for Cloud upload (Milestone I).
const DefaultJPEGQuality = 85

// JPEGQuality is maintained for backward compatibility.
const JPEGQuality = DefaultJPEGQuality

// RequestTimeout bounds a single frame upload. Router runs exactly one
// worker goroutine per sink (see internal/processing/router.go), so a stuck
// upload only ever delays this sink's own queue — it must still be bounded,
// or a dead SaaS would grow this sink's drop count without ever recovering.
const RequestTimeout = 5 * time.Second

// Sentinel errors distinguishing pre-POST throttling from transport or encode failures.
var (
	// ErrThrottled is returned by Route when a frame is dropped before POST
	// because upload bandwidth or frame rate limits are exceeded (Milestone I7).
	ErrThrottled = errors.New("cloudsink: frame throttled by rate limit")

	// ErrRateLimited is an alias for ErrThrottled.
	ErrRateLimited = ErrThrottled
)

// Config holds tunables for CloudSink bandwidth control (Milestone I7).
type Config struct {
	JPEGQuality    int     // 1 to 100, default 85
	MaxBytesPerSec int64   // > 0 enables byte rate limiting, 0 = unlimited
	BurstBytes     int64   // > 0 sets explicit burst capacity in bytes, 0 = auto
	MaxFPS         float64 // > 0 enables frame rate limiting towards Cloud, 0 = unlimited
}

// DefaultConfig returns the backward-compatible configuration matching PR #11.
func DefaultConfig() Config {
	return Config{
		JPEGQuality:    DefaultJPEGQuality,
		MaxBytesPerSec: 0,
		BurstBytes:     0,
		MaxFPS:         0,
	}
}

// Status captures I7 runtime metrics and configured limits for Cloud upload.
type Status struct {
	ThrottledFrames     int64   `json:"throttled_frames"`
	ThrottledBytes      int64   `json:"throttled_bytes"`
	UploadedFrames      int64   `json:"uploaded_frames"`
	UploadedBytes       int64   `json:"uploaded_bytes"`
	EffectiveUploadRate float64 `json:"effective_upload_rate"`
	ConfiguredLimit     int64   `json:"configured_limit"`
}

// CloudSink implements processing.Sink.
type CloudSink struct {
	sender     FrameSender
	deviceID   string
	credential string
	cfg        Config
	limiter    *TokenBucket
	logger     *slog.Logger

	throttledFrames atomic.Int64
	throttledBytes  atomic.Int64
	uploadedFrames  atomic.Int64
	uploadedBytes   atomic.Int64
	tracker         *uploadTracker
}

// New creates a CloudSink with default configuration (PR #11 behavior: quality 85, no rate limiting).
func New(sender FrameSender, deviceID, credential string, logger *slog.Logger) *CloudSink {
	return NewWithConfig(sender, deviceID, credential, DefaultConfig(), logger)
}

// NewWithConfig creates a CloudSink with configurable JPEG quality and bandwidth control (Milestone I7).
func NewWithConfig(sender FrameSender, deviceID, credential string, cfg Config, logger *slog.Logger) *CloudSink {
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.JPEGQuality <= 0 || cfg.JPEGQuality > 100 {
		cfg.JPEGQuality = DefaultJPEGQuality
	}

	var limiter *TokenBucket
	if cfg.MaxBytesPerSec > 0 || cfg.MaxFPS > 0 {
		limiter = NewLimiter(cfg.MaxBytesPerSec, cfg.BurstBytes, cfg.MaxFPS)
	}

	return &CloudSink{
		sender:     sender,
		deviceID:   deviceID,
		credential: credential,
		cfg:        cfg,
		limiter:    limiter,
		logger:     logger,
		tracker:    newUploadTracker(),
	}
}

// Name implements processing.Sink.
func (s *CloudSink) Name() string { return "cloud" }

// Route implements processing.Sink: encodes f (yuv420p) to JPEG and uploads
// it. Throttled frames are dropped BEFORE calling POST and counted in metrics,
// clearly distinguished from network transport errors and encode errors.
// A single failed network attempt is dropped, not retried (Milestone I6 handles buffering).
func (s *CloudSink) Route(f processing.Frame) error {
	img, err := yuv420pToImage(f.Data, f.OutputWidth, f.OutputHeight)
	if err != nil {
		return fmt.Errorf("cloudsink: %w", err)
	}

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: s.cfg.JPEGQuality}); err != nil {
		return fmt.Errorf("cloudsink: encode jpeg: %w", err)
	}

	frameBytes := int64(buf.Len())

	// Pre-POST bandwidth & rate limit control (Milestone I7).
	// Never modifies candidate_key, frame_seq, or timestamp.
	if s.limiter != nil && !s.limiter.Allow(frameBytes) {
		s.throttledFrames.Add(1)
		s.throttledBytes.Add(frameBytes)
		s.logger.Debug("frame throttled by rate limit",
			"candidate_key", f.CandidateKey,
			"seq", f.Seq,
			"bytes", frameBytes,
			"throttled_frames", s.throttledFrames.Load(),
		)
		return ErrThrottled
	}

	ctx, cancel := context.WithTimeout(context.Background(), RequestTimeout)
	defer cancel()
	if err := s.sender.PostFrame(ctx, s.deviceID, s.credential, f.CandidateKey, f.Seq, f.Timestamp, buf.Bytes()); err != nil {
		return fmt.Errorf("cloudsink: upload frame: %w", err)
	}

	s.uploadedFrames.Add(1)
	s.uploadedBytes.Add(frameBytes)
	s.tracker.record(frameBytes, time.Now())

	s.logger.Debug("frame uploaded", "candidate_key", f.CandidateKey, "seq", f.Seq, "bytes", frameBytes)
	return nil
}

// Status returns a point-in-time snapshot of the CloudSink's bandwidth metrics and limits.
func (s *CloudSink) Status() Status {
	var configuredLimit int64
	if s.limiter != nil {
		configuredLimit = s.limiter.ConfiguredLimit()
	}
	return Status{
		ThrottledFrames:     s.throttledFrames.Load(),
		ThrottledBytes:      s.throttledBytes.Load(),
		UploadedFrames:      s.uploadedFrames.Load(),
		UploadedBytes:       s.uploadedBytes.Load(),
		EffectiveUploadRate: s.tracker.rate(time.Now()),
		ConfiguredLimit:     configuredLimit,
	}
}

// Metrics is an alias for Status.
func (s *CloudSink) Metrics() Status {
	return s.Status()
}

// Limiter returns the active TokenBucket rate limiter, or nil if unconstrained.
func (s *CloudSink) Limiter() *TokenBucket {
	return s.limiter
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
