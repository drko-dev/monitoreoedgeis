// Package control implements the Edge-initiated, allowlisted control poll.
package control

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/transport"
)

const (
	defaultInterval = 15 * time.Second
	maxBackoff      = time.Minute
	authBackoff     = 5 * time.Minute
)

type Client interface {
	ClaimNextControlCommand(context.Context, string, string) (*transport.ControlCommand, error)
	ReportControlCommand(context.Context, string, string, string, string, map[string]any, string) error
}
type Executor interface {
	Status() map[string]any
	Rediscover(context.Context) error
	// ReloadConfig fetches and applies the current SaaS-assigned remote
	// config (Hito O). Triggered by the allowlisted "reload_config"
	// command -- SaaS queues it whenever the desired config changes; the
	// Edge fetches and applies it through this same outbound poll cadence,
	// never a second poller.
	ReloadConfig(context.Context) error
}
type Module struct {
	client               Client
	executor             Executor
	ledger               *Ledger
	deviceID, credential string
	pollInterval         time.Duration
	cancel               context.CancelFunc
	wg                   sync.WaitGroup

	mu       sync.Mutex
	executed map[string]commandExecution
	// executedOrder is the insertion order of executed's keys, used to bound
	// the map. See trackExecutedLocked.
	executedOrder []string
}

// defaultMaxTrackedExecutions bounds Module.executed when no ledger is
// configured. It is deliberately larger than the ledger's own default of 100 so
// the in-memory map never forgets a command the durable ledger still remembers
// — the two together are the idempotency guarantee, and a smaller in-memory
// bound than the durable one would let a re-delivered command run twice.
const defaultMaxTrackedExecutions = 256

type commandExecution struct {
	status    string
	result    map[string]any
	errorCode string
}

// Option configures Module options.
type Option func(*Module)

// WithLedger injects a durable ledger for idempotency across restarts.
func WithLedger(l *Ledger) Option {
	return func(m *Module) {
		m.ledger = l
	}
}

func New(client Client, executor Executor, deviceID, credential string, opts ...Option) *Module {
	m := &Module{
		client:       client,
		executor:     executor,
		deviceID:     deviceID,
		credential:   credential,
		pollInterval: defaultInterval,
		executed:     make(map[string]commandExecution),
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// SetPollInterval sets the interval between polls (useful for testing).
func (m *Module) SetPollInterval(d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if d > 0 {
		m.pollInterval = d
	}
}
func (m *Module) Name() string { return "control" }
func (m *Module) Start(context.Context) error {
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.wg.Add(1)
	go func() { defer m.wg.Done(); m.loop(ctx) }()
	return nil
}
func (m *Module) Stop(ctx context.Context) error {
	if m.cancel == nil {
		return nil
	}
	m.cancel()
	done := make(chan struct{})
	go func() { m.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (m *Module) loop(ctx context.Context) {
	m.mu.Lock()
	interval := m.pollInterval
	m.mu.Unlock()

	wait, backoff := interval, time.Duration(0)
	for {
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return
		}
		cmd, err := m.client.ClaimNextControlCommand(ctx, m.deviceID, m.credential)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			if errors.Is(err, transport.ErrUnauthorized) {
				wait = authBackoff
				continue
			}
			var rl *transport.RateLimitError
			if errors.As(err, &rl) && rl.RetryAfter > 0 {
				wait = rl.RetryAfter
				continue
			}
			if backoff == 0 {
				backoff = interval
			} else {
				backoff *= 2
				if backoff > maxBackoff {
					backoff = maxBackoff
				}
			}
			wait = backoff
			continue
		}
		m.mu.Lock()
		interval = m.pollInterval
		m.mu.Unlock()
		backoff, wait = 0, interval
		if cmd == nil {
			continue
		}
		state, result, code, execErr := m.execute(ctx, cmd)
		if execErr != nil {
			// Hard ledger persistence failure: do NOT report success to SaaS.
			// Degrade and stop polling commands to avoid unsafe execution.
			return
		}
		if err := m.client.ReportControlCommand(ctx, m.deviceID, m.credential, cmd.ID, state, result, code); err != nil && errors.Is(err, context.Canceled) {
			return
		}
	}
}

func (m *Module) execute(ctx context.Context, cmd *transport.ControlCommand) (string, map[string]any, string, error) {
	if cmd.ID == "" || len(cmd.Payload) != 0 {
		return StatusFailed, nil, "INVALID_COMMAND", nil
	}

	// 1. Check durable ledger if available
	if m.ledger != nil {
		if record, found := m.ledger.Get(cmd.ID); found {
			if record.Status == StatusExecuting {
				// Incomplete / was executing before restart: do NOT re-execute.
				// Report failed with deterministic error code.
				return StatusFailed, nil, ErrIndeterminateAfterRestart, nil
			}
			return record.Status, record.Result, record.ErrorCode, nil
		}
	}

	// 2. Check in-memory map
	m.mu.Lock()
	if existing, seen := m.executed[cmd.ID]; seen {
		m.mu.Unlock()
		if existing.status == StatusExecuting {
			return StatusFailed, nil, ErrIndeterminateAfterRestart, nil
		}
		return existing.status, existing.result, existing.errorCode, nil
	}
	m.mu.Unlock()

	// 3. Mark "executing" atomically in ledger BEFORE calling Executor
	if m.ledger != nil {
		if err := m.ledger.Begin(cmd.ID); err != nil {
			// Begin failed: DO NOT call executor. Return error to stop loop.
			return "", nil, "", fmt.Errorf("control: begin command in ledger: %w", err)
		}
	}

	m.mu.Lock()
	m.trackExecutedLocked(cmd.ID, commandExecution{status: StatusExecuting})
	m.mu.Unlock()

	var state, code string
	var result map[string]any

	switch cmd.CommandType {
	case "request_status":
		state = StatusSucceeded
		result = m.executor.Status()
	case "rediscovery":
		if err := m.executor.Rediscover(ctx); err != nil {
			state, code = StatusFailed, "REDISCOVERY_FAILED"
		} else {
			state = StatusSucceeded
			result = map[string]any{}
		}
	case "reload_config":
		if err := m.executor.ReloadConfig(ctx); err != nil {
			state, code = StatusFailed, "RELOAD_CONFIG_FAILED"
		} else {
			state = StatusSucceeded
			result = map[string]any{}
		}
	case "restart_video_pipeline":
		state, code = StatusFailed, "UNSUPPORTED"
	default:
		state, code = StatusFailed, "UNKNOWN_COMMAND"
	}

	// S11 security event log: every dispatched command, allowlisted-type-only
	// (see the switch above -- there is no free-form/shell exec path), with
	// its outcome. Never logs the result payload itself, only that one was
	// produced, since a future command type could carry sensitive fields.
	slog.Default().Info("control command executed",
		"command_id", cmd.ID,
		"command_type", cmd.CommandType,
		"status", state,
		"error_code", code,
	)

	// 4. Persist terminal outcome atomically in ledger BEFORE reporting to SaaS
	if m.ledger != nil {
		if err := m.ledger.Complete(cmd.ID, CommandExecution{
			Status:    state,
			Result:    result,
			ErrorCode: code,
		}); err != nil {
			// Complete failed: DO NOT return success to report. Return error to halt.
			return "", nil, "", fmt.Errorf("control: complete command in ledger: %w", err)
		}
	}

	m.mu.Lock()
	m.trackExecutedLocked(cmd.ID, commandExecution{
		status:    state,
		result:    result,
		errorCode: code,
	})
	m.mu.Unlock()

	return state, result, code, nil
}

// trackExecutedLocked records a command's outcome and keeps the in-memory
// idempotency map bounded, oldest-first — the same policy the durable ledger
// applies to its own record set. The map previously had no delete path at all,
// so every distinct control command the SaaS ever dispatched added a permanent
// entry for the lifetime of the process. m.mu must be held.
func (m *Module) trackExecutedLocked(id string, exec commandExecution) {
	if _, seen := m.executed[id]; !seen {
		m.executedOrder = append(m.executedOrder, id)
	}
	m.executed[id] = exec

	bound := defaultMaxTrackedExecutions
	if m.ledger != nil {
		if n := m.ledger.MaxEntries(); n > bound {
			bound = n
		}
	}
	for len(m.executedOrder) > bound {
		oldest := m.executedOrder[0]
		m.executedOrder = m.executedOrder[1:]
		delete(m.executed, oldest)
	}
}

// ExecuteCommand executes a single control command directly (useful for tests).
func (m *Module) ExecuteCommand(ctx context.Context, cmd *transport.ControlCommand) (string, map[string]any, string, error) {
	return m.execute(ctx, cmd)
}
