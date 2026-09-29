package credentialrotation

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/credentials"
	"github.com/drko-dev/monitoreoedgeis/internal/transport"
)

type Status struct {
	Enabled             bool      `json:"enabled"`
	LastSuccessAt       time.Time `json:"last_success_at,omitzero"`
	NextDueAt           time.Time `json:"next_due_at,omitzero"`
	LastAttemptAt       time.Time `json:"last_attempt_at,omitzero"`
	ConsecutiveFailures int       `json:"consecutive_failures"`
	State               string    `json:"state"`
}

type ModuleOptions struct {
	Service                                                 *Service
	Store                                                   Store
	DataDir                                                 string
	Enabled                                                 bool
	Interval, MinimumAge, JitterWindow, RetryBase, RetryMax time.Duration
	Now                                                     func() time.Time
	Rand                                                    func() float64
	NewTimer                                                func(time.Duration) (<-chan time.Time, func() bool)
	Log                                                     *slog.Logger
	OnStatus                                                func(Status)
}

type Module struct {
	opts   ModuleOptions
	mu     sync.RWMutex
	status Status
	cancel context.CancelFunc
	done   chan struct{}
}

func NewModule(o ModuleOptions) *Module {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Rand == nil {
		src := rand.New(rand.NewPCG(uint64(time.Now().UnixNano()), 17))
		o.Rand = src.Float64
	}
	if o.NewTimer == nil {
		o.NewTimer = func(d time.Duration) (<-chan time.Time, func() bool) { t := time.NewTimer(d); return t.C, t.Stop }
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	state := "disabled"
	if o.Enabled {
		state = "waiting"
	}
	return &Module{opts: o, status: Status{Enabled: o.Enabled, State: state}}
}

func (m *Module) Name() string   { return "credential-rotation" }
func (m *Module) Status() Status { m.mu.RLock(); defer m.mu.RUnlock(); return m.status }
func (m *Module) set(s Status) {
	m.mu.Lock()
	m.status = s
	m.mu.Unlock()
	if m.opts.OnStatus != nil {
		m.opts.OnStatus(s)
	}
}

func (m *Module) Start(parent context.Context) error {
	if !m.opts.Enabled {
		m.set(Status{Enabled: false, State: "disabled"})
		return nil
	}
	if m.opts.Interval <= 0 || m.opts.RetryBase <= 0 || m.opts.RetryMax < m.opts.RetryBase {
		m.set(Status{Enabled: true, State: "degraded"})
		return nil
	}
	ctx, cancel := context.WithCancel(parent)
	m.cancel = cancel
	m.done = make(chan struct{})
	m.set(Status{Enabled: true, State: "waiting"})
	go func() { defer close(m.done); m.loop(ctx) }()
	return nil
}

func (m *Module) Stop(ctx context.Context) error {
	if m.cancel == nil {
		return nil
	}
	m.cancel()
	select {
	case <-m.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *Module) loop(ctx context.Context) {
	failures := 0
	for {
		if ctx.Err() != nil {
			return
		}
		c, err := m.opts.Store.Load(m.opts.DataDir)
		if err != nil {
			m.opts.Log.Error("credential rotation state unavailable", "error", "local persistence failure")
			m.set(Status{Enabled: true, State: "degraded", ConsecutiveFailures: failures})
			failures++
			if !m.wait(ctx, m.backoff(failures)) {
				return
			}
			continue
		}
		if !c.IsEnrolled() {
			m.publishFrom(c, "disabled")
			return
		}
		if m.opts.Service == nil {
			m.publishFrom(c, "degraded")
			return
		}
		if c.NextRotationDueAt.IsZero() {
			due := DueAt(c, m.opts.Interval)
			minimum := c.EnrolledAt.Add(m.opts.MinimumAge)
			if minimum.After(due) {
				due = minimum
			}
			c.NextRotationDueAt = due.Add(Jitter(m.opts.JitterWindow, m.opts.Rand))
			if err := m.opts.Store.Save(m.opts.DataDir, c); err != nil {
				m.opts.Log.Error("credential rotation schedule persistence failed", "error", "local persistence failure")
				failures++
				m.set(Status{Enabled: true, State: "degraded", ConsecutiveFailures: failures})
				if !m.wait(ctx, m.backoff(failures)) {
					return
				}
				continue
			}
		}
		due := c.NextRotationDueAt
		if due.IsZero() {
			due = DueAt(c, m.opts.Interval)
		}
		delay := due.Sub(m.opts.Now())
		if delay > 0 {
			// Recheck for clock jumps and manual-rotation schedule changes.
			delay = min(delay, time.Minute)
			m.publishFrom(c, "waiting")
			if !m.wait(ctx, delay) {
				return
			}
			continue
		}
		attemptAt := m.opts.Now().UTC()
		st := m.Status()
		st.Enabled = true
		st.State = "rotating"
		st.LastAttemptAt = attemptAt
		st.LastSuccessAt = c.LastSuccessfulRotationAt
		st.NextDueAt = due
		st.ConsecutiveFailures = failures
		m.set(st)
		if err := m.opts.Service.Rotate(ctx); err == nil {
			failures = 0
			updated, err := m.opts.Store.Load(m.opts.DataDir)
			if err != nil {
				m.opts.Log.Error("credential rotation state reload failed", "error", "local persistence failure")
				failures++
				m.set(Status{Enabled: true, State: "degraded", LastAttemptAt: attemptAt, ConsecutiveFailures: failures})
				if !m.wait(ctx, m.backoff(failures)) {
					return
				}
				continue
			}
			m.publishFrom(updated, "waiting")
			continue
		} else {
			failures++
			state := "backoff"
			if isUnauthorized(err) {
				state = "unauthorized"
			} else if errors.Is(err, ErrLocalPersistence) || errors.Is(err, ErrPostVerification) || errors.Is(err, transport.ErrConflict) {
				state = "degraded"
			}
			backoff := m.backoff(failures)
			if state == "unauthorized" {
				backoff = m.opts.RetryMax
			}
			latest, loadErr := m.opts.Store.Load(m.opts.DataDir)
			if loadErr != nil {
				state = "degraded"
			} else {
				c = latest
				c.NextRotationDueAt = m.opts.Now().Add(backoff).UTC()
			}
			if loadErr != nil || m.opts.Store.Save(m.opts.DataDir, c) != nil {
				state = "degraded"
				m.opts.Log.Error("credential rotation retry schedule persistence failed", "error", "local persistence failure")
			}
			m.opts.Log.Warn("automatic credential rotation failed", "state", state, "error", err.Error())
			m.set(Status{Enabled: true, LastSuccessAt: c.LastSuccessfulRotationAt, NextDueAt: c.NextRotationDueAt, LastAttemptAt: attemptAt, ConsecutiveFailures: failures, State: state})
		}
	}
}

func (m *Module) publishFrom(c credentials.Credentials, state string) {
	m.set(Status{Enabled: m.opts.Enabled, LastSuccessAt: c.LastSuccessfulRotationAt, NextDueAt: c.NextRotationDueAt, State: state})
}
func (m *Module) wait(ctx context.Context, d time.Duration) bool {
	ch, stop := m.opts.NewTimer(d)
	defer stop()
	select {
	case <-ctx.Done():
		return false
	case <-ch:
		return true
	}
}
func (m *Module) backoff(n int) time.Duration {
	d := m.opts.RetryBase
	for i := 1; i < n && d < m.opts.RetryMax; i++ {
		d *= 2
	}
	if d > m.opts.RetryMax {
		d = m.opts.RetryMax
	}
	d += Jitter(d/2, m.opts.Rand)
	if d > m.opts.RetryMax {
		d = m.opts.RetryMax
	}
	return d
}
func isUnauthorized(err error) bool { return errors.Is(err, transport.ErrUnauthorized) }
