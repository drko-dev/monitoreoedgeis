package cloudsink

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ErrBufferFull is returned by Buffer.Enqueue when adding a frame would
// exceed maxBytes or maxFrames. The caller's frame is not written; it is
// simply lost, same as an unbuffered upload failure was before I6 — the
// buffer only widens the window before that happens, it never removes it.
var ErrBufferFull = errors.New("cloudsink: buffer full")

// bufferMeta is the small JSON header written before each buffered frame's
// raw JPEG bytes. It never carries deviceID or credential: those are read
// fresh from CloudSink at replay time, never persisted to disk.
type bufferMeta struct {
	CandidateKey string    `json:"candidate_key"`
	Seq          uint64    `json:"seq"`
	Timestamp    time.Time `json:"timestamp"`
	Size         int       `json:"size"`
}

// BufferedFrame is one frame recovered from, or about to enter, the offline
// spool. It carries the already-encoded JPEG (the exact bytes that would
// have been uploaded), never the raw yuv420p.
type BufferedFrame struct {
	CandidateKey string
	Seq          uint64
	Timestamp    time.Time
	JPEG         []byte
}

// BufferStats is a point-in-time snapshot of I6 metrics.
type BufferStats struct {
	BufferedFrames int
	BufferedBytes  int64
	ReplayedFrames int64
	DroppedFull    int64
	CorruptEntries int64
}

const (
	frameFileSuffix = ".frame"
	tmpFileSuffix   = ".tmp"
)

// queuedEntry is the in-memory index for one spooled file. The JPEG bytes
// themselves stay on disk until replay, so the index stays small even for a
// large buffer.
type queuedEntry struct {
	path         string
	candidateKey string
	timestamp    time.Time
	size         int64
}

// Buffer is a bounded, disk-backed FIFO spool for frames whose Cloud upload
// failed for a recoverable reason. Frames are written atomically (tmp file
// + rename) and named by a monotonically increasing counter, so directory
// listing order is always insertion order — including across restarts.
// Since a given camera's frames are always enqueued in the order CloudSink
// received them (Router runs exactly one worker goroutine per sink), this
// single global FIFO order already preserves per-camera order too, with no
// need for a separate queue per candidate_key.
type Buffer struct {
	dir       string
	maxBytes  int64
	maxFrames int
	maxAge    time.Duration
	now       func() time.Time

	mu      sync.Mutex
	queue   []queuedEntry
	bytes   int64
	counter uint64

	replayed       int64
	droppedFull    atomic.Int64
	corruptEntries atomic.Int64
}

// OpenBuffer creates dir if needed and recovers any spool left by a
// previous process: every *.frame file is validated and re-queued in
// filename (counter) order; a corrupt file is discarded instead of
// blocking recovery of the rest. maxAge <= 0 disables age-based eviction.
func OpenBuffer(dir string, maxBytes int64, maxFrames int, maxAge time.Duration) (*Buffer, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("cloudsink: buffer dir: %w", err)
	}
	b := &Buffer{dir: dir, maxBytes: maxBytes, maxFrames: maxFrames, maxAge: maxAge, now: time.Now}
	if err := b.recover(); err != nil {
		return nil, err
	}
	return b, nil
}

func (b *Buffer) recover() error {
	entries, err := os.ReadDir(b.dir)
	if err != nil {
		return fmt.Errorf("cloudsink: buffer recover: %w", err)
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		switch {
		case strings.HasSuffix(e.Name(), tmpFileSuffix):
			// Leftover from a write interrupted by a crash: never valid,
			// never counted.
			_ = os.Remove(filepath.Join(b.dir, e.Name()))
		case strings.HasSuffix(e.Name(), frameFileSuffix):
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	var maxCounter uint64
	for _, name := range names {
		path := filepath.Join(b.dir, name)
		f, err := readEntry(path)
		if err != nil {
			b.corruptEntries.Add(1)
			_ = os.Remove(path)
			continue
		}
		info, err := os.Stat(path)
		if err != nil {
			b.corruptEntries.Add(1)
			_ = os.Remove(path)
			continue
		}
		b.queue = append(b.queue, queuedEntry{
			path:         path,
			candidateKey: f.CandidateKey,
			timestamp:    f.Timestamp,
			size:         info.Size(),
		})
		b.bytes += info.Size()
		if c, ok := counterFromName(name); ok && c > maxCounter {
			maxCounter = c
		}
	}
	b.counter = maxCounter
	return nil
}

func counterFromName(name string) (uint64, bool) {
	raw := strings.TrimSuffix(name, frameFileSuffix)
	c, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, false
	}
	return c, true
}

// Enqueue spools f to disk. It fails with ErrBufferFull without writing
// anything when doing so would exceed maxBytes or maxFrames — the incoming
// frame is dropped, not an older one, mirroring Router.Dispatch's existing
// drop-newest-on-full policy elsewhere in this pipeline.
func (b *Buffer) Enqueue(f BufferedFrame) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	size := int64(len(f.JPEG))
	if len(b.queue)+1 > b.maxFrames || b.bytes+size > b.maxBytes {
		b.droppedFull.Add(1)
		return ErrBufferFull
	}

	b.counter++
	name := fmt.Sprintf("%020d%s", b.counter, frameFileSuffix)
	path := filepath.Join(b.dir, name)
	if err := writeAtomic(path, f); err != nil {
		return fmt.Errorf("cloudsink: buffer enqueue: %w", err)
	}

	b.queue = append(b.queue, queuedEntry{
		path:         path,
		candidateKey: f.CandidateKey,
		timestamp:    f.Timestamp,
		size:         size,
	})
	b.bytes += size
	return nil
}

// HasPending reports whether candidateKey already has at least one frame
// waiting in the buffer. CloudSink uses this to keep a new frame queuing
// behind older ones for the same camera instead of racing ahead with a
// direct upload, which would replay out of order.
func (b *Buffer) HasPending(candidateKey string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, e := range b.queue {
		if e.candidateKey == candidateKey {
			return true
		}
	}
	return false
}

// Peek returns the oldest buffered frame without removing it, discarding
// (and counting) any entry along the way that is too old (maxAge) or
// unreadable (corrupt). ok is false once the buffer is empty.
func (b *Buffer) Peek() (BufferedFrame, bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	for len(b.queue) > 0 {
		e := b.queue[0]
		if b.maxAge > 0 && b.now().Sub(e.timestamp) > b.maxAge {
			b.removeFrontLocked()
			continue
		}
		f, err := readEntry(e.path)
		if err != nil {
			b.corruptEntries.Add(1)
			b.removeFrontLocked()
			continue
		}
		return f, true, nil
	}
	return BufferedFrame{}, false, nil
}

// Advance removes the oldest buffered frame after a successful replay.
func (b *Buffer) Advance() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.queue) == 0 {
		return
	}
	b.removeFrontLocked()
	b.replayed++
}

// Discard removes the oldest buffered frame without counting it as
// replayed — used when a replay attempt fails for a non-recoverable
// reason (e.g. the credential was revoked), so it never blocks every
// frame queued behind it.
func (b *Buffer) Discard() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.queue) == 0 {
		return
	}
	b.removeFrontLocked()
}

// removeFrontLocked removes queue[0] and its backing file. b.mu must
// already be held.
func (b *Buffer) removeFrontLocked() {
	e := b.queue[0]
	b.queue = b.queue[1:]
	b.bytes -= e.size
	_ = os.Remove(e.path)
}

// Stats returns a snapshot of I6 buffering metrics.
func (b *Buffer) Stats() BufferStats {
	b.mu.Lock()
	defer b.mu.Unlock()
	return BufferStats{
		BufferedFrames: len(b.queue),
		BufferedBytes:  b.bytes,
		ReplayedFrames: b.replayed,
		DroppedFull:    b.droppedFull.Load(),
		CorruptEntries: b.corruptEntries.Load(),
	}
}

// writeAtomic writes f to finalPath via a temp file + rename, so a reader
// (including a future recovery pass) never observes a partially written
// entry.
func writeAtomic(finalPath string, f BufferedFrame) error {
	meta := bufferMeta{CandidateKey: f.CandidateKey, Seq: f.Seq, Timestamp: f.Timestamp, Size: len(f.JPEG)}
	header, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("cloudsink: encode buffer header: %w", err)
	}

	tmp := finalPath + tmpFileSuffix
	fh, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC|os.O_EXCL, 0o640)
	if err != nil {
		return err
	}
	if _, err := fh.Write(append(header, '\n')); err != nil {
		fh.Close()
		os.Remove(tmp)
		return err
	}
	if _, err := fh.Write(f.JPEG); err != nil {
		fh.Close()
		os.Remove(tmp)
		return err
	}
	if err := fh.Sync(); err != nil {
		fh.Close()
		os.Remove(tmp)
		return err
	}
	if err := fh.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, finalPath)
}

// readEntry parses one spooled file back into a BufferedFrame. Any
// malformed header, truncated payload, or size mismatch is reported as an
// error — never a partial/garbage frame.
func readEntry(path string) (BufferedFrame, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return BufferedFrame{}, err
	}
	nl := bytes.IndexByte(data, '\n')
	if nl < 0 {
		return BufferedFrame{}, fmt.Errorf("cloudsink: buffer entry %s: missing header", path)
	}
	var meta bufferMeta
	if err := json.Unmarshal(data[:nl], &meta); err != nil {
		return BufferedFrame{}, fmt.Errorf("cloudsink: buffer entry %s: invalid header: %w", path, err)
	}
	payload := data[nl+1:]
	if len(payload) != meta.Size {
		return BufferedFrame{}, fmt.Errorf("cloudsink: buffer entry %s: size mismatch: header=%d actual=%d",
			path, meta.Size, len(payload))
	}
	return BufferedFrame{
		CandidateKey: meta.CandidateKey,
		Seq:          meta.Seq,
		Timestamp:    meta.Timestamp,
		JPEG:         append([]byte(nil), payload...),
	}, nil
}
