// Package health tracks the agent lifecycle state and exposes a snapshot of
// it over the local HTTP surface (/healthz, /readyz, /status).
//
// Local health is deliberately independent of SaaS reachability: the agent
// stays READY, and /readyz keeps returning 200, while the SaaS is
// unreachable, because the Edge's local function is unaffected by an outage
// in a service it only reports to. The heartbeat module's own DEGRADED state
// is visible in Snapshot.Heartbeat instead. A rejected credential is the one
// SaaS-side condition that does mark the whole agent DEGRADED, because an
// Edge the SaaS refuses to recognise is genuinely not doing its job.
package health

import (
	"sync"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/config"
	"github.com/drko-dev/monitoreoedgeis/internal/discovery"
	"github.com/drko-dev/monitoreoedgeis/internal/heartbeat"
	"github.com/drko-dev/monitoreoedgeis/internal/identity"
	"github.com/drko-dev/monitoreoedgeis/internal/platform"
	"github.com/drko-dev/monitoreoedgeis/internal/processing"
	"github.com/drko-dev/monitoreoedgeis/internal/rtsp"
)

// State is the lifecycle state of the agent.
type State string

const (
	StateStarting State = "STARTING"
	StateReady    State = "READY"
	StateDegraded State = "DEGRADED"
	StateStopping State = "STOPPING"
)

func (s State) String() string { return string(s) }

// Snapshot is a point-in-time view of agent health and runtime info.
type Snapshot struct {
	Status           State             `json:"status"`
	Version          string            `json:"version"`
	EdgeID           string            `json:"edge_id"`
	EnrollmentStatus string            `json:"enrollment_status"`
	CredentialStatus string            `json:"credential_status"`
	Hostname         string            `json:"hostname"`
	OS               string            `json:"os"`
	Architecture     string            `json:"architecture"`
	ProcessingMode   string            `json:"processing_mode"`
	UptimeSeconds    int64             `json:"uptime_seconds"`
	Uptime           string            `json:"uptime"`
	Modules          map[string]string `json:"modules"`
	// Heartbeat is the SaaS-heartbeat module's own state. It is omitted when
	// the module is not running (unenrolled Edge, or no SaaS URL set). It
	// carries timings, counters and an error class only — never the
	// credential, never an Authorization header, never a hash.
	Heartbeat *heartbeat.Status         `json:"heartbeat,omitempty"`
	Discovery *discovery.ModuleStatus   `json:"discovery,omitempty"`
	Cameras   []rtsp.CameraStreamStatus `json:"cameras,omitempty"`
	// VideoPipeline is the Hito H video pipeline's small per-camera summary
	// (camera_count + one PipelineStatus per camera). Omitted when the
	// video pipeline is disabled. Never carries frame bytes.
	VideoPipeline *processing.VideoPipelineSummary `json:"video_pipeline,omitempty"`
}

// Reporter holds the mutable health state of the agent, including per-module
// lifecycle state. It is the single safe accessor for runtime state — it is
// safe for concurrent use and nothing about it is a package-level global.
type Reporter struct {
	mu               sync.RWMutex
	state            State
	startedAt        time.Time
	modules          map[string]string
	credentialStatus string
	heartbeat        *heartbeat.Status
	discovery        *discovery.ModuleStatus
	cameras          []rtsp.CameraStreamStatus
	videoPipeline    *processing.VideoPipelineSummary

	version string
	cfg     *config.Config
	ident   identity.Identity
	host    platform.Info
}

// New creates a Reporter in the STARTING state.
func New(version string, cfg *config.Config, ident identity.Identity, host platform.Info) *Reporter {
	return &Reporter{
		state:     StateStarting,
		startedAt: time.Now(),
		modules:   make(map[string]string),
		version:   version,
		cfg:       cfg,
		ident:     ident,
		host:      host,
	}
}

// Set transitions the reporter to a new state.
func (r *Reporter) Set(s State) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.state = s
}

// SetModuleState records the lifecycle state of a named module (e.g.
// "starting", "running", "stopped", "failed").
func (r *Reporter) SetModuleState(name, state string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.modules[name] = state
}

// SetCredentialStatus records the local SaaS credential status (see
// internal/credentials.Status) for reporting in Snapshot. It is never the
// credential secret itself — only the status string.
func (r *Reporter) SetCredentialStatus(status string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.credentialStatus = status
}

// SetHeartbeatStatus records the SaaS-heartbeat module's latest status for
// reporting in Snapshot. The module calls this on every status change.
func (r *Reporter) SetHeartbeatStatus(s heartbeat.Status) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.heartbeat = &s
}

// SetDiscoveryStatus records the current status of the discovery module.
func (r *Reporter) SetDiscoveryStatus(status discovery.ModuleStatus) {
	r.mu.Lock()
	defer r.mu.Unlock()
	copied := status
	r.discovery = &copied
}

// SetCameras records the current snapshot of monitored camera streams.
func (r *Reporter) SetCameras(cameras []rtsp.CameraStreamStatus) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if cameras == nil {
		r.cameras = nil
		return
	}
	r.cameras = make([]rtsp.CameraStreamStatus, len(cameras))
	copy(r.cameras, cameras)
}

// State returns the current state.
func (r *Reporter) State() State {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.state
}

// Uptime returns how long the agent has been running.
func (r *Reporter) Uptime() time.Duration {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return time.Since(r.startedAt)
}

// Snapshot returns the current health and runtime information.
func (r *Reporter) Snapshot() Snapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()

	uptime := time.Since(r.startedAt)
	modules := make(map[string]string, len(r.modules))
	for name, state := range r.modules {
		modules[name] = state
	}
	// Copy rather than alias: the caller must not be able to mutate reporter
	// state through the returned snapshot.
	var hb *heartbeat.Status
	if r.heartbeat != nil {
		copied := *r.heartbeat
		hb = &copied
	}
	var disc *discovery.ModuleStatus
	if r.discovery != nil {
		copied := *r.discovery
		disc = &copied
	}
	var cams []rtsp.CameraStreamStatus
	if len(r.cameras) > 0 {
		cams = make([]rtsp.CameraStreamStatus, len(r.cameras))
		copy(cams, r.cameras)
	}
	var vp *processing.VideoPipelineSummary
	if r.videoPipeline != nil {
		copied := *r.videoPipeline
		vp = &copied
	}

	return Snapshot{
		Status:           r.state,
		Version:          r.version,
		EdgeID:           r.ident.EdgeID,
		EnrollmentStatus: r.ident.Status.String(),
		CredentialStatus: r.credentialStatus,
		Hostname:         r.host.Hostname,
		OS:               r.host.OS,
		Architecture:     r.host.GOARCH,
		ProcessingMode:   r.cfg.ProcessingMode.String(),
		UptimeSeconds:    int64(uptime.Seconds()),
		Uptime:           uptime.Round(time.Second).String(),
		Modules:          modules,
		Heartbeat:        hb,
		Discovery:        disc,
		Cameras:          cams,
		VideoPipeline:    vp,
	}
}

// SetVideoPipeline records the latest video pipeline status summary
// (implements processing.HealthSink).
func (r *Reporter) SetVideoPipeline(summary processing.VideoPipelineSummary) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.videoPipeline = &summary
}
