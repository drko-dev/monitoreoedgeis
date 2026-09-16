package processing

import (
	"context"
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

	// decoderFactory defaults to a real FFmpegDecoder; tests override it
	// with a fake to exercise the pipeline without spawning ffmpeg — this
	// is what makes pipeline_test.go's H11 acceptance test ffmpeg-free.
	decoderFactory func() (VideoDecoder, error)

	wg     sync.WaitGroup
	doneCh chan struct{}

	mu                sync.Mutex
	state             string
	decoder           VideoDecoder
	lastFrameAt       *time.Time
	lastDecodeLatency time.Duration

	// statusMu-guarded rate-computation state: the delta between two
	// Status() calls, not a true instantaneous rate.
	statusMu         sync.Mutex
	lastStatusAt     time.Time
	lastDecodedCount int64
	lastSampledCount int64

	framesReceived   atomic.Int64
	framesSampled    atomic.Int64
	framesDropped    atomic.Int64 // packet-queue + AU-queue drops
	decoderRestarts  atomic.Int64
	depackIncomplete atomic.Int64
	depackErrors     atomic.Int64
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
		sampler:      NewSampler(cfg.TargetFPS),
		ring:         NewRingBuffer(cfg.RingBufferSize),
		doneCh:       make(chan struct{}),
		state:        "starting",
	}
	p.decoderFactory = func() (VideoDecoder, error) {
		return NewFFmpegDecoder(
			FFmpegDecoderConfig{BinaryPath: cfg.FFmpegPath},
			desc.Width, desc.Height,
			desc.SpropParameterSets,
			logger,
		)
	}
	return p
}

// Start launches the pipeline's goroutines in the background.
func (p *cameraPipeline) Start(ctx context.Context) { go p.run(ctx) }

// Wait blocks until the pipeline has fully stopped (all goroutines exited).
func (p *cameraPipeline) Wait() { <-p.doneCh }

// OnPacket enqueues one video RTP payload for depacketization. Never
// blocks: a full queue drops the packet and counts it.
func (p *cameraPipeline) OnPacket(payload []byte, recvAt time.Time) {
	p.framesReceived.Add(1)
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
		dec, err := p.decoderFactory()
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
		p.setDecoder(dec)
		p.setState("running")

		subCtx, subCancel := context.WithCancel(ctx)
		var subWG sync.WaitGroup
		subWG.Add(2)
		go func() { defer subWG.Done(); p.feedLoop(subCtx, dec) }()
		go func() { defer subWG.Done(); p.readLoop(subCtx, dec) }()

		select {
		case <-ctx.Done():
			subCancel()
			subWG.Wait()
			_ = dec.Close()
			p.wg.Wait()
			return
		case <-dec.Done():
			subCancel()
			subWG.Wait()
			_ = dec.Close()
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
			if au == nil {
				continue
			}
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
			resized := Resize(frame, p.cfg.OutputWidth, p.cfg.OutputHeight)

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
			}
			p.ring.Push(f)
			if p.router != nil {
				p.router.Dispatch(f)
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

func (p *cameraPipeline) setDecoder(d VideoDecoder) {
	p.mu.Lock()
	p.decoder = d
	p.mu.Unlock()
}

// Status returns a point-in-time snapshot for /status. It never includes
// frame bytes or per-frame history. DecodedFPS/OutputFPS are the delta
// between this call and the previous one, not a true instantaneous rate.
func (p *cameraPipeline) Status() PipelineStatus {
	p.mu.Lock()
	state := p.state
	dec := p.decoder
	lastFrameAt := p.lastFrameAt
	latencyMs := float64(p.lastDecodeLatency) / float64(time.Millisecond)
	p.mu.Unlock()

	var decoded, decoderDropped int64
	if dec != nil {
		decoded = dec.DecodedCount()
		decoderDropped = dec.DroppedCount()
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

	return PipelineStatus{
		CandidateKey:   p.candidateKey,
		State:          state,
		Codec:          p.descriptor.Codec,
		InputFPS:       p.descriptor.FPS,
		DecodedFPS:     decodedFPS,
		OutputFPS:      outputFPS,
		FramesReceived: p.framesReceived.Load(),
		FramesDecoded:  decoded,
		FramesSampled:  sampled,
		FramesDropped: p.framesDropped.Load() + decoderDropped +
			p.depackIncomplete.Load() + p.depackErrors.Load(),
		QueueDepth:      len(p.packetCh) + len(p.auCh),
		BufferUsage:     bufUsage,
		DecodeLatencyMs: latencyMs,
		LastFrameAt:     lastFrameAt,
	}
}
