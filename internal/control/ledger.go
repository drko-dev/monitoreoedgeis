package control

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	ledgerFileName    = "control_ledger.json"
	defaultMaxEntries = 100
)

// ErrCorruptLedger is returned when control_ledger.json exists but cannot be parsed.
var ErrCorruptLedger = errors.New("control: stored ledger is invalid")

// CommandExecution records terminal outcome of an executed command.
type CommandExecution struct {
	Status      string         `json:"status"`
	Result      map[string]any `json:"result,omitempty"`
	ErrorCode   string         `json:"error_code,omitempty"`
	CompletedAt string         `json:"completed_at"`
}

type ledgerRecord struct {
	Commands   map[string]CommandExecution `json:"commands"`
	Order      []string                    `json:"order"` // Insertion/completion order for bounding FIFO
	MaxEntries int                         `json:"max_entries"`
}

// Ledger provides thread-safe, durable, atomic persistence of executed control commands.
type Ledger struct {
	dataDir    string
	path       string
	maxEntries int

	mu       sync.Mutex
	commands map[string]CommandExecution
	order    []string
}

// OpenLedger opens or creates the durable control ledger in dataDir.
func OpenLedger(dataDir string, maxEntries int) (*Ledger, error) {
	if maxEntries <= 0 {
		maxEntries = defaultMaxEntries
	}
	path := filepath.Join(dataDir, ledgerFileName)
	l := &Ledger{
		dataDir:    dataDir,
		path:       path,
		maxEntries: maxEntries,
		commands:   make(map[string]CommandExecution),
		order:      make([]string, 0),
	}

	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return l, nil
	case err != nil:
		return nil, fmt.Errorf("control: read ledger %s: %w", path, err)
	}

	var rec ledgerRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, fmt.Errorf("%w: %s: invalid JSON: %v", ErrCorruptLedger, path, err)
	}

	if rec.Commands == nil {
		rec.Commands = make(map[string]CommandExecution)
	}
	l.commands = rec.Commands
	l.order = rec.Order

	// Reconcile order with commands map if needed
	existing := make(map[string]bool)
	for _, id := range l.order {
		if _, ok := l.commands[id]; ok {
			existing[id] = true
		}
	}
	for id := range l.commands {
		if !existing[id] {
			l.order = append(l.order, id)
		}
	}

	return l, nil
}

const (
	StatusExecuting = "executing"
	StatusSucceeded = "succeeded"
	StatusFailed    = "failed"

	ErrIndeterminateAfterRestart = "INDETERMINATE_AFTER_RESTART"
)

// Get checks if a command_id has a recorded outcome.
func (l *Ledger) Get(commandID string) (CommandExecution, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	exec, ok := l.commands[commandID]
	return exec, ok
}

// Begin records that execution of commandID has started ("executing").
// It writes atomically to disk before side effects begin.
func (l *Ledger) Begin(commandID string) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if _, exists := l.commands[commandID]; !exists {
		l.order = append(l.order, commandID)
	}
	l.commands[commandID] = CommandExecution{
		Status:      StatusExecuting,
		CompletedAt: time.Now().UTC().Format(time.RFC3339),
	}

	if len(l.order) > l.maxEntries {
		pruneCount := len(l.order) - l.maxEntries
		for i := 0; i < pruneCount; i++ {
			delete(l.commands, l.order[i])
		}
		l.order = l.order[pruneCount:]
	}

	return l.saveLocked()
}

// Complete records the terminal outcome (succeeded/failed) atomically to disk.
func (l *Ledger) Complete(commandID string, exec CommandExecution) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if _, exists := l.commands[commandID]; !exists {
		l.order = append(l.order, commandID)
	}
	if exec.CompletedAt == "" {
		exec.CompletedAt = time.Now().UTC().Format(time.RFC3339)
	}
	l.commands[commandID] = exec

	// Prune old entries if exceeding maxEntries (bounded)
	if len(l.order) > l.maxEntries {
		pruneCount := len(l.order) - l.maxEntries
		for i := 0; i < pruneCount; i++ {
			delete(l.commands, l.order[i])
		}
		l.order = l.order[pruneCount:]
	}

	return l.saveLocked()
}

// Record saves a terminal outcome atomically to disk (kept for backwards compatibility).
func (l *Ledger) Record(commandID string, exec CommandExecution) error {
	return l.Complete(commandID, exec)
}

// saveLocked writes the ledger via temp-file-sync-rename.
func (l *Ledger) saveLocked() error {
	if err := os.MkdirAll(l.dataDir, 0o700); err != nil {
		return fmt.Errorf("control: create data dir %s: %w", l.dataDir, err)
	}

	rec := ledgerRecord{
		Commands:   l.commands,
		Order:      l.order,
		MaxEntries: l.maxEntries,
	}

	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("control: encode ledger: %w", err)
	}

	tmp, err := os.CreateTemp(l.dataDir, ".control_ledger-*.json.tmp")
	if err != nil {
		return fmt.Errorf("control: create temp ledger file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("control: write temp ledger file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("control: sync temp ledger file: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("control: chmod temp ledger file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("control: close temp ledger file: %w", err)
	}
	if err := os.Rename(tmpPath, l.path); err != nil {
		return fmt.Errorf("control: rename ledger into place: %w", err)
	}
	return nil
}

// MaxEntries returns how many commands the ledger retains. Module uses it to
// size its own in-memory idempotency map so that map can never be smaller than
// the durable record set — forgetting a command the ledger still remembers
// would let a re-delivered command execute twice.
func (l *Ledger) MaxEntries() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.maxEntries
}

// Count returns the number of recorded commands in memory.
func (l *Ledger) Count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.commands)
}
