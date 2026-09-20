package processing

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/rtsp"
)

type packetItem struct {
	payload []byte
	recvAt  time.Time
}

// cameraPipeline owns the full depacketize -> decode -> sample -> resize ->
// ring buffer -> route chain for exactly one camera. Every hop between
// stages is a fixed-capacity channel or ring buffer — see the bounded
// queues below — so a slow or crashed decoder never blocks packet ingest
// from internal/rtsp (H9).
type cameraPipeline struct {
	candidateKey string
	descriptor   rtsp.StreamDescriptor
	cfg          Config
	router       *Router
	logger       *slog.Logger

	packetCh chan packetItem   // OnPacket -> depacketizeLoop, drop-new when full
	auCh     chan AccessUnit   // depacketizeLoop -> feedLoop, drop-new when full
	depack   *H264Depacketizer // owned solely by depacketizeLoop
	sampler  *Sampler          // owned solely by readLoop
	ring     *RingBuffer

	// motion is nil unless cfg.Hybrid.Enabled; owned solely by readLoop,
	// same as sampler (Milestone J).
	motion *MotionDetector

	// decoderFactory defaults to a real FFmpegDecoder; tests override it
	// with a fake to exercise the pipeline without spawning ffmpeg — this
	// is what makes pipeline_test.go's H11 acceptance test ffmpeg-free.
	decoderFactory func() (VideoDecoder, error)

	wg     sync.WaitGroup
	doneCh chan struct{}
	cancel context.CancelFunc

	mu                sync.Mutex
	state             string
	decoder           VideoDecoder
	lastFrameAt       *time.Time // reset to nil at the start of each decoder generation
	lastDecodeLatency time.Duration
	lastAUPushedAt    time.Time // NOT reset per generation: reflects upstream (camera) flow
	decoderStartedAt  time.Time // reset at the start of each decoder generation

	// decodedBase/droppedBase accumulate the final counts of every PRIOR
	// decoder instance, so FramesDecoded/FramesDropped in Status() are
	// cumulative across restarts and never reset to zero (and therefore
	// DecodedFPS/OutputFPS, a delta between two Status() calls, never goes
	// negative right after a restart). See foldDecoderCounts.
	decodedBase int64
	droppedBase int64

	// statusMu-guarded rate-computation state: the delta between two
	// Status() calls, not a true instantaneous rate.
	statusMu         sync.Mutex
	lastStatusAt     time.Time
	lastDecodedCount int64
	lastSampledCount int64

	rtpPacketsReceived atomic.Int64 // raw RTP packets (OnPacket calls)
	framesReceived     atomic.Int64 // completed access units (depacketizer output)
	framesSampled      atomic.Int64
	framesDropped      atomic.Int64 // packet-queue + AU-queue drops
	decoderRestarts    atomic.Int64
	depackIncomplete   atomic.Int64
	depackErrors       atomic.Int64
	depackUnsupported  atomic.Int64
	depackOversized    atomic.Int64
	ringDropped        atomic.Int64

	// Milestone J hybrid-filter counters. Separate from framesDropped
	// (errors/full queues): a frame the hybrid evaluator filters out is a
	// deliberate policy decision, never an error.
	hybridFramesEvaluated atomic.Int64
	hybridCandidates      atomic.Int64
	hybridFiltered        atomic.Int64

	// hybridMu guards the small bit of hybrid state Status() reads from a
	// different goroutine than readLoop (which owns motion/sampler
	// themselves and must never be touched concurrently).
	hybridMu        sync.Mutex
	hybridState     string // "idle" | "active", only meaningful when motion != nil
	lastMotionScore float64
}

func newCameraPipeline(candidateKey string, desc rtsp.StreamDescriptor, cfg Config, router *Router, logger *slog.Logger) *cameraPipeline {
	if logger == nil {
		logger = slog.Default()
	}
	p := &cameraPipeline{
		candidateKey: candidateKey,
		descriptor:   desc,
		cfg:          cfg,
		router:       router,
		logger:       logger,
		packetCh:     make(chan packetItem, cfg.QueueDepth),
		auCh:         make(chan AccessUnit, cfg.QueueDepth),
		depack:       NewH264Depacketizer(),
		sampler:      NewAdaptiveSampler(cfg.TargetFPS, cfg.Hybrid.IdleFPS, cfg.Hybrid.IdleAfter),
		ring:         NewRingBuffer(cfg.RingBufferSize),
		doneCh:       make(chan struct{}),
		state:        "starting",
	}
	if cfg.Hybrid.Enabled {
		p.motion = NewMotionDetector(cfg.Hybrid)
		p.hybridState = "idle"
	}
	p.decoderFactory = func() (VideoDecoder, error) {
		return NewFFmpegDecoder(
			FFmpegDecoderConfig{BinaryPath: cfg.FFmpegPath, QueueDepth: cfg.DecodeQueueDepth},
			desc.Width, desc.Height,
			desc.SpropParameterSets,
			logger,
		)
	}
	return p
}

// Start launches the pipeline's goroutines in the background.
func (p *cameraPipeline) Start(ctx context.Context) {
	p.mu.Lock()
	pCtx, cancel := context.WithCancel(ctx)
	p.cancel = cancel
	p.mu.Unlock()
	go p.run(pCtx)
}

// Stop terminates the pipeline cleanly and waits for goroutines to exit.
func (p *cameraPipeline) Stop(ctx context.Context) error {
	p.mu.Lock()
	if p.cancel != nil {
		p.cancel()
	}
	p.mu.Unlock()
	return p.WaitContext(ctx)
}

// SetTargetFPS dynamically updates the pipeline's sampling target rate.
func (p *cameraPipeline) SetTargetFPS(fps float64) {
	p.mu.Lock()
	p.cfg.TargetFPS = fps
	p.mu.Unlock()
	if p.sampler != nil {
		p.sampler.SetTargetFPS(fps)
	}
}

// Sampler returns the pipeline's frame sampler.
func (p *cameraPipeline) Sampler() *Sampler {
	return p.sampler
}

// MotionDetector returns the hybrid motion evaluator (nil if not in hybrid mode).
func (p *cameraPipeline) MotionDetector() *MotionDetector {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.motion
}

// Config returns a copy of the pipeline configuration.
func (p *cameraPipeline) Config() Config {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cfg
}

// SetRouter updates the destination router for sampled frames.
func (p *cameraPipeline) SetRouter(r *Router) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.router = r
}

// Router returns the current destination router.
func (p *cameraPipeline) Router() *Router {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.router
}

// State returns the current operational state string.
func (p *cameraPipeline) State() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.state
}

// SetDecoderFactory overrides the decoder constructor (used in tests and transitions).
func (p *cameraPipeline) SetDecoderFactory(f func() (VideoDecoder, error)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.decoderFactory = f
}

// currentDecoderFactory returns the decoder constructor the next decoder
// generation will use, read under the same lock SetDecoderFactory writes it
// with.
//
// The guard has to be two-sided: SetDecoderFactory may be called on a live
// pipeline (a harness substituting the ffmpeg subprocess, a transition that
// swaps the decoder for a camera that changed profile), and run() reads this
// field at the top of every generation. Reading it unlocked while a writer
// holds the mutex is a data race, not a benign one — `go test -race` reports it
// as a write in SetDecoderFactory racing a read in run().
func (p *cameraPipeline) currentDecoderFactory() func() (VideoDecoder, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.decoderFactory
}

// Wait blocks until the pipeline has fully stopped (all goroutines exited).
// Unbounded — callers on a shutdown path that must respect a deadline
// should use WaitContext instead.
func (p *cameraPipeline) Wait() { <-p.doneCh }

// WaitContext blocks until the pipeline has fully stopped or ctx is done,
// whichever comes first, returning ctx.Err() in the latter case. Used by
// Manager.Stop() so a stuck pipeline can never make shutdown unbounded.
func (p *cameraPipeline) WaitContext(ctx context.Context) error {
	select {
	case <-p.doneCh:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// OnPacket enqueues one video RTP payload for depacketization. Never
// blocks: a full queue drops the packet and counts it. This counts raw RTP
// packets (RTPPacketsReceived) — FramesReceived is incremented separately,
// in depacketizeLoop, only for completed access units.
func (p *cameraPipeline) OnPacket(payload []byte, recvAt time.Time) {
	p.rtpPacketsReceived.Add(1)
	select {
	case p.packetCh <- packetItem{payload: payload, recvAt: recvAt}:
	default:
		p.framesDropped.Add(1)
	}
}

func (p *cameraPipeline) run(ctx context.Context) {
	defer close(p.doneCh)

	p.wg.Add(1)
	go p.depacketizeLoop(ctx)

	backoff := 500 * time.Millisecond
	const maxBackoff = 10 * time.Second

	for {
		select {
		case <-ctx.Done():
			p.wg.Wait()
			return
		default:
		}

		p.setState("starting")
		newDecoder := p.currentDecoderFactory()
		dec, err := newDecoder()
		if err != nil {
			p.logger.Warn("video decoder start failed, backing off",
				"candidate_key", p.candidateKey, "error", truncateErr(err), "backoff", backoff)
			p.decoderRestarts.Add(1)
			p.setState("error")
			if !p.sleepBackoff(ctx, backoff) {
				p.wg.Wait()
				return
			}
			backoff = min(backoff*2, maxBackoff)
			continue
		}
		backoff = 500 * time.Millisecond
		p.mu.Lock()
		p.decoder = dec
		p.decoderStartedAt = time.Now()
		p.lastFrameAt = nil
		p.mu.Unlock()
		p.setState("running")

		subCtx, subCancel := context.WithCancel(ctx)
		var subWG sync.WaitGroup
		subWG.Add(3)
		go func() { defer subWG.Done(); p.feedLoop(subCtx, dec) }()
		go func() { defer subWG.Done(); p.readLoop(subCtx, dec) }()
		go func() { defer subWG.Done(); p.watchdogLoop(subCtx, dec) }()

		select {
		case <-ctx.Done():
			// dec.Close() MUST run before subWG.Wait(): feedLoop can be
			// blocked inside dec.Push()'s synchronous stdin Write if
			// ffmpeg is alive but has stopped consuming input, and
			// cancelling subCtx cannot interrupt a syscall already in
			// flight. Closing dec first closes that pipe (os.File.Close
			// on Unix unblocks a concurrent blocked Write/Read on the
			// same fd), which is what actually lets feedLoop's Push
			// return and the goroutine exit — waiting first would risk
			// hanging forever. See TestPipeline_ShutdownDoesNotDeadlockOnBlockedPush.
			subCancel()
			_ = dec.Close()
			subWG.Wait()
			p.foldDecoderCounts(dec)
			p.wg.Wait()
			return
		case <-dec.Done():
			subCancel()
			_ = dec.Close()
			subWG.Wait()
			p.foldDecoderCounts(dec)
			p.decoderRestarts.Add(1)
			p.setState("error")
			p.logger.Warn("video decoder exited, restarting", "candidate_key", p.candidateKey)
		}

		if !p.sleepBackoff(ctx, backoff) {
			p.wg.Wait()
			return
		}
		backoff = min(backoff*2, maxBackoff)
	}
}

// foldDecoderCounts accumulates dec's final decoded/dropped counts into the
// pipeline's cumulative base before it is discarded, so Status() never
// reports a count that drops back toward zero across a decoder restart.
func (p *cameraPipeline) foldDecoderCounts(dec VideoDecoder) {
	if dec == nil {
		return
	}
	p.mu.Lock()
	p.decodedBase += dec.DecodedCount()
	p.droppedBase += dec.DroppedCount()
	if p.decoder == dec {
		p.decoder = nil
	}
	p.mu.Unlock()
}

// depacketizeLoop runs for the pipeline's entire lifetime, independent of
// decoder restarts, so packet ingest is never blocked by decoder churn.
func (p *cameraPipeline) depacketizeLoop(ctx context.Context) {
	defer p.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case item := <-p.packetCh:
			hdr, err := ParseRTPHeader(item.payload)
			if err != nil {
				p.framesDropped.Add(1)
				continue
			}
			au, _ := p.depack.Push(hdr, hdr.Payload(item.payload), item.recvAt)
			// depack is owned solely by this goroutine; publishing its
			// counters here (rather than reading them from Status(), a
			// different goroutine) keeps it race-free without atomics.
			p.depackIncomplete.Store(p.depack.IncompleteAUsDropped)
			p.depackErrors.Store(p.depack.ReassemblyErrors)
			// UnsupportedNALTypes and OversizedAUsDropped were counted by the
			// depacketizer but never republished, so wire data the Edge could
			// not use was dropped with no operator-visible signal at all.
			p.depackUnsupported.Store(p.depack.UnsupportedNALTypes)
			p.depackOversized.Store(p.depack.OversizedAUsDropped)
			if au == nil {
				continue
			}
			p.framesReceived.Add(1) // one completed access unit
			select {
			case p.auCh <- *au:
			default:
				p.framesDropped.Add(1)
			}
		}
	}
}

func (p *cameraPipeline) feedLoop(ctx context.Context, dec VideoDecoder) {
	for {
		select {
		case <-ctx.Done():
			return
		case au := <-p.auCh:
			if err := dec.Push(au); err != nil {
				return // decoder is dead; the outer loop detects it via Done()
			}
			p.mu.Lock()
			p.lastAUPushedAt = time.Now()
			p.mu.Unlock()
		}
	}
}

func (p *cameraPipeline) readLoop(ctx context.Context, dec VideoDecoder) {
	for {
		select {
		case <-ctx.Done():
			return
		case frame, ok := <-dec.Frames():
			if !ok {
				return
			}
			at := frame.DecodedAt
			latency := frame.DecodedAt.Sub(frame.SourceReceivedAt)
			p.mu.Lock()
			p.lastFrameAt = &at
			p.lastDecodeLatency = latency
			p.mu.Unlock()

			if !p.sampler.ShouldEmit(frame.DecodedAt) {
				continue
			}
			p.framesSampled.Add(1)

			srcW, srcH := frame.Width, frame.Height
			p.mu.Lock()
			outW, outH := p.cfg.OutputWidth, p.cfg.OutputHeight
			p.mu.Unlock()
			resized := Resize(frame, outW, outH)

			f := Frame{
				CandidateKey:     p.candidateKey,
				Timestamp:        resized.DecodedAt,
				SourceReceivedAt: resized.SourceReceivedAt,
				Seq:              resized.PipelineSeq,
				SourceWidth:      srcW,
				SourceHeight:     srcH,
				OutputWidth:      resized.Width,
				OutputHeight:     resized.Height,
				Codec:            p.descriptor.Codec,
				StreamRole:       p.descriptor.StreamRole,
				Data:             resized.Data,
				// Hito N: assigned unconditionally for every mode (edge,
				// cloud, hybrid) — this is the one place the canonical
				// correlation id is created; nothing downstream re-derives
				// it (see internal/vision, internal/agent/fulledge_wiring.go).
				CorrelationID: fmt.Sprintf("%s-%d", p.candidateKey, resized.PipelineSeq),
			}

			dispatch := true
			if p.motion != nil {
				// Milestone J: análisis local liviano -> decisión
				// candidato/no candidato, run on the Y (luma) plane of
				// the already-resized frame (cheaper and at a fixed,
				// configured resolution, unlike the source frame which
				// can vary per camera).
				yLen := resized.Width * resized.Height
				y := resized.Data
				if yLen < len(y) {
					y = y[:yLen]
				}
				result := p.motion.Evaluate(y, resized.Width, resized.Height, resized.PipelineSeq, resized.DecodedAt)
				p.hybridFramesEvaluated.Add(1)
				p.sampler.NoteMotion(resized.DecodedAt, result.Candidate)

				state := "active"
				if p.sampler.IsIdle(resized.DecodedAt) {
					state = "idle"
				}
				p.hybridMu.Lock()
				p.hybridState = state
				p.lastMotionScore = result.Score
				p.hybridMu.Unlock()

				// Connect the real local candidate decision to the transport
				// metadata (Milestone J7-J9): every hybrid frame carries its
				// mode and score. CorrelationID is already set unconditionally
				// above (Hito N) — hybrid reuses it, never re-derives it, so a
				// buffered/replayed frame keeps the same id across retries.
				f.ProcessingMode = ProcessingModeHybrid
				f.CandidateScore = result.Score

				switch {
				case result.Unevaluable:
					// Fail-safe: the motion detector could not evaluate this
					// frame safely. Never drop it silently -- treat it as a
					// candidate and send it to Cloud.
					f.CandidateReason = "failsafe_unevaluable"
					p.hybridCandidates.Add(1)
				case result.Candidate:
					f.CandidateReason = "motion_detected"
					p.hybridCandidates.Add(1)
				default:
					f.CandidateReason = "below_threshold"
					p.hybridFiltered.Add(1)
					dispatch = false
				}
			}

			// The ring buffer is a small in-process debug/inspection
			// buffer, never traffic sent anywhere -- Milestone J's
			// candidate filter only governs router.Dispatch below, so it
			// is always pushed regardless of hybrid mode.
			p.ring.Push(f)
			// The ring buffer overwrites its oldest frame when full and has
			// always counted that, but nothing ever read the counter, so
			// history loss for clip/debug snapshots was invisible in /status
			// and in logs. Publishing it here (same goroutine that pushes, so
			// no cross-goroutine read) makes it observable.
			p.ringDropped.Store(p.ring.Dropped())

			p.mu.Lock()
			router := p.router
			p.mu.Unlock()
			if dispatch && router != nil {
				router.Dispatch(f)
			}
		}
	}
}

// watchdogLoop detects a stalled decoder — access units are still arriving
// from the camera (lastAUPushedAt is recent) but no frame has come out for
// cfg.DecodeTimeout — and distinguishes that from the camera simply not
// sending RTP right now (in which case it does nothing: restarting a
// decoder starved of input would accomplish nothing). On a detected stall
// it sets state "stalled" and closes dec; the outer run() loop's existing
// <-dec.Done() branch does the actual restart, backoff, and metric
// increment — this only ever triggers that one existing path, it never
// duplicates it.
func (p *cameraPipeline) watchdogLoop(ctx context.Context, dec VideoDecoder) {
	if p.cfg.DecodeTimeout <= 0 {
		return
	}
	interval := p.cfg.DecodeTimeout / 2
	if interval <= 0 {
		interval = p.cfg.DecodeTimeout
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.mu.Lock()
			lastAU := p.lastAUPushedAt
			lastFrame := p.lastFrameAt
			startedAt := p.decoderStartedAt
			p.mu.Unlock()

			if lastAU.IsZero() {
				continue // this decoder generation has never received an AU yet
			}
			now := time.Now()
			if now.Sub(lastAU) >= p.cfg.DecodeTimeout {
				continue // upstream isn't sending — not a decoder stall
			}

			baseline := startedAt
			if lastFrame != nil && lastFrame.After(baseline) {
				baseline = *lastFrame
			}
			if now.Sub(baseline) >= p.cfg.DecodeTimeout {
				p.logger.Warn("video decoder stalled: access units flowing but no frames produced",
					"candidate_key", p.candidateKey, "since", now.Sub(baseline))
				p.setState("stalled")
				_ = dec.Close()
				return
			}
		}
	}
}

func (p *cameraPipeline) sleepBackoff(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (p *cameraPipeline) setState(s string) {
	p.mu.Lock()
	p.state = s
	p.mu.Unlock()
}

// Status returns a point-in-time snapshot for /status. It never includes
// frame bytes or per-frame history. DecodedFPS/OutputFPS are the delta
// between this call and the previous one, not a true instantaneous rate —
// and, because FramesDecoded is cumulative across decoder restarts (see
// foldDecoderCounts), that delta never goes negative right after one.
func (p *cameraPipeline) Status() PipelineStatus {
	p.mu.Lock()
	state := p.state
	dec := p.decoder
	decodedBase := p.decodedBase
	droppedBase := p.droppedBase
	lastFrameAt := p.lastFrameAt
	latencyMs := float64(p.lastDecodeLatency) / float64(time.Millisecond)
	p.mu.Unlock()

	decoded, decoderDropped := decodedBase, droppedBase
	if dec != nil {
		decoded += dec.DecodedCount()
		decoderDropped += dec.DroppedCount()
	}
	sampled := p.framesSampled.Load()

	now := time.Now()
	var decodedFPS, outputFPS float64
	p.statusMu.Lock()
	if !p.lastStatusAt.IsZero() {
		if elapsed := now.Sub(p.lastStatusAt).Seconds(); elapsed > 0 {
			decodedFPS = float64(decoded-p.lastDecodedCount) / elapsed
			outputFPS = float64(sampled-p.lastSampledCount) / elapsed
		}
	}
	p.lastStatusAt = now
	p.lastDecodedCount = decoded
	p.lastSampledCount = sampled
	p.statusMu.Unlock()

	bufUsage, _ := p.ring.Usage()
	p.ringDropped.Store(p.ring.Dropped())

	var hybridStatus *HybridStatus
	if p.motion != nil {
		p.hybridMu.Lock()
		state := p.hybridState
		lastScore := p.lastMotionScore
		p.hybridMu.Unlock()
		hybridStatus = &HybridStatus{
			Enabled:          true,
			FramesEvaluated:  p.hybridFramesEvaluated.Load(),
			MotionCandidates: p.hybridCandidates.Load(),
			FramesFiltered:   p.hybridFiltered.Load(),
			AdaptiveState:    state,
			IdleFPS:          p.cfg.Hybrid.IdleFPS,
			ActiveFPS:        p.cfg.TargetFPS,
			ROICount:         len(p.cfg.Hybrid.ROIs),
			LastMotionScore:  lastScore,
		}
	}

	return PipelineStatus{
		CandidateKey:       p.candidateKey,
		State:              state,
		Codec:              p.descriptor.Codec,
		InputFPS:           p.descriptor.FPS,
		DecodedFPS:         decodedFPS,
		OutputFPS:          outputFPS,
		RTPPacketsReceived: p.rtpPacketsReceived.Load(),
		FramesReceived:     p.framesReceived.Load(),
		FramesDecoded:      decoded,
		FramesSampled:      sampled,
		FramesDropped: p.framesDropped.Load() + decoderDropped +
			p.depackIncomplete.Load() + p.depackErrors.Load() +
			p.depackUnsupported.Load() + p.depackOversized.Load() +
			p.ringDropped.Load(),
		QueueDepth:          len(p.packetCh) + len(p.auCh),
		BufferUsage:         bufUsage,
		RingBufferDropped:   p.ringDropped.Load(),
		UnsupportedNALTypes: p.depackUnsupported.Load(),
		OversizedAUsDropped: p.depackOversized.Load(),
		DecodeLatencyMs:     latencyMs,
		LastFrameAt:         lastFrameAt,
		Hybrid:              hybridStatus,
	}
}
