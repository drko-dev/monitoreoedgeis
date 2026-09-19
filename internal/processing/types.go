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

// Processing modes for video frames.
const (
	ProcessingModeCloud  = "cloud"
	ProcessingModeHybrid = "hybrid"
)

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

	// Hybrid candidate metadata (Milestone J6-J9). Optional; default/empty
	// or "cloud" indicates standard cloud sampling.
	ProcessingMode  string
	CandidateReason string
	CandidateScore  float64
	CorrelationID   string
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
	CandidateKey       string  `json:"candidate_key"`
	State              string  `json:"state"` // starting|running|stalled|error|skipped_limit
	Codec              string  `json:"codec"`
	InputFPS           float64 `json:"input_fps"`
	DecodedFPS         float64 `json:"decoded_fps"`
	OutputFPS          float64 `json:"output_fps"`
	RTPPacketsReceived int64   `json:"rtp_packets_received"`
	FramesReceived     int64   `json:"frames_received"`
	FramesDecoded      int64   `json:"frames_decoded"`
	FramesSampled      int64   `json:"frames_sampled"`
	FramesDropped      int64   `json:"frames_dropped"`
	QueueDepth         int     `json:"queue_depth"`
	BufferUsage        int     `json:"buffer_usage"`
	// RingBufferDropped counts frames the small in-process history ring
	// overwrote because it was full. It is a subset of FramesDropped (which
	// folds it in), reported separately so history loss for clip/debug
	// snapshots is distinguishable from decoder/queue loss. The counter
	// existed internally for a long time with no reader at all.
	RingBufferDropped int64 `json:"ring_buffer_dropped"`
	// UnsupportedNALTypes counts RTP payloads whose H.264 NAL type this
	// depacketizer does not implement (FU-B, MTAP, STAP-B, reserved). Also a
	// subset of FramesDropped, and also previously uncounted in /status.
	UnsupportedNALTypes int64 `json:"unsupported_nal_types"`
	// OversizedAUsDropped counts access units abandoned because reassembly
	// exceeded the protocol-safety ceiling (maxAccessUnitBytes /
	// maxAccessUnitNALUs) — a sender that never closes a frame, rather than
	// packet loss. See H264Depacketizer.
	OversizedAUsDropped int64      `json:"oversized_aus_dropped"`
	DecodeLatencyMs     float64    `json:"decode_latency_ms"`
	LastFrameAt         *time.Time `json:"last_frame_at,omitempty"`
	// Hybrid is nil unless Milestone J's local evaluator is active for this
	// camera (Config.Hybrid.Enabled).
	Hybrid *HybridStatus `json:"hybrid,omitempty"`
}

// VideoPipelineSummary is the small block published to /status under
// "video_pipeline": a camera count plus each camera's PipelineStatus. Never
// carries frame bytes or per-frame history.
type VideoPipelineSummary struct {
	CameraCount int              `json:"camera_count"`
	Cameras     []PipelineStatus `json:"cameras"`
	// CloudBuffer is nil unless a registered Sink implements
	// CloudBufferReporter and reports buffering as active (Milestone I6).
	CloudBuffer  *CloudBufferStats  `json:"cloud_buffer,omitempty"`
	RouterQueues []RouterQueueStats `json:"router_queues,omitempty"`
}

// CloudBufferStats is a point-in-time snapshot of Milestone I6's offline
// buffer (cloudsink.CloudSink, when GEOCAM_CLOUD_BUFFER_MAX_BYTES/FRAMES
// enable it). Defined here rather than in internal/cloudsink so Manager can
// read it without importing cloudsink, which would cycle back to
// processing (cloudsink already imports processing.Frame/Sink).
type CloudBufferStats struct {
	BufferedFrames int   `json:"buffered_frames"`
	BufferedBytes  int64 `json:"buffered_bytes"`
	ReplayedFrames int64 `json:"replayed_frames"`
	DroppedFull    int64 `json:"dropped_buffer_full"`
	CorruptEntries int64 `json:"corrupt_buffer_entries"`
	// DroppedOversize counts a buffered frame discarded during replay
	// because its JPEG size permanently exceeds the currently configured
	// Milestone I7 rate limiter burst capacity (GEOCAM_CLOUD_BURST_BYTES) —
	// it can never be sent under that config, no matter how long the drain
	// loop waits. Distinct from DroppedFull (a live buffer that was full at
	// enqueue time): this is a replay-time, config-driven discard.
	DroppedOversize int64 `json:"dropped_oversize"`
	// DroppedAge counts buffered frames discarded because they sat in the
	// spool longer than GEOCAM_CLOUD_BUFFER_MAX_AGE. The eviction itself is
	// deliberate (a stale frame of a past event is worth less than the space
	// it holds), but it used to happen with no counter and no log at all, so
	// a frame could vanish from the spool with nothing to show for it.
	DroppedAge int64 `json:"dropped_age"`
	// DroppedOverCapacity counts entries discarded while recovering a spool
	// whose on-disk contents exceeded the configured maxFrames/maxBytes —
	// typically because the bound was lowered between releases, or the
	// process died while over the bound. Recovery evicts oldest-first (the
	// newest frames are the ones still likely to matter) and counts every
	// eviction, so an over-capacity spool is bounded and visible instead of
	// silently replayed at its over-capacity footprint.
	DroppedOverCapacity int64      `json:"dropped_over_capacity"`
	Capacity            int        `json:"capacity,omitempty"`
	OldestPending       *time.Time `json:"oldest_pending,omitempty"`
}

// CloudBufferReporter is implemented by a Sink that exposes I6 buffer
// metrics. Only cloudsink.CloudSink does, and only when buffering is
// enabled (see cloudsink.WithBuffer) — Manager checks for it with a type
// assertion, so a Sink without buffering (or DebugSink) is unaffected.
type CloudBufferReporter interface {
	CloudBufferStats() CloudBufferStats
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
	// Hybrid holds Milestone J's local-analysis tunables. Meaningless
	// unless Hybrid.Enabled (set by agent wiring from
	// config.ProcessingMode == config.ModeHybrid, not a second on/off
	// knob) -- when false, cameraPipeline.readLoop behaves exactly as it
	// did before Milestone J: every sampled frame is dispatched.
	Hybrid HybridConfig
}

// HybridConfig holds Milestone J's local motion-filter tunables, all
// sourced from GEOCAM_VIDEO_HYBRID_* environment variables (see
// internal/config).
type HybridConfig struct {
	// Enabled gates the whole hybrid evaluator. Set by agent wiring, true
	// only when config.ProcessingMode == config.ModeHybrid.
	Enabled bool
	// MotionThreshold is the minimum absolute change in a block's average
	// luma (0..255 scale) between consecutive frames for that block to
	// count as "changed".
	MotionThreshold float64
	// MinChangedArea is the minimum fraction (0..1) of evaluated blocks
	// that must be "changed" for the frame to be flagged a motion
	// candidate.
	MinChangedArea float64
	// BlockSize is the pixel edge length of each square block used for
	// the block-based luma diff.
	BlockSize int
	// ROIs restricts motion evaluation to these normalized (0..1) regions
	// of the frame. Empty means "analyze the whole frame" (default).
	ROIs []ROI
	// IdleFPS, when > 0, enables Milestone J5 adaptive sampling: after
	// IdleAfter with no motion candidate, cameraPipeline.readLoop's
	// Sampler drops its emit rate to IdleFPS (never above TargetFPS,
	// which remains the ceiling). 0 disables adaptive sampling entirely
	// -- Sampler then behaves exactly like NewSampler(TargetFPS) always
	// has.
	IdleFPS float64
	// IdleAfter is how long the evaluator must see no motion candidate
	// before dropping to IdleFPS. Only meaningful when IdleFPS > 0.
	IdleAfter time.Duration
}

// HybridStatus is Milestone J's telemetry block, included in
// PipelineStatus only when Hybrid mode is active for that camera. Never
// carries an RTSP URI, credential, or frame bytes -- only counters and the
// current adaptive state.
type HybridStatus struct {
	Enabled          bool    `json:"enabled"`
	FramesEvaluated  int64   `json:"frames_evaluated"`
	MotionCandidates int64   `json:"motion_candidates"`
	FramesFiltered   int64   `json:"frames_filtered"`
	AdaptiveState    string  `json:"adaptive_state"` // "idle" | "active"
	IdleFPS          float64 `json:"idle_fps"`
	ActiveFPS        float64 `json:"active_fps"`
	ROICount         int     `json:"roi_count"`
	LastMotionScore  float64 `json:"last_motion_score"`
}
