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

	// Guard against overwriting an existing event file.
	// If identical event already exists, return nil idempotently without double-counting backlog.
	// If divergent event content exists for the same EventUUID, return ErrEventConflict.
	if existingData, err := os.ReadFile(finalPath); err == nil {
		var existingEvt LocalEvent
		if err := json.Unmarshal(existingData, &existingEvt); err == nil {
			if eventsEquivalent(&existingEvt, evt) {
				return nil
			}
			return fmt.Errorf("%w: %s", ErrEventConflict, evt.EventUUID)
		}
	}

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

// eventsEquivalent compares semantic payload fields of two events to decide if an event retry is identical.
func eventsEquivalent(a, b *LocalEvent) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.EventUUID != b.EventUUID ||
		a.EdgeID != b.EdgeID ||
		a.CandidateKey != b.CandidateKey ||
		a.TenantID != b.TenantID ||
		a.SiteID != b.SiteID ||
		a.CorrelationID != b.CorrelationID ||
		a.FrameSeq != b.FrameSeq ||
		!a.SourceTimestamp.Equal(b.SourceTimestamp) ||
		a.Tipo != b.Tipo ||
		a.ClassID != b.ClassID ||
		a.Confidence != b.Confidence ||
		a.BBox != b.BBox ||
		a.Model != b.Model ||
		a.Device != b.Device ||
		a.ProcessingMode != b.ProcessingMode {
		return false
	}
	// Evidence check
	if (a.Evidence == nil) != (b.Evidence == nil) {
		return false
	}
	if a.Evidence != nil && b.Evidence != nil {
		if a.Evidence.Path != b.Evidence.Path ||
			a.Evidence.SHA256 != b.Evidence.SHA256 ||
			a.Evidence.SizeBytes != b.Evidence.SizeBytes {
			return false
		}
	}
	return true
}

// MarkSynced transitions a persisted event to SyncStatusSynced once K12's
// backlog confirms metadata+capture+clip all landed on the SaaS. Without
// this, EventStore's SyncStatus stayed "pending" forever regardless of
// what actually happened to the event (see internal/agent's wiring, which
// registers this as edgebacklog.Backlog's onSynced callback).
func (s *EventStore) MarkSynced(eventUUID string) error {
	return s.transitionLocked(eventUUID, SyncStatusSynced, "")
}

// MarkQuarantined transitions a persisted event to SyncStatusFailed after
// K12's backlog gives up on it permanently (invalid payload, not a
// transient/network error) — inspectable on disk, and stops the backlog
// from retrying it forever.
func (s *EventStore) MarkQuarantined(eventUUID, reason string) error {
	return s.transitionLocked(eventUUID, SyncStatusFailed, reason)
}

func (s *EventStore) transitionLocked(eventUUID string, status SyncStatus, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	path := filepath.Join(s.baseDir, eventUUID+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("fulledge: load event %s for sync transition: %w", eventUUID, err)
	}
	var evt LocalEvent
	if err := json.Unmarshal(data, &evt); err != nil {
		return fmt.Errorf("%w: %s: %v", ErrEventCorrupt, path, err)
	}
	wasPending := evt.SyncStatus == SyncStatusPending
	evt.SyncStatus = status
	if reason != "" {
		evt.QuarantineReason = reason
	}

	out, err := json.MarshalIndent(&evt, "", "  ")
	if err != nil {
		return fmt.Errorf("fulledge: marshal event %s: %w", eventUUID, err)
	}

	tmpFile, err := os.CreateTemp(s.baseDir, ".event-*.tmp")
	if err != nil {
		return fmt.Errorf("fulledge: create temp event file: %w", err)
	}
	tmpPath := tmpFile.Name()

	cleanTmp := true
	defer func() {
		if cleanTmp {
			_ = tmpFile.Close()
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := tmpFile.Write(out); err != nil {
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
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("fulledge: atomic rename %s -> %s: %w", tmpPath, path, err)
	}
	cleanTmp = false

	if wasPending && status != SyncStatusPending {
		s.backlogCount.Add(-1)
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
