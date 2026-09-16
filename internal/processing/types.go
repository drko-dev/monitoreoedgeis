// Package processing implements the Hito H video pipeline: RTP
// depacketization, H.264 decode, FPS sampling, resize, a bounded ring
// buffer, and frame routing with backpressure — all downstream of the
// existing internal/rtsp transport (Hito G), never a second RTSP client.
//
// Supported codec: H.264 only. H.265 is not depacketized/decoded this
// milestone; constructing a pipeline for a StreamDescriptor with
// Codec=="H265" returns ErrUnsupportedCodec explicitly, never a silent
// no-op.
package processing

import (
	"errors"
	"time"
)

// ErrUnsupportedCodec is returned when a camera's negotiated codec has no
// depacketizer/decoder implementation in this milestone (e.g. H.265).
var ErrUnsupportedCodec = errors.New("processing: unsupported codec")

// AccessUnit is one or more NAL units that together form a decodable H.264
// frame boundary (RFC 6184 §5.1 marker-bit boundary, with a documented
// fallback — see depacketizer.go). NALUs are raw (no Annex-B start code);
// the decoder prefixes them.
type AccessUnit struct {
	NALUs [][]byte
	// ReceivedAt is the ingest time of the AU's first RTP packet — the
	// closest thing to a real capture timestamp this pipeline has.
	ReceivedAt time.Time
}

// DecodedFrame is one decoded raw video frame (yuv420p).
//
// It deliberately has no "RTPTime" field: the ffmpeg subprocess's rawvideo
// stdout does not carry the original RTP timestamp, so this type only
// exposes timestamps the pipeline can actually stand behind:
//   - PipelineSeq is a monotonic counter local to this pipeline, not derived
//     from RTP at all.
//   - SourceReceivedAt is a best-effort FIFO correlation with the
//     AccessUnit.ReceivedAt of the AU presumed to have produced this frame
//     (see ffmpeg_decoder.go). It assumes the decoder emits frames in the
//     same relative order AUs were pushed; with B-frame reordering this can
//     be inexact, and that limitation is intentionally not hidden by a
//     misleadingly precise field name.
//   - DecodedAt is the actual wall-clock time the frame was read back.
type DecodedFrame struct {
	Data             []byte
	Width, Height    int
	PipelineSeq      uint64
	SourceReceivedAt time.Time
	DecodedAt        time.Time
}

// Frame is a decoded frame after sampling/resize, ready for routing. It
// retains only non-sensitive metadata — never a credential or RTSP URI.
type Frame struct {
	CandidateKey     string
	Timestamp        time.Time // == DecodedFrame.DecodedAt
	SourceReceivedAt time.Time
	Seq              uint64
	SourceWidth      int
	SourceHeight     int
	OutputWidth      int
	OutputHeight     int
	Codec            string
	StreamRole       string
	Data             []byte
}

// PipelineStatus is the small, per-camera summary published to /status. It
// never carries frame bytes or any per-frame history.
//
// FramesReceived counts completed access units produced by the depacketizer
// — not raw RTP packets (an access unit is typically several packets, e.g.
// one FU-A run). RTPPacketsReceived is the raw-packet counter, kept
// separate rather than silently redefining "frames" to mean "packets".
//
// FramesDecoded/FramesDropped are cumulative across decoder subprocess
// restarts (they fold in the outgoing decoder's final counts before a new
// one starts, see cameraPipeline.foldDecoderCounts) — they never reset to
// zero mid-pipeline-lifetime, so DecodedFPS/OutputFPS (computed as a delta
// between two Status() calls) never goes negative across a restart.
type PipelineStatus struct {
	CandidateKey       string     `json:"candidate_key"`
	State              string     `json:"state"` // starting|running|stalled|error|skipped_limit
	Codec              string     `json:"codec"`
	InputFPS           float64    `json:"input_fps"`
	DecodedFPS         float64    `json:"decoded_fps"`
	OutputFPS          float64    `json:"output_fps"`
	RTPPacketsReceived int64      `json:"rtp_packets_received"`
	FramesReceived     int64      `json:"frames_received"`
	FramesDecoded      int64      `json:"frames_decoded"`
	FramesSampled      int64      `json:"frames_sampled"`
	FramesDropped      int64      `json:"frames_dropped"`
	QueueDepth         int        `json:"queue_depth"`
	BufferUsage        int        `json:"buffer_usage"`
	DecodeLatencyMs    float64    `json:"decode_latency_ms"`
	LastFrameAt        *time.Time `json:"last_frame_at,omitempty"`
}

// VideoPipelineSummary is the small block published to /status under
// "video_pipeline": a camera count plus each camera's PipelineStatus. Never
// carries frame bytes or per-frame history.
type VideoPipelineSummary struct {
	CameraCount int              `json:"camera_count"`
	Cameras     []PipelineStatus `json:"cameras"`
}

// Config holds the video pipeline's tunables, all sourced from
// GEOCAM_VIDEO_* environment variables (see internal/config).
type Config struct {
	Enabled                bool
	TargetFPS              float64
	OutputWidth            int
	OutputHeight           int
	RingBufferSize         int
	QueueDepth             int
	DecodeQueueDepth       int
	MaxConcurrentPipelines int
	FFmpegPath             string
	DecodeTimeout          time.Duration
}
