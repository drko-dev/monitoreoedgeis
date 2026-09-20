package edgebacklog

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/transport"
)

type sender struct {
	calls []string
	errs  []error
}

func (s *sender) PostLocalEvent(_ context.Context, _, _ string, e transport.LocalEvent) error {
	s.calls = append(s.calls, "metadata:"+e.EventUUID)
	return s.next()
}
func (s *sender) PutLocalEventEvidence(_ context.Context, _, _, id, _, kind string, _ []byte, _ string, _ int64) error {
	s.calls = append(s.calls, kind+":"+id)
	return s.next()
}
func (s *sender) next() error {
	if len(s.errs) == 0 {
		return nil
	}
	err := s.errs[0]
	s.errs = s.errs[1:]
	return err
}

func submission(t *testing.T, dir, id string) Submission {
	t.Helper()
	path := filepath.Join(dir, id+".jpg")
	data := []byte("jpeg")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	sum, size, err := SHA256File(path)
	if err != nil {
		t.Fatal(err)
	}
	return Submission{Event: transport.LocalEvent{EventUUID: id, CandidateKey: "camera-a", Timestamp: "2026-01-01T00:00:00Z"}, Capture: &Evidence{Path: path, SHA256: sum, Size: size}}
}
func open(t *testing.T, dir string) *Backlog {
	t.Helper()
	return openWithConfig(t, dir, time.Millisecond, time.Millisecond)
}

func openWithConfig(t *testing.T, dir string, retryBase, retryMax time.Duration) *Backlog {
	t.Helper()
	b, err := Open(Config{Dir: dir, MaxOperations: 2, MaxBytes: 1024, RetryBase: retryBase, RetryMax: retryMax})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestBacklogFIFOAndDuplicate(t *testing.T) {
	d := t.TempDir()
	b := open(t, d)
	if err := b.Enqueue(submission(t, d, "one")); err != nil {
		t.Fatal(err)
	}
	if err := b.Enqueue(submission(t, d, "one")); err != nil {
		t.Fatal(err)
	}
	if err := b.Enqueue(submission(t, d, "two")); err != nil {
		t.Fatal(err)
	}
	s := &sender{}
	for i := 0; i < 6; i++ {
		b.ProcessOne(context.Background(), s, "d", "c")
	}
	want := []string{"metadata:one", "capture:one", "metadata:two", "capture:two"}
	if len(s.calls) != len(want) {
		t.Fatalf("calls=%v", s.calls)
	}
	for i := range want {
		if s.calls[i] != want[i] {
			t.Fatalf("calls=%v", s.calls)
		}
	}
}

func TestBacklogRestartAndRetryClasses(t *testing.T) {
	d := t.TempDir()
	b := open(t, d)
	if err := b.Enqueue(submission(t, d, "one")); err != nil {
		t.Fatal(err)
	}
	b = open(t, d)
	s := &sender{errs: []error{transport.ErrRetryableStatus}}
	b.ProcessOne(context.Background(), s, "d", "c")
	if got := b.Status().BacklogCount; got != 1 {
		t.Fatalf("count=%d", got)
	}
	time.Sleep(2 * time.Millisecond)
	b.ProcessOne(context.Background(), s, "d", "c")
	if s.calls[0] != "metadata:one" || s.calls[1] != "metadata:one" {
		t.Fatalf("calls=%v", s.calls)
	}
}

func TestBacklogUnauthorizedRetainsAndBadPayloadQuarantines(t *testing.T) {
	d := t.TempDir()
	b := open(t, d)
	if err := b.Enqueue(submission(t, d, "unauthorized")); err != nil {
		t.Fatal(err)
	}
	b.ProcessOne(context.Background(), &sender{errs: []error{transport.ErrUnauthorized}}, "d", "c")
	st := b.Status()
	if !st.Degraded || st.BacklogCount != 1 {
		t.Fatalf("status=%+v", st)
	}
	d2 := t.TempDir()
	b = open(t, d2)
	if err := b.Enqueue(submission(t, d2, "bad")); err != nil {
		t.Fatal(err)
	}
	b.ProcessOne(context.Background(), &sender{errs: []error{transport.ErrInvalidRequest}}, "d", "c")
	if st := b.Status(); st.BacklogCount != 0 || st.Quarantined != 1 {
		t.Fatalf("status=%+v", st)
	}
}

func TestBacklogBoundedAndShutdown(t *testing.T) {
	d := t.TempDir()
	b := open(t, d)
	if err := b.Enqueue(submission(t, d, "one")); err != nil {
		t.Fatal(err)
	}
	if err := b.Enqueue(submission(t, d, "two")); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(b.Enqueue(submission(t, d, "three")), ErrFull) {
		t.Fatal("want full")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { b.Run(ctx, &sender{}, "d", "c", time.Hour); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not stop cleanly")
	}
}

func TestBacklogRateLimitErrorWithRetryAfter(t *testing.T) {
	d := t.TempDir()
	b := openWithConfig(t, d, 100*time.Millisecond, time.Second)
	if err := b.Enqueue(submission(t, d, "rate-limited")); err != nil {
		t.Fatal(err)
	}

	rle := &transport.RateLimitError{RetryAfter: 250 * time.Millisecond}
	s := &sender{errs: []error{rle}}

	before := time.Now()
	b.ProcessOne(context.Background(), s, "d", "c")

	// Immediate next attempt should return false because NextAttempt is in the future
	if b.ProcessOne(context.Background(), s, "d", "c") {
		t.Fatal("ProcessOne should return false while backed off due to Retry-After")
	}

	b.mu.Lock()
	nextAttempt := b.queue[0].NextAttempt
	b.mu.Unlock()

	expectedMin := before.Add(240 * time.Millisecond)
	if nextAttempt.Before(expectedMin) {
		t.Errorf("NextAttempt %v is before expected min %v", nextAttempt, expectedMin)
	}
}

func TestBacklogDivergentSubmissionConflict(t *testing.T) {
	d := t.TempDir()
	b := open(t, d)

	sub1 := submission(t, d, "conflict-evt")
	if err := b.Enqueue(sub1); err != nil {
		t.Fatalf("sub1: %v", err)
	}

	// Divergent submission: different Class for the exact same event_uuid
	sub2 := sub1
	sub2.Event.Class = "vehicle"

	err := b.Enqueue(sub2)
	if !errors.Is(err, ErrSubmissionConflict) {
		t.Fatalf("expected ErrSubmissionConflict, got %v", err)
	}
}

func TestBacklogDedupe_SamePayloadIdempotent(t *testing.T) {
	d := t.TempDir()
	b := open(t, d)

	sub1 := submission(t, d, "idempotent-evt")
	sub1.Event.CorrelationID = "cam-1-100"
	sub1.Event.BBox = map[string]float64{"x": 10, "y": 20, "width": 30, "height": 40}
	if err := b.Enqueue(sub1); err != nil {
		t.Fatalf("initial enqueue: %v", err)
	}

	// Re-enqueue identical submission
	if err := b.Enqueue(sub1); err != nil {
		t.Fatalf("identical retry should be idempotent, got: %v", err)
	}

	if b.Status().BacklogCount != 1 {
		t.Fatalf("expected backlog count 1, got %d", b.Status().BacklogCount)
	}
}

func TestBacklogDedupe_DifferentBBoxConflict(t *testing.T) {
	d := t.TempDir()
	b := open(t, d)

	sub1 := submission(t, d, "bbox-evt")
	sub1.Event.BBox = map[string]float64{"x": 10, "y": 20, "width": 30, "height": 40}
	if err := b.Enqueue(sub1); err != nil {
		t.Fatalf("initial enqueue: %v", err)
	}

	sub2 := sub1
	sub2.Event.BBox = map[string]float64{"x": 99, "y": 20, "width": 30, "height": 40}
	err := b.Enqueue(sub2)
	if !errors.Is(err, ErrSubmissionConflict) {
		t.Fatalf("expected ErrSubmissionConflict on divergent BBox, got %v", err)
	}
}

func TestBacklogDedupe_DifferentCorrelationIDConflict(t *testing.T) {
	d := t.TempDir()
	b := open(t, d)

	sub1 := submission(t, d, "cid-evt")
	sub1.Event.CorrelationID = "cam-1-100"
	if err := b.Enqueue(sub1); err != nil {
		t.Fatalf("initial enqueue: %v", err)
	}

	sub2 := sub1
	sub2.Event.CorrelationID = "cam-1-200"
	err := b.Enqueue(sub2)
	if !errors.Is(err, ErrSubmissionConflict) {
		t.Fatalf("expected ErrSubmissionConflict on divergent CorrelationID, got %v", err)
	}
}

// TestBacklogRecover_QuarantinesCorruptPendingFile (Y8/Y2): a pending record
// file that is truncated or otherwise unparsable -- the deterministic proxy
// for a power loss mid-write -- must not be silently dropped or left
// invisibly stuck in pending/ forever. Open() must move it into the
// existing quarantine/ directory and count it, exactly as a bad record
// found at send time already is (see saas_outage_test.go).
func TestBacklogRecover_QuarantinesCorruptPendingFile(t *testing.T) {
	d := t.TempDir()

	// A healthy record recovers normally alongside the corrupt one.
	b := open(t, d)
	if err := b.Enqueue(submission(t, d, "good-one")); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	// Simulate an abrupt kill mid-write: a pending/*.json file that is
	// present but truncated (not valid JSON), sharing the same naming
	// convention Open()'s recovery scan expects.
	corruptPath := filepath.Join(d, "pending", "00000000000000000099.json")
	if err := os.WriteFile(corruptPath, []byte(`{"sequence":99,"submissio`), 0o640); err != nil {
		t.Fatal(err)
	}

	b2, err := Open(Config{Dir: d, MaxOperations: 2, MaxBytes: 1024, RetryBase: time.Millisecond, RetryMax: time.Millisecond})
	if err != nil {
		t.Fatalf("Open after corrupt pending file: %v", err)
	}

	status := b2.Status()
	if status.BacklogCount != 1 {
		t.Errorf("BacklogCount after recovery = %d, want 1 (only the healthy record)", status.BacklogCount)
	}
	if status.Quarantined != 1 {
		t.Errorf("Quarantined after recovery = %d, want 1 (the corrupt record)", status.Quarantined)
	}

	if _, err := os.Stat(corruptPath); !os.IsNotExist(err) {
		t.Errorf("corrupt file still present at %s, want it moved out of pending/", corruptPath)
	}
	quarantinedPath := filepath.Join(d, "quarantine", "00000000000000000099.json")
	if _, err := os.Stat(quarantinedPath); err != nil {
		t.Errorf("expected corrupt file preserved at %s for diagnosis: %v", quarantinedPath, err)
	}
}

// TestBacklogWriteLocked_SurvivesLeftoverTmpFile (Y2): a stale .tmp file
// left behind by a process killed between CreateTemp and the final Rename
// (the deterministic proxy for power loss during a write) must never be
// mistaken for a real pending record, and a subsequent write must still
// succeed cleanly.
func TestBacklogWriteLocked_SurvivesLeftoverTmpFile(t *testing.T) {
	d := t.TempDir()
	if err := os.MkdirAll(filepath.Join(d, "pending"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(d, "quarantine"), 0o750); err != nil {
		t.Fatal(err)
	}

	// Leftover partial write from a previous, abruptly killed process.
	leftoverTmp := filepath.Join(d, "pending", "00000000000000000001.json.tmp")
	if err := os.WriteFile(leftoverTmp, []byte("partial-gar"), 0o640); err != nil {
		t.Fatal(err)
	}

	b := open(t, d)
	if err := b.Enqueue(submission(t, d, "after-crash")); err != nil {
		t.Fatalf("enqueue after leftover tmp file: %v", err)
	}
	if got := b.Status().BacklogCount; got != 1 {
		t.Fatalf("BacklogCount = %d, want 1", got)
	}

	// The stale .tmp is not a ".json" pending record and must be ignored by
	// recovery, never surfacing as a phantom entry or a quarantine count.
	if got := b.Status().Quarantined; got != 0 {
		t.Errorf("Quarantined = %d, want 0 (a stray .tmp file is not a record)", got)
	}
}
