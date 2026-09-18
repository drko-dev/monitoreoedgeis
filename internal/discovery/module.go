package discovery

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"strings"
	"sync"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/transport"
)

const (
	// DefaultPullInterval is the polling interval to check for pending SaaS discovery runs.
	DefaultPullInterval = 15 * time.Second
	// MinPullInterval is the fastest polling interval allowed.
	MinPullInterval = 5 * time.Second
	// MaxPullInterval is the slowest polling interval when backing off.
	MaxPullInterval = 60 * time.Second
	// AuthFailurePullInterval is the slow poll used when credentials are rejected.
	AuthFailurePullInterval = 5 * time.Minute
	// InitialScanDelay is the delay before the first local periodic scan.
	InitialScanDelay = 5 * time.Second
)

// ModuleState constants for discovery.
const (
	StateIdle     = "idle"
	StateScanning = "scanning"
	StateDegraded = "degraded"
	StateStopped  = "stopped"
)

// TransportClient defines the HTTP contract used by the discovery module.
type TransportClient interface {
	ClaimNextDiscoveryRun(ctx context.Context, deviceID, credential string) (*int, error)
	ReportDiscoveryRun(ctx context.Context, deviceID, credential string, runID int, req transport.DiscoveryReportRequest) error
}

// ModuleStatus is the runtime health and metrics snapshot of the discovery module.
type ModuleStatus struct {
	State               string    `json:"state"`
	DeviceCount         int       `json:"device_count"`
	LastScanAt          time.Time `json:"last_scan_at,omitzero"`
	LastSuccessAt       time.Time `json:"last_success_at,omitzero"`
	ConsecutiveFailures int       `json:"consecutive_failures"`
	LastError           string    `json:"last_error,omitempty"`
}

// ModuleOptions configures the discovery module.
type ModuleOptions struct {
	Engine       *Engine
	Client       TransportClient
	DeviceID     string
	Credential   string
	Interval     time.Duration
	PullInterval time.Duration
	Log          *slog.Logger
	OnStatus     func(ModuleStatus)
	Now          func() time.Time
}

// Module implements the agent.Module interface for device discovery.
type Module struct {
	opts   ModuleOptions
	engine *Engine
	client TransportClient
	log    *slog.Logger

	mu     sync.RWMutex
	status ModuleStatus
	scanMu sync.Mutex

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewModule creates a new discovery module.
func NewModule(opts ModuleOptions) (*Module, error) {
	if opts.Engine == nil {
		return nil, errors.New("discovery module: engine is required")
	}
	if opts.Interval <= 0 {
		opts.Interval = 5 * time.Minute
	}
	if opts.PullInterval <= 0 {
		opts.PullInterval = DefaultPullInterval
	}
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}

	return &Module{
		opts:   opts,
		engine: opts.Engine,
		client: opts.Client,
		log:    opts.Log,
		status: ModuleStatus{
			State: StateIdle,
		},
	}, nil
}

// Name identifies the module to the agent.
func (m *Module) Name() string { return "discovery" }

// Engine returns the underlying discovery engine.
func (m *Module) Engine() *Engine { return m.engine }

// Rediscover uses the existing serialized scan lifecycle; it opens no new
// listener and accepts no remote parameters.
func (m *Module) Rediscover(ctx context.Context) error { _, err := m.executeScan(ctx); return err }

// Start starts the background periodic discovery and SaaS polling loops.
func (m *Module) Start(_ context.Context) error {
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel

	m.setStatus(func(s *ModuleStatus) {
		s.State = StateIdle
		s.DeviceCount = m.engine.Inventory().Count()
	})

	// 1. Periodic local rediscovery loop
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		m.periodicScanLoop(ctx)
	}()

	// 2. SaaS pull loop (only if client and credentials are provided)
	if m.client != nil && m.opts.DeviceID != "" && m.opts.Credential != "" {
		m.wg.Add(1)
		go func() {
			defer m.wg.Done()
			m.saasPullLoop(ctx)
		}()
	} else {
		m.log.Info("discovery: SaaS pull loop disabled (offline or unenrolled)")
	}

	return nil
}

// Stop gracefully terminates discovery loops and waits for pending scans.
func (m *Module) Stop(ctx context.Context) error {
	if m.cancel == nil {
		return nil
	}
	m.cancel()

	stopped := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(stopped)
	}()

	select {
	case <-stopped:
	case <-ctx.Done():
		return fmt.Errorf("discovery: shutdown timed out: %w", ctx.Err())
	}

	m.setStatus(func(s *ModuleStatus) {
		s.State = StateStopped
	})
	return nil
}

// Status returns a point-in-time snapshot of the discovery module status.
func (m *Module) Status() ModuleStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.status
}

func (m *Module) setStatus(fn func(*ModuleStatus)) {
	m.mu.Lock()
	fn(&m.status)
	snap := m.status
	m.mu.Unlock()

	if m.opts.OnStatus != nil {
		m.opts.OnStatus(snap)
	}
}

// executeScan wraps Engine.RunScan with a mutex and status updates.
func (m *Module) executeScan(ctx context.Context) (*ScanResult, error) {
	m.scanMu.Lock()
	defer m.scanMu.Unlock()

	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	m.setStatus(func(s *ModuleStatus) {
		s.State = StateScanning
		s.LastScanAt = m.opts.Now().UTC()
	})

	result, err := m.engine.RunScan(ctx)
	if err != nil {
		m.setStatus(func(s *ModuleStatus) {
			s.State = StateDegraded
			s.ConsecutiveFailures++
			s.LastError = "SCAN_ERROR"
		})
		return nil, err
	}

	count := m.engine.Inventory().Count()
	m.setStatus(func(s *ModuleStatus) {
		s.State = StateIdle
		s.DeviceCount = count
		s.LastSuccessAt = m.opts.Now().UTC()
		s.ConsecutiveFailures = 0
		s.LastError = ""
	})

	return result, nil
}

func (m *Module) periodicScanLoop(ctx context.Context) {
	// Initial jittered delay
	jitter := time.Duration(rand.Int63n(int64(InitialScanDelay)))
	select {
	case <-time.After(jitter):
	case <-ctx.Done():
		return
	}

	// Perform initial scan
	if _, err := m.executeScan(ctx); err != nil && !errors.Is(err, context.Canceled) {
		m.log.Warn("discovery: initial scan failed", slog.Any("error", err))
	}

	ticker := time.NewTicker(m.opts.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if _, err := m.executeScan(ctx); err != nil && !errors.Is(err, context.Canceled) {
				m.log.Warn("discovery: periodic scan failed", slog.Any("error", err))
			}
		case <-ctx.Done():
			return
		}
	}
}

func (m *Module) saasPullLoop(ctx context.Context) {
	interval := m.opts.PullInterval
	var backoff time.Duration

	for {
		wait := interval
		if backoff > 0 {
			wait = backoff
		}

		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return
		}

		runID, err := m.client.ClaimNextDiscoveryRun(ctx, m.opts.DeviceID, m.opts.Credential)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			if errors.Is(err, transport.ErrUnauthorized) {
				m.log.Warn("discovery: SaaS rejected credentials, pausing pull loop", slog.Any("error", err))
				backoff = AuthFailurePullInterval
				continue
			}
			var rlErr *transport.RateLimitError
			if errors.As(err, &rlErr) && rlErr.RetryAfter > 0 {
				m.log.Warn("discovery: rate limited by SaaS", slog.Duration("retry_after", rlErr.RetryAfter))
				backoff = rlErr.RetryAfter
				continue
			}

			// Transient error: exponential backoff
			if backoff == 0 {
				backoff = MinPullInterval
			} else {
				backoff *= 2
				if backoff > MaxPullInterval {
					backoff = MaxPullInterval
				}
			}
			m.log.Debug("discovery: claim run failed, backing off", slog.Any("error", err), slog.Duration("backoff", backoff))
			continue
		}

		// Reset backoff on successful contact
		backoff = 0

		if runID == nil {
			// No run pending
			continue
		}

		m.log.Info("discovery: claimed SaaS run, executing scan", slog.Int("run_id", *runID))
		scanResult, scanErr := m.executeScan(ctx)
		if scanErr != nil {
			m.log.Error("discovery: scan for claimed run failed", slog.Int("run_id", *runID), slog.Any("error", scanErr))
			reportReq := transport.DiscoveryReportRequest{
				Status:       "failed",
				ErrorCode:    "SCAN_ERROR",
				ErrorMessage: SanitizeText(scanErr.Error(), 512),
			}
			if repErr := m.client.ReportDiscoveryRun(ctx, m.opts.DeviceID, m.opts.Credential, *runID, reportReq); repErr != nil {
				m.log.Error("discovery: failed to report failed run to SaaS", slog.Int("run_id", *runID), slog.Any("error", repErr))
			}
			continue
		}

		// Prepare candidates payload
		candidates := make([]transport.DiscoveryCandidatePayload, 0, len(scanResult.DevicesFound))
		for _, dev := range scanResult.DevicesFound {
			candidates = append(candidates, CandidatePayloadFromDevice(dev))
		}

		reportReq := transport.DiscoveryReportRequest{
			Status:     "completed",
			Candidates: candidates,
		}
		if err := m.client.ReportDiscoveryRun(ctx, m.opts.DeviceID, m.opts.Credential, *runID, reportReq); err != nil {
			m.log.Error("discovery: failed to report completed run to SaaS", slog.Int("run_id", *runID), slog.Any("error", err))
		} else {
			m.log.Info("discovery: successfully reported run to SaaS",
				slog.Int("run_id", *runID),
				slog.Int("candidates_count", len(candidates)),
			)
		}
	}
}

// CandidatePayloadFromDevice converts a DiscoveredDevice to transport.DiscoveryCandidatePayload.
func CandidatePayloadFromDevice(d DiscoveredDevice) transport.DiscoveryCandidatePayload {
	scopesStr := strings.Join(d.Scopes, " ")
	if len(scopesStr) > MaxScopesLength {
		scopesStr = scopesStr[:MaxScopesLength]
	}

	devType := string(d.DeviceType)
	if devType == string(DeviceTypeUnknown) {
		devType = ""
	}

	return transport.DiscoveryCandidatePayload{
		Protocol:     "onvif",
		EndpointHost: d.IP,
		EndpointPort: d.Port,
		EndpointPath: d.Path,
		EPRAddress:   d.EPRAddress,
		Types:        d.Types,
		Scopes:       scopesStr,
		Manufacturer: d.Manufacturer,
		Model:        d.Model,
		Serial:       d.Serial,
		Firmware:     d.Firmware,
		DeviceType:   devType,
		AuthRequired: d.AuthRequired,
	}
}
