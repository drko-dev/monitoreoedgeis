package fulledge

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
)

const (
	eventsSubdir = "events"
	dirPerm      = 0o700
	filePerm     = 0o600
)

// EventStore manages durable, atomic disk-backed persistence of LocalEvent records.
type EventStore struct {
	mu           sync.RWMutex
	baseDir      string
	backlogCount atomic.Int64
}

// NewEventStore initializes an EventStore under dataDir and primes the backlog counter.
func NewEventStore(dataDir string) (*EventStore, error) {
	eventsDir := filepath.Join(dataDir, eventsSubdir)
	if err := os.MkdirAll(eventsDir, dirPerm); err != nil {
		return nil, fmt.Errorf("fulledge: create events dir %s: %w", eventsDir, err)
	}

	store := &EventStore{
		baseDir: eventsDir,
	}

	// Prime backlog count from disk
	count, err := store.countPendingOnDisk()
	if err != nil {
		return nil, fmt.Errorf("fulledge: scan existing events in %s: %w", eventsDir, err)
	}
	store.backlogCount.Store(count)

	return store, nil
}

// BaseDir returns the absolute path where event files are stored.
func (s *EventStore) BaseDir() string {
	return s.baseDir
}

// BacklogCount returns the current number of unsynced events on disk.
func (s *EventStore) BacklogCount() int64 {
	return s.backlogCount.Load()
}

// Save atomically writes an event to disk via temp-file + rename.
func (s *EventStore) Save(evt *LocalEvent) error {
	if evt == nil || evt.EventUUID == "" {
		return fmt.Errorf("fulledge: cannot save empty event or missing event_uuid")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := json.MarshalIndent(evt, "", "  ")
	if err != nil {
		return fmt.Errorf("fulledge: marshal event %s: %w", evt.EventUUID, err)
	}

	finalPath := filepath.Join(s.baseDir, evt.EventUUID+".json")
	tmpFile, err := os.CreateTemp(s.baseDir, ".event-*.tmp")
	if err != nil {
		return fmt.Errorf("fulledge: create temp event file: %w", err)
	}
	tmpPath := tmpFile.Name()

	// Ensure cleanup if anything fails prior to rename
	cleanTmp := true
	defer func() {
		if cleanTmp {
			_ = tmpFile.Close()
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := tmpFile.Write(data); err != nil {
		return fmt.Errorf("fulledge: write temp event file %s: %w", tmpPath, err)
	}

	if err := tmpFile.Sync(); err != nil {
		return fmt.Errorf("fulledge: sync temp event file %s: %w", tmpPath, err)
	}

	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("fulledge: close temp event file %s: %w", tmpPath, err)
	}

	if err := os.Chmod(tmpPath, filePerm); err != nil {
		return fmt.Errorf("fulledge: chmod temp event file %s: %w", tmpPath, err)
	}

	if err := os.Rename(tmpPath, finalPath); err != nil {
		return fmt.Errorf("fulledge: atomic rename %s -> %s: %w", tmpPath, finalPath, err)
	}

	cleanTmp = false
	if evt.SyncStatus == SyncStatusPending {
		s.backlogCount.Add(1)
	}

	return nil
}

// Get loads a LocalEvent from disk by event UUID.
func (s *EventStore) Get(eventUUID string) (*LocalEvent, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	path := filepath.Join(s.baseDir, eventUUID+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var evt LocalEvent
	if err := json.Unmarshal(data, &evt); err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrEventCorrupt, path, err)
	}

	return &evt, nil
}

// ListPending loads all events currently flagged as SyncStatusPending.
func (s *EventStore) ListPending() ([]*LocalEvent, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entries, err := os.ReadDir(s.baseDir)
	if err != nil {
		return nil, fmt.Errorf("fulledge: read events dir: %w", err)
	}

	var pending []*LocalEvent
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		path := filepath.Join(s.baseDir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var evt LocalEvent
		if err := json.Unmarshal(data, &evt); err != nil {
			continue
		}
		if evt.SyncStatus == SyncStatusPending {
			pending = append(pending, &evt)
		}
	}

	return pending, nil
}

func (s *EventStore) countPendingOnDisk() (int64, error) {
	entries, err := os.ReadDir(s.baseDir)
	if err != nil {
		return 0, err
	}

	var count int64
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		path := filepath.Join(s.baseDir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var evt LocalEvent
		if err := json.Unmarshal(data, &evt); err == nil && evt.SyncStatus == SyncStatusPending {
			count++
		}
	}

	return count, nil
}
