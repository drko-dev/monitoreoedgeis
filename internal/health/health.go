// Package health tracks the agent lifecycle state and exposes a snapshot of it.
//
// No HTTP server is started in this milestone: the agent listens on no port.
// The snapshot is the internal, testable contract a future health endpoint
// would serialise.
package health

import (
	"sync"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/config"
	"github.com/drko-dev/monitoreoedgeis/internal/identity"
	"github.com/drko-dev/monitoreoedgeis/internal/platform"
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
	Hostname         string            `json:"hostname"`
	OS               string            `json:"os"`
	Architecture     string            `json:"architecture"`
	ProcessingMode   string            `json:"processing_mode"`
	UptimeSeconds    int64             `json:"uptime_seconds"`
	Uptime           string            `json:"uptime"`
	Modules          map[string]string `json:"modules"`
}

// Reporter holds the mutable health state of the agent, including per-module
// lifecycle state. It is the single safe accessor for runtime state — it is
// safe for concurrent use and nothing about it is a package-level global.
type Reporter struct {
	mu        sync.RWMutex
	state     State
	startedAt time.Time
	modules   map[string]string

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

	return Snapshot{
		Status:           r.state,
		Version:          r.version,
		EdgeID:           r.ident.EdgeID,
		EnrollmentStatus: r.ident.Status.String(),
		Hostname:         r.host.Hostname,
		OS:               r.host.OS,
		Architecture:     r.host.GOARCH,
		ProcessingMode:   r.cfg.ProcessingMode.String(),
		UptimeSeconds:    int64(uptime.Seconds()),
		Uptime:           uptime.Round(time.Second).String(),
		Modules:          modules,
	}
}
