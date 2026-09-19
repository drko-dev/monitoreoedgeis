package processing

import (
	"context"
	"fmt"
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

// FrameHistory is the read-only, bounded video-history surface available to a
// local evidence producer. It deliberately exposes no decoder or RTSP handle.
type FrameHistory interface{ Snapshot() []Frame }

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

// FrameHistory returns the existing decoded-frame ring for candidateKey, or
// nil when no pipeline is active. This permits local clip creation without a
// second RTSP connection or decoder.
func (m *Manager) FrameHistory(candidateKey string) FrameHistory {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p := m.pipelines[candidateKey]; p != nil {
		return p.ring
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

	var routerQueues []RouterQueueStats
	if m.router != nil {
		routerQueues = m.router.QueueStats()
	}

	m.health.SetVideoPipeline(VideoPipelineSummary{
		CameraCount:  len(statuses),
		Cameras:      statuses,
		CloudBuffer:  m.cloudBufferStats(),
		RouterQueues: routerQueues,
	})
}

// cloudBufferStats returns the I6 buffer snapshot from whichever registered
// sink reports one (nil when none does, e.g. buffering disabled).
func (m *Manager) cloudBufferStats() *CloudBufferStats {
	m.mu.Lock()
	router := m.router
	m.mu.Unlock()
	if router == nil {
		return nil
	}
	for _, s := range router.sinks {
		if r, ok := s.(CloudBufferReporter); ok {
			stats := r.CloudBufferStats()
			return &stats
		}
	}
	return nil
}

// RestartCameraPipeline terminates the existing pipeline for candidateKey (if any),
// and creates a new pipeline with newCfg. Safe for concurrent use with OnPacket.
func (m *Manager) RestartCameraPipeline(ctx context.Context, candidateKey string, newCfg Config) error {
	m.mu.Lock()
	oldPipeline, exists := m.pipelines[candidateKey]
	var desc rtsp.StreamDescriptor
	var hasDesc bool
	if m.rtsp != nil {
		desc, hasDesc = m.rtsp.DescriptorFor(candidateKey)
	}
	if !hasDesc && oldPipeline != nil {
		desc = oldPipeline.descriptor
		hasDesc = true
	}
	if !hasDesc && m.rtsp != nil && m.rtsp.HasCamera(candidateKey) {
		desc = rtsp.StreamDescriptor{
			CandidateKey: candidateKey,
			Codec:        "H264",
			Width:        m.cfg.OutputWidth,
			Height:       m.cfg.OutputHeight,
		}
		hasDesc = true
	}
	if !hasDesc {
		m.mu.Unlock()
		return fmt.Errorf("processing: cannot restart pipeline for unknown camera %q", candidateKey)
	}
	router := m.router
	m.mu.Unlock()

	// 1. Controlled stop of the old pipeline
	if exists && oldPipeline != nil {
		if err := oldPipeline.Stop(ctx); err != nil {
			m.logger.Warn("old pipeline stop encountered error", "candidate_key", candidateKey, "error", err)
		}
	}

	// 2. Create new pipeline
	newPipeline := newCameraPipeline(candidateKey, desc, newCfg, router, m.logger.With("candidate_key", candidateKey))
	if exists && oldPipeline != nil {
		// Read through the accessor, never the field: the old pipeline's
		// run() goroutine reads the same field, so an unsynchronized read
		// here would race with a concurrent SetDecoderFactory.
		if factory := oldPipeline.currentDecoderFactory(); factory != nil {
			newPipeline.SetDecoderFactory(factory)
		}
	}

	// 3. Register and start new pipeline
	m.mu.Lock()
	m.pipelines[candidateKey] = newPipeline
	if m.ctx != nil {
		newPipeline.Start(m.ctx)
	}
	m.mu.Unlock()

	return nil
}

// SetTargetFPS updates the frame sampling rate dynamically. If candidateKey is non-empty,
// it updates only that camera's pipeline; otherwise, it updates the default config and
// all active pipelines.
func (m *Manager) SetTargetFPS(candidateKey string, fps float64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if candidateKey != "" {
		p, ok := m.pipelines[candidateKey]
		if !ok {
			return fmt.Errorf("processing: camera pipeline %q not found", candidateKey)
		}
		p.SetTargetFPS(fps)
		return nil
	}
	m.cfg.TargetFPS = fps
	for _, p := range m.pipelines {
		p.SetTargetFPS(fps)
	}
	return nil
}

// SetCameraROI updates the regions of interest for hybrid motion detection on candidateKey.
func (m *Manager) SetCameraROI(candidateKey string, rois []ROI) error {
	m.mu.Lock()
	p, ok := m.pipelines[candidateKey]
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("processing: camera pipeline %q not found", candidateKey)
	}
	if p.MotionDetector() == nil {
		return fmt.Errorf("processing: camera %q is not running in hybrid mode", candidateKey)
	}
	p.MotionDetector().SetROIs(rois)
	return nil
}

// SetCameraResolution restarts the camera pipeline for candidateKey with new OutputWidth and OutputHeight.
func (m *Manager) SetCameraResolution(ctx context.Context, candidateKey string, width, height int) error {
	if err := ValidateOutputDimensions(width, height); err != nil {
		return err
	}
	m.mu.Lock()
	p, ok := m.pipelines[candidateKey]
	cfg := m.cfg
	if ok && p != nil {
		cfg = p.Config()
	}
	m.mu.Unlock()
	cfg.OutputWidth = width
	cfg.OutputHeight = height
	return m.RestartCameraPipeline(ctx, candidateKey, cfg)
}

// UpdateRouterSinks stops the current router (draining in-flight frames) and replaces
// it with a new router routing to NewDebugSink() + sinks.
func (m *Manager) UpdateRouterSinks(sinks ...Sink) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.router != nil {
		m.router.Stop()
	}
	m.extraSinks = sinks
	allSinks := append([]Sink{NewDebugSink()}, sinks...)
	m.router = NewRouter(allSinks, m.cfg.QueueDepth, m.logger)
	for _, p := range m.pipelines {
		p.SetRouter(m.router)
	}
}

// ExtraSinks returns the list of current extra routing sinks.
func (m *Manager) ExtraSinks() []Sink {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Sink, len(m.extraSinks))
	copy(out, m.extraSinks)
	return out
}

// Pipeline returns the cameraPipeline for candidateKey if active.
func (m *Manager) Pipeline(candidateKey string) *cameraPipeline {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.pipelines[candidateKey]
}

// ActivePipelines returns the list of candidate keys with active pipelines.
func (m *Manager) ActivePipelines() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	keys := make([]string, 0, len(m.pipelines))
	for k := range m.pipelines {
		keys = append(keys, k)
	}
	return keys
}

// Config returns the default pipeline manager configuration.
func (m *Manager) Config() Config {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cfg
}

// SetConfig updates the default pipeline manager configuration for newly started pipelines.
func (m *Manager) SetConfig(cfg Config) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cfg = cfg
}
