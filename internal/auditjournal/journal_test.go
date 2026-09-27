package auditjournal

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func newTestJournal(t *testing.T) (*Journal, string) {
	t.Helper()
	dataDir := t.TempDir()
	j, err := Open(dataDir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = j.Close() })
	return j, filepath.Join(dataDir, DirName, FileName)
}

func TestOpen_Genesis(t *testing.T) {
	j, path := newTestJournal(t)
	snap := j.Snapshot()
	if snap.Health != HealthHealthy {
		t.Fatalf("health = %s, want %s", snap.Health, HealthHealthy)
	}
	if snap.Records != 0 || snap.LastSequence != 0 {
		t.Fatalf("expected empty journal, got %+v", snap)
	}
	if _, err := os.Stat(filepath.Dir(path)); err != nil {
		t.Fatalf("audit dir not created: %v", err)
	}
}

func TestAppend_One(t *testing.T) {
	j, path := newTestJournal(t)
	rec, err := j.Append(Record{EventType: EventEnrollmentSuccess, Result: ResultSuccess, EdgeID: "edge-1"})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if rec.Sequence != 1 {
		t.Fatalf("sequence = %d, want 1", rec.Sequence)
	}
	if rec.PrevHash != GenesisHash {
		t.Fatalf("prev_hash = %s, want genesis", rec.PrevHash)
	}
	if rec.RecordHash == "" {
		t.Fatal("record_hash must not be empty")
	}

	result, err := VerifyFile(path)
	if err != nil {
		t.Fatalf("VerifyFile: %v", err)
	}
	if result.Status != StatusPass || result.RecordCount != 1 {
		t.Fatalf("verify = %+v, want PASS with 1 record", result)
	}
}

func TestAppend_Many(t *testing.T) {
	j, path := newTestJournal(t)
	const n = 50
	for i := 0; i < n; i++ {
		if _, err := j.Append(Record{EventType: EventControlCommandReceived, Result: ResultSuccess}); err != nil {
			t.Fatalf("Append #%d: %v", i, err)
		}
	}
	result, err := VerifyFile(path)
	if err != nil {
		t.Fatalf("VerifyFile: %v", err)
	}
	if result.Status != StatusPass {
		t.Fatalf("status = %s, want PASS: %s", result.Status, result.FailureReason)
	}
	if result.RecordCount != n || result.FirstSequence != 1 || result.LastSequence != n {
		t.Fatalf("unexpected counters: %+v", result)
	}
}

func TestRestart_ContinuesChain(t *testing.T) {
	dataDir := t.TempDir()
	j1, err := Open(dataDir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	rec1, err := j1.Append(Record{EventType: EventEnrollmentSuccess, Result: ResultSuccess})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := j1.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	j2, err := Open(dataDir)
	if err != nil {
		t.Fatalf("re-Open: %v", err)
	}
	defer j2.Close()
	rec2, err := j2.Append(Record{EventType: EventCredentialRotationSuccess, Result: ResultSuccess})
	if err != nil {
		t.Fatalf("Append after restart: %v", err)
	}
	if rec2.Sequence != 2 {
		t.Fatalf("sequence after restart = %d, want 2 (chain must continue, not restart)", rec2.Sequence)
	}
	if rec2.PrevHash != rec1.RecordHash {
		t.Fatalf("prev_hash after restart = %s, want %s", rec2.PrevHash, rec1.RecordHash)
	}

	path := filepath.Join(dataDir, DirName, FileName)
	result, err := VerifyFile(path)
	if err != nil {
		t.Fatalf("VerifyFile: %v", err)
	}
	if result.Status != StatusPass || result.RecordCount != 2 {
		t.Fatalf("verify after restart = %+v", result)
	}
}

func TestDeterministicSerialization(t *testing.T) {
	rec := Record{EventType: EventEnrollmentSuccess, Result: ResultSuccess, EdgeID: "edge-1", DeviceID: "dev-1"}
	h1, err := computeRecordHash(GenesisHash, rec)
	if err != nil {
		t.Fatalf("computeRecordHash: %v", err)
	}
	h2, err := computeRecordHash(GenesisHash, rec)
	if err != nil {
		t.Fatalf("computeRecordHash: %v", err)
	}
	if h1 != h2 {
		t.Fatalf("same logical record produced different hashes: %s vs %s", h1, h2)
	}

	// Marshaling twice must produce byte-identical output (no map, no
	// nondeterministic field order).
	b1, _ := json.Marshal(rec)
	b2, _ := json.Marshal(rec)
	if string(b1) != string(b2) {
		t.Fatalf("non-deterministic marshaling: %s vs %s", b1, b2)
	}
}

func TestConcurrentWriters_SequenceAndChainStayValid(t *testing.T) {
	j, path := newTestJournal(t)
	const goroutines = 20
	const perGoroutine = 10

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				if _, err := j.Append(Record{EventType: EventControlCommandExecuted, Result: ResultSuccess}); err != nil {
					t.Errorf("concurrent Append: %v", err)
				}
			}
		}()
	}
	wg.Wait()

	result, err := VerifyFile(path)
	if err != nil {
		t.Fatalf("VerifyFile: %v", err)
	}
	want := uint64(goroutines * perGoroutine)
	if result.Status != StatusPass {
		t.Fatalf("status = %s, want PASS: %s", result.Status, result.FailureReason)
	}
	if result.RecordCount != want {
		t.Fatalf("record count = %d, want %d (lost write under concurrency)", result.RecordCount, want)
	}
}

func TestSequenceMonotonic(t *testing.T) {
	j, _ := newTestJournal(t)
	var last uint64
	for i := 0; i < 10; i++ {
		rec, err := j.Append(Record{EventType: EventControlCommandReceived, Result: ResultSuccess})
		if err != nil {
			t.Fatalf("Append: %v", err)
		}
		if rec.Sequence != last+1 {
			t.Fatalf("sequence %d, want %d", rec.Sequence, last+1)
		}
		last = rec.Sequence
	}
}

func TestPermissions_DirAndFileAreRestrictive(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits are not enforced")
	}
	_, path := newTestJournal(t)

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat file: %v", err)
	}
	if fi.Mode().Perm() != filePerm {
		t.Fatalf("file perm = %o, want %o", fi.Mode().Perm(), filePerm)
	}

	di, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if di.Mode().Perm() != dirPerm {
		t.Fatalf("dir perm = %o, want %o", di.Mode().Perm(), dirPerm)
	}
}

func TestOpen_RefusesCorruptJournal(t *testing.T) {
	dataDir := t.TempDir()
	dir := filepath.Join(dataDir, DirName)
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, FileName)

	// A single, well-formed JSON record whose prev_hash does not match
	// genesis: unambiguously corrupt, never explainable as a crash mid-write.
	rec := Record{SchemaVersion: SchemaVersion, Sequence: 1, EventType: EventEnrollmentSuccess, Result: ResultSuccess, PrevHash: "not-genesis", RecordHash: "irrelevant"}
	line, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(line, '\n'), filePerm); err != nil {
		t.Fatal(err)
	}

	_, err = Open(dataDir)
	if err == nil {
		t.Fatal("Open must refuse to silently start appending onto a corrupt journal")
	}
}

// errWriter is a syncWriteCloser test seam simulating disk-full / IO
// failures (B14) without needing an actually full disk.
type errWriter struct {
	writeErr error
	syncErr  error
}

func (e *errWriter) Write(p []byte) (int, error) {
	if e.writeErr != nil {
		return 0, e.writeErr
	}
	return len(p), nil
}
func (e *errWriter) Sync() error  { return e.syncErr }
func (e *errWriter) Close() error { return nil }

func TestAppend_WriteFailureSetsHealthAndReturnsError(t *testing.T) {
	j := newJournal(&errWriter{writeErr: errors.New("no space left on device")}, "", GenesisHash, 0, HealthHealthy)
	_, err := j.Append(Record{EventType: EventControlCommandReceived, Result: ResultFailure})
	if err == nil {
		t.Fatal("expected write error to propagate")
	}
	if snap := j.Snapshot(); snap.Health != HealthWriteFailed {
		t.Fatalf("health = %s, want %s", snap.Health, HealthWriteFailed)
	}
}

func TestAppend_FsyncFailureSetsHealthAndReturnsError(t *testing.T) {
	j := newJournal(&errWriter{syncErr: errors.New("fsync failed")}, "", GenesisHash, 0, HealthHealthy)
	_, err := j.Append(Record{EventType: EventControlCommandReceived, Result: ResultFailure})
	if err == nil {
		t.Fatal("expected fsync error to propagate")
	}
	if snap := j.Snapshot(); snap.Health != HealthWriteFailed {
		t.Fatalf("health = %s, want %s", snap.Health, HealthWriteFailed)
	}
	// A write failure must not corrupt in-memory chain state used by the
	// next attempt: sequence/hash only advance on confirmed success.
	if j.lastSeq != 0 {
		t.Fatalf("lastSeq advanced despite failed append: %d", j.lastSeq)
	}
}
