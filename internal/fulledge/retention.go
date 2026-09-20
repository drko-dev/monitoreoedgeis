package fulledge

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// This file implements bounded, configurable retention for the two artifact
// kinds Full Edge owns whose lifetime is NOT already governed elsewhere:
//
//   - <DataDir>/events/<event_uuid>.json        (event metadata)
//   - <DataDir>/evidence/captures/<capture_uuid>.jpg  (JPEG evidence)
//
// MP4 clips live under <DataDir>/evidence/clips and are deliberately NOT
// handled here (B3-A scope).
//
// The transport backlog (<DataDir>/local-event-backlog) is a DIFFERENT concern
// and is never touched: it already bounds itself, and it owns its own eviction.
//
// Retention deletes evidence permanently, so the two safety rules below are the
// heart of this file, not refinements:
//
//	F-A  one JPEG can be referenced by MANY event records. Captures are named by
//	     a fresh capture UUID per inference, and one event JSON is written per
//	     DETECTION -- so N detections produce N event records all pointing at the
//	     same JPEG (service.go:98-124 then :146). A capture may therefore only be
//	     deleted when no SURVIVING event record names it. Deleting it by "oldest
//	     file first" alone would destroy evidence still referenced by live
//	     records. References are always rebuilt by reading the real event JSONs;
//	     capture UUID == event UUID is never assumed.
//
//	F-B  evidence referenced by a still-PENDING backlog record must not be
//	     deleted. edgebacklog stat()s referenced files and requires an exact size
//	     match (backlog.go:394-406), and send() re-validates before every stage;
//	     a missing or resized file maps to ErrInvalidRequest and QUARANTINES the
//	     whole record, skipping its remaining stages (backlog.go:450-457,488-494).
//	     So deleting such a file would not merely free disk, it would destroy that
//	     event's sync. Equally, event metadata that is still pending must not be
//	     evicted. The source of truth is the durable on-disk state -- the event
//	     JSON's own SyncStatus and local-event-backlog/pending/*.json -- never
//	     an in-memory approximation.
//
// No default bound is invented anywhere: every bound is 0 = disabled.

const (
	// pendingBacklogSubdir mirrors internal/agent/local_events_module.go's
	// filepath.Join(cfg.DataDir, "local-event-backlog") plus edgebacklog's
	// "pending" directory (backlog.go:203). It is duplicated here as a constant
	// rather than imported so a retention regression cannot silently start
	// reading (or worse, evicting) another subsystem's directory: the path is
	// only ever READ, and only for the pending set.
	pendingBacklogSubdir = "local-event-backlog"

	// clipsSubdir mirrors internal/evidence/clips.go:104's literal "clips". It
	// exists only so the pending snapshot can RECOGNISE a clip reference
	// without confusing it for a capture. B3-A never deletes clips.
	clipsSubdir = "clips"

	// tempFileGracePeriod is how long a temp file must be untouched before it is
	// considered abandoned. Every writer removes its own temp on failure, so a
	// young temp file belongs to a write in flight and must not be swept.
	tempFileGracePeriod = 5 * time.Minute
)

// RetentionBounds is one tree's bound set. Every field is 0 = disabled, so an
// unconfigured appliance retains everything exactly as it does today.
type RetentionBounds struct {
	// MaxCount bounds the number of retained files in the tree.
	MaxCount int64
	// MaxBytes bounds the total size of retained files in the tree.
	MaxBytes int64
	// MaxAge bounds how old a file may be. Compared against the event's own
	// SourceTimestamp for events, and against mtime for captures.
	MaxAge time.Duration
}

// Enabled reports whether any bound is set.
func (b RetentionBounds) Enabled() bool {
	return b.MaxCount > 0 || b.MaxBytes > 0 || b.MaxAge > 0
}

// RetentionConfig configures a RetentionManager.
type RetentionConfig struct {
	// DataDir is the same GEOCAM_DATA_DIR the rest of Full Edge uses.
	DataDir string
	// Events and Captures are the two trees' bounds.
	Events   RetentionBounds
	Captures RetentionBounds
	// PendingBacklogDir overrides the pending-record directory used for the F-B
	// check. Empty means <DataDir>/local-event-backlog/pending. Tests set it.
	PendingBacklogDir string
	Logger            *slog.Logger
}

// RetentionReport is the sanitized outcome of one sweep. It carries counts and
// byte totals only -- never an absolute path, never event content.
type RetentionReport struct {
	EventsScanned   int   `json:"events_scanned"`
	EventsEvicted   int   `json:"events_evicted"`
	CapturesScanned int   `json:"captures_scanned"`
	CapturesEvicted int   `json:"captures_evicted"`
	BytesReclaimed  int64 `json:"bytes_reclaimed"`
	// Protected counts files retained specifically because of the F-A/F-B
	// safety rules, so a disk that is not shrinking is diagnosable rather than
	// mysterious.
	Protected           int      `json:"protected"`
	Failed              int      `json:"failed"`
	RefusedUnsafe       int      `json:"refused_unsafe"`
	TempOrphansRemoved  int      `json:"temp_orphans_removed"`
	FirstError          string   `json:"first_error,omitempty"`
	BoundsConfiguration []string `json:"bounds_configured,omitempty"`
}

// RetentionManager evicts the oldest artifacts first, within bounds, while
// preserving everything the safety rules above protect.
//
// It is safe for concurrent use with the writers in this package: every
// operation it performs is a single os.Remove of a fully written file, and it
// holds its own mutex so two sweeps cannot interleave. It never holds that
// mutex across a caller's write, and it never rewrites or truncates a file.
type RetentionManager struct {
	cfg     RetentionConfig
	logger  *slog.Logger
	mu      sync.Mutex
	dataDir string
	// eventsDir/capturesDir are the only two directories this manager may ever
	// delete from, resolved once so no call can widen the blast radius.
	eventsDir   string
	capturesDir string
	clipsDir    string
	pendingDir  string

	lastSweepMu sync.Mutex
	lastSweep   time.Time

	// removeFile is the delete seam, defaulting to os.Remove. Tests inject a
	// failure to prove that a single failed delete STOPS that tree rather than
	// deleting around it -- a property that is otherwise untestable without
	// root, since chmod-based failures are all-or-nothing (they make every
	// remove fail, which cannot distinguish "stops" from "keeps trying").
	removeFile func(string) error
}

// NewRetentionManager builds a RetentionManager. It requires DataDir and
// creates nothing: a missing tree is simply empty until the owning component
// creates it.
func NewRetentionManager(cfg RetentionConfig) (*RetentionManager, error) {
	if strings.TrimSpace(cfg.DataDir) == "" {
		return nil, errors.New("fulledge: retention requires a data dir")
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	pending := cfg.PendingBacklogDir
	if pending == "" {
		pending = filepath.Join(cfg.DataDir, pendingBacklogSubdir, "pending")
	}
	return &RetentionManager{
		cfg:         cfg,
		logger:      logger,
		dataDir:     cfg.DataDir,
		eventsDir:   filepath.Join(cfg.DataDir, eventsSubdir),
		capturesDir: filepath.Join(cfg.DataDir, evidenceSubdir, capturesSubdir),
		clipsDir:    filepath.Join(cfg.DataDir, evidenceSubdir, clipsSubdir),
		pendingDir:  pending,
		removeFile:  os.Remove,
	}, nil
}

// RetentionEnabled reports whether either tree has a bound configured, so the
// caller can skip the sweep entirely on an unconfigured appliance.
func (r *RetentionManager) RetentionEnabled() bool {
	return r.cfg.Events.Enabled() || r.cfg.Captures.Enabled()
}

// Sweep applies the configured bounds once, oldest-first, and returns a
// sanitized report.
//
// A non-nil error means at least one delete failed; eviction for that tree
// STOPS at the first failure rather than continuing around it, because
// continuing would leave the accounting claiming a bound is satisfied while
// files it believes it removed are still on disk.
func (r *RetentionManager) Sweep(now time.Time) (RetentionReport, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	var report RetentionReport
	if r.cfg.Events.Enabled() {
		report.BoundsConfiguration = append(report.BoundsConfiguration,
			fmt.Sprintf("events:count=%d,bytes=%d,age=%s", r.cfg.Events.MaxCount, r.cfg.Events.MaxBytes, r.cfg.Events.MaxAge))
	}
	if r.cfg.Captures.Enabled() {
		report.BoundsConfiguration = append(report.BoundsConfiguration,
			fmt.Sprintf("captures:count=%d,bytes=%d,age=%s", r.cfg.Captures.MaxCount, r.cfg.Captures.MaxBytes, r.cfg.Captures.MaxAge))
	}

	// 0. Establish the pending snapshot ONCE, before any delete, and fail
	//    closed. If we cannot determine what the durable backlog still needs, we
	//    must not delete anything at all: "could not read" must never be read as
	//    "not referenced", because deleting evidence a pending upload needs makes
	//    edgebacklog quarantine the whole record and destroys that event's sync.
	if !r.cfg.Events.Enabled() && !r.cfg.Captures.Enabled() {
		// Nothing is bounded, so there is nothing destructive to guard. Temp
		// orphans are still garbage and are safe to reclaim.
		r.sweepTempOrphans(r.eventsDir, "event", now, &report)
		r.sweepTempOrphans(r.capturesDir, "evidence", now, &report)
		return report, nil
	}
	pending, pendingErr := r.loadPendingRetentionRefs()
	if pendingErr != nil {
		report.FirstError = "pending_scan_failed"
		r.logger.Warn("fulledge retention: pending backlog cannot be read, skipping this sweep entirely",
			"error", pendingErr)
		return report, pendingErr
	}

	// 1. Event metadata: decide survivors first, because the capture pass must
	//    know which events are actually still present (F-A).
	eventOutcome, err := r.sweepEvents(now, pending, &report)
	if err != nil {
		return report, err
	}

	// 2. Captures: protected by any SURVIVING event reference and by any
	//    pending backlog record.
	if r.cfg.Captures.Enabled() {
		if err := r.sweepCaptures(now, eventOutcome.survivingCaptureRefs, pending, &report); err != nil {
			return report, err
		}
	}

	// 3. Temp orphans are swept regardless of whether a bound is configured:
	//    they are garbage, not retained data, and leaving them is unbounded
	//    growth by definition.
	r.sweepTempOrphans(r.eventsDir, "event", now, &report)
	r.sweepTempOrphans(r.capturesDir, "evidence", now, &report)

	return report, nil
}

// eventSweepOutcome carries what the capture pass needs from the event pass.
type eventSweepOutcome struct {
	// survivingCaptureRefs is the set of capture BASENAMES still referenced by
	// an event record that survived this sweep. A capture in this set is never
	// evicted, no matter how old it is.
	survivingCaptureRefs map[string]bool
}

// dirEntry is one secure directory listing entry. It deliberately carries no
// parsed record: the events pass parses separately, and captures have none.
type dirEntry struct {
	name    string
	path    string
	size    int64
	modTime time.Time
}

// sweepEvents evicts event metadata oldest-first within the event bounds.
func (r *RetentionManager) sweepEvents(now time.Time, pending retentionPendingRefs, report *RetentionReport) (eventSweepOutcome, error) {
	outcome := eventSweepOutcome{survivingCaptureRefs: map[string]bool{}}

	entries, err := secureListDir(r.eventsDir, ".json", "event")
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return outcome, nil
		}
		return outcome, fmt.Errorf("fulledge: retention list events: %w", err)
	}
	report.EventsScanned = len(entries)

	// Parse each event. An unreadable or corrupt event is PROTECTED, never
	// evicted: retention must not turn a corruption into a deletion.
	type eventCandidate struct {
		dirEntry
		event   *LocalEvent
		pending bool
		refs    []string
	}
	candidates := make([]eventCandidate, 0, len(entries))
	var totalBytes int64
	for _, e := range entries {
		raw, readErr := os.ReadFile(e.path)
		if readErr != nil {
			report.Protected++
			continue
		}
		var evt LocalEvent
		if unmarshalErr := json.Unmarshal(raw, &evt); unmarshalErr != nil {
			report.Protected++
			continue
		}
		ec := eventCandidate{dirEntry: e, event: &evt}
		ec.pending = evt.SyncStatus == SyncStatusPending
		if evt.Evidence != nil && evt.Evidence.Path != "" {
			ec.refs = append(ec.refs, filepath.Base(evt.Evidence.Path))
		}
		candidates = append(candidates, ec)
		totalBytes += e.size
	}

	// Deterministic oldest-first: by the event's own SourceTimestamp (falling
	// back to file mtime when unset), tie-broken by filename so ordering never
	// depends on directory order.
	sort.Slice(candidates, func(i, j int) bool {
		ti, tj := eventAge(candidates[i].event, candidates[i].modTime), eventAge(candidates[j].event, candidates[j].modTime)
		if !ti.Equal(tj) {
			return ti.Before(tj)
		}
		return candidates[i].name < candidates[j].name
	})

	surviving := make([]eventCandidate, 0, len(candidates))
	evicted := 0
	var freed int64
	var firstErr error

	for i, c := range candidates {
		remaining := int64(len(candidates) - i)
		overCount := r.cfg.Events.MaxCount > 0 && (int64(len(surviving))+remaining > r.cfg.Events.MaxCount)
		overBytes := r.cfg.Events.MaxBytes > 0 && (totalBytes > r.cfg.Events.MaxBytes)
		overAge := r.cfg.Events.MaxAge > 0 && now.Sub(eventAge(c.event, c.modTime)) > r.cfg.Events.MaxAge

		if !overCount && !overBytes && !overAge {
			surviving = append(surviving, c)
			continue
		}

		// F-B: never evict metadata the durable backlog still needs. Two
		// independent reasons to keep it:
		//   - its own SyncStatus says pending; or
		//   - its UUID appears in a pending record, regardless of what the local
		//     SyncStatus claims. The durable backlog is the more trustworthy
		//     source, which is exactly what protects a partially recovered or
		//     inconsistently transitioned event.
		if c.pending || pending.eventUUIDs[c.event.EventUUID] {
			report.Protected++
			surviving = append(surviving, c)
			continue
		}

		if removeErr := r.removeFile(c.path); removeErr != nil {
			// STOP the whole tree here. Continuing would keep deleting other
			// candidates around a failure that may be systemic (a read-only or
			// full filesystem), and would report a bound as satisfied while
			// files this sweep believes it removed are still on disk. Everything
			// not yet processed, including this candidate, is treated as
			// surviving.
			report.Failed++
			report.FirstError = "remove_failed"
			firstErr = fmt.Errorf("fulledge: retention remove %s: %w", c.name, removeErr)
			surviving = append(surviving, c)
			surviving = append(surviving, candidates[i+1:]...)
			break
		}
		evicted++
		freed += c.size
		totalBytes -= c.size
	}

	for _, c := range surviving {
		for _, ref := range c.refs {
			outcome.survivingCaptureRefs[ref] = true
		}
	}

	report.EventsEvicted = evicted
	report.BytesReclaimed += freed
	if evicted > 0 {
		r.logger.Info("fulledge retention: evicted event metadata",
			"count", evicted, "bytes", freed, "protected", report.Protected)
	}
	return outcome, firstErr
}

// sweepCaptures evicts JPEG evidence oldest-first within the capture bounds,
// never touching a capture that a surviving event references (F-A) or that a
// pending backlog record references (F-B).
func (r *RetentionManager) sweepCaptures(now time.Time, survivingRefs map[string]bool, pending retentionPendingRefs, report *RetentionReport) error {
	entries, err := secureListDir(r.capturesDir, ".jpg", "evidence")
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("fulledge: retention list captures: %w", err)
	}
	report.CapturesScanned = len(entries)

	sort.Slice(entries, func(i, j int) bool {
		if !entries[i].modTime.Equal(entries[j].modTime) {
			return entries[i].modTime.Before(entries[j].modTime)
		}
		return entries[i].name < entries[j].name
	})

	var totalBytes int64
	for _, e := range entries {
		totalBytes += e.size
	}

	survivingCount := int64(0)
	var firstErr error
	for i, e := range entries {
		remaining := int64(len(entries) - i)

		// F-A / F-B protection is absolute: a referenced capture is retained
		// even when it is the sole reason a bound is exceeded.
		if survivingRefs[e.name] || pending.captures[e.name] {
			report.Protected++
			survivingCount++
			continue
		}

		overCount := r.cfg.Captures.MaxCount > 0 && (survivingCount+remaining > r.cfg.Captures.MaxCount)
		overBytes := r.cfg.Captures.MaxBytes > 0 && (totalBytes > r.cfg.Captures.MaxBytes)
		overAge := r.cfg.Captures.MaxAge > 0 && now.Sub(e.modTime) > r.cfg.Captures.MaxAge
		if !overCount && !overBytes && !overAge {
			survivingCount++
			continue
		}
		if removeErr := r.removeFile(e.path); removeErr != nil {
			// Same stop-the-tree rule as the events pass, for the same reason.
			report.Failed++
			report.FirstError = "remove_failed"
			firstErr = fmt.Errorf("fulledge: retention remove %s: %w", e.name, removeErr)
			break
		}
		report.CapturesEvicted++
		report.BytesReclaimed += e.size
		totalBytes -= e.size
	}

	if report.CapturesEvicted > 0 {
		r.logger.Info("fulledge retention: evicted JPEG evidence",
			"count", report.CapturesEvicted, "protected", report.Protected)
	}
	return firstErr
}

// retentionPendingRefs is a single, consistent snapshot of what the durable
// transport backlog still needs. It is loaded ONCE per sweep, before any
// delete, and passed to both passes.
type retentionPendingRefs struct {
	// eventUUIDs are event UUIDs named by a pending record. Metadata for these
	// must survive regardless of its own local SyncStatus: a partially
	// recovered or inconsistently-transitioned event is exactly the case where
	// the durable backlog is the more trustworthy source.
	eventUUIDs map[string]bool
	// captures and clips are evidence BASENAMES referenced by pending records.
	captures map[string]bool
	clips    map[string]bool
}

// loadPendingRetentionRefs builds the pending snapshot.
//
// It FAILS CLOSED. "I could not determine what is pending" must never be
// interpreted as "nothing is pending", because that reading deletes evidence a
// pending upload still needs -- and edgebacklog quarantines the whole record
// when its evidence disappears, destroying that event's sync.
//
// The only condition that yields an empty set with no error is the pending
// directory not existing at all: that is a valid state (nothing has ever been
// queued) and is distinct from being unreadable.
func (r *RetentionManager) loadPendingRetentionRefs() (retentionPendingRefs, error) {
	refs := retentionPendingRefs{
		eventUUIDs: map[string]bool{},
		captures:   map[string]bool{},
		clips:      map[string]bool{},
	}

	entries, err := os.ReadDir(r.pendingDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// Valid: no backlog directory means nothing is pending.
			return refs, nil
		}
		return retentionPendingRefs{}, fmt.Errorf("fulledge: retention cannot read pending backlog %s: %w", r.pendingDir, err)
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		raw, readErr := os.ReadFile(filepath.Join(r.pendingDir, entry.Name()))
		if readErr != nil {
			return retentionPendingRefs{}, fmt.Errorf("fulledge: retention cannot read pending record %s: %w", entry.Name(), readErr)
		}
		// The record shape is edgebacklog's. Only the identity and evidence
		// paths are decoded, keeping this a read-only observer with no import
		// cycle.
		var rec struct {
			Submission struct {
				Event *struct {
					EventUUID string `json:"event_uuid"`
				} `json:"event,omitempty"`
				Capture *struct {
					Path string `json:"path"`
				} `json:"capture,omitempty"`
				Clip *struct {
					Path string `json:"path"`
				} `json:"clip,omitempty"`
			} `json:"submission"`
		}
		if unmarshalErr := json.Unmarshal(raw, &rec); unmarshalErr != nil {
			return retentionPendingRefs{}, fmt.Errorf("fulledge: retention found a malformed pending record %s: %w", entry.Name(), unmarshalErr)
		}
		// A pending record always carries the event it is delivering; without
		// its UUID this sweep cannot know which metadata to protect, so it is
		// treated as incomplete rather than ignored.
		if rec.Submission.Event == nil || rec.Submission.Event.EventUUID == "" {
			return retentionPendingRefs{}, fmt.Errorf("fulledge: retention found an incomplete pending record %s: missing submission.event.event_uuid", entry.Name())
		}
		refs.eventUUIDs[rec.Submission.Event.EventUUID] = true

		// Only a file DIRECTLY inside a managed evidence directory is recorded.
		// A path elsewhere is IGNORED rather than resolved, so a crafted record
		// cannot widen the blast radius -- and ignoring it is safe, because we
		// only ever delete from directories we manage, and a genuinely managed
		// file's reference would be honoured here.
		for _, p := range []*struct {
			Path string `json:"path"`
		}{rec.Submission.Capture, rec.Submission.Clip} {
			if p == nil || p.Path == "" {
				continue
			}
			if filepath.Dir(p.Path) == r.capturesDir {
				refs.captures[filepath.Base(p.Path)] = true
			}
			if filepath.Dir(p.Path) == r.clipsDir {
				refs.clips[filepath.Base(p.Path)] = true
			}
		}
	}
	return refs, nil
}

// secureListDir lists regular files in dir whose name ends in suffix, using
// Lstat so a symlink is never followed.
//
// Safety rules, all fail-closed (the entry is skipped, never deleted):
//   - the entry name must be a plain base name (no separator, no "..")
//   - the entry must NOT be a symlink and must be a regular file
//   - dot-prefixed temp files are excluded (swept separately, with a grace
//     period, so a write in flight is never removed)
//   - the joined path must still be inside dir
func secureListDir(dir, suffix, kind string) ([]dirEntry, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := make([]dirEntry, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") || !strings.HasSuffix(name, suffix) {
			continue
		}
		if name != filepath.Base(name) || strings.Contains(name, "..") {
			continue
		}
		full := filepath.Join(dir, name)
		if !pathInside(dir, full) {
			continue
		}
		// Lstat, deliberately: a symlink is not evidence we own, and following
		// one could delete a file outside the data dir.
		info, lstatErr := os.Lstat(full)
		if lstatErr != nil {
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			continue
		}
		out = append(out, dirEntry{name: name, path: full, size: info.Size(), modTime: info.ModTime()})
	}
	return out, nil
}

// pathInside reports whether p is strictly inside dir after cleaning.
func pathInside(dir, p string) bool {
	cd := filepath.Clean(dir)
	cp := filepath.Clean(p)
	if cp == cd {
		return false
	}
	rel, err := filepath.Rel(cd, cp)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// sweepTempOrphans removes abandoned temp files older than the grace period.
func (r *RetentionManager) sweepTempOrphans(dir, prefix string, now time.Time, report *RetentionReport) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	marker := "." + prefix + "-"
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, marker) || !strings.HasSuffix(name, ".tmp") {
			continue
		}
		full := filepath.Join(dir, name)
		if !pathInside(dir, full) {
			report.RefusedUnsafe++
			continue
		}
		info, lstatErr := os.Lstat(full)
		if lstatErr != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			continue
		}
		if now.Sub(info.ModTime()) < tempFileGracePeriod {
			continue
		}
		if removeErr := r.removeFile(full); removeErr != nil {
			report.Failed++
			if report.FirstError == "" {
				report.FirstError = "temp_remove_failed"
			}
			continue
		}
		report.TempOrphansRemoved++
	}
}

// MaybeSweep runs a sweep only if at least minInterval has elapsed since the
// last one, so a caller on the write path cannot turn retention into an O(n^2)
// directory scan per write. minInterval <= 0 sweeps every time.
//
// It never returns an error: this runs on the evidence write path, and a
// retention problem must never fail an event write. Failures are logged and
// carried in the report instead.
func (r *RetentionManager) MaybeSweep(now time.Time, minInterval time.Duration) (RetentionReport, bool) {
	if !r.RetentionEnabled() {
		return RetentionReport{}, false
	}
	if minInterval > 0 {
		r.lastSweepMu.Lock()
		last := r.lastSweep
		r.lastSweepMu.Unlock()
		if !last.IsZero() && now.Sub(last) < minInterval {
			return RetentionReport{}, false
		}
	}
	rep, err := r.Sweep(now)
	r.lastSweepMu.Lock()
	r.lastSweep = now
	r.lastSweepMu.Unlock()
	if err != nil {
		r.logger.Warn("fulledge retention: sweep reported a failure", "error", err)
	}
	return rep, true
}

// eventAge is the timestamp retention orders and ages events by: the event's own
// SourceTimestamp when set, else the file's mtime.
func eventAge(evt *LocalEvent, modTime time.Time) time.Time {
	if evt != nil && !evt.SourceTimestamp.IsZero() {
		return evt.SourceTimestamp
	}
	return modTime
}
