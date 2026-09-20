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

	// reconcileMu serializes whole SetTargets reconciliations without holding
	// mu across the blocking supervisor Stop/Start phase. Holding mu there was
	// a lock-cycle hazard: a supervisor's own goroutine can be inside
	// PacketSink.OnPacket, which takes processing.mu and then calls back into
	// DescriptorFor -> mu.
	reconcileMu sync.Mutex
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

// Start launches the manager's coordination loop and starts every supervisor
// that SetTargets registered before Start was called.
//
// The agent builds its camera targets during construction and calls Start
// afterwards, so targets can legitimately exist first. Starting them here (as
// well as in SetTargets) keeps that order working: without it the supervisors
// would never run, and Stop would wait on goroutines that were never
// launched.
//
// Start is idempotent. A second call is a no-op rather than a second
// coordination goroutine; two would both close stopped on exit.
func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	if m.cancel != nil {
		m.mu.Unlock()
		return nil
	}
	m.ctx, m.cancel = context.WithCancel(ctx)
	started := 0
	for key, sup := range m.supervisors {
		sup.Start(m.ctx)
		started++
		m.logger.Info("started camera stream supervisor", "candidate_key", key)
	}
	m.mu.Unlock()

	if started > 0 {
		m.logger.Info("rtsp manager started with existing camera targets", "count", started)
	}
	go m.run()
	return nil
}

// Stop terminates all supervisors and halts the manager.
//
// Stop on a manager that was never started returns immediately instead of
// waiting on a coordination goroutine that does not exist. Calling Stop twice
// is safe.
func (m *Manager) Stop(ctx context.Context) error {
	m.mu.Lock()
	if m.cancel == nil {
		for key, sup := range m.supervisors {
			sup.Stop()
			delete(m.supervisors, key)
		}
		m.mu.Unlock()
		return nil
	}
	m.cancel()
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

// SetDescriptorFor sets the stream descriptor for candidateKey (used in tests and simulation).
func (m *Manager) SetDescriptorFor(candidateKey string, desc StreamDescriptor) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if sup, ok := m.supervisors[candidateKey]; ok {
		sup.SetDescriptor(desc)
	}
}

// supervisorNeedsRestart reports whether an existing supervisor must be
// replaced for target. Only connection-affecting fields count: Addr, RTSPPath
// and the credentials. A change to Codec/Width/Height/FPS/StreamRole alone is
// metadata and does not justify tearing down a working stream.
func supervisorNeedsRestart(existing, target CameraTarget) bool {
	return existing.Addr != target.Addr ||
		existing.RTSPPath != target.RTSPPath ||
		existing.Username != target.Username ||
		existing.Password != target.Password
}

// SetTargets synchronizes the set of active supervisors to match desired
// targets.
//
// Concurrency and locking, deliberately:
//
//   - reconcileMu serializes whole reconciliations, so two concurrent calls
//     cannot interleave their stop/start phases and cannot create two
//     supervisors for one candidate key.
//   - mu is held ONLY to compute the diff and to mutate the supervisor map. It
//     is never held across Supervisor.Stop/Start, which block for as long as a
//     supervisor goroutine takes to unwind.
//
// The second point fixes a real lock cycle. Before, mu was held while calling
// sup.Stop(). That supervisor's goroutine may be inside
// processing.Manager.OnPacket, which holds processing.mu and calls back into
// rtsp.Manager.DescriptorFor — which needs mu. Reconciliation callbacks make
// that window far more reachable, so the blocking work now happens outside mu.
func (m *Manager) SetTargets(targets []CameraTarget) {
	desired := make(map[string]CameraTarget, len(targets))
	for _, t := range targets {
		if t.CandidateKey != "" && t.Addr != "" {
			desired[t.CandidateKey] = t
		}
	}

	m.reconcileMu.Lock()
	defer m.reconcileMu.Unlock()

	m.mu.Lock()
	var toStop []*Supervisor
	for key, sup := range m.supervisors {
		target, want := desired[key]
		if !want || supervisorNeedsRestart(sup.target, target) {
			toStop = append(toStop, sup)
			delete(m.supervisors, key)
		}
	}
	var toStart []*Supervisor
	for key, target := range desired {
		if _, ok := m.supervisors[key]; ok {
			continue
		}
		sup := NewSupervisor(target, m.cfg, m.logger)
		sup.SetPacketSink(m.packetSink)
		m.supervisors[key] = sup
		toStart = append(toStart, sup)
	}
	ctx := m.ctx
	m.mu.Unlock()

	// Blocking phase, outside mu.
	for _, sup := range toStop {
		key := sup.target.CandidateKey
		sup.Stop()
		m.logger.Info("stopped camera stream supervisor", "candidate_key", key)
	}
	if ctx != nil {
		for _, sup := range toStart {
			sup.Start(ctx)
			m.logger.Info("started camera stream supervisor",
				"candidate_key", sup.target.CandidateKey,
				"addr", sup.target.Addr,
				"role", sup.target.StreamRole)
		}
	} else {
		for _, sup := range toStart {
			m.logger.Info("registered camera stream supervisor (manager not started yet)",
				"candidate_key", sup.target.CandidateKey)
		}
	}

	m.publishStatus()
}

// publishStatus takes mu briefly to snapshot the supervisor set.
func (m *Manager) publishStatus() {
	m.mu.Lock()
	defer m.mu.Unlock()
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

// HasCamera reports whether candidateKey is configured in the RTSP manager.
func (m *Manager) HasCamera(candidateKey string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.supervisors[candidateKey]
	return ok
}

// KnownCameras returns the candidate keys of all configured camera supervisors.
func (m *Manager) KnownCameras() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	keys := make([]string, 0, len(m.supervisors))
	for k := range m.supervisors {
		keys = append(keys, k)
	}
	return keys
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
