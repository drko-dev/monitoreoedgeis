package rtsp

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// StatusSink accepts periodic camera status updates (e.g. *health.Reporter).
type StatusSink interface {
	SetCameras(cameras []CameraStreamStatus)
}

// Manager coordinates RTSP stream supervisors across all configured cameras.
type Manager struct {
	cfg    Config
	sink   StatusSink
	logger *slog.Logger

	mu          sync.Mutex
	supervisors map[string]*Supervisor
	packetSink  PacketSink
	ctx         context.Context
	cancel      context.CancelFunc
	stopped     chan struct{}
}

// NewManager creates a manager with the given config and status sink.
func NewManager(cfg Config, sink StatusSink, logger *slog.Logger) *Manager {
	if logger == nil {
		logger = slog.Default()
	}
	return &Manager{
		cfg:         cfg,
		sink:        sink,
		logger:      logger,
		supervisors: make(map[string]*Supervisor),
		stopped:     make(chan struct{}),
	}
}

// Name implements agent.Module.
func (m *Manager) Name() string {
	return "rtsp-manager"
}

// Start launches the manager's coordination loop.
func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	m.ctx, m.cancel = context.WithCancel(ctx)
	m.mu.Unlock()

	go m.run()
	return nil
}

// Stop terminates all supervisors and halts the manager.
func (m *Manager) Stop(ctx context.Context) error {
	m.mu.Lock()
	if m.cancel != nil {
		m.cancel()
	}
	// Stop all supervisors
	for key, sup := range m.supervisors {
		sup.Stop()
		delete(m.supervisors, key)
	}
	m.mu.Unlock()

	select {
	case <-m.stopped:
	case <-ctx.Done():
		return ctx.Err()
	}
	return nil
}

// SetPacketSink registers sink to receive video RTP payloads from every
// camera supervisor, applying it atomically to every supervisor that
// currently exists and to every one created afterward by SetTargets. Passing
// nil deregisters it everywhere. Safe for concurrent use.
func (m *Manager) SetPacketSink(sink PacketSink) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.packetSink = sink
	for _, sup := range m.supervisors {
		sup.SetPacketSink(sink)
	}
}

// DescriptorFor returns the non-sensitive stream metadata for the given
// camera, once its supervisor has completed a connection, and whether it is
// available yet.
func (m *Manager) DescriptorFor(candidateKey string) (StreamDescriptor, bool) {
	m.mu.Lock()
	sup, ok := m.supervisors[candidateKey]
	m.mu.Unlock()
	if !ok {
		return StreamDescriptor{}, false
	}
	return sup.Descriptor()
}

// SetTargets synchronizes the set of active supervisors to match desired targets.
func (m *Manager) SetTargets(targets []CameraTarget) {
	m.mu.Lock()
	defer m.mu.Unlock()

	desired := make(map[string]CameraTarget, len(targets))
	for _, t := range targets {
		if t.CandidateKey != "" && t.Addr != "" {
			desired[t.CandidateKey] = t
		}
	}

	// Remove supervisors not in desired list
	for key, sup := range m.supervisors {
		if _, ok := desired[key]; !ok {
			sup.Stop()
			delete(m.supervisors, key)
			m.logger.Info("stopped camera stream supervisor", "candidate_key", key)
		}
	}

	// Add or update supervisors
	for key, target := range desired {
		existing, ok := m.supervisors[key]
		if !ok {
			sup := NewSupervisor(target, m.cfg, m.logger)
			sup.SetPacketSink(m.packetSink)
			m.supervisors[key] = sup
			if m.ctx != nil {
				sup.Start(m.ctx)
			}
			m.logger.Info("started camera stream supervisor", "candidate_key", key, "addr", target.Addr, "role", target.StreamRole)
		} else if existing.target.Addr != target.Addr || existing.target.RTSPPath != target.RTSPPath ||
			existing.target.Username != target.Username || existing.target.Password != target.Password {
			// Configuration changed, restart supervisor
			existing.Stop()
			sup := NewSupervisor(target, m.cfg, m.logger)
			sup.SetPacketSink(m.packetSink)
			m.supervisors[key] = sup
			if m.ctx != nil {
				sup.Start(m.ctx)
			}
			m.logger.Info("restarted camera stream supervisor with updated config", "candidate_key", key)
		}
	}

	m.publishStatusLocked()
}

// Snapshot returns the current status of all managed camera streams.
func (m *Manager) Snapshot() []CameraStreamStatus {
	m.mu.Lock()
	defer m.mu.Unlock()

	statuses := make([]CameraStreamStatus, 0, len(m.supervisors))
	for _, sup := range m.supervisors {
		statuses = append(statuses, sup.Snapshot())
	}
	return statuses
}

func (m *Manager) run() {
	defer close(m.stopped)

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			m.mu.Lock()
			m.publishStatusLocked()
			m.mu.Unlock()
		}
	}
}

func (m *Manager) publishStatusLocked() {
	if m.sink == nil {
		return
	}
	statuses := make([]CameraStreamStatus, 0, len(m.supervisors))
	for _, sup := range m.supervisors {
		statuses = append(statuses, sup.Snapshot())
	}
	m.sink.SetCameras(statuses)
}
