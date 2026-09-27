package auditjournal

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	// DirName is the subdirectory under GEOCAM_DATA_DIR holding the journal.
	DirName = "audit"
	// FileName is the journal's filename within DirName.
	FileName = "security-audit.jsonl"

	dirPerm  = 0o700
	filePerm = 0o600
)

// Health describes the journal's current operational state — safe to expose
// on /status as-is, never derived from record contents.
type Health string

const (
	HealthHealthy     Health = "AUDIT_HEALTHY"
	HealthDegraded    Health = "AUDIT_DEGRADED"
	HealthCorrupt     Health = "AUDIT_CORRUPT"
	HealthWriteFailed Health = "AUDIT_WRITE_FAILED"
)

// Snapshot is a cheap, pre-computed, safe-to-expose view of journal state.
// It is updated in memory on every Append/Open — /status must never trigger
// a full journal re-scan.
type Snapshot struct {
	Health       Health `json:"health"`
	Records      uint64 `json:"records"`
	LastSequence uint64 `json:"last_sequence"`
	LastWriteAt  string `json:"last_write_at,omitempty"`
}

// syncWriteCloser is the minimal seam Journal needs from its backing file.
// Satisfied by *os.File; a fake implementation lets tests exercise disk-full
// / IO-failure paths without needing an actual full disk (see B14).
type syncWriteCloser interface {
	Write(p []byte) (int, error)
	Sync() error
	Close() error
}

// Journal is a durable, local, hash-chained, append-only security audit
// journal. See the package doc comment for its threat model.
//
// Concurrency: a single mutex serializes every Append end to end (sequence
// assignment, hashing, the write syscall, and fsync). Security audit events
// are low-volume by design, so a global lock is the deliberately simple,
// obviously-correct choice here — see docs/security/audit.md's performance
// notes for the measured cost.
// ponytail: global lock serializes all appends; move to per-shard/striped
// locking if audit volume ever becomes hot-path (not expected — see B23).
type Journal struct {
	mu       sync.Mutex
	w        syncWriteCloser
	path     string
	lastHash string
	lastSeq  uint64
	health   Health
	writeAt  time.Time
}

// Open opens (creating if necessary) the durable audit journal under
// dataDir, replaying any existing records to recover the last sequence/hash
// so a new write continues the existing chain rather than restarting it.
func Open(dataDir string) (*Journal, error) {
	dir := filepath.Join(dataDir, DirName)
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return nil, fmt.Errorf("auditjournal: create directory %s: %w", dir, err)
	}
	path := filepath.Join(dir, FileName)

	lastHash, lastSeq, health, err := recoverState(path)
	if err != nil {
		return nil, err
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, filePerm)
	if err != nil {
		return nil, fmt.Errorf("auditjournal: open journal: %w", err)
	}
	if err := f.Chmod(filePerm); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("auditjournal: secure journal permissions: %w", err)
	}

	return newJournal(f, path, lastHash, lastSeq, health), nil
}

func newJournal(w syncWriteCloser, path, lastHash string, lastSeq uint64, health Health) *Journal {
	return &Journal{w: w, path: path, lastHash: lastHash, lastSeq: lastSeq, health: health}
}

// recoverState replays path (if it exists) to compute the last valid
// sequence/hash a new Append must continue from. A missing file is a valid,
// empty (genesis) journal. A TRUNCATED_LAST_WRITE is recovered from its
// valid prefix (the incomplete tail is simply never counted) and reported
// as AUDIT_DEGRADED rather than blocking startup. Any other corruption is a
// hard error: Open refuses to silently start appending onto a broken chain.
func recoverState(path string) (lastHash string, lastSeq uint64, health Health, err error) {
	verifyResult, err := VerifyFile(path)
	if err != nil {
		return "", 0, HealthCorrupt, err
	}
	switch verifyResult.Status {
	case StatusEmpty, StatusPass:
		return verifyResult.LastHash, verifyResult.LastSequence, HealthHealthy, nil
	case StatusTruncatedTail:
		return verifyResult.LastHash, verifyResult.LastSequence, HealthDegraded, nil
	default: // StatusCorrupt
		return "", 0, HealthCorrupt, fmt.Errorf("auditjournal: refusing to open corrupt journal %s: %s", path, verifyResult.FailureReason)
	}
}

// Append assigns the next sequence number, timestamp, and hash to rec,
// writes it durably (write + fsync), and returns the fully populated
// record. SafeReason is redacted unconditionally before it is hashed or
// written — callers cannot opt out.
//
// A write or fsync failure sets Health to AUDIT_WRITE_FAILED and is
// returned to the caller; it never panics and never silently drops the
// event.
func (j *Journal) Append(rec Record) (Record, error) {
	j.mu.Lock()
	defer j.mu.Unlock()

	rec.SchemaVersion = SchemaVersion
	rec.Sequence = j.lastSeq + 1
	rec.Timestamp = time.Now().UTC().Format(time.RFC3339Nano)
	rec.SafeReason = RedactSafeReason(rec.SafeReason)
	rec.PrevHash = j.lastHash
	rec.RecordHash = ""

	hash, err := computeRecordHash(j.lastHash, rec)
	if err != nil {
		j.health = HealthWriteFailed
		return Record{}, err
	}
	rec.RecordHash = hash

	line, err := jsonMarshalLine(rec)
	if err != nil {
		j.health = HealthWriteFailed
		return Record{}, fmt.Errorf("auditjournal: encode record: %w", err)
	}

	if _, err := j.w.Write(line); err != nil {
		j.health = HealthWriteFailed
		return Record{}, fmt.Errorf("auditjournal: write record: %w", err)
	}
	if err := j.w.Sync(); err != nil {
		j.health = HealthWriteFailed
		return Record{}, fmt.Errorf("auditjournal: fsync record: %w", err)
	}

	j.lastHash = rec.RecordHash
	j.lastSeq = rec.Sequence
	j.writeAt = time.Now().UTC()
	j.health = HealthHealthy
	return rec, nil
}

// Snapshot returns a cheap, safe-to-expose view of current journal state.
func (j *Journal) Snapshot() Snapshot {
	j.mu.Lock()
	defer j.mu.Unlock()
	s := Snapshot{Health: j.health, Records: j.lastSeq, LastSequence: j.lastSeq}
	if !j.writeAt.IsZero() {
		s.LastWriteAt = j.writeAt.Format(time.RFC3339)
	}
	return s
}

// Path returns the journal's on-disk path (for `audit verify`/`audit status`).
func (j *Journal) Path() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.path
}

// Close releases the underlying file handle.
func (j *Journal) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.w.Close()
}

func jsonMarshalLine(rec Record) ([]byte, error) {
	data, err := json.Marshal(rec)
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}
