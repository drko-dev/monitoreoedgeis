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

// JPEGQuality is fixed, not configurable: the frame is already
// sampled/resized by the Hito H pipeline, so quality tuning belongs to a
// later bandwidth-control milestone (I7), not this first slice.
const JPEGQuality = 85

// RequestTimeout bounds a single frame upload. Router runs exactly one
// worker goroutine per sink (see internal/processing/router.go), so a stuck
// upload only ever delays this sink's own queue — it must still be bounded,
// or a dead SaaS would grow this sink's drop count without ever recovering.
const RequestTimeout = 5 * time.Second

// HealthSink accepts periodic cloud-transport status updates (e.g.
// *health.Reporter). cloudsink never imports internal/health — same
// one-way dependency direction as processing.HealthSink (see
// docs/ARCHITECTURE.md).
type HealthSink interface {
	SetCloudStatus(s Status)
}

// Status is a point-in-time snapshot of real resources consumed by the
// Cloud transport (Hito I10). It measures actual encode/upload activity —
// never an estimated or priced cost. All counters are cumulative since
// process start and reset only on process restart.
type Status struct {
	FramesEncoded         uint64  `json:"frames_encoded"`
	FramesUploadAttempted uint64  `json:"frames_upload_attempted"`
	FramesUploadSucceeded uint64  `json:"frames_upload_succeeded"`
	FramesUploadFailed    uint64  `json:"frames_upload_failed"`
	JPEGBytesGenerated    uint64  `json:"jpeg_bytes_generated"`
	JPEGBytesUploaded     uint64  `json:"jpeg_bytes_uploaded"`
	EncodeLatencyAvgMs    float64 `json:"encode_latency_avg_ms"`
	UploadLatencyAvgMs    float64 `json:"upload_latency_avg_ms"`
	EffectiveBytesPerSec  float64 `json:"effective_bytes_per_sec"`
	EffectiveFramesPerSec float64 `json:"effective_frames_per_sec"`
	SinceSeconds          float64 `json:"since_seconds"`
}

// stats holds the atomic counters backing Status. Route may run
// concurrently with an unrelated /status read, so every field is accessed
// through sync/atomic — never a mutex, to keep the hot path lock-free.
type stats struct {
	framesEncoded        atomic.Uint64
	uploadAttempted      atomic.Uint64
	uploadSucceeded      atomic.Uint64
	uploadFailed         atomic.Uint64
	jpegBytesGenerated   atomic.Uint64
	jpegBytesUploaded    atomic.Uint64
	encodeLatencyNsSum   atomic.Uint64
	encodeLatencySamples atomic.Uint64
	uploadLatencyNsSum   atomic.Uint64
	uploadLatencySamples atomic.Uint64
}

func (s *stats) snapshot(startedAt time.Time) Status {
	encodeSamples := s.encodeLatencySamples.Load()
	uploadSamples := s.uploadLatencySamples.Load()
	var encodeAvgMs, uploadAvgMs float64
	if encodeSamples > 0 {
		encodeAvgMs = float64(s.encodeLatencyNsSum.Load()) / float64(encodeSamples) / float64(time.Millisecond)
	}
	if uploadSamples > 0 {
		uploadAvgMs = float64(s.uploadLatencyNsSum.Load()) / float64(uploadSamples) / float64(time.Millisecond)
	}

	elapsed := time.Since(startedAt).Seconds()
	bytesUploaded := s.jpegBytesUploaded.Load()
	framesSucceeded := s.uploadSucceeded.Load()
	var bytesPerSec, framesPerSec float64
	if elapsed > 0 {
		bytesPerSec = float64(bytesUploaded) / elapsed
		framesPerSec = float64(framesSucceeded) / elapsed
	}

	return Status{
		FramesEncoded:         s.framesEncoded.Load(),
		FramesUploadAttempted: s.uploadAttempted.Load(),
		FramesUploadSucceeded: framesSucceeded,
		FramesUploadFailed:    s.uploadFailed.Load(),
		JPEGBytesGenerated:    s.jpegBytesGenerated.Load(),
		JPEGBytesUploaded:     bytesUploaded,
		EncodeLatencyAvgMs:    encodeAvgMs,
		UploadLatencyAvgMs:    uploadAvgMs,
		EffectiveBytesPerSec:  bytesPerSec,
		EffectiveFramesPerSec: framesPerSec,
		SinceSeconds:          elapsed,
	}
}

// CloudSink implements processing.Sink. It holds no per-frame mutable state
// beyond stats: Router guarantees Route is called by a single goroutine per
// sink, so encoding/upload itself needs no mutex — stats stay atomic only
// because /status reads them from a different goroutine.
type CloudSink struct {
	sender     FrameSender
	deviceID   string
	credential string
	logger     *slog.Logger

	health    HealthSink
	stats     stats
	startedAt time.Time
}

// New creates a CloudSink. deviceID/credential are the Edge's own enrolled
// identity (internal/credentials) — the same ones the heartbeat module uses,
// never a separate credential. health is optional (nil skips /status
// publishing, e.g. in tests).
func New(sender FrameSender, deviceID, credential string, logger *slog.Logger, health HealthSink) *CloudSink {
	if logger == nil {
		logger = slog.Default()
	}
	return &CloudSink{sender: sender, deviceID: deviceID, credential: credential, logger: logger, health: health, startedAt: time.Now()}
}

// Status returns a thread-safe snapshot of real transport resource usage.
func (s *CloudSink) Status() Status {
	return s.stats.snapshot(s.startedAt)
}

// Name implements processing.Sink.
func (s *CloudSink) Name() string { return "cloud" }

// Route implements processing.Sink: encodes f (yuv420p) to JPEG and uploads
// it. A single failed attempt is dropped, not retried — retry/backoff for
// this path is a later milestone (I6, offline buffering), not this slice.
func (s *CloudSink) Route(f processing.Frame) error {
	img, err := yuv420pToImage(f.Data, f.OutputWidth, f.OutputHeight)
	if err != nil {
		return fmt.Errorf("cloudsink: %w", err)
	}

	encodeStart := time.Now()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: JPEGQuality}); err != nil {
		return fmt.Errorf("cloudsink: encode jpeg: %w", err)
	}
	encodeLatency := time.Since(encodeStart)
	s.stats.framesEncoded.Add(1)
	s.stats.jpegBytesGenerated.Add(uint64(buf.Len()))
	s.stats.encodeLatencyNsSum.Add(uint64(encodeLatency.Nanoseconds()))
	s.stats.encodeLatencySamples.Add(1)

	ctx, cancel := context.WithTimeout(context.Background(), RequestTimeout)
	defer cancel()

	s.stats.uploadAttempted.Add(1)
	uploadStart := time.Now()
	err = s.sender.PostFrame(ctx, s.deviceID, s.credential, f.CandidateKey, f.Seq, f.Timestamp, buf.Bytes())
	uploadLatency := time.Since(uploadStart)
	s.stats.uploadLatencyNsSum.Add(uint64(uploadLatency.Nanoseconds()))
	s.stats.uploadLatencySamples.Add(1)

	if err != nil {
		s.stats.uploadFailed.Add(1)
		s.publishStatus()
		return fmt.Errorf("cloudsink: upload frame: %w", err)
	}
	s.stats.uploadSucceeded.Add(1)
	s.stats.jpegBytesUploaded.Add(uint64(buf.Len()))
	s.publishStatus()

	s.logger.Debug("frame uploaded", "candidate_key", f.CandidateKey, "seq", f.Seq, "bytes", buf.Len())
	return nil
}

// publishStatus pushes the current snapshot to health, when configured. It
// runs after every frame: Route is already dominated by JPEG encode + HTTP
// upload, so one atomic-backed snapshot and a map write on the health side
// is not a relevant hot-path cost.
func (s *CloudSink) publishStatus() {
	if s.health == nil {
		return
	}
	s.health.SetCloudStatus(s.stats.snapshot(s.startedAt))
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
