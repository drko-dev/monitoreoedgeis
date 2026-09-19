package cloudsink

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/platform"
)

func frameFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), frameFileSuffix) || strings.HasSuffix(e.Name(), tmpFileSuffix) {
			names = append(names, e.Name())
		}
	}
	return names
}

// TestBuffer_EnqueueDiskFullIsClassified covers Y6 on the one buffered path
// that stores JPEG payloads: an out-of-space write must be refused, classified,
// counted nowhere as a capacity drop, and leave no partial or temp file behind.
func TestBuffer_EnqueueDiskFullIsClassified(t *testing.T) {
	dir := t.TempDir()
	b, err := OpenBuffer(dir, 1<<20, 10, 0)
	if err != nil {
		t.Fatal(err)
	}

	if err := b.Enqueue(BufferedFrame{CandidateKey: "cam-1", Seq: 1, Timestamp: time.Now(), JPEG: []byte("ok")}); err != nil {
		t.Fatalf("first enqueue: %v", err)
	}
	before := frameFiles(t, dir)
	if len(before) != 1 {
		t.Fatalf("frame files after a successful enqueue = %v, want 1", before)
	}
	validPath := filepath.Join(dir, before[0])
	validBytes, err := os.ReadFile(validPath)
	if err != nil {
		t.Fatal(err)
	}

	b.writeFn = func(finalPath string, _ BufferedFrame) error {
		return &os.PathError{Op: "write", Path: finalPath, Err: syscall.ENOSPC}
	}
	err = b.Enqueue(BufferedFrame{CandidateKey: "cam-1", Seq: 2, Timestamp: time.Now(), JPEG: []byte("nope")})
	if err == nil {
		t.Fatal("enqueue on a full disk returned nil, want an error")
	}
	if !platform.IsDiskFull(err) {
		t.Errorf("enqueue error %v is not classified as a full disk", err)
	}
	if !errors.Is(err, syscall.ENOSPC) {
		t.Errorf("enqueue error %v lost the raw ENOSPC", err)
	}
	// A write failure is not ErrBufferFull: the frame was not refused because
	// the buffer was full, and conflating the two would misreport capacity.
	if errors.Is(err, ErrBufferFull) {
		t.Error("a disk-full write failure must not be reported as ErrBufferFull")
	}
	if got := b.Stats(); got.BufferedFrames != 1 {
		t.Errorf("buffered_frames = %d, want 1: the refused frame must not be buffered", got.BufferedFrames)
	}
	if got := b.Stats(); got.DroppedFull != 0 {
		t.Errorf("dropped_buffer_full = %d, want 0: a disk-full write is not a capacity drop", got.DroppedFull)
	}

	after := frameFiles(t, dir)
	if len(after) != 1 {
		t.Errorf("frame files after a failed enqueue = %v, want only the pre-existing one", after)
	}
	for _, name := range after {
		if strings.HasSuffix(name, tmpFileSuffix) {
			t.Errorf("orphan temp file %q survived a failed write", name)
		}
	}
	nowBytes, err := os.ReadFile(validPath)
	if err != nil {
		t.Fatalf("the previously buffered frame was destroyed: %v", err)
	}
	if string(nowBytes) != string(validBytes) {
		t.Error("the previously buffered frame was modified by a failed write")
	}

	// Recovery once space returns.
	b.writeFn = writeAtomic
	if err := b.Enqueue(BufferedFrame{CandidateKey: "cam-1", Seq: 3, Timestamp: time.Now(), JPEG: []byte("ok again")}); err != nil {
		t.Fatalf("enqueue after space returned: %v", err)
	}
	if got := b.Stats(); got.BufferedFrames != 2 {
		t.Errorf("buffered_frames = %d, want 2 after recovery", got.BufferedFrames)
	}
}

// TestBuffer_RecoveryTrimsSpoolToMaxFrames covers the recovery-time bound hole:
// recover() loaded every *.frame on disk regardless of the configured bound, so
// a spool written under a larger configuration (or left over from a crash) was
// replayed above its own limit. Recovery now trims oldest-first and counts it.
func TestBuffer_RecoveryTrimsSpoolToMaxFrames(t *testing.T) {
	dir := t.TempDir()
	big, err := OpenBuffer(dir, 1<<20, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 6; i++ {
		if err := big.Enqueue(BufferedFrame{CandidateKey: "cam-1", Seq: uint64(i), Timestamp: time.Now(), JPEG: []byte{byte(i)}}); err != nil {
			t.Fatalf("seed enqueue %d: %v", i, err)
		}
	}
	if got := len(frameFiles(t, dir)); got != 6 {
		t.Fatalf("seeded %d frame files, want 6", got)
	}

	small, err := OpenBuffer(dir, 1<<20, 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	st := small.Stats()
	if st.BufferedFrames != 2 {
		t.Errorf("buffered_frames after recovery = %d, want 2 (the configured bound)", st.BufferedFrames)
	}
	if st.DroppedOverCapacity != 4 {
		t.Errorf("dropped_over_capacity = %d, want 4", st.DroppedOverCapacity)
	}
	if got := len(frameFiles(t, dir)); got != 2 {
		t.Errorf("%d frame files left on disk, want 2: the trim must delete the evicted files", got)
	}

	// Oldest-first: the two newest frames must be the survivors.
	first, ok, err := small.Peek()
	if err != nil || !ok {
		t.Fatalf("Peek() = (%+v, %v, %v)", first, ok, err)
	}
	if first.Seq != 5 {
		t.Errorf("surviving head Seq = %d, want 5 (oldest evicted first)", first.Seq)
	}
}

// TestBuffer_RecoveryTrimsSpoolToMaxBytes is the same property for the byte
// bound, which is the one a small appliance actually hits.
func TestBuffer_RecoveryTrimsSpoolToMaxBytes(t *testing.T) {
	dir := t.TempDir()
	big, err := OpenBuffer(dir, 1<<20, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	payload := make([]byte, 4096)
	for i := 1; i <= 5; i++ {
		if err := big.Enqueue(BufferedFrame{CandidateKey: "cam-1", Seq: uint64(i), Timestamp: time.Now(), JPEG: payload}); err != nil {
			t.Fatalf("seed enqueue %d: %v", i, err)
		}
	}

	// Each entry on disk is header + 4096 bytes, so 10 KiB keeps roughly two.
	small, err := OpenBuffer(dir, 10000, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	st := small.Stats()
	if st.BufferedBytes > 10000 {
		t.Errorf("buffered_bytes after recovery = %d, want <= 10000", st.BufferedBytes)
	}
	if st.DroppedOverCapacity == 0 {
		t.Error("dropped_over_capacity = 0 although the recovered spool exceeded maxBytes")
	}
	if got := len(frameFiles(t, dir)); got != st.BufferedFrames {
		t.Errorf("%d frame files on disk but buffered_frames = %d; the trim must keep them in step", got, st.BufferedFrames)
	}
}

// TestBuffer_AgeEvictionIsCounted closes the silent drop the audit found: the
// stale-frame branch removed an entry with no counter at all, while the
// corrupt-entry branch beside it did count. A frame must never leave the spool
// without a number moving.
func TestBuffer_AgeEvictionIsCounted(t *testing.T) {
	b, _ := mustOpenBuffer(t, 1<<20, 10, time.Minute)
	old := time.Now().Add(-2 * time.Hour)
	if err := b.Enqueue(BufferedFrame{CandidateKey: "cam-1", Seq: 1, Timestamp: old, JPEG: []byte{1}}); err != nil {
		t.Fatal(err)
	}
	if err := b.Enqueue(BufferedFrame{CandidateKey: "cam-1", Seq: 2, Timestamp: time.Now(), JPEG: []byte{2}}); err != nil {
		t.Fatal(err)
	}
	if got := b.Stats().DroppedAge; got != 0 {
		t.Fatalf("dropped_age = %d before any Peek, want 0", got)
	}
	if _, ok, err := b.Peek(); err != nil || !ok {
		t.Fatalf("Peek() = (%v, %v)", ok, err)
	}
	if got := b.Stats().DroppedAge; got != 1 {
		t.Errorf("dropped_age = %d, want 1: the age eviction must be counted", got)
	}
}

// TestBuffer_PermissionFailureIsNotClassifiedAsDiskFull keeps the classifier
// honest: a refused write is only a full disk when the kernel says so.
func TestBuffer_PermissionFailureIsNotClassifiedAsDiskFull(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: a read-only directory stays writable")
	}
	dir := t.TempDir()
	b, err := OpenBuffer(dir, 1<<20, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	err = b.Enqueue(BufferedFrame{CandidateKey: "cam-1", Seq: 1, Timestamp: time.Now(), JPEG: []byte("x")})
	if err == nil {
		t.Skip("the directory stayed writable on this host; skipping rather than asserting a false premise")
	}
	if platform.IsDiskFull(err) {
		t.Errorf("a permission failure was classified as a full disk: %v", err)
	}
}

// TestBuffer_RecoveryTrimIsIdempotentAndStable reopens an already-trimmed spool
// to confirm the trim cannot be re-applied and cannot drift the counters.
func TestBuffer_RecoveryTrimIsIdempotentAndStable(t *testing.T) {
	dir := t.TempDir()
	big, _ := OpenBuffer(dir, 1<<20, 10, 0)
	for i := 1; i <= 5; i++ {
		if err := big.Enqueue(BufferedFrame{CandidateKey: "cam-1", Seq: uint64(i), Timestamp: time.Now(), JPEG: []byte{byte(i)}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := OpenBuffer(dir, 1<<20, 2, 0); err != nil {
		t.Fatal(err)
	}
	again, err := OpenBuffer(dir, 1<<20, 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	st := again.Stats()
	if st.BufferedFrames != 2 {
		t.Errorf("buffered_frames = %d after a second recovery, want 2", st.BufferedFrames)
	}
	if st.DroppedOverCapacity != 0 {
		t.Errorf("dropped_over_capacity = %d on a second recovery, want 0: the trim must not re-fire", st.DroppedOverCapacity)
	}
}
