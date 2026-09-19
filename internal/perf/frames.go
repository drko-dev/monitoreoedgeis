package perf

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image/jpeg"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/processing"
)

// FrameCollector is a processing.Sink the harness registers alongside the
// pipeline's own DebugSink. It exists for two reasons:
//
//  1. It gives the harness a cheap, lock-free counter to poll while waiting
//     for a decode run to settle, without calling PipelineStatus repeatedly —
//     Status() computes rates as a delta between calls, so polling it would
//     corrupt the pipeline's own decoded_fps/output_fps fields.
//
//  2. It captures the pipeline's real sampled output frames (raw yuv420p),
//     which is what the inference benchmark feeds to the vision worker. That
//     way both measurements run on the same footage produced by the same real
//     pipeline, instead of the inference harness inventing frames.
//
// It is a sink, and therefore subject to the Router's bounded per-sink queue:
// if this collector were slow, frames would be dropped for it alone
// (Router.Dropped("perf-collector")). Those drops are reported, so a
// measurement artifact can never masquerade as a pipeline result.
type FrameCollector struct {
	count atomic.Int64

	mu       sync.Mutex
	keep     int
	frames   [][]byte
	width    int
	height   int
	seqs     []uint64
	corrIDs  []string
	modeSeen map[string]int
	firstAt  time.Time
	lastAt   time.Time
}

// NewFrameCollector creates a collector that retains the raw bytes of the
// first keep frames (0 = retain none, count only).
func NewFrameCollector(keep int) *FrameCollector {
	return &FrameCollector{keep: keep, modeSeen: make(map[string]int)}
}

// Name implements processing.Sink.
func (c *FrameCollector) Name() string { return "perf-collector" }

// Route implements processing.Sink.
func (c *FrameCollector) Route(f processing.Frame) error {
	c.count.Add(1)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.firstAt.IsZero() {
		c.firstAt = f.Timestamp
	}
	c.lastAt = f.Timestamp
	if len(c.seqs) < c.keep {
		c.seqs = append(c.seqs, f.Seq)
		c.corrIDs = append(c.corrIDs, f.CorrelationID)
	}
	mode := f.ProcessingMode
	if mode == "" {
		mode = processing.ProcessingModeCloud
	}
	c.modeSeen[mode]++
	if len(c.frames) < c.keep {
		c.frames = append(c.frames, append([]byte(nil), f.Data...))
		c.width, c.height = f.OutputWidth, f.OutputHeight
	}
	return nil
}

// Count returns how many frames this sink has received.
func (c *FrameCollector) Count() int64 { return c.count.Load() }

// Collected is the retained raw-frame evidence from a run.
type Collected struct {
	Frames         [][]byte
	Width          int
	Height         int
	Seqs           []uint64
	CorrelationIDs []string
	ModeCounts     map[string]int
	FirstAt        time.Time
	LastAt         time.Time
}

// Collected returns a copy of the retained frames and their metadata.
func (c *FrameCollector) Collected() Collected {
	c.mu.Lock()
	defer c.mu.Unlock()
	return Collected{
		Frames:         c.frames,
		Width:          c.width,
		Height:         c.height,
		Seqs:           c.seqs,
		CorrelationIDs: c.corrIDs,
		ModeCounts:     c.modeSeen,
		FirstAt:        c.firstAt,
		LastAt:         c.lastAt,
	}
}

// JPEGQuality matches internal/vision's fixed jpegQuality so the inference
// benchmark's encode cost is the production one rather than a friendlier
// stand-in.
const JPEGQuality = 90

// FrameJPEG encodes one raw yuv420p pipeline frame exactly the way
// internal/vision.Sink.Route does before handing it to the worker:
// processing.YUV420PToImage followed by a JPEG encode at quality 90.
func FrameJPEG(yuv []byte, width, height int) ([]byte, error) {
	img, err := processing.YUV420PToImage(yuv, width, height)
	if err != nil {
		return nil, fmt.Errorf("perf: yuv420p -> image: %w", err)
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: JPEGQuality}); err != nil {
		return nil, fmt.Errorf("perf: jpeg encode: %w", err)
	}
	return buf.Bytes(), nil
}

// SyntheticFrames builds n deterministic JPEG frames without ffmpeg, for
// harness smoke tests and for measuring the vision worker's raw inference
// rate when no decoded footage is available. The content is a moving
// gradient rather than solid colour, so the encoder does real work and
// Ultralytics does not see a degenerate all-flat image.
//
// Every report built from these frames is marked synthetic: they are not
// camera footage and cannot stand in for a decode measurement.
func SyntheticFrames(n, width, height int) ([][]byte, error) {
	out := make([][]byte, 0, n)
	ySize := width * height
	cSize := (width / 2) * (height / 2)
	for i := 0; i < n; i++ {
		yuv := make([]byte, ySize+2*cSize)
		for y := 0; y < height; y++ {
			for x := 0; x < width; x++ {
				yuv[y*width+x] = byte((x + y + i*3) % 256)
			}
		}
		for j := 0; j < cSize; j++ {
			yuv[ySize+j] = byte(96 + (j+i)%64)
		}
		jpg, err := FrameJPEG(yuv, width, height)
		if err != nil {
			return nil, err
		}
		out = append(out, jpg)
	}
	return out, nil
}

// spropBase64 renders SPS/PPS as RFC 6184 §8.1 sprop-parameter-sets, the
// exact SDP value a real camera publishes and internal/rtsp parses back into
// StreamDescriptor.SpropParameterSets.
func spropBase64(sps, pps []byte) string {
	parts := make([]string, 0, 2)
	if len(sps) > 0 {
		parts = append(parts, base64.StdEncoding.EncodeToString(sps))
	}
	if len(pps) > 0 {
		parts = append(parts, base64.StdEncoding.EncodeToString(pps))
	}
	if len(parts) == 0 {
		return ""
	}
	return "a=fmtp:96 sprop-parameter-sets=" + strings.Join(parts, ",") + ";packetization-mode=1\r\n"
}

// buildSDP mirrors internal/rtsptest's default single-H.264-track SDP and
// adds sprop-parameter-sets when the harness knows them, so the production
// SDP parser exercises the same path a real camera would.
func buildSDP(sps, pps []byte) string {
	return "v=0\r\n" +
		"o=- 1 1 IN IP4 127.0.0.1\r\n" +
		"s=geocam-perf\r\n" +
		"t=0 0\r\n" +
		"m=video 0 RTP/AVP 96\r\n" +
		"a=rtpmap:96 H264/90000\r\n" +
		spropBase64(sps, pps) +
		"a=control:track1\r\n"
}

// discardLogger is used when a caller passes nil: benchmark runs are already
// noisy in their own right and the pipeline's structured logs are not part of
// the result.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(discardWriter{}, &slog.HandlerOptions{Level: slog.LevelError}))
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }
