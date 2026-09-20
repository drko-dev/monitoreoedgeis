package edgebacklog

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// PendingReferences is a read-only, static snapshot of what pending/ (this
// package's own durable spool) currently references. It requires no open
// Backlog and never mutates pending/.
//
// Hito Z B3 (bounded Full Edge retention, see
// docs/product/B3_RETENTION_DESIGN.md F-B) uses this as the single source of
// truth for "this evidence/event is still pending sync, do not delete it":
// deleting a JPEG/MP4 that a pending record still references does not just
// free disk, it quarantines that event's sync (Enqueue/send os.Stat the
// referenced file and require an exact size match).
type PendingReferences struct {
	// EvidencePaths holds every absolute capture/clip path referenced by a
	// pending record, exactly as stored in the record (already resolved to
	// an absolute path by the producer before Enqueue).
	EvidencePaths map[string]bool
	// EventUUIDs holds the event UUID of every pending record.
	EventUUIDs map[string]bool
}

// LoadPendingReferences reads every record under dir/pending. A record that
// cannot be parsed is treated as an error rather than skipped: retention must
// never guess that an unparsable-but-real pending record has nothing to
// protect.
func LoadPendingReferences(dir string) (PendingReferences, error) {
	refs := PendingReferences{EvidencePaths: map[string]bool{}, EventUUIDs: map[string]bool{}}

	pendingDir := filepath.Join(dir, "pending")
	entries, err := os.ReadDir(pendingDir)
	if os.IsNotExist(err) {
		return refs, nil
	}
	if err != nil {
		return PendingReferences{}, fmt.Errorf("edgebacklog: list %s: %w", pendingDir, err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		path := filepath.Join(pendingDir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return PendingReferences{}, fmt.Errorf("edgebacklog: read pending record %s: %w", path, err)
		}

		var r record
		if err := json.Unmarshal(data, &r); err != nil {
			return PendingReferences{}, fmt.Errorf("edgebacklog: parse pending record %s: %w", path, err)
		}

		if r.Submission.Event.EventUUID != "" {
			refs.EventUUIDs[r.Submission.Event.EventUUID] = true
		}
		if r.Submission.Capture != nil && r.Submission.Capture.Path != "" {
			refs.EvidencePaths[r.Submission.Capture.Path] = true
		}
		if r.Submission.Clip != nil && r.Submission.Clip.Path != "" {
			refs.EvidencePaths[r.Submission.Clip.Path] = true
		}
	}

	return refs, nil
}
