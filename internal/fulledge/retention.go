package fulledge

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/edgebacklog"
)

// tempOrphanGrace is how old a leftover ".tmp" file must be before retention
// treats it as an orphan from a crashed write rather than a write still in
// progress. Not configurable: it is pure hygiene, unrelated to the
// count/byte/age bounds below.
const tempOrphanGrace = 1 * time.Hour

// RetentionConfig configures Hito Z B3 bounded retention across the three
// Full Edge-owned trees. Every bound is 0 = disabled; nothing here invents a
// commercial default (see docs/product/B3_RETENTION_DESIGN.md).
type RetentionConfig struct {
	DataDir string

	MaxEvents     int64
	MaxEventBytes int64
	MaxEventAge   time.Duration

	MaxCaptures     int64
	MaxCaptureBytes int64
	MaxCaptureAge   time.Duration

	MaxClips     int64
	MaxClipBytes int64
	MaxClipAge   time.Duration

	// EvictPending allows retention to delete a pending (not-yet-synced)
	// event JSON once otherwise eligible. Default false. This never applies
	// to evidence (captures/clips): a pending record's evidence is always
	// protected regardless of this flag (see F-B in the design doc).
	EvictPending bool
	// SweepInterval rate-limits MaybeSweepAfterWrite; 0 means every write
	// triggers a sweep attempt (still cheap: a no-op scan when nothing is
	// past its bound).
	SweepInterval time.Duration

	Logger *slog.Logger
}

// RetentionTreeReport is a sanitized (no absolute paths) per-tree tally.
type RetentionTreeReport struct {
	Scanned        int   `json:"scanned"`
	Evicted        int   `json:"evicted"`
	Failed         int   `json:"failed"`
	RefusedUnsafe  int   `json:"refused_unsafe"`
	TempOrphans    int   `json:"temp_orphans_removed"`
	BytesReclaimed int64 `json:"bytes_reclaimed"`
}

// RetentionReport summarizes one Sweep across all three trees.
type RetentionReport struct {
	At       time.Time           `json:"at"`
	Events   RetentionTreeReport `json:"events"`
	Captures RetentionTreeReport `json:"captures"`
	Clips    RetentionTreeReport `json:"clips"`
}

// RetentionManager enforces bounded retention over events/, evidence/captures/
// and evidence/clips/ — the only three trees Full Edge owns (see the
// ownership map in the design doc). It never touches anything else under
// DataDir.
type RetentionManager struct {
	cfg         RetentionConfig
	store       *EventStore
	eventsDir   string
	capturesDir string
	clipsDir    string
	backlogDir  string
	mu          sync.Mutex
	lastSweep   time.Time
	lastReport  RetentionReport
}

// NewRetentionManager constructs a RetentionManager and immediately runs one
// sweep, bounding any pre-existing growth from before this process started.
// A construction-time sweep error is returned rather than silently ignored,
// but never panics: retention degrading must never take down Full Edge.
func NewRetentionManager(cfg RetentionConfig, store *EventStore) (*RetentionManager, error) {
	if cfg.DataDir == "" {
		return nil, fmt.Errorf("fulledge: retention DataDir is required")
	}
	if store == nil {
		return nil, fmt.Errorf("fulledge: retention requires a non-nil EventStore")
	}
	r := &RetentionManager{
		cfg:         cfg,
		store:       store,
		eventsDir:   filepath.Join(cfg.DataDir, eventsSubdir),
		capturesDir: filepath.Join(cfg.DataDir, evidenceSubdir, capturesSubdir),
		clipsDir:    filepath.Join(cfg.DataDir, "evidence", "clips"),
		backlogDir:  filepath.Join(cfg.DataDir, "local-event-backlog"),
	}
	report, err := r.Sweep(time.Now())
	if err != nil {
		return nil, fmt.Errorf("fulledge: construction-time retention sweep: %w", err)
	}
	r.logSweep(report)
	return r, nil
}

func (r *RetentionManager) anyBoundActive() bool {
	c := r.cfg
	return c.MaxEvents > 0 || c.MaxEventBytes > 0 || c.MaxEventAge > 0 ||
		c.MaxCaptures > 0 || c.MaxCaptureBytes > 0 || c.MaxCaptureAge > 0 ||
		c.MaxClips > 0 || c.MaxClipBytes > 0 || c.MaxClipAge > 0
}

// MaybeSweepAfterWrite runs a sweep if enough time has passed since the last
// one (SweepInterval), or unconditionally when SweepInterval is 0. It never
// returns an error to a caller mid-write path — errors are logged only, the
// same "degrade safely" posture SaveJPEG failures already use.
func (r *RetentionManager) MaybeSweepAfterWrite(now time.Time) {
	r.mu.Lock()
	due := r.cfg.SweepInterval <= 0 || now.Sub(r.lastSweep) >= r.cfg.SweepInterval
	r.mu.Unlock()
	if !due {
		return
	}
	report, err := r.Sweep(now)
	if err != nil {
		if r.cfg.Logger != nil {
			r.cfg.Logger.Warn("fulledge: retention sweep failed, evidence growth is unbounded until it succeeds", slog.Any("error", err))
		}
		return
	}
	r.logSweep(report)
}

// LastReport returns the most recently completed sweep's sanitized report.
func (r *RetentionManager) LastReport() RetentionReport {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastReport
}

// Sweep performs one full, safe pass: temp-orphan cleanup (unconditional),
// then bounded eviction (only if any bound is configured). It never deletes
// anything referenced by a pending edgebacklog record, and it stops a tree's
// eviction the moment a delete fails rather than deleting around the
// failure.
func (r *RetentionManager) Sweep(now time.Time) (RetentionReport, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	report := RetentionReport{At: now}

	report.Events.TempOrphans = sweepTempOrphans(r.eventsDir, ".event-", ".tmp", now)
	report.Captures.TempOrphans = sweepTempOrphans(r.capturesDir, ".evidence-", ".tmp", now)
	report.Clips.TempOrphans = sweepTempOrphans(r.clipsDir, "", ".mp4.tmp", now)

	if !r.anyBoundActive() {
		r.lastSweep = now
		r.lastReport = report
		return report, nil
	}

	pending, err := edgebacklog.LoadPendingReferences(r.backlogDir)
	if err != nil {
		return RetentionReport{}, fmt.Errorf("fulledge: retention sweep: %w", err)
	}

	survivorCaptureRefs, survivorEventUUIDs, eventsReport, err := r.sweepEvents(now, pending)
	if err != nil {
		return RetentionReport{}, err
	}
	report.Events = mergeTempOrphans(eventsReport, report.Events.TempOrphans)

	capturesReport, err := r.sweepCaptures(now, pending, survivorCaptureRefs)
	if err != nil {
		return RetentionReport{}, err
	}
	report.Captures = mergeTempOrphans(capturesReport, report.Captures.TempOrphans)

	clipsReport, err := r.sweepClips(now, pending, survivorEventUUIDs)
	if err != nil {
		return RetentionReport{}, err
	}
	report.Clips = mergeTempOrphans(clipsReport, report.Clips.TempOrphans)

	r.lastSweep = now
	r.lastReport = report
	return report, nil
}

func mergeTempOrphans(t RetentionTreeReport, tempOrphans int) RetentionTreeReport {
	t.TempOrphans = tempOrphans
	return t
}

func (r *RetentionManager) logSweep(report RetentionReport) {
	if r.cfg.Logger == nil {
		return
	}
	total := report.Events.Evicted + report.Captures.Evicted + report.Clips.Evicted
	if total == 0 && report.Events.Failed == 0 && report.Captures.Failed == 0 && report.Clips.Failed == 0 {
		return
	}
	r.cfg.Logger.Info("fulledge: retention sweep completed",
		slog.Int("events_evicted", report.Events.Evicted),
		slog.Int("captures_evicted", report.Captures.Evicted),
		slog.Int("clips_evicted", report.Clips.Evicted),
		slog.Int("events_failed", report.Events.Failed),
		slog.Int("captures_failed", report.Captures.Failed),
		slog.Int("clips_failed", report.Clips.Failed))
}

// retentionCandidate is one file eligible (or not) for eviction. path is
// absolute; it is used for deletion and for cross-referencing the pending
// set, never logged or reported raw.
type retentionCandidate struct {
	path      string
	size      int64
	when      time.Time
	protected bool

	// eventUUID/evidenceRelPath are only set for the events tree, to recover
	// per-event bookkeeping (SyncStatus, Evidence.Path) after planning.
	eventUUID  string
	evidence   *EvidenceRef
	syncStatus SyncStatus
}

// planEviction decides, oldest-first, which candidates must be removed to
// satisfy count/byte/age bounds. candidates must already be sorted oldest
// first. A protected candidate is never returned, but still counts against
// count/byte totals (it cannot be reclaimed, so the bound may remain
// violated — that is a reported state, not an error). 0 disables a bound.
// Pure and side-effect free so it can be unit tested without a filesystem.
func planEviction(candidates []retentionCandidate, now time.Time, maxCount, maxBytes int64, maxAge time.Duration) []retentionCandidate {
	var evict []retentionCandidate

	var afterTTL []retentionCandidate
	for _, c := range candidates {
		if maxAge > 0 && !c.protected && now.Sub(c.when) > maxAge {
			evict = append(evict, c)
			continue
		}
		afterTTL = append(afterTTL, c)
	}

	if maxCount <= 0 && maxBytes <= 0 {
		return evict
	}

	count := int64(len(afterTTL))
	var total int64
	for _, c := range afterTTL {
		total += c.size
	}

	for _, c := range afterTTL {
		violated := (maxCount > 0 && count > maxCount) || (maxBytes > 0 && total > maxBytes)
		if !violated {
			break
		}
		if c.protected {
			continue
		}
		evict = append(evict, c)
		count--
		total -= c.size
	}

	return evict
}

func sortOldestFirst(candidates []retentionCandidate) {
	sort.Slice(candidates, func(i, j int) bool {
		if !candidates[i].when.Equal(candidates[j].when) {
			return candidates[i].when.Before(candidates[j].when)
		}
		return candidates[i].path < candidates[j].path
	})
}

// safeDirEntries lists the direct, non-directory, non-symlink entries of
// dir. A symlink is refused (not followed, not deleted) and counted in
// refusedUnsafe — this is the path-traversal/symlink-escape guard: retention
// only ever considers a file it can prove is a direct, real entry of an
// owned tree.
func safeDirEntries(dir string) (entries []os.DirEntry, refusedUnsafe int, err error) {
	all, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	for _, e := range all {
		info, err := os.Lstat(filepath.Join(dir, e.Name()))
		if err != nil {
			refusedUnsafe++
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			refusedUnsafe++
			continue
		}
		if info.IsDir() {
			continue
		}
		entries = append(entries, e)
	}
	return entries, refusedUnsafe, nil
}

func (r *RetentionManager) sweepEvents(now time.Time, pending edgebacklog.PendingReferences) (survivorCaptureRefs map[string]bool, survivorEventUUIDs map[string]bool, report RetentionTreeReport, err error) {
	survivorCaptureRefs = map[string]bool{}
	survivorEventUUIDs = map[string]bool{}

	entries, refused, err := safeDirEntries(r.eventsDir)
	if err != nil {
		return nil, nil, RetentionTreeReport{}, fmt.Errorf("fulledge: retention: list events dir: %w", err)
	}
	report.RefusedUnsafe += refused

	var candidates []retentionCandidate
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".json") || strings.HasPrefix(name, ".") {
			continue
		}
		report.Scanned++
		abs := filepath.Join(r.eventsDir, name)
		data, err := os.ReadFile(abs)
		if err != nil {
			report.RefusedUnsafe++
			continue
		}
		var evt LocalEvent
		if err := json.Unmarshal(data, &evt); err != nil || evt.EventUUID == "" {
			// A corrupt/unparsable event is never deleted or refcounted by
			// retention — it survives untouched, and its (unknown) evidence
			// path cannot be protected either. Rare and out of scope beyond
			// "never delete it blind".
			report.RefusedUnsafe++
			survivorEventUUIDs[strings.TrimSuffix(name, ".json")] = true
			continue
		}
		when := evt.SourceTimestamp
		if when.IsZero() {
			when = evt.CreatedAt
		}
		protected := pending.EventUUIDs[evt.EventUUID] && !r.cfg.EvictPending
		candidates = append(candidates, retentionCandidate{
			path: abs, size: int64(len(data)), when: when, protected: protected,
			eventUUID: evt.EventUUID, evidence: evt.Evidence, syncStatus: evt.SyncStatus,
		})
	}

	sortOldestFirst(candidates)
	toEvict := planEviction(candidates, now, r.cfg.MaxEvents, r.cfg.MaxEventBytes, r.cfg.MaxEventAge)
	evictSet := make(map[string]bool, len(toEvict))
	for _, c := range toEvict {
		evictSet[c.path] = true
	}

	stopped := false
	for _, c := range candidates {
		if !evictSet[c.path] {
			survivorEventUUIDs[c.eventUUID] = true
			if c.evidence != nil && c.evidence.Path != "" {
				survivorCaptureRefs[c.evidence.Path] = true
			}
			continue
		}
		if stopped {
			// A prior delete already failed this sweep; stop evicting this
			// tree rather than continuing around the failure, but everything
			// not yet evicted still survives and must still be counted.
			survivorEventUUIDs[c.eventUUID] = true
			if c.evidence != nil && c.evidence.Path != "" {
				survivorCaptureRefs[c.evidence.Path] = true
			}
			continue
		}
		if err := r.store.Evict(c.eventUUID, c.syncStatus == SyncStatusPending); err != nil {
			report.Failed++
			stopped = true
			survivorEventUUIDs[c.eventUUID] = true
			if c.evidence != nil && c.evidence.Path != "" {
				survivorCaptureRefs[c.evidence.Path] = true
			}
			continue
		}
		report.Evicted++
		report.BytesReclaimed += c.size
	}

	return survivorCaptureRefs, survivorEventUUIDs, report, nil
}

func (r *RetentionManager) sweepCaptures(now time.Time, pending edgebacklog.PendingReferences, survivorCaptureRefs map[string]bool) (RetentionTreeReport, error) {
	var report RetentionTreeReport

	entries, refused, err := safeDirEntries(r.capturesDir)
	if err != nil {
		return RetentionTreeReport{}, fmt.Errorf("fulledge: retention: list captures dir: %w", err)
	}
	report.RefusedUnsafe += refused

	var candidates []retentionCandidate
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".jpg") || strings.HasPrefix(name, ".") {
			continue
		}
		report.Scanned++
		abs := filepath.Join(r.capturesDir, name)
		info, err := os.Stat(abs)
		if err != nil {
			report.RefusedUnsafe++
			continue
		}
		relPath, err := filepath.Rel(r.cfg.DataDir, abs)
		if err != nil {
			relPath = filepath.Join(evidenceSubdir, capturesSubdir, name)
		}
		protected := survivorCaptureRefs[relPath] || pending.EvidencePaths[abs]
		candidates = append(candidates, retentionCandidate{path: abs, size: info.Size(), when: info.ModTime(), protected: protected})
	}

	sortOldestFirst(candidates)
	toEvict := planEviction(candidates, now, r.cfg.MaxCaptures, r.cfg.MaxCaptureBytes, r.cfg.MaxCaptureAge)
	for _, c := range toEvict {
		if err := os.Remove(c.path); err != nil {
			report.Failed++
			break
		}
		report.Evicted++
		report.BytesReclaimed += c.size
	}

	return report, nil
}

func (r *RetentionManager) sweepClips(now time.Time, pending edgebacklog.PendingReferences, survivorEventUUIDs map[string]bool) (RetentionTreeReport, error) {
	var report RetentionTreeReport
	_ = survivorEventUUIDs // clips are 1:1 with an event UUID (never shared); no refcounting needed, only the pending check (F-B) applies.

	entries, refused, err := safeDirEntries(r.clipsDir)
	if err != nil {
		return RetentionTreeReport{}, fmt.Errorf("fulledge: retention: list clips dir: %w", err)
	}
	report.RefusedUnsafe += refused

	var candidates []retentionCandidate
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".mp4") || strings.HasPrefix(name, ".") {
			continue
		}
		report.Scanned++
		abs := filepath.Join(r.clipsDir, name)
		info, err := os.Stat(abs)
		if err != nil {
			report.RefusedUnsafe++
			continue
		}
		protected := pending.EvidencePaths[abs]
		candidates = append(candidates, retentionCandidate{path: abs, size: info.Size(), when: info.ModTime(), protected: protected})
	}

	sortOldestFirst(candidates)
	toEvict := planEviction(candidates, now, r.cfg.MaxClips, r.cfg.MaxClipBytes, r.cfg.MaxClipAge)
	for _, c := range toEvict {
		if err := os.Remove(c.path); err != nil {
			report.Failed++
			break
		}
		report.Evicted++
		report.BytesReclaimed += c.size
	}

	return report, nil
}

// sweepTempOrphans removes direct entries of dir matching prefix+suffix
// (e.g. ".event-"+".tmp") whose mtime is older than tempOrphanGrace, so a
// write still genuinely in progress is never touched. Returns the count
// removed; errors are swallowed into a 0 count since this is best-effort
// hygiene, never a correctness requirement.
func sweepTempOrphans(dir, prefix, suffix string, now time.Time) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	removed := 0
	for _, e := range entries {
		name := e.Name()
		if prefix != "" && !strings.HasPrefix(name, prefix) {
			continue
		}
		if !strings.HasSuffix(name, suffix) {
			continue
		}
		abs := filepath.Join(dir, name)
		info, err := os.Lstat(abs)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || info.IsDir() {
			continue
		}
		if now.Sub(info.ModTime()) < tempOrphanGrace {
			continue
		}
		if os.Remove(abs) == nil {
			removed++
		}
	}
	return removed
}
