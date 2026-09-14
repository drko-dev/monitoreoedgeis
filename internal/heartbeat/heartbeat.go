// Package heartbeat keeps the Edge's liveness and telemetry flowing to the
// SaaS.
//
// # Responsibilities
//
// One goroutine, owned by the agent's module lifecycle, builds a payload,
// posts it to the SaaS, and schedules the next attempt. Nothing here decides
// whether this Edge is "online": that is the SaaS's call, made from the
// arrival time of these requests. This package only reports what the Edge
// knows about itself.
//
// # Failure policy
//
// A SaaS outage must never take the Edge down. Transient failures (timeout,
// connection refused, 5xx) retry with exponential backoff plus jitter; the
// agent itself stays READY because its local function is unaffected, while
// this module reports DEGRADED through the health surface. A credential
// rejection (401/403) is different in kind: it is a standing administrative
// fact, not a blip, so it drops to a slow poll and marks the whole agent
// DEGRADED — but it never deletes the credential, never generates a new one,
// and never re-enrolls. Recovering from a revocation is an operator action.
package heartbeat

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"sync"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/transport"
)

// Scheduling constants.
const (
	// JitterFraction spreads scheduled sends over +/-10% of the interval so a
	// fleet that boots together does not beat in lockstep.
	JitterFraction = 0.10
	// InitialDelay is the nominal wait before the first heartbeat, jittered
	// over [0, InitialDelay). Short enough that a freshly started Edge shows
	// up promptly, random enough that a fleet restart does not arrive as one
	// spike.
	InitialDelay = 2 * time.Second
	// AuthFailureInterval is the slow poll used after a 401/403. A revoked
	// credential is an administrative state that only an operator clears, so
	// re-checking every few minutes is enough and costs the SaaS nothing.
	AuthFailureInterval = 5 * time.Minute
)

// Module lifecycle states reported through Status.State. These are the
// module's own states and are deliberately distinct from the agent-wide
// health.State values.
const (
	StateIdle         = "idle"
	StateRunning      = "running"
	StateDegraded     = "degraded"
	StateUnauthorized = "unauthorized"
	StateStopped      = "stopped"
)

// Sender posts a single heartbeat. It is satisfied by *transport.Client and
// replaced by a fake in tests.
type Sender interface {
	Heartbeat(ctx context.Context, deviceID, credential string, req transport.HeartbeatRequest) error
}

// Status is the module's externally visible state, surfaced on the agent's
// local /status endpoint.
//
// It carries no credential, no Authorization header, no hash and no token —
// only timing, counters and a sanitized error class. See LastError.
type Status struct {
	State               string    `json:"state"`
	LastSuccessAt       time.Time `json:"last_success_at,omitzero"`
	LastAttemptAt       time.Time `json:"last_attempt_at,omitzero"`
	ConsecutiveFailures int       `json:"consecutive_failures"`
	// LastError is a fixed error *class*, never a raw error string: raw
	// transport errors can echo URLs and response bodies, and this field is
	// served over HTTP.
	LastError string `json:"last_error,omitempty"`
}

// Options configures a Module. Only Sender, DeviceID, Credential and Build
// are required; the rest have production defaults and exist mainly so tests
// can drive the scheduler deterministically.
type Options struct {
	Sender     Sender
	DeviceID   string
	Credential string
	// Build produces the payload for one heartbeat, sampled fresh at send
	// time rather than reused, so uptime and resource figures are current.
	Build func() transport.HeartbeatRequest
	// Interval is the nominal gap between successful heartbeats.
	Interval time.Duration
	Log      *slog.Logger

	// OnStatus, when set, is called on every status change (used to mirror
	// the module state into the agent health reporter).
	OnStatus func(Status)
	// OnUnauthorized, when set, is called once each time a 401/403 is newly
	// observed, so the agent can mark itself DEGRADED.
	OnUnauthorized func()

	// Now, Rand and NewTimer are seams for deterministic tests. Zero values
	// select the real clock, a locally seeded RNG and time.NewTimer.
	Now      func() time.Time
	Rand     func() float64
	NewTimer func(time.Duration) (<-chan time.Time, func() bool)
}

// Module is the agent module that sends heartbeats. It implements the
// agent.Module interface (Name/Start/Stop) without importing the agent
// package, which would be an import cycle.
type Module struct {
	opts Options

	mu     sync.RWMutex
	status Status

	cancel context.CancelFunc
	done   chan struct{}
}

// New builds a Module, filling in defaults for every optional Option.
func New(opts Options) (*Module, error) {
	if opts.Sender == nil {
		return nil, errors.New("heartbeat: sender is required")
	}
	if opts.Build == nil {
		return nil, errors.New("heartbeat: build function is required")
	}
	if opts.DeviceID == "" || opts.Credential == "" {
		return nil, errors.New("heartbeat: device id and credential are required")
	}
	if opts.Interval <= 0 {
		return nil, fmt.Errorf("heartbeat: interval must be positive, got %s", opts.Interval)
	}
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Rand == nil {
		// Package-local source: never touch the global RNG, which other code
		// may depend on being reproducible.
		src := rand.New(rand.NewSource(time.Now().UnixNano()))
		var mu sync.Mutex
		opts.Rand = func() float64 {
			mu.Lock()
			defer mu.Unlock()
			return src.Float64()
		}
	}
	if opts.NewTimer == nil {
		opts.NewTimer = func(d time.Duration) (<-chan time.Time, func() bool) {
			t := time.NewTimer(d)
			return t.C, t.Stop
		}
	}
	return &Module{opts: opts, status: Status{State: StateIdle}}, nil
}

// Name identifies the module in lifecycle logs and in the health snapshot.
func (m *Module) Name() string { return "heartbeat" }

// Start launches the scheduling goroutine and returns immediately. The
// context passed here bounds startup only; the loop runs until Stop.
func (m *Module) Start(_ context.Context) error {
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.done = make(chan struct{})

	m.setStatus(func(s *Status) { s.State = StateRunning })

	go func() {
		defer close(m.done)
		m.loop(ctx)
	}()
	return nil
}

// Stop cancels the loop and waits for it to finish, bounded by ctx.
//
// No final "stopping" heartbeat is sent. It would be best-effort at best —
// a SIGKILL or a power cut skips it anyway — so the SaaS must detect
// departure by heartbeat age regardless, and a farewell request only adds a
// code path that is never the one that matters.
func (m *Module) Stop(ctx context.Context) error {
	if m.cancel == nil {
		return nil
	}
	m.cancel()
	select {
	case <-m.done:
	case <-ctx.Done():
		return fmt.Errorf("heartbeat: shutdown timed out: %w", ctx.Err())
	}
	m.setStatus(func(s *Status) { s.State = StateStopped })
	return nil
}

// Status returns a copy of the current module status.
func (m *Module) Status() Status {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.status
}

func (m *Module) setStatus(mutate func(*Status)) {
	m.mu.Lock()
	mutate(&m.status)
	snapshot := m.status
	m.mu.Unlock()
	if m.opts.OnStatus != nil {
		m.opts.OnStatus(snapshot)
	}
}

// loop is the single scheduling goroutine: wait, send, decide the next wait.
func (m *Module) loop(ctx context.Context) {
	bo := newBackoff(BaseBackoff, MaxBackoff)
	// The very first delay is jittered over the whole initial window rather
	// than +/-10% of it, so a fleet restarting together fans out immediately
	// instead of converging on one second.
	delay := time.Duration(m.opts.Rand() * float64(InitialDelay))
	unauthorizedReported := false

	for {
		if !m.wait(ctx, delay) {
			return
		}

		err := m.send(ctx)
		switch {
		case ctx.Err() != nil:
			// Cancelled mid-flight: a shutdown, not a failure.
			return

		case err == nil:
			bo.reset()
			unauthorizedReported = false
			now := m.opts.Now()
			m.setStatus(func(s *Status) {
				s.State = StateRunning
				s.LastAttemptAt = now
				s.LastSuccessAt = now
				s.ConsecutiveFailures = 0
				s.LastError = ""
			})
			delay = m.jittered(m.opts.Interval)

		case errors.Is(err, transport.ErrUnauthorized):
			// Credential revoked or device disabled. Slow poll, no backoff
			// escalation, and crucially no re-enrollment: the stored
			// credential and identity stay exactly as they are.
			m.recordFailure("unauthorized", StateUnauthorized)
			if !unauthorizedReported {
				unauthorizedReported = true
				m.opts.Log.Error("heartbeat rejected: credential revoked or device disabled; " +
					"not retrying aggressively, not re-enrolling, credential left untouched")
				if m.opts.OnUnauthorized != nil {
					m.opts.OnUnauthorized()
				}
			}
			delay = m.jittered(AuthFailureInterval)

		default:
			class, wait := m.classify(err, bo)
			m.recordFailure(class, StateDegraded)
			m.opts.Log.Warn("heartbeat failed, will retry",
				slog.String("class", class),
				slog.Duration("retry_in", wait),
				slog.Int("consecutive_failures", m.Status().ConsecutiveFailures),
			)
			delay = wait
		}
	}
}

// classify maps a send error to a stable error class and the delay before the
// next attempt.
func (m *Module) classify(err error, bo *backoff) (string, time.Duration) {
	var rl *transport.RateLimitError
	switch {
	case errors.As(err, &rl):
		// Honour the server's own cooldown when it stated one; fall back to
		// our backoff when it did not.
		if rl.RetryAfter > 0 {
			bo.next() // keep the sequence advancing so repeated 429s still escalate
			return "rate_limited", rl.RetryAfter
		}
		return "rate_limited", m.jittered(bo.next())
	case errors.Is(err, transport.ErrTimeout):
		return "timeout", m.jittered(bo.next())
	case errors.Is(err, transport.ErrSaaSUnavailable):
		return "unreachable", m.jittered(bo.next())
	case errors.Is(err, transport.ErrInvalidRequest):
		// The payload does not match the server model. Resending an
		// identical body cannot help, so back off to the full interval
		// rather than hammering with a request that is guaranteed to fail.
		return "rejected_payload", m.jittered(m.opts.Interval)
	default:
		return "server_error", m.jittered(bo.next())
	}
}

func (m *Module) recordFailure(class, state string) {
	now := m.opts.Now()
	m.setStatus(func(s *Status) {
		s.State = state
		s.LastAttemptAt = now
		s.ConsecutiveFailures++
		s.LastError = class
	})
}

// send builds and posts one heartbeat.
func (m *Module) send(ctx context.Context) error {
	return m.opts.Sender.Heartbeat(ctx, m.opts.DeviceID, m.opts.Credential, m.opts.Build())
}

// wait sleeps for d, returning false if the context was cancelled first.
func (m *Module) wait(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		d = time.Millisecond
	}
	c, stop := m.opts.NewTimer(d)
	defer stop()
	select {
	case <-ctx.Done():
		return false
	case <-c:
		return true
	}
}

// jittered spreads d over +/-JitterFraction so concurrent Edges drift apart
// instead of synchronising on a shared tick.
func (m *Module) jittered(d time.Duration) time.Duration {
	if d <= 0 {
		return d
	}
	// Rand() in [0,1) maps to a factor in [1-f, 1+f).
	factor := 1 + JitterFraction*(2*m.opts.Rand()-1)
	out := time.Duration(float64(d) * factor)
	if out <= 0 {
		return d
	}
	return out
}
