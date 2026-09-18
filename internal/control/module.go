// Package control implements the Edge-initiated, allowlisted control poll.
package control

import (
	"context"
	"errors"
	"fmt"
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
}

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
	m.executed[cmd.ID] = commandExecution{status: StatusExecuting}
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
	case "restart_video_pipeline", "reload_config":
		state, code = StatusFailed, "UNSUPPORTED"
	default:
		state, code = StatusFailed, "UNKNOWN_COMMAND"
	}

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
	m.executed[cmd.ID] = commandExecution{
		status:    state,
		result:    result,
		errorCode: code,
	}
	m.mu.Unlock()

	return state, result, code, nil
}
