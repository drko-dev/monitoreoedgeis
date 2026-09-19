package perf

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/processing"
	"github.com/drko-dev/monitoreoedgeis/internal/rtsp"
	"github.com/drko-dev/monitoreoedgeis/internal/rtsptest"
)

// DecodeMode selects which real chain the measurement drives.
type DecodeMode string

const (
	// DecodeModePipeline is the production path end to end: RTP over an RTSP
	// session served by internal/rtsptest.Simulator, consumed by
	// internal/rtsp.Manager, depacketized and decoded by
	// internal/processing.Manager (real ffmpeg subprocess), then sampled and
	// routed. This is where Hito X's decode FPS should be read from.
	DecodeModePipeline DecodeMode = "pipeline"
	// DecodeModeDirect feeds access units straight into the production
	// processing.FFmpegDecoder, bypassing RTP/RTSP. It isolates the decoder
	// itself and yields a genuine per-frame decode-latency distribution (the
	// pipeline only exposes a last-frame snapshot), so a difference between the
	// two modes localises a bottleneck to decoding or to ingest.
	DecodeModeDirect DecodeMode = "decoder-direct"
)

// Pacing describes how access units are released to the transport.
type Pacing string

const (
	// PacingRealtime releases frame i at start + i/nominalFPS, i.e. what a real
	// camera does. The question it answers: does the Edge keep up with a camera
	// in real time?
	PacingRealtime Pacing = "realtime"
	// PacingMax releases frames as fast as the transport accepts them. It is a
	// burst, not a sustained ceiling: over interleaved TCP the sender's rate is
	// a property of the socket, and the pipeline answers by shedding frames at
	// its bounded queues.
	PacingMax Pacing = "max"
)

// Pacing rate multipliers worth running. A sustained ceiling is found by
// feeding faster than real time while STILL pacing: 1x answers "does the Edge
// keep up with the camera", 3x answers "how much decode headroom is there
// before frames start being shed".
const (
	PacingMultiplierRealtime = 1.0
	PacingMultiplierThreeX   = 3.0
)

// DecodeOptions configures one decode measurement. Every field that changes the
// number is echoed into the report.
type DecodeOptions struct {
	Mode DecodeMode
	Clip ClipInfo
	AUs  []AccessUnit
	// SPS/PPS are injected into the simulator's SDP as sprop-parameter-sets
	// (and, in direct mode, into the decoder the way production does from
	// StreamDescriptor.SpropParameterSets). Nil is valid: the harness's clip
	// repeats parameter sets in-band at every IDR.
	SPS, PPS []byte

	Pacing Pacing
	// PacingMultiplier releases access units at PacingMultiplier times the
	// clip's nominal frame rate while still pacing them evenly. It only applies
	// to PacingRealtime and defaults to 1. Timestamps inside the stream are
	// unaffected — this changes how fast the harness plays the clip out, which
	// is exactly the variable a capacity probe needs.
	PacingMultiplier float64

	CandidateKey string
	FFmpegPath   string
	Logger       *slog.Logger

	// PrimingFrames is how many access units are fed BEFORE the measured window
	// opens, and it is not cosmetic. This harness measured — and PrimingMS and
	// TimeToFirstFrameMS exist to report — a real property of the decode path:
	// fed a raw H.264 elementary stream, ffmpeg emits nothing at all for a
	// multi-second priming period, after which it sustains the input rate while
	// holding a persistent lag of roughly a dozen frames. Folding that startup
	// into a rate would report a priming artifact as decode throughput, so
	// priming is excluded from every rate and reported on its own.
	//
	// Zero disables priming. The default consumes about four seconds of source
	// at the clip's nominal frame rate.
	PrimingFrames int

	// Timeout bounds the tail drain that follows the measured feed.
	Timeout time.Duration
	// Stability is how long a counter must stay unchanged before a drain is
	// considered complete.
	Stability time.Duration

	// Pipeline knobs, defaulted from internal/config's documented defaults so a
	// report is comparable with a real deployment.
	StreamRole             string
	OutputWidth            int
	OutputHeight           int
	SampleFPS              float64
	QueueDepth             int
	DecodeQueueDepth       int
	MaxConcurrentPipelines int
	DecodeTimeout          time.Duration
	RTSPPacketTimeout      time.Duration

	// KeepFrames asks the collector to retain the raw bytes of that many
	// sampled frames, for the inference benchmark to reuse.
	KeepFrames int

	// TraceInterval, when > 0, logs a pipeline status sample at that interval
	// for the whole run. It is a diagnostic: it answers "where did the frames
	// go" with observations instead of inference. It also perturbs the
	// pipeline's own decoded_fps/output_fps fields, because those are deltas
	// between Status() calls — the harness's own rates, computed from absolute
	// counters, are unaffected.
	TraceInterval time.Duration

	// DecoderFactory replaces the ffmpeg subprocess with a fake. Test-only:
	// every run that sets it is reported with DecoderFake=true, because a fake
	// decoder validates the harness and proves nothing about ffmpeg.
	DecoderFactory func() (processing.VideoDecoder, error)
}

// DecodeResult is the machine-readable outcome of one decode measurement.
//
// The measurement is deliberately split into three phases — priming, measured
// feed, tail drain — because the decode path behaves differently in each and
// blending them would produce a number that describes none of them:
//
//   - PrimingMS / TimeToFirstFrameMS: startup. Excluded from every rate.
//   - DecodeFPS / IngestFPS / SampleFPS: steady state while the camera keeps
//     producing. These are the numbers to compare against a camera's frame
//     rate.
//   - TailFramesFlushed / TailFlushMS / DecodeFPSEndToEnd: what it costs to get
//     the last frames out once the source stops, which is a real cost on a
//     decoder restart or shutdown.
type DecodeResult struct {
	Mode        DecodeMode `json:"mode"`
	Pacing      Pacing     `json:"pacing"`
	DecoderFake bool       `json:"decoder_fake"`
	Boundary    string     `json:"access_unit_boundary"`
	// PacingFPS is the actual release rate: the clip's nominal rate times
	// PacingMultiplier for paced runs.
	PacingFPS float64 `json:"pacing_fps"`

	// FramesFed counts every access unit the harness released, priming
	// included. MeasuredFrames is the subset inside the measured window.
	FramesFed      int     `json:"frames_fed"`
	MeasuredFrames int     `json:"measured_frames"`
	PrimingFrames  int     `json:"priming_frames"`
	PrimingMS      float64 `json:"priming_ms,omitempty"`

	// RTPPacketsSent counts packets released inside the measured window only,
	// so it is directly comparable with RTPPacketsReceived below.
	RTPPacketsSent int     `json:"rtp_packets_sent,omitempty"`
	PacketsPerAU   float64 `json:"rtp_packets_per_access_unit,omitempty"`

	// SendElapsedMS is how long the harness took to release the measured access
	// units; FeedElapsedMS additionally covers the pipeline taking them in, and
	// is the denominator of every steady-state rate below. Over an interleaved
	// TCP transport the two differ sharply under max pacing, which is exactly
	// why they are reported separately.
	SendElapsedMS  float64 `json:"send_elapsed_ms"`
	FeedElapsedMS  float64 `json:"feed_elapsed_ms"`
	ElapsedMS      float64 `json:"elapsed_ms"`
	DecoderStartMS float64 `json:"decoder_start_ms,omitempty"`
	TailFlushMS    float64 `json:"tail_flush_ms,omitempty"`

	// HarnessFeedFPS is access units the harness released per second of its own
	// send window. Over an interleaved TCP transport a fast sender merely fills
	// socket buffers, so under max pacing this is a property of the harness and
	// the socket — never of the pipeline. IngestFPS is the number that describes
	// the pipeline: access units completed per second of the measured window,
	// i.e. "input frames/time". DecodeFPS is decoded frames per second of the
	// same window.
	HarnessFeedFPS float64 `json:"harness_feed_fps"`
	IngestFPS      float64 `json:"ingest_fps"`
	DecodeFPS      float64 `json:"decode_fps"`
	SampleFPS      float64 `json:"sampled_fps,omitempty"`
	// DecodeFPSEndToEnd amortises the tail drain over the whole run: decoded
	// frames divided by the measured feed window plus the tail flush. It is what
	// a fixed-length clip actually costs in wall clock, and it is always at or
	// below DecodeFPS. Note that TailFlushMS is bounded below by the harness's
	// stability window, so this figure is conservative.
	DecodeFPSEndToEnd float64 `json:"decode_fps_end_to_end"`

	// DecodeToIngestRatio is DecodeFPS / IngestFPS, and SteadyStateAchieved is
	// the harness's own check that the measured window really was steady state:
	// a ratio materially below 1 means the decoder was still catching up while
	// being measured, so the rate describes a warming decoder rather than a
	// sustained one.
	DecodeToIngestRatio float64 `json:"decode_to_ingest_ratio,omitempty"`
	SteadyStateAchieved bool    `json:"steady_state_achieved"`

	TimeToFirstFrameMS float64 `json:"time_to_first_frame_ms"`

	// Counters below come from the component that owns them: the pipeline's own
	// PipelineStatus in pipeline mode, or the decoder's own
	// DecodedCount/DroppedCount in direct mode. All of them are deltas over the
	// measured window, except TailFramesFlushed, which is measured afterwards.
	RTPPacketsReceived int64 `json:"rtp_packets_received,omitempty"`
	FramesReceived     int64 `json:"frames_received_access_units"`
	FramesDecoded      int64 `json:"frames_decoded"`
	FramesSampled      int64 `json:"frames_sampled,omitempty"`
	FramesDropped      int64 `json:"frames_dropped"`
	TailFramesFlushed  int64 `json:"tail_frames_flushed,omitempty"`
	// BufferedLagFrames is how many access units the decoder had taken in but
	// not yet emitted when the feed ended: the pipeline's steady-state buffering
	// depth, and the reason FramesDecoded lags IngestFPS by a constant amount
	// rather than tracking it one-for-one.
	BufferedLagFrames int64 `json:"decoder_buffered_lag_frames,omitempty"`
	SinkFrames        int64 `json:"sink_frames_routed,omitempty"`
	// SinkFramesDuringDrain counts frames this harness's sink received after the
	// measured window closed: the pipeline's own delivery pipeline catching up.
	SinkFramesDuringDrain int64 `json:"sink_frames_during_drain,omitempty"`
	// SinkFramesNotObserved counts frames the pipeline sampled but this
	// harness's own sink never received — dropped by that sink's bounded Router
	// queue, or still queued when the snapshot was taken. It is a
	// measurement-side number, reported so it can never be mistaken for a
	// pipeline result.
	SinkFramesNotObserved int64 `json:"sink_frames_not_observed,omitempty"`

	// TransportDeliveryRatio compares packets the harness sent with packets the
	// RTSP session actually delivered. A shortfall means the transport, not the
	// decoder, lost frames — the measurement would otherwise blame the wrong
	// stage.
	TransportDeliveryRatio float64 `json:"transport_delivery_ratio,omitempty"`

	// DecodeLatency is per-frame (DecodedAt - SourceReceivedAt) in direct mode,
	// where the harness owns the drain loop. In pipeline mode the pipeline only
	// exposes the last frame's value, reported as
	// PipelineLastDecodeLatencyMS and explicitly not a distribution.
	DecodeLatency               LatencyStats `json:"decode_latency"`
	PipelineLastDecodeLatencyMS float64      `json:"pipeline_last_decode_latency_ms,omitempty"`

	Errors []string `json:"errors,omitempty"`

	// StatusBefore/Mid/After are the pipeline's own PipelineStatus snapshots
	// taken at the three phase boundaries, included verbatim so the derived
	// numbers above can be audited against the component that produced them.
	// Their decoded_fps/output_fps fields are deltas between Status() calls and
	// are NOT this benchmark's decode_fps.
	StatusBefore *processing.PipelineStatus `json:"pipeline_status_before,omitempty"`
	StatusMid    *processing.PipelineStatus `json:"pipeline_status_mid,omitempty"`
	StatusAfter  *processing.PipelineStatus `json:"pipeline_status_after,omitempty"`

	// Collected carries the retained raw frames; not serialized.
	Collected Collected `json:"-"`
	Clip      ClipInfo  `json:"-"`
}

func (o *DecodeOptions) applyDefaults() {
	if o.Mode == "" {
		o.Mode = DecodeModePipeline
	}
	if o.Pacing == "" {
		o.Pacing = PacingRealtime
	}
	if o.PacingMultiplier <= 0 {
		o.PacingMultiplier = PacingMultiplierRealtime
	}
	if o.Pacing != PacingRealtime {
		o.PacingMultiplier = 0
	}
	if o.CandidateKey == "" {
		o.CandidateKey = "perf-cam-1"
	}
	if o.FFmpegPath == "" {
		o.FFmpegPath = "ffmpeg"
	}
	if o.StreamRole == "" {
		o.StreamRole = rtsp.StreamRoleSub
	}
	if o.OutputWidth <= 0 {
		o.OutputWidth = 640
	}
	if o.OutputHeight <= 0 {
		o.OutputHeight = 360
	}
	if o.SampleFPS <= 0 {
		o.SampleFPS = 5
	}
	if o.QueueDepth <= 0 {
		o.QueueDepth = 64
	}
	if o.DecodeQueueDepth <= 0 {
		o.DecodeQueueDepth = 4
	}
	if o.MaxConcurrentPipelines <= 0 {
		o.MaxConcurrentPipelines = 4
	}
	if o.DecodeTimeout <= 0 {
		o.DecodeTimeout = 10 * time.Second
	}
	if o.RTSPPacketTimeout <= 0 {
		// Longer than the production default (5s): the benchmark feeds a fixed,
		// finite clip and then drains. A reconnect firing during the drain would
		// replace the decoder mid-measurement and corrupt every counter. This
		// changes failure-detection timing, not decode throughput, and it also
		// bounds how long a stop waits for a read already in flight — see the
		// simulator close in runDecodePipeline.
		o.RTSPPacketTimeout = 10 * time.Second
	}
	if o.Stability <= 0 {
		// Bounded below by the stability check itself, so a shorter window keeps
		// the tail-flush measurement honest without changing any rate.
		o.Stability = 750 * time.Millisecond
	}
	if o.Timeout <= 0 {
		o.Timeout = 2 * time.Minute
	}
	if o.Logger == nil {
		o.Logger = discardLogger()
	}
	if o.Clip.NominalSourceFPS <= 0 {
		o.Clip.NominalSourceFPS = DefaultClipSpec().FPS
	}
	if o.PrimingFrames < 0 {
		o.PrimingFrames = 0
	}
	if o.PrimingFrames == 0 && o.Mode == DecodeModePipeline {
		// In pipeline mode the cameraPipeline object — and with it the only
		// place a status baseline can be read from — is created by the first
		// RTP packet, so at least one access unit must precede the measured
		// window. One frame of pre-roll is also what a real camera always
		// provides before a decoder starts counting.
		o.PrimingFrames = 1
	}
	if o.PrimingFrames > len(o.AUs) {
		o.PrimingFrames = len(o.AUs)
	}
}

// DefaultPrimingFrames returns the priming pre-roll a caller should use for a
// given pacing, and exists so callers cannot quietly get this wrong.
//
// Real-time pacing needs a pre-roll: it reproduces a camera, and a camera does
// not start at full rate. Under maximum pacing a pre-roll would be actively
// wrong — the whole point of that mode is the ceiling, and the decoder's
// input-driven priming is over within milliseconds — so it returns 0 and the
// entire clip becomes the measured window.
//
// The four seconds come from measurement, not from a guess: see
// docs/performance/VIDEO_DECODE_INFERENCE.md.
func DefaultPrimingFrames(clip ClipInfo, aus int, pacing Pacing, multiplier float64) int {
	if pacing != PacingRealtime {
		return 0
	}
	rate := clip.NominalSourceFPS
	if multiplier > 0 {
		rate *= multiplier
	}
	want := int(rate * primingSeconds)
	if want < 1 {
		want = 1
	}
	if aus <= 0 {
		return 0
	}
	if max := aus / 3; want > max {
		want = max
	}
	return want
}

// pacingFPS is the rate access units are released at.
func (o *DecodeOptions) pacingFPS() float64 {
	if o.Pacing != PacingRealtime || o.PacingMultiplier <= 0 {
		return 0
	}
	return o.Clip.NominalSourceFPS * o.PacingMultiplier
}

// measuredCount is the number of access units inside the measured window.
func (o *DecodeOptions) measuredCount() int {
	n := len(o.AUs) - o.PrimingFrames
	if n < 0 {
		return 0
	}
	return n
}

// RunDecode measures decode throughput with the selected mode.
func RunDecode(ctx context.Context, opts DecodeOptions) (DecodeResult, error) {
	opts.applyDefaults()
	if len(opts.AUs) == 0 {
		return DecodeResult{}, errors.New("perf: no access units to decode")
	}
	if opts.measuredCount() == 0 {
		return DecodeResult{}, fmt.Errorf("perf: priming consumes all %d access units, leaving nothing to measure", len(opts.AUs))
	}
	switch opts.Mode {
	case DecodeModePipeline:
		return runDecodePipeline(ctx, opts)
	case DecodeModeDirect:
		return runDecodeDirect(ctx, opts)
	default:
		return DecodeResult{}, fmt.Errorf("perf: unknown decode mode %q", opts.Mode)
	}
}

// decoderSprop returns the parameter sets to inject, omitting absent ones.
// Passing a nil entry would make FFmpegDecoder write a bare start code to the
// decoder's stdin, which is an empty NAL unit, not "no parameter sets".
func decoderSprop(sps, pps []byte) [][]byte {
	out := make([][]byte, 0, 2)
	if len(sps) > 0 {
		out = append(out, sps)
	}
	if len(pps) > 0 {
		out = append(out, pps)
	}
	return out
}

func nalBytes(au AccessUnit) [][]byte {
	out := make([][]byte, 0, len(au.NALUs))
	for _, n := range au.NALUs {
		out = append(out, n.Bytes)
	}
	return out
}

// frameOffset is how long after the start of a phase frame i should be sent to
// reproduce the clip's own frame cadence.
func frameOffset(i int, nominalFPS float64) time.Duration {
	if nominalFPS <= 0 {
		return 0
	}
	return time.Duration(float64(i) / nominalFPS * float64(time.Second))
}

func sleepUntil(ctx context.Context, target time.Time) error {
	if d := time.Until(target); d > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(d):
		}
	}
	return nil
}

// runDecodeDirect drives the production ffmpeg decoder subprocess with access
// units and drains its raw output as fast as possible.
func runDecodeDirect(ctx context.Context, opts DecodeOptions) (DecodeResult, error) {
	res := DecodeResult{
		Mode:           opts.Mode,
		Pacing:         opts.Pacing,
		DecoderFake:    opts.DecoderFactory != nil,
		Boundary:       opts.Clip.AccessUnitBoundary,
		PacingFPS:      opts.pacingFPS(),
		FramesFed:      len(opts.AUs),
		MeasuredFrames: opts.measuredCount(),
		PrimingFrames:  opts.PrimingFrames,
		Clip:           opts.Clip,
	}

	newDecoder := opts.DecoderFactory
	if newDecoder == nil {
		newDecoder = func() (processing.VideoDecoder, error) {
			return processing.NewFFmpegDecoder(processing.FFmpegDecoderConfig{
				BinaryPath: opts.FFmpegPath,
				QueueDepth: opts.DecodeQueueDepth,
			}, opts.Clip.Width, opts.Clip.Height, decoderSprop(opts.SPS, opts.PPS), opts.Logger)
		}
	}

	start := time.Now()
	dec, err := newDecoder()
	if err != nil {
		return res, fmt.Errorf("perf: start decoder: %w", err)
	}
	res.DecoderStartMS = RoundMS(MS(time.Since(start)))

	var (
		drainWG sync.WaitGroup
		drainMu sync.Mutex
		latency []float64
		keep    [][]byte
		firstAt time.Time
	)
	// The decoder never closes its Frames channel (it is open for the decoder's
	// lifetime, see processing.VideoDecoder), so the drain loop is bounded by
	// Done plus a final non-blocking sweep of buffered frames.
	drainWG.Add(1)
	go func() {
		defer drainWG.Done()
		record := func(frame processing.DecodedFrame) {
			drainMu.Lock()
			// The decoder's own DecodedAt is used rather than time.Now(): it is
			// when the frame was actually read out, so time-to-first-frame is
			// not skewed by drain-loop scheduling. It is read while the drain
			// goroutine is still running (the priming phase reports it), so it
			// is written under the same mutex that guards it.
			if firstAt.IsZero() {
				firstAt = frame.DecodedAt
			}
			latency = append(latency, MS(frame.DecodedAt.Sub(frame.SourceReceivedAt)))
			if len(keep) < opts.KeepFrames {
				keep = append(keep, append([]byte(nil), frame.Data...))
			}
			drainMu.Unlock()
		}
		for {
			select {
			case frame, ok := <-dec.Frames():
				if !ok {
					return
				}
				record(frame)
			case <-dec.Done():
				for {
					select {
					case frame, ok := <-dec.Frames():
						if !ok {
							return
						}
						record(frame)
					default:
						return
					}
				}
			}
		}
	}()

	push := func(i int) error {
		// ReceivedAt is the ingest timestamp the decoder correlates a decoded
		// frame back to; leaving it zero would make every latency measurement
		// meaningless (it would be measured against the zero time).
		return dec.Push(processing.AccessUnit{NALUs: nalBytes(opts.AUs[i]), ReceivedAt: time.Now()})
	}

	// Phase 1: priming, excluded from every rate.
	primeStart := time.Now()
	for i := 0; i < opts.PrimingFrames; i++ {
		if opts.Pacing == PacingRealtime {
			if err := sleepUntil(ctx, primeStart.Add(frameOffset(i, opts.pacingFPS()))); err != nil {
				res.Errors = append(res.Errors, err.Error())
				break
			}
		}
		if err := push(i); err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("priming push %d failed: %v", i, err))
			break
		}
	}
	// Priming is a continuous pre-roll, never a pause: this decoder's raw H.264
	// input buffering is driven by the ongoing stream, and deliberately going
	// quiet between priming and measuring makes its first output arrive later
	// than it would under a real camera — measured, not assumed.
	res.PrimingMS = RoundMS(MS(time.Since(primeStart)))
	drainMu.Lock()
	first := firstAt
	drainMu.Unlock()
	if !first.IsZero() {
		res.TimeToFirstFrameMS = RoundMS(MS(first.Sub(primeStart)))
	}

	// Phase 2: the measured feed.
	before := dec.DecodedCount()
	dropsBefore := dec.DroppedCount()
	feedStart := time.Now()
	for i := opts.PrimingFrames; i < len(opts.AUs); i++ {
		if opts.Pacing == PacingRealtime {
			if err := sleepUntil(ctx, feedStart.Add(frameOffset(i-opts.PrimingFrames, opts.pacingFPS()))); err != nil {
				res.Errors = append(res.Errors, err.Error())
				break
			}
		}
		if err := push(i); err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("push %d failed: %v", i, err))
			break
		}
	}
	feedElapsed := time.Since(feedStart)
	mid := dec.DecodedCount()

	res.SendElapsedMS = RoundMS(MS(feedElapsed))
	res.FeedElapsedMS = RoundMS(MS(feedElapsed))
	res.HarnessFeedFPS = FPS(int64(opts.measuredCount()), feedElapsed)
	res.IngestFPS = FPS(int64(opts.measuredCount()), feedElapsed)
	res.FramesReceived = int64(opts.measuredCount())
	res.FramesDecoded = mid - before
	res.DecodeFPS = FPS(res.FramesDecoded, feedElapsed)
	res.BufferedLagFrames = res.FramesReceived - res.FramesDecoded

	// Phase 3: drain the tail. The subprocess still holds buffered input when
	// the last measured access unit has been written, and Close() terminates it,
	// so closing immediately would silently discard the tail.
	drainStart := time.Now()
	if _, derr := waitUntilStable(ctx, opts.Timeout, opts.Stability, func() int64 {
		return dec.DecodedCount()
	}); derr != nil {
		res.Errors = append(res.Errors, derr.Error())
	}
	res.TailFlushMS = RoundMS(MS(time.Since(drainStart)))
	res.TailFramesFlushed = dec.DecodedCount() - mid
	res.FramesDropped = dec.DroppedCount() - dropsBefore
	res.DecodeToIngestRatio = ratio(res.DecodeFPS, res.IngestFPS)
	res.SteadyStateAchieved = res.DecodeToIngestRatio >= steadyStateFloor
	res.ElapsedMS = RoundMS(MS(time.Since(start)))
	if total := feedElapsed + time.Since(drainStart); total > 0 {
		res.DecodeFPSEndToEnd = FPS(res.FramesDecoded+res.TailFramesFlushed, total)
	}

	_ = dec.Close()
	drained := make(chan struct{})
	go func() { drainWG.Wait(); close(drained) }()
	select {
	case <-drained:
	case <-time.After(30 * time.Second):
		res.Errors = append(res.Errors, "drain loop did not finish within 30s of closing the decoder")
	}

	drainMu.Lock()
	res.DecodeLatency = SummarizeMS(latency)
	res.Collected = Collected{Frames: keep, Width: opts.Clip.Width, Height: opts.Clip.Height}
	drainMu.Unlock()
	return res, nil
}

// runDecodePipeline drives the full production chain against a real RTSP
// session served by internal/rtsptest.Simulator.
func runDecodePipeline(ctx context.Context, opts DecodeOptions) (DecodeResult, error) {
	collector := NewFrameCollector(opts.KeepFrames)
	res := DecodeResult{
		Mode:           opts.Mode,
		Pacing:         opts.Pacing,
		DecoderFake:    opts.DecoderFactory != nil,
		Boundary:       opts.Clip.AccessUnitBoundary,
		PacingFPS:      opts.pacingFPS(),
		FramesFed:      len(opts.AUs),
		MeasuredFrames: opts.measuredCount(),
		PrimingFrames:  opts.PrimingFrames,
		Clip:           opts.Clip,
	}

	sim, err := rtsptest.NewSimulator(rtsptest.Options{SDP: buildSDP(opts.SPS, opts.PPS)})
	if err != nil {
		return res, fmt.Errorf("perf: start RTSP simulator: %w", err)
	}
	defer sim.Close()

	rtspCfg := rtsp.Config{
		StreamRole:     opts.StreamRole,
		PacketTimeout:  opts.RTSPPacketTimeout,
		InitialBackoff: 100 * time.Millisecond,
		MaxBackoff:     time.Second,
		DialTimeout:    5 * time.Second,
		Enabled:        true,
	}
	rtspMgr := rtsp.NewManager(rtspCfg, nil, opts.Logger)

	procCfg := processing.Config{
		Enabled:                true,
		TargetFPS:              opts.SampleFPS,
		OutputWidth:            opts.OutputWidth,
		OutputHeight:           opts.OutputHeight,
		RingBufferSize:         8,
		QueueDepth:             opts.QueueDepth,
		DecodeQueueDepth:       opts.DecodeQueueDepth,
		MaxConcurrentPipelines: opts.MaxConcurrentPipelines,
		FFmpegPath:             opts.FFmpegPath,
		DecodeTimeout:          opts.DecodeTimeout,
	}
	procMgr := processing.NewManager(procCfg, rtspMgr, nil, opts.Logger, collector)

	if err := rtspMgr.Start(ctx); err != nil {
		return res, fmt.Errorf("perf: start rtsp manager: %w", err)
	}
	if err := procMgr.Start(ctx); err != nil {
		return res, fmt.Errorf("perf: start processing manager: %w", err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_ = procMgr.Stop(stopCtx)
		_ = rtspMgr.Stop(stopCtx)
	}()

	rtspMgr.SetTargets([]rtsp.CameraTarget{{
		CandidateKey: opts.CandidateKey,
		Addr:         sim.Addr(),
		RTSPPath:     "/live",
		StreamRole:   opts.StreamRole,
		Codec:        "H264",
		Width:        opts.Clip.Width,
		Height:       opts.Clip.Height,
		FPS:          opts.Clip.NominalSourceFPS,
	}})

	if err := waitFor(ctx, 15*time.Second, 20*time.Millisecond, func() bool {
		_, ok := rtspMgr.DescriptorFor(opts.CandidateKey)
		return ok
	}); err != nil {
		return res, fmt.Errorf("perf: RTSP session never came online against the simulator: %w", err)
	}

	pkt := NewPacketizer(DefaultRTPSSRC, DefaultRTPMTU)

	if opts.DecoderFactory != nil {
		// Test-only decoder substitution, see DecodeOptions.DecoderFactory.
		if err := injectDecoderFactory(ctx, opts, sim, pkt, procMgr); err != nil {
			return res, err
		}
	}

	sent := 0
	send := func(i int) error {
		packets, perr := pkt.PacketizeAU(opts.AUs[i], FrameTimestamp(i, opts.Clip.NominalSourceFPS))
		if perr != nil {
			return perr
		}
		for _, p := range packets {
			if serr := sim.SendPacket(p); serr != nil {
				return fmt.Errorf("simulator send failed at access unit %d: %w", i, serr)
			}
			sent++
		}
		return nil
	}

	if opts.TraceInterval > 0 {
		traceCtx, traceCancel := context.WithCancel(ctx)
		defer traceCancel()
		traceStart := time.Now()
		traceBaseline := collector.Count()
		go func() {
			ticker := time.NewTicker(opts.TraceInterval)
			defer ticker.Stop()
			for {
				select {
				case <-traceCtx.Done():
					return
				case <-ticker.C:
					p := procMgr.Pipeline(opts.CandidateKey)
					if p == nil {
						continue
					}
					st := p.Status()
					opts.Logger.Info("perf trace",
						"t_ms", RoundMS(MS(time.Since(traceStart))),
						"state", st.State,
						"rtp_rx", st.RTPPacketsReceived,
						"au_rx", st.FramesReceived,
						"decoded", st.FramesDecoded,
						"sampled", st.FramesSampled,
						"dropped", st.FramesDropped,
						"queue_depth", st.QueueDepth,
						"decode_latency_ms", RoundMS(st.DecodeLatencyMs),
						"sink_frames", collector.Count()-traceBaseline)
				}
			}
		}()
	}

	// Phase 1: priming, excluded from every rate.
	primeStart := time.Now()
	for i := 0; i < opts.PrimingFrames; i++ {
		if opts.Pacing == PacingRealtime {
			if err := sleepUntil(ctx, primeStart.Add(frameOffset(i, opts.pacingFPS()))); err != nil {
				res.Errors = append(res.Errors, err.Error())
				break
			}
		}
		if err := send(i); err != nil {
			res.Errors = append(res.Errors, err.Error())
			break
		}
	}
	// Priming is a continuous pre-roll, never a pause — see the direct-mode
	// comment above: pausing between priming and measuring delays this
	// decoder's first output instead of advancing past it.
	res.PrimingMS = RoundMS(MS(time.Since(primeStart)))
	if c := collector.Collected(); !c.FirstAt.IsZero() {
		res.TimeToFirstFrameMS = RoundMS(MS(c.FirstAt.Sub(primeStart)))
	}

	// Let the priming frames finish being routed before the baseline is taken.
	// The router hands frames to this sink on its own goroutine, so without
	// this a priming frame can arrive after the baseline and be counted as a
	// measured-window frame — which would make the sink appear to have seen
	// more frames than the pipeline sampled.
	if _, serr := waitUntilStable(ctx, opts.Timeout, routingSettle, func() int64 {
		return collector.Count()
	}); serr != nil {
		res.Errors = append(res.Errors, serr.Error())
	}
	sinkBaseline := collector.Count()
	res.StatusBefore = snapshotPipeline(procMgr, opts.CandidateKey)

	// Phase 2: the measured feed.
	sentBefore := sent
	ingestBase := pipelinePacketsReceived(procMgr, opts.CandidateKey)
	feedStart := time.Now()
	for i := opts.PrimingFrames; i < len(opts.AUs); i++ {
		if opts.Pacing == PacingRealtime {
			if err := sleepUntil(ctx, feedStart.Add(frameOffset(i-opts.PrimingFrames, opts.pacingFPS()))); err != nil {
				res.Errors = append(res.Errors, err.Error())
				break
			}
		}
		if err := send(i); err != nil {
			res.Errors = append(res.Errors, err.Error())
			break
		}
	}
	sendElapsed := time.Since(feedStart)
	// The measured window closes when the PIPELINE has taken in what the harness
	// sent, not when the harness finished writing. Over interleaved TCP a fast
	// sender only fills socket buffers — under max pacing the last write lands
	// milliseconds after the first while the pipeline is still draining the
	// socket — so closing the window on the write would report an empty window
	// and blame the decoder for it.
	if err := waitForIngest(ctx, procMgr, opts, ingestBase, int64(sent-sentBefore)); err != nil {
		res.Errors = append(res.Errors, err.Error())
	}
	feedElapsed := time.Since(feedStart)
	res.StatusMid = snapshotPipeline(procMgr, opts.CandidateKey)
	// Read this sink's count at the same boundary as the pipeline's counters.
	// The router delivers frames on its own goroutine, so a count taken later
	// would include frames sampled after the window closed and could make the
	// sink appear to have seen more than the pipeline produced.
	sinkAtMid := collector.Count()

	res.SendElapsedMS = RoundMS(MS(sendElapsed))
	res.FeedElapsedMS = RoundMS(MS(feedElapsed))
	res.RTPPacketsSent = sent - sentBefore
	if n := opts.measuredCount(); n > 0 {
		res.PacketsPerAU = float64(res.RTPPacketsSent) / float64(n)
	}
	res.HarnessFeedFPS = FPS(int64(opts.measuredCount()), sendElapsed)

	// Phase 3: drain the tail. The decoder still holds buffered access units
	// when the last measured one has been written (see BufferedLagFrames).
	//
	// The drain is detected through the pipeline's own frame counter, which
	// means calling Status() while waiting. That only affects the snapshot's
	// decoded_fps/output_fps deltas — never the harness's rates, which come from
	// absolute counter differences.
	drainStart := time.Now()
	if _, derr := waitUntilStable(ctx, opts.Timeout, opts.Stability, func() int64 {
		p := procMgr.Pipeline(opts.CandidateKey)
		if p == nil {
			return 0
		}
		return p.Status().FramesDecoded
	}); derr != nil {
		res.Errors = append(res.Errors, derr.Error())
	}
	res.TailFlushMS = RoundMS(MS(time.Since(drainStart)))
	res.StatusAfter = snapshotPipeline(procMgr, opts.CandidateKey)
	res.SinkFrames = sinkAtMid - sinkBaseline
	res.Collected = collector.Collected()

	// Close the camera connection now that every counter has been snapshotted.
	// This is what a camera disappearing looks like on the wire, and it also
	// makes shutdown immediate: an RTSP read already blocked on the packet
	// timeout would otherwise keep Supervisor.Stop waiting for its deadline,
	// which has nothing to do with the measurement.
	sim.Close()

	before, mid, after := res.StatusBefore, res.StatusMid, res.StatusAfter
	if before != nil && mid != nil {
		res.RTPPacketsReceived = mid.RTPPacketsReceived - before.RTPPacketsReceived
		res.FramesReceived = mid.FramesReceived - before.FramesReceived
		res.FramesDecoded = mid.FramesDecoded - before.FramesDecoded
		res.FramesSampled = mid.FramesSampled - before.FramesSampled
		res.BufferedLagFrames = res.FramesReceived - res.FramesDecoded
		res.IngestFPS = FPS(res.FramesReceived, feedElapsed)
		res.DecodeFPS = FPS(res.FramesDecoded, feedElapsed)
		res.SampleFPS = FPS(res.FramesSampled, feedElapsed)
		res.PipelineLastDecodeLatencyMS = RoundMS(mid.DecodeLatencyMs)
	}
	if before != nil && after != nil {
		res.FramesDropped = after.FramesDropped - before.FramesDropped
		if mid != nil {
			res.TailFramesFlushed = after.FramesDecoded - mid.FramesDecoded
		}
	}
	if res.RTPPacketsSent > 0 {
		res.TransportDeliveryRatio = float64(res.RTPPacketsReceived) / float64(res.RTPPacketsSent)
	}
	res.SinkFramesDuringDrain = collector.Count() - sinkAtMid
	if res.FramesSampled > res.SinkFrames {
		res.SinkFramesNotObserved = res.FramesSampled - res.SinkFrames
	}
	res.DecodeToIngestRatio = ratio(res.DecodeFPS, res.IngestFPS)
	res.SteadyStateAchieved = res.DecodeToIngestRatio >= steadyStateFloor
	res.ElapsedMS = RoundMS(MS(time.Since(feedStart)))
	if total := feedElapsed + time.Since(drainStart); total > 0 {
		res.DecodeFPSEndToEnd = FPS(res.FramesDecoded+res.TailFramesFlushed, total)
	}
	return res, nil
}

// routingSettle is how long the harness's own sink must stop receiving frames
// before a phase boundary is considered clean.
const routingSettle = 250 * time.Millisecond

// primingSeconds is how much source the default priming pre-roll consumes.
// Four seconds is the measured crossing point of this decoder's raw H.264
// priming at a camera-like frame rate.
const primingSeconds = 4

// steadyStateFloor is how close the decode rate must be to the ingest rate for
// the measured window to count as steady state. The tolerance covers the
// sampler/resize work sharing the same scheduler as the decoder, not a
// decoder that is still catching up.
const steadyStateFloor = 0.85

func ratio(a, b float64) float64 {
	if b <= 0 {
		return 0
	}
	return a / b
}

// waitForIngest blocks until the pipeline has received at least want packets at
// the RTSP layer, so the measured window ends with the pipeline's own work
// rather than with the harness's writes. A timeout is reported, never treated
// as success: the resulting window would be too short to mean anything.
func waitForIngest(ctx context.Context, m *processing.Manager, opts DecodeOptions, base, want int64) error {
	if want <= 0 {
		return nil
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		got := pipelinePacketsReceived(m, opts.CandidateKey) - base
		if got >= want {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("perf: the RTSP session delivered only %d of the %d packets sent in the measured window",
				got, want)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func pipelinePacketsReceived(m *processing.Manager, key string) int64 {
	p := m.Pipeline(key)
	if p == nil {
		return 0
	}
	return p.Status().RTPPacketsReceived
}

// injectDecoderFactory performs the test-only decoder substitution described on
// DecodeOptions.DecoderFactory.
func injectDecoderFactory(
	ctx context.Context,
	opts DecodeOptions,
	sim *rtsptest.Simulator,
	pkt *Packetizer,
	procMgr *processing.Manager,
) error {
	packets, err := pkt.PacketizeAU(opts.AUs[0], FrameTimestamp(0, opts.Clip.NominalSourceFPS))
	if err != nil {
		return err
	}
	for _, p := range packets {
		if err := sim.SendPacket(p); err != nil {
			return err
		}
	}
	if err := waitFor(ctx, 10*time.Second, 10*time.Millisecond, func() bool {
		return procMgr.Pipeline(opts.CandidateKey) != nil
	}); err != nil {
		return fmt.Errorf("perf: pipeline was never created for decoder injection: %w", err)
	}
	procMgr.Pipeline(opts.CandidateKey).SetDecoderFactory(opts.DecoderFactory)
	if err := procMgr.RestartCameraPipeline(ctx, opts.CandidateKey, procMgr.Config()); err != nil {
		return fmt.Errorf("perf: restart pipeline for decoder injection: %w", err)
	}
	return waitFor(ctx, 10*time.Second, 10*time.Millisecond, func() bool {
		return procMgr.Pipeline(opts.CandidateKey) != nil
	})
}

func snapshotPipeline(m *processing.Manager, key string) *processing.PipelineStatus {
	p := m.Pipeline(key)
	if p == nil {
		return nil
	}
	st := p.Status()
	return &st
}

func waitFor(ctx context.Context, timeout, interval time.Duration, cond func() bool) error {
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("perf: condition not met within %s", timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}

// waitUntilStable waits until sample() stops changing for the stability window,
// bounded by timeout. It reports whether it settled.
func waitUntilStable(ctx context.Context, timeout, stability time.Duration, sample func() int64) (bool, error) {
	deadline := time.Now().Add(timeout)
	last := sample()
	lastChange := time.Now()
	tick := stability / 5
	if tick < 20*time.Millisecond {
		tick = 20 * time.Millisecond
	}
	for {
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(tick):
		}
		cur := sample()
		if cur != last {
			last = cur
			lastChange = time.Now()
		}
		if time.Since(lastChange) >= stability {
			return true, nil
		}
		if time.Now().After(deadline) {
			return false, fmt.Errorf("perf: counter did not settle after %s (last value %d)", timeout, cur)
		}
	}
}
