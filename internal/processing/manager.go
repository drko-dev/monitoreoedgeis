package processing

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/rtsp"
)

// HealthSink accepts periodic video pipeline status updates (e.g.
// *health.Reporter). processing never imports internal/health — see
// docs/ARCHITECTURE.md for why (dependency direction stays one-way, mirrors
// rtsp.StatusSink).
type HealthSink interface {
	SetVideoPipeline(summary VideoPipelineSummary)
}

// Manager implements agent.Module: it fans out video RTP from
// internal/rtsp (as an rtsp.PacketSink) into one cameraPipeline per camera,
// bounded by Config.MaxConcurrentPipelines, and periodically publishes a
// small status summary.
//
// Shutdown ordering (see Stop) is deliberately strict: the packet sink is
// deregistered from rtsp.Manager FIRST, then a stopped flag makes any
// in-flight OnPacket call a no-op immediately, and only then are pipelines
// cancelled and drained. This is what makes it safe to call OnPacket
// concurrently with Stop() without ever sending on a closed channel — see
// manager_test.go's -race lifecycle test.
type Manager struct {
	cfg    Config
	rtsp   *rtsp.Manager
	health HealthSink
	logger *slog.Logger

	ctx     context.Context
	cancel  context.CancelFunc
	stopped atomic.Bool
	doneCh  chan struct{}

	mu          sync.Mutex
	pipelines   map[string]*cameraPipeline
	unsupported map[string]bool
	router      *Router
	extraSinks  []Sink
}

// NewManager creates a video pipeline manager. rtspMgr is Hito G's existing
// RTSP connectivity manager — Manager registers itself as its PacketSink,
// never opening a second connection to any camera.
//
// extraSinks are additional Sink implementations appended after DebugSink
// (e.g. Milestone I's cloudsink.CloudSink) — empty is the common case
// (Milestone H, or Milestone I with the cloud sink disabled/unconfigured).
func NewManager(cfg Config, rtspMgr *rtsp.Manager, health HealthSink, logger *slog.Logger, extraSinks ...Sink) *Manager {
	if logger == nil {
		logger = slog.Default()
	}
	return &Manager{
		cfg:         cfg,
		rtsp:        rtspMgr,
		health:      health,
		logger:      logger,
		pipelines:   make(map[string]*cameraPipeline),
		unsupported: make(map[string]bool),
		doneCh:      make(chan struct{}),
		extraSinks:  extraSinks,
	}
}

// Name implements agent.Module.
func (m *Manager) Name() string { return "video-pipeline" }

// DebugSink exposes the manager's debug sink (nil before Start), primarily
// for diagnostics: e.g. saving one recent frame to disk during real-camera
// validation.
func (m *Manager) DebugSink() *DebugSink {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.router == nil {
		return nil
	}
	for _, s := range m.router.sinks {
		if d, ok := s.(*DebugSink); ok {
			return d
		}
	}
	return nil
}

// Start implements agent.Module.
func (m *Manager) Start(ctx context.Context) error {
	m.ctx, m.cancel = context.WithCancel(ctx)
	sinks := append([]Sink{NewDebugSink()}, m.extraSinks...)
	m.router = NewRouter(sinks, m.cfg.QueueDepth, m.logger)
	m.rtsp.SetPacketSink(m)
	go m.run()
	return nil
}

// Stop implements agent.Module. See the strict ordering documented on
// Manager itself.
func (m *Manager) Stop(ctx context.Context) error {
	m.rtsp.SetPacketSink(nil)
	m.stopped.Store(true)

	m.mu.Lock()
	if m.cancel != nil {
		m.cancel()
	}
	pipelines := make([]*cameraPipeline, 0, len(m.pipelines))
	for _, p := range m.pipelines {
		pipelines = append(pipelines, p)
	}
	m.pipelines = make(map[string]*cameraPipeline)
	router := m.router
	m.mu.Unlock()

	for _, p := range pipelines {
		// Bounded by ctx: a stuck pipeline (e.g. a decoder subprocess
		// refusing to die) must never make the whole agent's shutdown
		// hang indefinitely.
		if err := p.WaitContext(ctx); err != nil {
			m.logger.Warn("video pipeline did not stop within the shutdown deadline",
				"candidate_key", p.candidateKey, "error", err)
		}
	}
	if router != nil {
		router.Stop()
	}

	select {
	case <-m.doneCh:
	case <-ctx.Done():
		return ctx.Err()
	}
	return nil
}

// OnPacket implements rtsp.PacketSink. It is safe to call concurrently with
// Stop(): the stopped flag is checked first, before any pipeline lookup or
// creation, so a call already in flight when Stop() begins either sees
// stopped==true and returns immediately, or completes against a pipeline
// object whose channels are never closed (see cameraPipeline.OnPacket) —
// either way, never a send on a closed channel.
func (m *Manager) OnPacket(candidateKey string, payload []byte, recvAt time.Time) {
	if m.stopped.Load() {
		return
	}

	m.mu.Lock()
	p, ok := m.pipelines[candidateKey]
	if !ok {
		if m.unsupported[candidateKey] {
			m.mu.Unlock()
			return
		}
		if len(m.pipelines) >= m.cfg.MaxConcurrentPipelines {
			m.mu.Unlock()
			return
		}
		desc, resolved := m.rtsp.DescriptorFor(candidateKey)
		if !resolved {
			m.mu.Unlock()
			return
		}
		if desc.Codec != "" && !strings.EqualFold(desc.Codec, "H264") {
			m.unsupported[candidateKey] = true
			m.mu.Unlock()
			m.logger.Warn("unsupported codec for video pipeline, stream will not be decoded",
				"candidate_key", candidateKey, "codec", desc.Codec)
			return
		}

		p = newCameraPipeline(candidateKey, desc, m.cfg, m.router, m.logger.With("candidate_key", candidateKey))
		m.pipelines[candidateKey] = p
		if m.ctx != nil {
			p.Start(m.ctx)
		}
		m.logger.Info("started video pipeline", "candidate_key", candidateKey,
			"codec", desc.Codec, "width", desc.Width, "height", desc.Height)
	}
	m.mu.Unlock()

	p.OnPacket(payload, recvAt)
}

func (m *Manager) run() {
	defer close(m.doneCh)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			m.publishStatus()
		}
	}
}

func (m *Manager) publishStatus() {
	if m.health == nil {
		return
	}
	m.mu.Lock()
	statuses := make([]PipelineStatus, 0, len(m.pipelines))
	for _, p := range m.pipelines {
		statuses = append(statuses, p.Status())
	}
	m.mu.Unlock()

	m.health.SetVideoPipeline(VideoPipelineSummary{
		CameraCount: len(statuses),
		Cameras:     statuses,
	})
}
