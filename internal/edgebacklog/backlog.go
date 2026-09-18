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
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/transport"
)

var (
	ErrFull               = errors.New("edgebacklog: bounded backlog is full")
	ErrSubmissionConflict = errors.New("edgebacklog: divergent submission for event")
)

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
	b := &Backlog{cfg: cfg}
	if err := b.recover(); err != nil {
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
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
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
	return nil
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
		return ErrFull
	}
	b.counter++
	r := record{Sequence: b.counter, CreatedAt: time.Now().UTC(), Stage: "metadata", Submission: s}
	if err := b.writeLocked(r); err != nil {
		return err
	}
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
		_ = b.writeLocked(r)
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
	_ = b.writeLocked(r)
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
func (b *Backlog) writeLocked(r record) error {
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	tmp := b.path(r) + ".tmp"
	if err = os.WriteFile(tmp, data, 0o640); err != nil {
		return err
	}
	return os.Rename(tmp, b.path(r))
}
func (b *Backlog) removeLocked(r record) { _ = os.Remove(b.path(r)); b.queue = b.queue[1:] }

func (b *Backlog) notifySynced(r record) {
	if b.onSynced != nil {
		b.onSynced(r.Submission.Event.EventUUID)
	}
}
func (b *Backlog) quarantineLocked(r record) {
	_ = os.Rename(b.path(r), filepath.Join(b.cfg.Dir, "quarantine", fmt.Sprintf("%020d.json", r.Sequence)))
	b.queue = b.queue[1:]
	b.quarantined++
}

func (b *Backlog) Status() Status {
	b.mu.Lock()
	defer b.mu.Unlock()
	st := Status{BacklogCount: len(b.queue), PendingBytes: pendingBytes(b.queue), LastSuccess: b.lastSuccess, LastError: b.lastError, Degraded: b.degraded, Quarantined: b.quarantined}
	if len(b.queue) > 0 {
		t := b.queue[0].CreatedAt
		st.OldestPending = &t
	}
	return st
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
	return err.Error()
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
		a.Event.Timestamp != b.Event.Timestamp {
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
		if a.SHA256 != b.SHA256 || a.Size != b.Size {
			return false
		}
	}
	return true
}
