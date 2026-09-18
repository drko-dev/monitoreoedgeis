package remoteconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const stateFileName = "remote_config_state.json"

// ErrCorruptState is returned when remote_config_state.json exists but
// cannot be parsed.
var ErrCorruptState = errors.New("remoteconfig: stored state is invalid")

// State is the durable record of the remote-config apply lifecycle. It
// never carries anything IA2's payload itself did not already have --
// passwords, Bearer tokens, device credentials and RTSP credentials are
// managed by their own storage (internal/credentials, internal/cameracreds)
// and must never appear in a remote-config Payload in the first place;
// this store does not attempt to scrub them out of an opaque payload it
// does not understand.
type State struct {
	AppliedVersion int64   `json:"applied_version"`
	AppliedConfig  *Config `json:"applied_config,omitempty"`

	PreviousKnownGoodVersion int64   `json:"previous_known_good_version"`
	PreviousKnownGoodConfig  *Config `json:"previous_known_good_config,omitempty"`

	// Staging holds a config that has passed validation and is being (or
	// was being, at last crash) applied, but has not yet been promoted to
	// AppliedConfig. Cleared once the apply attempt reaches a terminal
	// outcome (applied or failed/rolled_back).
	Staging *Config `json:"staging,omitempty"`

	LastFailedVersion int64       `json:"last_failed_version,omitempty"`
	LastFailedConfig  *Config     `json:"last_failed_config,omitempty"`
	LastApplyStatus   ApplyStatus `json:"last_apply_status,omitempty"`
	LastApplyAt       string      `json:"last_apply_at,omitempty"`
	LastErrorSafe     string      `json:"last_error_safe,omitempty"`
	RollbackCount     int64       `json:"rollback_count"`
	ReceivedAt        string      `json:"received_at,omitempty"`
}

// Store provides thread-safe, durable, atomic persistence of the remote-
// config apply lifecycle, matching internal/control.Ledger's pattern.
type Store struct {
	dataDir string
	path    string

	mu    sync.Mutex
	state State

	// limitWrites/writeBudget: test-only hook for exercising persistence
	// failure paths without real disk faults. Zero value (limitWrites
	// false) never limits writes -- production behavior is unaffected.
	// When limitWrites is true, writeBudget further saveLocked calls
	// succeed normally and every call after that fails synthetically.
	limitWrites bool
	writeBudget int
}

// OpenStore opens or creates the durable remote-config state in dataDir.
func OpenStore(dataDir string) (*Store, error) {
	path := filepath.Join(dataDir, stateFileName)
	s := &Store{dataDir: dataDir, path: path}

	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return s, nil
	case err != nil:
		return nil, fmt.Errorf("remoteconfig: read state %s: %w", path, err)
	}

	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, fmt.Errorf("%w: %s: invalid JSON: %v", ErrCorruptState, path, err)
	}
	s.state = st
	return s, nil
}

// Get returns a copy of the current state.
func (s *Store) Get() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// Save persists a full state replacement atomically. On a persistence
// failure the in-memory state is left exactly as it was before the call --
// a caller must never observe a state change that did not durably commit.
func (s *Store) Save(st State) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.saveLocked(st); err != nil {
		return err
	}
	s.state = st
	return nil
}

// Update applies fn to a copy of the current state, persists the *result*
// atomically first, and only then commits it to memory. If persistence
// fails, the in-memory state is left completely unchanged -- fn's effect
// never becomes visible without a durable commit backing it. Returns the
// state that is now in effect (the candidate on success, the prior state
// on failure) plus the error.
func (s *Store) Update(fn func(State) State) (State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	candidate := fn(s.state)
	if err := s.saveLocked(candidate); err != nil {
		return s.state, err
	}
	s.state = candidate
	return s.state, nil
}

func (s *Store) saveLocked(st State) error {
	if s.limitWrites {
		if s.writeBudget <= 0 {
			return errors.New("remoteconfig: simulated write failure (test)")
		}
		s.writeBudget--
	}

	if err := os.MkdirAll(s.dataDir, 0o700); err != nil {
		return fmt.Errorf("remoteconfig: create data dir %s: %w", s.dataDir, err)
	}

	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("remoteconfig: encode state: %w", err)
	}

	tmp, err := os.CreateTemp(s.dataDir, ".remote_config_state-*.json.tmp")
	if err != nil {
		return fmt.Errorf("remoteconfig: create temp state file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // no-op once the rename below succeeds

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("remoteconfig: write temp state file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("remoteconfig: sync temp state file: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("remoteconfig: chmod temp state file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("remoteconfig: close temp state file: %w", err)
	}
	if err := os.Rename(tmpPath, s.path); err != nil {
		return fmt.Errorf("remoteconfig: rename state into place: %w", err)
	}
	return nil
}

func nowRFC3339() string {
	return time.Now().UTC().Format(time.RFC3339)
}
