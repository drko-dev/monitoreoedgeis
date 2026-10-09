package edgebacklog

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

func TestBacklogCameraInactiveRetainsAndRecovers(t *testing.T) {
	d := t.TempDir()
	retryMax := 500 * time.Millisecond
	b := openWithConfig(t, d, 5*time.Millisecond, retryMax)

	var syncedUUID string
	b.SetSyncCallbacks(func(eventUUID string) {
		syncedUUID = eventUUID
	}, func(eventUUID, reason string) {
		t.Fatalf("unexpected quarantine of %s: %s", eventUUID, reason)
	})

	sub := submission(t, d, "inactive-evt")
	if err := b.Enqueue(sub); err != nil {
		t.Fatal(err)
	}

	// First attempt: camera administratively inactive in SaaS
	s := &sender{errs: []error{transport.ErrCameraInactive}}
	if !b.ProcessOne(context.Background(), s, "device", "token") {
		t.Fatal("expected ProcessOne to process record on first attempt")
	}

	// Must NOT be quarantined
	st := b.Status()
	if st.Quarantined != 0 {
		t.Fatalf("Quarantined = %d, want 0 (inactive camera must not quarantine)", st.Quarantined)
	}
	if st.BacklogCount != 1 {
		t.Fatalf("BacklogCount = %d, want 1 (retained in pending)", st.BacklogCount)
	}

	// Immediate next attempt returns false due to cooldown
	if b.ProcessOne(context.Background(), s, "device", "token") {
		t.Fatal("expected ProcessOne to return false while in cooldown")
	}

	// Wait for cooldown to expire (simulating reactivation)
	time.Sleep(retryMax + 10*time.Millisecond)

	// Sender now succeeds for metadata and capture
	s.errs = nil
	if !b.ProcessOne(context.Background(), s, "device", "token") {
		t.Fatal("expected ProcessOne to process metadata stage after reactivation")
	}
	if !b.ProcessOne(context.Background(), s, "device", "token") {
		t.Fatal("expected ProcessOne to process capture stage after reactivation")
	}

	st = b.Status()
	if st.BacklogCount != 0 {
		t.Fatalf("BacklogCount = %d, want 0 after full recovery", st.BacklogCount)
	}
	if syncedUUID != "inactive-evt" {
		t.Fatalf("syncedUUID = %q, want inactive-evt", syncedUUID)
	}
}

func TestBacklogNoHeadOfLineBlockingAcrossCameras(t *testing.T) {
	d := t.TempDir()
	// Long retry max to keep inactive camera deferred
	b := openWithConfig(t, d, 100*time.Millisecond, 10*time.Second)

	subA := submission(t, d, "evt-cam-inactive")
	subA.Event.CandidateKey = "cam-inactive"
	if err := b.Enqueue(subA); err != nil {
		t.Fatal(err)
	}

	subB := submission(t, d, "evt-cam-active")
	subB.Event.CandidateKey = "cam-active"
	if err := b.Enqueue(subB); err != nil {
		t.Fatal(err)
	}

	// Call 1: cam-inactive returns ErrCameraInactive
	// Call 2: cam-active succeeds
	s := &sender{errs: []error{transport.ErrCameraInactive, nil}}

	// Process first record (cam-inactive) -> sets NextAttempt 10s into future
	if !b.ProcessOne(context.Background(), s, "device", "token") {
		t.Fatal("expected ProcessOne to process cam-inactive")
	}

	// Process second record (cam-active) -> must NOT be blocked by cam-inactive!
	if !b.ProcessOne(context.Background(), s, "device", "token") {
		t.Fatal("cam-active was blocked by deferred cam-inactive (head-of-line blocking violation)")
	}

	// Check calls made
	if len(s.calls) < 2 {
		t.Fatalf("expected at least 2 calls, got %v", s.calls)
	}
	if s.calls[0] != "metadata:evt-cam-inactive" {
		t.Errorf("call 0 = %q, want metadata:evt-cam-inactive", s.calls[0])
	}
	if s.calls[1] != "metadata:evt-cam-active" {
		t.Errorf("call 1 = %q, want metadata:evt-cam-active", s.calls[1])
	}
}

func TestBacklogInactiveCameraAntiStarvation(t *testing.T) {
	d := t.TempDir()
	b := openWithConfig(t, d, 100*time.Millisecond, 10*time.Second)

	// 1. Enqueue event for cam-inactive
	subInactive := submission(t, d, "evt-inactive-1")
	subInactive.Event.CandidateKey = "cam-inactive"
	if err := b.Enqueue(subInactive); err != nil {
		t.Fatalf("failed to enqueue first event: %v", err)
	}

	// 2. Process event -> encounters ErrCameraInactive (HTTP 423)
	s := &sender{errs: []error{transport.ErrCameraInactive}}
	if !b.ProcessOne(context.Background(), s, "device", "token") {
		t.Fatal("expected ProcessOne to process metadata stage")
	}

	// 3. Status reports cam-inactive in InactiveCandidates
	st := b.Status()
	if len(st.InactiveCandidates) != 1 || st.InactiveCandidates[0] != "cam-inactive" {
		t.Fatalf("expected InactiveCandidates [cam-inactive], got %v", st.InactiveCandidates)
	}
	if st.InactiveDrops != 0 {
		t.Fatalf("expected 0 InactiveDrops, got %d", st.InactiveDrops)
	}
	if st.Quarantined != 0 {
		t.Fatalf("inactive event must not be quarantined, quarantined=%d", st.Quarantined)
	}

	// 4. New submission for inactive camera MUST be refused at Enqueue (anti-starvation)
	subInactive2 := submission(t, d, "evt-inactive-2")
	subInactive2.Event.CandidateKey = "cam-inactive"
	err := b.Enqueue(subInactive2)
	if !errors.Is(err, ErrCameraInactive) {
		t.Fatalf("expected ErrCameraInactive, got %v", err)
	}

	st = b.Status()
	if st.InactiveDrops != 1 {
		t.Fatalf("expected 1 InactiveDrops, got %d", st.InactiveDrops)
	}
	if b.InactiveDrops() != 1 {
		t.Fatalf("expected InactiveDrops() == 1, got %d", b.InactiveDrops())
	}

	// 5. Active camera can continue enqueuing and processing freely
	subActive := submission(t, d, "evt-active-1")
	subActive.Event.CandidateKey = "cam-active"
	if err := b.Enqueue(subActive); err != nil {
		t.Fatalf("active camera enqueue failed: %v", err)
	}

	s.errs = []error{nil, nil} // Metadata and capture succeed for active camera
	if !b.ProcessOne(context.Background(), s, "device", "token") {
		t.Fatal("expected active camera metadata to process")
	}
	if !b.ProcessOne(context.Background(), s, "device", "token") {
		t.Fatal("expected active camera capture to process")
	}

	// Active camera completed and was removed; inactive camera event is still pending
	st = b.Status()
	if st.BacklogCount != 1 {
		t.Fatalf("expected BacklogCount 1 (inactive pending), got %d", st.BacklogCount)
	}

	// 6. Camera is reactivated in SaaS -> MarkCandidateActive
	b.MarkCandidateActive("cam-inactive")
	st = b.Status()
	if len(st.InactiveCandidates) != 0 {
		t.Fatalf("expected 0 InactiveCandidates after reactivation, got %v", st.InactiveCandidates)
	}

	// Now new events can be enqueued for the reactivated camera
	if err := b.Enqueue(subInactive2); err != nil {
		t.Fatalf("expected successful enqueue after reactivation, got %v", err)
	}

	// And pending events can now be processed immediately without waiting for RetryMax
	s.errs = []error{nil, nil, nil, nil} // Both events complete metadata and capture
	for i := 0; i < 4; i++ {
		if !b.ProcessOne(context.Background(), s, "device", "token") {
			t.Fatalf("expected process loop %d to succeed", i)
		}
	}

	st = b.Status()
	if st.BacklogCount != 0 {
		t.Fatalf("expected empty backlog after all processed, got %d", st.BacklogCount)
	}
}

func openInactive(t *testing.T, dir string, retryMax, retention time.Duration) *Backlog {
	t.Helper()
	b, err := Open(Config{Dir: dir, MaxOperations: 8, MaxBytes: 1 << 20, RetryBase: time.Millisecond, RetryMax: retryMax, InactiveRetention: retention})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func enqueueFor(t *testing.T, b *Backlog, dir, id, key string) {
	t.Helper()
	s := submission(t, dir, id)
	s.Event.CandidateKey = key
	if err := b.Enqueue(s); err != nil {
		t.Fatalf("enqueue %s: %v", id, err)
	}
}

// drain runs ProcessOne until nothing is due.
func drain(b *Backlog, s *sender) {
	for b.ProcessOne(context.Background(), s, "device", "token") {
	}
}

func TestBacklogInactiveMarkSurvivesRestartAndProbeReactivates(t *testing.T) {
	d := t.TempDir()
	retryMax := 30 * time.Millisecond
	b := openInactive(t, d, retryMax, time.Hour)
	enqueueFor(t, b, d, "a1", "cam-a")
	enqueueFor(t, b, d, "a2", "cam-a")
	b.ProcessOne(context.Background(), &sender{errs: []error{transport.ErrCameraInactive}}, "device", "token")

	// Restart: pending records and the inactive mark come back from disk.
	b = openInactive(t, d, retryMax, time.Hour)
	st := b.Status()
	if st.BacklogCount != 2 || st.RecoveredOperations != 2 {
		t.Fatalf("after restart backlog=%d recovered=%d, want 2/2", st.BacklogCount, st.RecoveredOperations)
	}
	if len(st.InactiveCandidates) != 1 || st.InactiveCandidates[0] != "cam-a" {
		t.Fatalf("InactiveCandidates after restart = %v, want [cam-a]", st.InactiveCandidates)
	}
	s3 := submission(t, d, "a3")
	s3.Event.CandidateKey = "cam-a"
	if err := b.Enqueue(s3); !errors.Is(err, ErrCameraInactive) {
		t.Fatalf("new cam-a event after restart: err=%v, want ErrCameraInactive", err)
	}

	// Camera reactivated in SaaS: the next probe is accepted, with no
	// explicit MarkCandidateActive call. All retained records drain in order.
	time.Sleep(retryMax + 10*time.Millisecond)
	s := &sender{}
	drain(b, s)
	want := []string{"metadata:a1", "capture:a1", "metadata:a2", "capture:a2"}
	if fmt.Sprint(s.calls) != fmt.Sprint(want) {
		t.Fatalf("calls = %v, want %v (FIFO per camera after reactivation)", s.calls, want)
	}
	st = b.Status()
	if st.BacklogCount != 0 || len(st.InactiveCandidates) != 0 || st.Quarantined != 0 {
		t.Fatalf("after reactivation status = %+v, want empty backlog, no inactive, no quarantine", st)
	}
	if err := b.Enqueue(s3); err != nil {
		t.Fatalf("enqueue after reactivation: %v", err)
	}
}

func TestBacklogInactiveRetentionExpiresToQuarantine(t *testing.T) {
	d := t.TempDir()
	b := openInactive(t, d, time.Millisecond, 20*time.Millisecond)
	var quarantined []string
	b.SetSyncCallbacks(nil, func(id, reason string) { quarantined = append(quarantined, id+":"+reason) })
	enqueueFor(t, b, d, "old", "cam-a")

	inactive := func() *sender { return &sender{errs: []error{transport.ErrCameraInactive}} }
	b.ProcessOne(context.Background(), inactive(), "device", "token")
	if b.Status().BacklogCount != 1 {
		t.Fatal("record must be retained while within InactiveRetention")
	}
	time.Sleep(30 * time.Millisecond)
	b.ProcessOne(context.Background(), inactive(), "device", "token")
	st := b.Status()
	if st.BacklogCount != 0 || st.Quarantined != 1 || st.InactiveExpired != 1 {
		t.Fatalf("status = %+v, want record quarantined and InactiveExpired=1", st)
	}
	if len(quarantined) != 1 || quarantined[0] != "old:camera_inactive_retention_expired" {
		t.Fatalf("quarantine callback = %v", quarantined)
	}
}

func TestBacklogActiveAndInactiveCamerasCoexist(t *testing.T) {
	d := t.TempDir()
	b := openInactive(t, d, time.Hour, 24*time.Hour)
	enqueueFor(t, b, d, "i1", "cam-off")
	enqueueFor(t, b, d, "i2", "cam-off")
	b.ProcessOne(context.Background(), &sender{errs: []error{transport.ErrCameraInactive}}, "device", "token")

	// While cam-off stays inactive, cam-on keeps flowing through the shared
	// capacity: its events are accepted and fully synced in order.
	s := &sender{}
	for i := 0; i < 20; i++ {
		enqueueFor(t, b, d, fmt.Sprintf("on%02d", i), "cam-on")
		drain(b, s)
	}
	for _, c := range s.calls {
		if strings.Contains(c, ":i") {
			t.Fatalf("inactive camera record sent before its retry: %v", s.calls)
		}
	}
	if len(s.calls) != 40 {
		t.Fatalf("cam-on calls = %d, want 40 (20 events x metadata+capture)", len(s.calls))
	}
	st := b.Status()
	if st.BacklogCount != 2 || st.Drops != 0 || st.Quarantined != 0 {
		t.Fatalf("status = %+v, want only the 2 retained cam-off records, no drops", st)
	}
	// i2 was never sent: FIFO keeps it behind the deferred i1.
	if st.InactiveDrops != 0 {
		t.Fatalf("InactiveDrops = %d, want 0 (no new cam-off submissions)", st.InactiveDrops)
	}
}
