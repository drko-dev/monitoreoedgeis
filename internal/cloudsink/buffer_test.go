package cloudsink

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func mustOpenBuffer(t *testing.T, maxBytes int64, maxFrames int, maxAge time.Duration) (*Buffer, string) {
	t.Helper()
	dir := t.TempDir()
	b, err := OpenBuffer(dir, maxBytes, maxFrames, maxAge)
	if err != nil {
		t.Fatalf("OpenBuffer() error = %v", err)
	}
	return b, dir
}

func TestBuffer_EnqueuePeekAdvance_FIFO(t *testing.T) {
	b, _ := mustOpenBuffer(t, 1<<20, 10, 0)

	for i := uint64(1); i <= 3; i++ {
		f := BufferedFrame{CandidateKey: "cam-1", Seq: i, Timestamp: time.Now(), JPEG: []byte{byte(i)}}
		if err := b.Enqueue(f); err != nil {
			t.Fatalf("Enqueue(%d) error = %v", i, err)
		}
	}

	for want := uint64(1); want <= 3; want++ {
		f, ok, err := b.Peek()
		if err != nil || !ok {
			t.Fatalf("Peek() = (%v, %v, %v), want a frame", f, ok, err)
		}
		if f.Seq != want {
			t.Fatalf("Peek() Seq = %d, want %d (FIFO order)", f.Seq, want)
		}
		b.Advance()
	}

	if _, ok, _ := b.Peek(); ok {
		t.Fatal("Peek() after draining everything: want ok=false")
	}
}

func TestBuffer_UploadOK_NeverEnqueues(t *testing.T) {
	// Route()-level behavior (upload succeeds => never touches the buffer)
	// is covered in cloudsink_test.go; this only pins Buffer's own
	// contract: nothing enters the queue unless Enqueue is called.
	b, _ := mustOpenBuffer(t, 1<<20, 10, 0)
	if _, ok, _ := b.Peek(); ok {
		t.Fatal("freshly opened buffer: want empty")
	}
}

func TestBuffer_MetadataPreservedExactly(t *testing.T) {
	b, _ := mustOpenBuffer(t, 1<<20, 10, 0)
	ts := time.Date(2026, 9, 17, 10, 30, 0, 123456000, time.UTC)
	jpeg := []byte{1, 2, 3, 4, 5}
	if err := b.Enqueue(BufferedFrame{CandidateKey: "cam-9", Seq: 77, Timestamp: ts, JPEG: jpeg}); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}

	f, ok, err := b.Peek()
	if err != nil || !ok {
		t.Fatalf("Peek() = (%v, %v, %v)", f, ok, err)
	}
	if f.CandidateKey != "cam-9" || f.Seq != 77 || !f.Timestamp.Equal(ts) {
		t.Fatalf("metadata not preserved: got %+v", f)
	}
	if string(f.JPEG) != string(jpeg) {
		t.Fatalf("JPEG bytes not preserved: got %v, want %v", f.JPEG, jpeg)
	}
}

func TestBuffer_RestartRecoversSpool(t *testing.T) {
	b, dir := mustOpenBuffer(t, 1<<20, 10, 0)
	for i := uint64(1); i <= 2; i++ {
		if err := b.Enqueue(BufferedFrame{CandidateKey: "cam-1", Seq: i, Timestamp: time.Now(), JPEG: []byte{byte(i)}}); err != nil {
			t.Fatalf("Enqueue(%d) error = %v", i, err)
		}
	}

	reopened, err := OpenBuffer(dir, 1<<20, 10, 0)
	if err != nil {
		t.Fatalf("OpenBuffer() (recovery) error = %v", err)
	}
	stats := reopened.Stats()
	if stats.BufferedFrames != 2 {
		t.Fatalf("BufferedFrames after recovery = %d, want 2", stats.BufferedFrames)
	}
	f, ok, err := reopened.Peek()
	if err != nil || !ok || f.Seq != 1 {
		t.Fatalf("Peek() after recovery = (%+v, %v, %v), want Seq=1", f, ok, err)
	}
}

func TestBuffer_RestartDoesNotDuplicateMetadata(t *testing.T) {
	b, dir := mustOpenBuffer(t, 1<<20, 10, 0)
	if err := b.Enqueue(BufferedFrame{CandidateKey: "cam-1", Seq: 5, Timestamp: time.Now(), JPEG: []byte{9}}); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}

	reopened, err := OpenBuffer(dir, 1<<20, 10, 0)
	if err != nil {
		t.Fatalf("OpenBuffer() error = %v", err)
	}
	if got := reopened.Stats().BufferedFrames; got != 1 {
		t.Fatalf("BufferedFrames after recovery = %d, want exactly 1 (no duplication)", got)
	}
}

func TestBuffer_BoundedByMaxFrames(t *testing.T) {
	b, _ := mustOpenBuffer(t, 1<<20, 2, 0)
	for i := uint64(1); i <= 2; i++ {
		if err := b.Enqueue(BufferedFrame{CandidateKey: "cam-1", Seq: i, JPEG: []byte{1}}); err != nil {
			t.Fatalf("Enqueue(%d) error = %v", i, err)
		}
	}
	if err := b.Enqueue(BufferedFrame{CandidateKey: "cam-1", Seq: 3, JPEG: []byte{1}}); err != ErrBufferFull {
		t.Fatalf("Enqueue() over max frames: err = %v, want ErrBufferFull", err)
	}
	if got := b.Stats().DroppedFull; got != 1 {
		t.Fatalf("DroppedFull = %d, want 1", got)
	}
}

func TestBuffer_BoundedByMaxBytes(t *testing.T) {
	b, _ := mustOpenBuffer(t, 10, 100, 0)
	if err := b.Enqueue(BufferedFrame{CandidateKey: "cam-1", Seq: 1, JPEG: make([]byte, 8)}); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	if err := b.Enqueue(BufferedFrame{CandidateKey: "cam-1", Seq: 2, JPEG: make([]byte, 8)}); err != ErrBufferFull {
		t.Fatalf("Enqueue() over max bytes: err = %v, want ErrBufferFull", err)
	}
}

func TestBuffer_FullBufferPolicy_DropsIncomingNotOldest(t *testing.T) {
	// Explicit, tested full-buffer policy: the *new* frame is dropped, the
	// buffer's existing FIFO order is left untouched.
	b, _ := mustOpenBuffer(t, 1<<20, 1, 0)
	if err := b.Enqueue(BufferedFrame{CandidateKey: "cam-1", Seq: 1, JPEG: []byte{1}}); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	if err := b.Enqueue(BufferedFrame{CandidateKey: "cam-1", Seq: 2, JPEG: []byte{2}}); err != ErrBufferFull {
		t.Fatalf("Enqueue() over capacity: err = %v, want ErrBufferFull", err)
	}
	f, ok, err := b.Peek()
	if err != nil || !ok || f.Seq != 1 {
		t.Fatalf("Peek() after drop = (%+v, %v, %v), want the original Seq=1 untouched", f, ok, err)
	}
}

func TestBuffer_CorruptEntryDoesNotBlockQueue(t *testing.T) {
	b, dir := mustOpenBuffer(t, 1<<20, 10, 0)
	if err := b.Enqueue(BufferedFrame{CandidateKey: "cam-1", Seq: 1, JPEG: []byte{1}}); err != nil {
		t.Fatalf("Enqueue(1) error = %v", err)
	}
	if err := b.Enqueue(BufferedFrame{CandidateKey: "cam-1", Seq: 2, JPEG: []byte{2}}); err != nil {
		t.Fatalf("Enqueue(2) error = %v", err)
	}

	// Corrupt the first (oldest) entry on disk directly.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("ReadDir() = %d entries, want 2", len(entries))
	}
	if err := os.WriteFile(filepath.Join(dir, entries[0].Name()), []byte("not json\nbroken"), 0o640); err != nil {
		t.Fatalf("corrupting entry: %v", err)
	}

	f, ok, err := b.Peek()
	if err != nil || !ok || f.Seq != 2 {
		t.Fatalf("Peek() after corrupt front entry = (%+v, %v, %v), want to skip to Seq=2", f, ok, err)
	}
	if got := b.Stats().CorruptEntries; got != 1 {
		t.Fatalf("CorruptEntries = %d, want 1", got)
	}
}

func TestBuffer_RecoverySkipsCorruptFileWithoutBlockingOthers(t *testing.T) {
	dir := t.TempDir()
	b, err := OpenBuffer(dir, 1<<20, 10, 0)
	if err != nil {
		t.Fatalf("OpenBuffer() error = %v", err)
	}
	if err := b.Enqueue(BufferedFrame{CandidateKey: "cam-1", Seq: 1, JPEG: []byte{1}}); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	// A second, hand-crafted corrupt file dropped straight into the spool
	// dir, as if left behind by an earlier crash.
	if err := os.WriteFile(filepath.Join(dir, "00000000000000000002.frame"), []byte("garbage"), 0o640); err != nil {
		t.Fatalf("writing corrupt file: %v", err)
	}

	reopened, err := OpenBuffer(dir, 1<<20, 10, 0)
	if err != nil {
		t.Fatalf("OpenBuffer() (recovery) error = %v", err)
	}
	stats := reopened.Stats()
	if stats.BufferedFrames != 1 {
		t.Fatalf("BufferedFrames after recovery = %d, want 1 (corrupt one dropped)", stats.BufferedFrames)
	}
	if stats.CorruptEntries != 1 {
		t.Fatalf("CorruptEntries after recovery = %d, want 1", stats.CorruptEntries)
	}
}

func TestBuffer_MaxAgeEvictsStaleEntries(t *testing.T) {
	b, _ := mustOpenBuffer(t, 1<<20, 10, time.Minute)
	old := time.Now().Add(-2 * time.Hour)
	if err := b.Enqueue(BufferedFrame{CandidateKey: "cam-1", Seq: 1, Timestamp: old, JPEG: []byte{1}}); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	if err := b.Enqueue(BufferedFrame{CandidateKey: "cam-1", Seq: 2, Timestamp: time.Now(), JPEG: []byte{2}}); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}

	f, ok, err := b.Peek()
	if err != nil || !ok || f.Seq != 2 {
		t.Fatalf("Peek() = (%+v, %v, %v), want the stale Seq=1 evicted and Seq=2 returned", f, ok, err)
	}
}

func TestBuffer_HasPending(t *testing.T) {
	b, _ := mustOpenBuffer(t, 1<<20, 10, 0)
	if b.HasPending("cam-1") {
		t.Fatal("HasPending() on empty buffer: want false")
	}
	if err := b.Enqueue(BufferedFrame{CandidateKey: "cam-1", Seq: 1, JPEG: []byte{1}}); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	if !b.HasPending("cam-1") {
		t.Fatal("HasPending(cam-1): want true")
	}
	if b.HasPending("cam-2") {
		t.Fatal("HasPending(cam-2): want false (different camera)")
	}
}

func TestBuffer_DiscardDoesNotCountAsReplayed(t *testing.T) {
	b, _ := mustOpenBuffer(t, 1<<20, 10, 0)
	if err := b.Enqueue(BufferedFrame{CandidateKey: "cam-1", Seq: 1, JPEG: []byte{1}}); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	b.Discard()
	stats := b.Stats()
	if stats.ReplayedFrames != 0 {
		t.Fatalf("ReplayedFrames after Discard() = %d, want 0", stats.ReplayedFrames)
	}
	if stats.BufferedFrames != 0 {
		t.Fatalf("BufferedFrames after Discard() = %d, want 0", stats.BufferedFrames)
	}
}

func TestBuffer_NoSecretsPersisted(t *testing.T) {
	b, dir := mustOpenBuffer(t, 1<<20, 10, 0)
	if err := b.Enqueue(BufferedFrame{CandidateKey: "cam-1", Seq: 1, JPEG: []byte("jpeg-bytes")}); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("ReadFile(%s) error = %v", e.Name(), err)
		}
		for _, forbidden := range []string{"Bearer", "credential", "password", "token"} {
			if bytesContainsFold(data, forbidden) {
				t.Fatalf("spooled file %s contains forbidden substring %q", e.Name(), forbidden)
			}
		}
	}
}

func bytesContainsFold(data []byte, substr string) bool {
	lower := []byte(substr)
	for i := 0; i+len(lower) <= len(data); i++ {
		match := true
		for j := range lower {
			c := data[i+j]
			if c >= 'A' && c <= 'Z' {
				c += 'a' - 'A'
			}
			want := lower[j]
			if want >= 'A' && want <= 'Z' {
				want += 'a' - 'A'
			}
			if c != want {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func TestBuffer_RaceEnqueueAndPeekAdvance(t *testing.T) {
	b, _ := mustOpenBuffer(t, 1<<20, 1000, 0)
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := uint64(0); i < 200; i++ {
			_ = b.Enqueue(BufferedFrame{CandidateKey: "cam-1", Seq: i, JPEG: []byte{1, 2, 3}})
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			if _, ok, _ := b.Peek(); ok {
				b.Advance()
			}
			_ = b.Stats()
		}
	}()

	wg.Wait()
}
