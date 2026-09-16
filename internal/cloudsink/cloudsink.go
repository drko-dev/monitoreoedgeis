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

// CloudSink implements processing.Sink. It holds no per-frame state: Router
// guarantees Route is called by a single goroutine per sink, so nothing here
// needs a mutex.
type CloudSink struct {
	sender     FrameSender
	deviceID   string
	credential string
	logger     *slog.Logger
}

// New creates a CloudSink. deviceID/credential are the Edge's own enrolled
// identity (internal/credentials) — the same ones the heartbeat module uses,
// never a separate credential.
func New(sender FrameSender, deviceID, credential string, logger *slog.Logger) *CloudSink {
	if logger == nil {
		logger = slog.Default()
	}
	return &CloudSink{sender: sender, deviceID: deviceID, credential: credential, logger: logger}
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

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: JPEGQuality}); err != nil {
		return fmt.Errorf("cloudsink: encode jpeg: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), RequestTimeout)
	defer cancel()
	if err := s.sender.PostFrame(ctx, s.deviceID, s.credential, f.CandidateKey, f.Seq, f.Timestamp, buf.Bytes()); err != nil {
		return fmt.Errorf("cloudsink: upload frame: %w", err)
	}
	s.logger.Debug("frame uploaded", "candidate_key", f.CandidateKey, "seq", f.Seq, "bytes", buf.Len())
	return nil
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
