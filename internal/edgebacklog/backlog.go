// Package edgebacklog durably replays local-event operations without owning
// event detection or evidence creation. It stores only metadata and file
// references; capture and clip bytes remain in the evidence retention area.
package edgebacklog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/platform"
	"github.com/drko-dev/monitoreoedgeis/internal/transport"
)

var (
	ErrFull               = errors.New("edgebacklog: bounded backlog is full")
	ErrSubmissionConflict = errors.New("edgebacklog: divergent submission for event")
)

// maxErrorBytes bounds every string this package records for an operator, the
// same way rtsp.CameraStreamStatus.LastErrorSafe does. A persistence error
// message names a path and an errno — never a payload or a credential — but a
// bound keeps a pathological OS message from bloating /status.
const maxErrorBytes = 255

// Evidence is an immutable local reference. Path is never sent to the SaaS.
type Evidence struct {
	Path       string `json:"path"`
	SHA256     string `json:"sha256"`
	Size       int64  `json:"size"`
	DurationMS int64  `json:"duration_ms,omitempty"`
}

// Submission is the narrow producer interface. The future local event/evidence
// producer creates capture/clip first, then submits their stable references.
type Submission struct {
	Event   transport.LocalEvent `json:"event"`
	Capture *Evidence            `json:"capture,omitempty"`
	Clip    *Evidence            `json:"clip,omitempty"`
}

// Producer is the only interface a future local event/evidence producer needs.
type Producer interface{ Enqueue(Submission) error }

type Config struct {
	Dir           string
	MaxOperations int
	MaxBytes      int64
	RetryBase     time.Duration
	RetryMax      time.Duration
}

type Status struct {
	BacklogCount  int        `json:"backlog_count"`
	PendingBytes  int64      `json:"pending_bytes"`
	OldestPending *time.Time `json:"oldest_pending,omitempty"`
	LastSuccess   *time.Time `json:"last_success,omitempty"`
	LastError     string     `json:"last_error,omitempty"`
	Degraded      bool       `json:"degraded"`
	Quarantined   int        `json:"quarantined"`
	Capacity      int        `json:"capacity,omitempty"`
	Drops         int64      `json:"drops"`
	// PersistErrors counts every failed write of a pending record (stage
	// advance, retry state, removal, quarantine move). It is cumulative for
	// the process and is what makes a persistence failure visible at all:
	// before it existed, those write errors were discarded with `_ =`, so a
	// full disk silently reverted the on-disk record to an older stage.
	PersistErrors int64 `json:"persist_errors"`
	// DiskFull is true while the most recent persistence failure was an
	// out-of-space condition, and is cleared by the next successful
	// persistence. It is deliberately not sticky: the only thing that fixes
	// a full disk is freeing space, and the backlog must report recovery
	// when that happens rather than staying degraded for the process
	// lifetime.
	DiskFull bool `json:"disk_full,omitempty"`
	// QuarantineEvicted counts quarantined records deleted because the
	// quarantine arena is itself bounded (see enforceQuarantineBoundLocked).
	// The records are permanently-rejected operations the SaaS already
	// refused; the evidence bytes they reference are never deleted.
	QuarantineEvicted int64 `json:"quarantine_evicted,omitempty"`
	// OverCapacity is true when the in-memory queue is larger than the
	// configured bound. Enqueue refuses to grow the queue past
	// MaxOperations/MaxBytes, so this can only be true because recover()
	// loaded more records than the bound allows — either the spool was
	// written under a larger configuration that has since been lowered, or
	// files were copied into pending/. Recovery deliberately does NOT drop
	// the surplus: those are durable, unsynced events and discarding them
	// silently would destroy evidence. Reporting the violation is the honest
	// alternative to hiding it, and Enqueue already refuses new work while
	// the queue is over its bound.
	OverCapacity bool `json:"over_capacity,omitempty"`
	// RecoveredOperations is how many pending records recover() loaded from
	// disk at Open. It is reported so an over-capacity spool (see
	// OverCapacity) can be told apart from one that grew during this run.
	RecoveredOperations int `json:"recovered_operations,omitempty"`
}

type record struct {
	Sequence    uint64     `json:"sequence"`
	CreatedAt   time.Time  `json:"created_at"`
	Stage       string     `json:"stage"` // metadata, capture, clip
	Submission  Submission `json:"submission"`
	Attempts    int        `json:"attempts"`
	NextAttempt time.Time  `json:"next_attempt,omitempty"`
}

type Backlog struct {
	cfg         Config
	mu          sync.Mutex
	queue       []record
	counter     uint64
	lastSuccess *time.Time
	lastError   string
	degraded    bool
	quarantined int
	drops       int64

	persistErrors     int64
	diskFull          bool
	quarantineEvicted int64
	recoveredOps      int
	// quarantineSeqs tracks the sequence numbers currently sitting in
	// quarantine/, oldest first. quarantine/ is on disk and is never
	// drained by the sync loop, so without this it would grow for the
	// lifetime of the appliance; enforceQuarantineBoundLocked uses it to
	// hold the arena to the same MaxOperations bound the pending queue
	// already obeys.
	quarantineSeqs []uint64

	// writeFile/renameFile/removeFile are the filesystem seam. They default
	// to the real os functions and exist so a test can inject a
	// deterministic ENOSPC without root, without a fake filesystem, and
	// without filling a real disk — the same per-instance injection
	// cloudsink.Buffer already uses for its clock (Buffer.now). Nothing
	// outside this package can set them.
	writeFile  func(name string, data []byte, perm os.FileMode) error
	renameFile func(oldpath, newpath string) error
	removeFile func(name string) error

	// onSynced/onQuarantined let a caller (the K7 EventStore, via
	// internal/agent's wiring) keep its own SyncStatus in step with this
	// backlog's real outcome, instead of the two drifting independently —
	// see SetSyncCallbacks. Both are optional and called with b.mu held, so
	// they must not call back into the Backlog.
	onSynced      func(eventUUID string)
	onQuarantined func(eventUUID, reason string)
}

// SetSyncCallbacks registers the hooks a record's terminal state (fully
// synced or permanently quarantined) invokes. Either may be nil. Must be
// called before Run starts draining, and only once.
func (b *Backlog) SetSyncCallbacks(onSynced func(eventUUID string), onQuarantined func(eventUUID, reason string)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.onSynced = onSynced
	b.onQuarantined = onQuarantined
}

// Run polls the FIFO until ctx is cancelled. Cancellation is a clean shutdown:
// no pending record is advanced or removed until its request has succeeded.
func (b *Backlog) Run(ctx context.Context, sender transport.LocalEventSender, deviceID, credential string, interval time.Duration) {
	if interval <= 0 {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		if b.ProcessOne(ctx, sender, deviceID, credential) {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func Open(cfg Config) (*Backlog, error) {
	if cfg.MaxOperations <= 0 || cfg.MaxBytes <= 0 {
		return nil, fmt.Errorf("edgebacklog: MaxOperations and MaxBytes must be positive")
	}
	if cfg.RetryBase <= 0 {
		cfg.RetryBase = time.Second
	}
	if cfg.RetryMax < cfg.RetryBase {
		cfg.RetryMax = time.Minute
	}
	if err := os.MkdirAll(filepath.Join(cfg.Dir, "pending"), 0o750); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(cfg.Dir, "quarantine"), 0o750); err != nil {
		return nil, err
	}
	b := &Backlog{
		cfg:        cfg,
		writeFile:  os.WriteFile,
		renameFile: os.Rename,
		removeFile: os.Remove,
	}
	if err := b.recover(); err != nil {
		return nil, err
	}
	// A restart must not reset the quarantine accounting: the arena is
	// bounded on disk, so its current contents have to be discovered before
	// the bound can be enforced again.
	if err := b.loadQuarantineLocked(); err != nil {
		return nil, err
	}
	return b, nil
}

func (b *Backlog) recover() error {
	entries, err := os.ReadDir(filepath.Join(b.cfg.Dir, "pending"))
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		// A "*.json.tmp" is the leftover of a write that failed before its
		// rename — most often a disk that filled up mid-write. It is never
		// valid and recover() deliberately never loads it, so leaving it
		// behind would let repeated ENOSPC accumulate garbage forever.
		// writeLocked already removes its own temp file on failure; this
		// sweep cleans up after a crash that happened first, and after any
		// leak left by an older build.
		if strings.HasSuffix(entry.Name(), ".tmp") {
			_ = b.removeFile(filepath.Join(b.cfg.Dir, "pending", entry.Name()))
			continue
		}
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(b.cfg.Dir, "pending", entry.Name()))
		if err != nil {
			continue
		}
		var r record
		if json.Unmarshal(data, &r) != nil || r.Sequence == 0 || r.Submission.Event.EventUUID == "" {
			continue
		}
		b.queue = append(b.queue, r)
		if r.Sequence > b.counter {
			b.counter = r.Sequence
		}
	}
	sort.Slice(b.queue, func(i, j int) bool { return b.queue[i].Sequence < b.queue[j].Sequence })
	b.recoveredOps = len(b.queue)
	return nil
}

// loadQuarantineLocked rebuilds the in-memory view of quarantine/ so the
// arena bound survives a restart, and so Status.Quarantined reports what is
// really on disk instead of resetting to zero every process start.
func (b *Backlog) loadQuarantineLocked() error {
	dir := filepath.Join(b.cfg.Dir, "quarantine")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	seqs := make([]uint64, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		if seq, ok := sequenceFromName(entry.Name()); ok {
			seqs = append(seqs, seq)
		}
	}
	sort.Slice(seqs, func(i, j int) bool { return seqs[i] < seqs[j] })
	b.quarantineSeqs = seqs
	b.quarantined = len(seqs)
	return b.enforceQuarantineBoundLocked()
}

// sequenceFromName reads back the zero-padded sequence a record file is named
// after.
func sequenceFromName(name string) (uint64, bool) {
	seq, err := strconv.ParseUint(strings.TrimSuffix(name, ".json"), 10, 64)
	if err != nil {
		return 0, false
	}
	return seq, true
}

// enforceQuarantineBoundLocked holds quarantine/ to MaxOperations entries,
// oldest first. Quarantined records are operations the SaaS permanently
// rejected (see ProcessOne); they are kept for operator forensics but nothing
// ever drains them, so an unbounded arena would eventually fill a small
// appliance's disk on its own. The bound reuses MaxOperations rather than
// introducing a second, invented number, and eviction is counted and logged
// by the caller-visible QuarantineEvicted so it is never silent.
//
// The evidence files the records reference are NOT touched: only the
// metadata record is deleted, never a capture or a clip.
func (b *Backlog) enforceQuarantineBoundLocked() error {
	var firstErr error
	for len(b.quarantineSeqs) > b.cfg.MaxOperations {
		seq := b.quarantineSeqs[0]
		path := filepath.Join(b.cfg.Dir, "quarantine", fmt.Sprintf("%020d.json", seq))
		if err := b.removeFile(path); err != nil && !os.IsNotExist(err) {
			// Do not drop the bookkeeping entry when the delete failed: the
			// file is still there, and forgetting it would make the bound a
			// lie. Record and stop rather than looping on the same file.
			if firstErr == nil {
				firstErr = platform.WrapDiskError(err)
			}
			b.recordPersistFailureLocked(firstErr)
			break
		}
		b.quarantineSeqs = b.quarantineSeqs[1:]
		b.quarantined = len(b.quarantineSeqs)
		b.quarantineEvicted++
	}
	return firstErr
}

func (b *Backlog) Enqueue(s Submission) error {
	if s.Event.EventUUID == "" || s.Event.CandidateKey == "" || s.Event.Timestamp == "" {
		return fmt.Errorf("edgebacklog: event_uuid, candidate_key and timestamp are required")
	}
	if s.Capture != nil {
		if err := validateEvidence(s.Capture); err != nil {
			return err
		}
	}
	if s.Clip != nil {
		if err := validateEvidence(s.Clip); err != nil {
			return err
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, r := range b.queue {
		if r.Submission.Event.EventUUID == s.Event.EventUUID {
			if submissionsEquivalent(r.Submission, s) {
				return nil
			}
			return fmt.Errorf("%w: %s", ErrSubmissionConflict, s.Event.EventUUID)
		}
	}
	bytes := pendingBytes(b.queue) + submissionBytes(s)
	if len(b.queue) >= b.cfg.MaxOperations || bytes > b.cfg.MaxBytes {
		b.drops++
		return ErrFull
	}
	b.counter++
	r := record{Sequence: b.counter, CreatedAt: time.Now().UTC(), Stage: "metadata", Submission: s}
	if err := b.writeLocked(r); err != nil {
		// The submission is refused, but the failure is also recorded so
		// /status reports a full disk even when the caller only logs the
		// returned error. This is deliberately NOT a capacity drop: Drops
		// counts submissions rejected by MaxOperations/MaxBytes, and
		// conflating the two would hide which bound actually refused work.
		b.recordPersistFailureLocked(err)
		return err
	}
	b.diskFull = false
	b.queue = append(b.queue, r)
	return nil
}

func validateEvidence(e *Evidence) error {
	if e == nil || e.Path == "" || e.SHA256 == "" || e.Size < 0 {
		return fmt.Errorf("edgebacklog: invalid evidence reference")
	}
	info, err := os.Stat(e.Path)
	if err != nil {
		return fmt.Errorf("edgebacklog: evidence: %w", err)
	}
	if info.Size() != e.Size {
		return fmt.Errorf("edgebacklog: evidence size changed")
	}
	return nil
}

func (b *Backlog) ProcessOne(ctx context.Context, sender transport.LocalEventSender, deviceID, credential string) bool {
	b.mu.Lock()
	if len(b.queue) == 0 || (!b.queue[0].NextAttempt.IsZero() && time.Now().Before(b.queue[0].NextAttempt)) {
		b.mu.Unlock()
		return false
	}
	r := b.queue[0]
	b.mu.Unlock()
	err := b.send(ctx, sender, deviceID, credential, r)
	b.mu.Lock()
	defer b.mu.Unlock()
	if err == nil {
		now := time.Now().UTC()
		b.lastSuccess = &now
		b.lastError = ""
		b.degraded = false
		switch r.Stage {
		case "metadata":
			r.Stage = nextStage(r.Submission, "capture")
		case "capture":
			r.Stage = nextStage(r.Submission, "clip")
		default:
			b.removeLocked(r)
			b.notifySynced(r)
			return true
		}
		if r.Stage == "complete" {
			b.removeLocked(r)
			b.notifySynced(r)
			return true
		}
		b.queue[0] = r
		// A failed stage-advance write used to be discarded with `_ =`. The
		// record stays in memory and the send itself succeeded, so the run
		// continues — but on the next restart recover() would load the older
		// stage from disk and re-upload a stage the SaaS already accepted.
		// That is survivable (the SaaS is idempotent per stage) yet it must
		// not be invisible, so it is counted and classified.
		b.persistLocked(r)
		return true
	}
	b.lastError = safeError(err)
	if errors.Is(err, transport.ErrInvalidRequest) || errors.Is(err, transport.ErrUnexpectedStatus) {
		reason := b.lastError
		b.quarantineLocked(r)
		if b.onQuarantined != nil {
			b.onQuarantined(r.Submission.Event.EventUUID, reason)
		}
		return true
	}
	r.Attempts++
	delay := b.backoff(r.Attempts)
	var rle *transport.RateLimitError
	if errors.As(err, &rle) && rle.RetryAfter > 0 {
		delay = rle.RetryAfter
		if delay > b.cfg.RetryMax {
			delay = b.cfg.RetryMax
		}
	}
	r.NextAttempt = time.Now().Add(delay)
	if errors.Is(err, transport.ErrUnauthorized) {
		b.degraded = true
		r.NextAttempt = time.Now().Add(b.cfg.RetryMax)
	}
	b.queue[0] = r
	b.persistLocked(r)
	return true
}

func (b *Backlog) send(ctx context.Context, sender transport.LocalEventSender, deviceID, credential string, r record) error {
	switch r.Stage {
	case "metadata":
		return sender.PostLocalEvent(ctx, deviceID, credential, r.Submission.Event)
	case "capture", "clip":
		var e *Evidence
		if r.Stage == "capture" {
			e = r.Submission.Capture
		} else {
			e = r.Submission.Clip
		}
		if err := validateEvidence(e); err != nil {
			return fmt.Errorf("%w: %v", transport.ErrInvalidRequest, err)
		}
		data, err := os.ReadFile(e.Path)
		if err != nil {
			return fmt.Errorf("%w: evidence read", transport.ErrInvalidRequest)
		}
		return sender.PutLocalEventEvidence(ctx, deviceID, credential, r.Submission.Event.EventUUID, r.Submission.Event.CandidateKey, r.Stage, data, e.SHA256, e.Size)
	default:
		return fmt.Errorf("%w: unknown stage", transport.ErrInvalidRequest)
	}
}

func nextStage(s Submission, preferred string) string {
	if preferred == "capture" && s.Capture != nil {
		return "capture"
	}
	if s.Clip != nil {
		return "clip"
	}
	return "complete"
}
func (b *Backlog) backoff(attempt int) time.Duration {
	d := b.cfg.RetryBase
	for i := 1; i < attempt && d < b.cfg.RetryMax; i++ {
		d *= 2
	}
	if d > b.cfg.RetryMax {
		return b.cfg.RetryMax
	}
	return d
}
func (b *Backlog) path(r record) string {
	return filepath.Join(b.cfg.Dir, "pending", fmt.Sprintf("%020d.json", r.Sequence))
}

// writeLocked persists one record atomically: temp file, then rename. The
// temp file is removed when the write itself fails, so a sustained ENOSPC
// cannot accumulate orphan *.json.tmp files in pending/ — recover() ignores
// them by design (a partial record must never be loaded as if it were
// complete), which previously meant they leaked forever.
//
// The returned error is classified with platform.WrapDiskError so a caller
// can tell "the disk is full" from any other write failure.
func (b *Backlog) writeLocked(r record) error {
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	tmp := b.path(r) + ".tmp"
	if err = b.writeFile(tmp, data, 0o640); err != nil {
		_ = b.removeFile(tmp)
		return platform.WrapDiskError(err)
	}
	if err = b.renameFile(tmp, b.path(r)); err != nil {
		_ = b.removeFile(tmp)
		return platform.WrapDiskError(err)
	}
	return nil
}

// persistLocked is writeLocked plus the accounting a persistence failure
// needs to stop being invisible. It never returns an error because every
// caller is on a path that must keep making progress (the record stays in
// memory and is retried); the failure is recorded, counted, and surfaced.
func (b *Backlog) persistLocked(r record) {
	if err := b.writeLocked(r); err != nil {
		b.recordPersistFailureLocked(err)
		return
	}
	// Recovery is explicit: the next successful persistence clears the
	// disk-full flag instead of leaving the backlog latched.
	b.diskFull = false
}

// recordPersistFailureLocked records a failed persistence operation and
// classifies it. Must be called with b.mu held.
func (b *Backlog) recordPersistFailureLocked(err error) {
	if err == nil {
		return
	}
	b.persistErrors++
	b.diskFull = platform.IsDiskFull(err)
	b.lastError = boundedError(err)
}

// removeLocked deletes a record that has been fully synced. A failed delete
// is not harmless: the file survives, so after a restart recover() would
// queue the event again and the SaaS would receive a duplicate upload. The
// SaaS side is idempotent on event_uuid, so a duplicate is survivable — but
// it must never be silent.
func (b *Backlog) removeLocked(r record) {
	if err := b.removeFile(b.path(r)); err != nil && !os.IsNotExist(err) {
		b.recordPersistFailureLocked(platform.WrapDiskError(err))
	}
	b.queue = b.queue[1:]
}

func (b *Backlog) notifySynced(r record) {
	if b.onSynced != nil {
		b.onSynced(r.Submission.Event.EventUUID)
	}
}

// quarantineLocked moves a permanently-rejected record out of the pending
// queue and into quarantine/, where it is kept for operator forensics.
//
// A failed move must not silently resurrect the record: the in-memory queue
// drops it either way, so if the rename did not happen the file is still in
// pending/ and a restart would re-queue an operation the SaaS already refused
// forever. The fallback delete is therefore the honest completion of the
// move, and both outcomes are counted.
func (b *Backlog) quarantineLocked(r record) {
	src := b.path(r)
	dst := filepath.Join(b.cfg.Dir, "quarantine", fmt.Sprintf("%020d.json", r.Sequence))
	if err := b.renameFile(src, dst); err != nil {
		if rmErr := b.removeFile(src); rmErr != nil && !os.IsNotExist(rmErr) {
			b.recordPersistFailureLocked(platform.WrapDiskError(rmErr))
		} else {
			// The record could not be archived, but it is gone from the
			// retry path; surface it without classing it as a full disk.
			b.recordPersistFailureLocked(fmt.Errorf("edgebacklog: quarantine move failed: %w", err))
		}
		b.queue = b.queue[1:]
		return
	}
	b.queue = b.queue[1:]
	b.quarantineSeqs = append(b.quarantineSeqs, r.Sequence)
	sort.Slice(b.quarantineSeqs, func(i, j int) bool { return b.quarantineSeqs[i] < b.quarantineSeqs[j] })
	b.quarantined = len(b.quarantineSeqs)
	_ = b.enforceQuarantineBoundLocked()
}

func (b *Backlog) Status() Status {
	b.mu.Lock()
	defer b.mu.Unlock()
	st := Status{
		BacklogCount:        len(b.queue),
		PendingBytes:        pendingBytes(b.queue),
		LastSuccess:         b.lastSuccess,
		LastError:           b.lastError,
		Degraded:            b.degraded,
		Quarantined:         b.quarantined,
		Capacity:            b.cfg.MaxOperations,
		Drops:               b.drops,
		PersistErrors:       b.persistErrors,
		DiskFull:            b.diskFull,
		QuarantineEvicted:   b.quarantineEvicted,
		RecoveredOperations: b.recoveredOps,
	}
	st.OverCapacity = st.BacklogCount > b.cfg.MaxOperations || st.PendingBytes > b.cfg.MaxBytes
	if len(b.queue) > 0 {
		t := b.queue[0].CreatedAt
		st.OldestPending = &t
	}
	return st
}

// Drops returns the count of submissions dropped due to capacity bounds.
func (b *Backlog) Drops() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.drops
}
func pendingBytes(q []record) int64 {
	var n int64
	for _, r := range q {
		n += submissionBytes(r.Submission)
	}
	return n
}
func submissionBytes(s Submission) int64 {
	var n int64
	if s.Capture != nil {
		n += s.Capture.Size
	}
	if s.Clip != nil {
		n += s.Clip.Size
	}
	return n
}
func safeError(err error) string {
	if err == nil {
		return ""
	}
	return boundedError(err)
}

// boundedError renders an error for the operator-visible Status.LastError
// field, truncated to maxErrorBytes. Persistence errors carry a filesystem
// path and an errno — never event metadata, a payload or a credential — but a
// bound keeps /status predictable regardless of what the OS reports.
func boundedError(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	if len(msg) > maxErrorBytes {
		return msg[:maxErrorBytes]
	}
	return msg
}

// SHA256File returns immutable evidence metadata for a completed producer file.
func SHA256File(path string) (string, int64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", 0, err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), int64(len(data)), nil
}

func submissionsEquivalent(a, b Submission) bool {
	if a.Event.EventUUID != b.Event.EventUUID ||
		a.Event.CandidateKey != b.Event.CandidateKey ||
		a.Event.Class != b.Event.Class ||
		a.Event.Confidence != b.Event.Confidence ||
		a.Event.Timestamp != b.Event.Timestamp ||
		a.Event.CorrelationID != b.Event.CorrelationID ||
		!reflect.DeepEqual(a.Event.BBox, b.Event.BBox) {
		return false
	}
	if !evidenceEquivalent(a.Capture, b.Capture) {
		return false
	}
	if !evidenceEquivalent(a.Clip, b.Clip) {
		return false
	}
	return true
}

func evidenceEquivalent(a, b *Evidence) bool {
	if (a == nil) != (b == nil) {
		return false
	}
	if a != nil && b != nil {
		if a.SHA256 != b.SHA256 || a.Size != b.Size || a.DurationMS != b.DurationMS {
			return false
		}
	}
	return true
}
