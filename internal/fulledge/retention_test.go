package fulledge

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/edgebacklog"
)

// --- fixtures --------------------------------------------------------------

func newRetentionEventStore(t *testing.T, dataDir string) *EventStore {
	t.Helper()
	s, err := NewEventStore(dataDir)
	if err != nil {
		t.Fatalf("NewEventStore: %v", err)
	}
	return s
}

func writeTestEvent(t *testing.T, store *EventStore, uuid string, when time.Time, status SyncStatus, ev *EvidenceRef) {
	t.Helper()
	evt := &LocalEvent{
		EventUUID:       uuid,
		Tipo:            "person",
		Model:           "test-model",
		ProcessingMode:  "edge",
		SourceTimestamp: when,
		CreatedAt:       when,
		SyncStatus:      status,
		Evidence:        ev,
	}
	if err := store.Save(evt); err != nil {
		t.Fatalf("save event %s: %v", uuid, err)
	}
}

func writeCaptureFile(t *testing.T, dataDir, name string, size int, mtime time.Time) string {
	t.Helper()
	dir := filepath.Join(dataDir, "evidence", "captures")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, bytes.Repeat([]byte{0xAB}, size), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	return p
}

func writeClipFile(t *testing.T, dataDir, eventUUID string, size int, mtime time.Time) string {
	t.Helper()
	dir := filepath.Join(dataDir, "evidence", "clips")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, eventUUID+".mp4")
	if err := os.WriteFile(p, bytes.Repeat([]byte{0xCD}, size), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	return p
}

// writePendingRecord writes a raw pending/<seq>.json matching
// edgebacklog's own record/Submission/Evidence JSON shape exactly (that
// package's record type is unexported, so tests here reconstruct its wire
// format directly rather than reaching into edgebacklog's internals).
func writePendingRecord(t *testing.T, dataDir string, seq uint64, eventUUID, capturePath, clipPath string) {
	t.Helper()
	dir := filepath.Join(dataDir, "local-event-backlog", "pending")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}

	type evidence struct {
		Path   string `json:"path"`
		SHA256 string `json:"sha256"`
		Size   int64  `json:"size"`
	}
	rec := struct {
		Sequence   uint64    `json:"sequence"`
		CreatedAt  time.Time `json:"created_at"`
		Stage      string    `json:"stage"`
		Submission struct {
			Event struct {
				EventUUID string `json:"event_uuid"`
			} `json:"event"`
			Capture *evidence `json:"capture,omitempty"`
			Clip    *evidence `json:"clip,omitempty"`
		} `json:"submission"`
	}{Sequence: seq, CreatedAt: time.Now().UTC(), Stage: "metadata"}
	rec.Submission.Event.EventUUID = eventUUID
	if capturePath != "" {
		rec.Submission.Capture = &evidence{Path: capturePath, SHA256: "deadbeef", Size: 1}
	}
	if clipPath != "" {
		rec.Submission.Clip = &evidence{Path: clipPath, SHA256: "deadbeef", Size: 1}
	}

	data, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, fmt.Sprintf("%020d.json", seq))
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// --- pure planEviction: case 1 (count), 2 (bytes), 3 (TTL), 4 (oldest-first) ---

func TestPlanEviction_CountBound(t *testing.T) {
	now := time.Now()
	var c []retentionCandidate
	for i := 0; i < 5; i++ {
		c = append(c, retentionCandidate{path: fmt.Sprintf("f%d", i), size: 10, when: now.Add(time.Duration(i) * time.Minute)})
	}
	evicted := planEviction(c, now, 3, 0, 0)
	if len(evicted) != 2 {
		t.Fatalf("expected 2 evictions to bring 5 down to 3, got %d", len(evicted))
	}
	for _, e := range evicted {
		if e.path != "f0" && e.path != "f1" {
			t.Errorf("expected oldest two (f0,f1) evicted, got %s", e.path)
		}
	}
}

func TestPlanEviction_ByteBound(t *testing.T) {
	now := time.Now()
	c := []retentionCandidate{
		{path: "a", size: 100, when: now},
		{path: "b", size: 100, when: now.Add(time.Minute)},
		{path: "c", size: 100, when: now.Add(2 * time.Minute)},
	}
	evicted := planEviction(c, now, 0, 150, 0)
	if len(evicted) != 2 {
		t.Fatalf("expected 2 oldest evicted to fit 300 bytes into a 150 byte bound, got %d", len(evicted))
	}
	if evicted[0].path != "a" || evicted[1].path != "b" {
		t.Fatalf("expected oldest-first eviction a,b — got %+v", evicted)
	}
}

func TestPlanEviction_TTLBound(t *testing.T) {
	now := time.Now()
	c := []retentionCandidate{
		{path: "old", size: 1, when: now.Add(-2 * time.Hour)},
		{path: "new", size: 1, when: now.Add(-1 * time.Minute)},
	}
	evicted := planEviction(c, now, 0, 0, time.Hour)
	if len(evicted) != 1 || evicted[0].path != "old" {
		t.Fatalf("expected only the file older than the TTL evicted, got %+v", evicted)
	}
}

func TestPlanEviction_OldestFirstInterleavedOrder(t *testing.T) {
	now := time.Now()
	// Deliberately out-of-order input; planEviction assumes pre-sorted input
	// (sortOldestFirst is a separate, tested step), but oldest-first
	// selection must still walk in the given (sorted) order.
	c := []retentionCandidate{
		{path: "t0", size: 1, when: now.Add(0 * time.Minute)},
		{path: "t1", size: 1, when: now.Add(1 * time.Minute)},
		{path: "t2", size: 1, when: now.Add(2 * time.Minute)},
		{path: "t3", size: 1, when: now.Add(3 * time.Minute)},
	}
	evicted := planEviction(c, now, 1, 0, 0)
	if len(evicted) != 3 || evicted[0].path != "t0" || evicted[1].path != "t1" || evicted[2].path != "t2" {
		t.Fatalf("expected t0,t1,t2 evicted in that order, got %+v", evicted)
	}
}

func TestPlanEviction_ZeroBoundsDisabled(t *testing.T) {
	now := time.Now()
	c := []retentionCandidate{{path: "a", size: 999999, when: now.Add(-999 * time.Hour)}}
	if evicted := planEviction(c, now, 0, 0, 0); len(evicted) != 0 {
		t.Fatalf("all-zero bounds must evict nothing, got %+v", evicted)
	}
}

func TestPlanEviction_ProtectedNeverEvictedEvenWhenOverBound(t *testing.T) {
	now := time.Now()
	c := []retentionCandidate{
		{path: "protected-old", size: 1, when: now.Add(-1 * time.Hour), protected: true},
		{path: "evictable", size: 1, when: now.Add(-30 * time.Minute)},
	}
	evicted := planEviction(c, now, 1, 0, 0)
	if len(evicted) != 1 || evicted[0].path != "evictable" {
		t.Fatalf("expected only the unprotected candidate evicted, got %+v", evicted)
	}
}

// --- sortOldestFirst -------------------------------------------------------

func TestSortOldestFirst(t *testing.T) {
	now := time.Now()
	c := []retentionCandidate{
		{path: "z", when: now.Add(2 * time.Minute)},
		{path: "a", when: now},
		{path: "m", when: now.Add(time.Minute)},
	}
	sortOldestFirst(c)
	if c[0].path != "a" || c[1].path != "m" || c[2].path != "z" {
		t.Fatalf("expected a,m,z order, got %+v", c)
	}
}

// --- integration: RetentionManager.Sweep over a real filesystem -----------

// Case: count bound (event tree) + case: byte bound (capture tree) + case:
// TTL bound (clip tree) + case: oldest-first, exercised together against
// real files.
func TestRetentionManager_CountByteTTLBoundsAcrossTrees(t *testing.T) {
	dir := t.TempDir()
	store := newRetentionEventStore(t, dir)
	now := time.Now()

	// Events: 4 events, MaxEvents=2 -> oldest 2 evicted.
	for i := 0; i < 4; i++ {
		writeTestEvent(t, store, fmt.Sprintf("evt-%d", i), now.Add(-time.Duration(4-i)*time.Hour), SyncStatusSynced, nil)
	}

	// Captures: 3 files of 100 bytes, MaxCaptureBytes=150 -> oldest 2 evicted.
	writeCaptureFile(t, dir, "cap-0.jpg", 100, now.Add(-3*time.Hour))
	writeCaptureFile(t, dir, "cap-1.jpg", 100, now.Add(-2*time.Hour))
	writeCaptureFile(t, dir, "cap-2.jpg", 100, now.Add(-1*time.Hour))

	// Clips: one old (past TTL), one recent.
	writeClipFile(t, dir, "clip-old", 10, now.Add(-48*time.Hour))
	writeClipFile(t, dir, "clip-new", 10, now.Add(-1*time.Minute))

	r, err := NewRetentionManager(RetentionConfig{
		DataDir:         dir,
		MaxEvents:       2,
		MaxCaptureBytes: 150,
		MaxClipAge:      24 * time.Hour,
	}, store)
	if err != nil {
		t.Fatalf("NewRetentionManager: %v", err)
	}

	report := r.LastReport() // populated by the construction-time sweep

	if report.Events.Evicted != 2 {
		t.Errorf("expected 2 events evicted, got %d (report=%+v)", report.Events.Evicted, report.Events)
	}
	for i := 0; i < 2; i++ {
		if _, err := os.Stat(filepath.Join(dir, "events", fmt.Sprintf("evt-%d.json", i))); !os.IsNotExist(err) {
			t.Errorf("expected evt-%d evicted", i)
		}
	}
	for i := 2; i < 4; i++ {
		if _, err := os.Stat(filepath.Join(dir, "events", fmt.Sprintf("evt-%d.json", i))); err != nil {
			t.Errorf("expected evt-%d to survive: %v", i, err)
		}
	}

	if report.Captures.Evicted != 2 {
		t.Errorf("expected 2 captures evicted to satisfy the byte bound, got %d", report.Captures.Evicted)
	}
	if _, err := os.Stat(filepath.Join(dir, "evidence", "captures", "cap-2.jpg")); err != nil {
		t.Errorf("expected the newest capture to survive: %v", err)
	}

	if report.Clips.Evicted != 1 {
		t.Errorf("expected exactly the TTL-expired clip evicted, got %d", report.Clips.Evicted)
	}
	if _, err := os.Stat(filepath.Join(dir, "evidence", "clips", "clip-old.mp4")); !os.IsNotExist(err) {
		t.Error("expected clip-old evicted past its TTL")
	}
	if _, err := os.Stat(filepath.Join(dir, "evidence", "clips", "clip-new.mp4")); err != nil {
		t.Errorf("expected clip-new to survive: %v", err)
	}
}

// Case: event metadata eviction keeps EventStore.BacklogCount coherent.
func TestRetentionManager_EventEvictionKeepsBacklogCountCoherent(t *testing.T) {
	dir := t.TempDir()
	store := newRetentionEventStore(t, dir)
	now := time.Now()

	writeTestEvent(t, store, "pending-old", now.Add(-3*time.Hour), SyncStatusPending, nil)
	writeTestEvent(t, store, "synced-old", now.Add(-2*time.Hour), SyncStatusSynced, nil)
	writeTestEvent(t, store, "pending-new", now.Add(-1*time.Hour), SyncStatusPending, nil)

	if got := store.BacklogCount(); got != 2 {
		t.Fatalf("precondition: expected backlog count 2, got %d", got)
	}

	r, err := NewRetentionManager(RetentionConfig{
		DataDir:      dir,
		MaxEvents:    1,
		EvictPending: true,
	}, store)
	if err != nil {
		t.Fatalf("NewRetentionManager: %v", err)
	}
	report := r.LastReport()
	if report.Events.Evicted != 2 {
		t.Fatalf("expected 2 events evicted, got %d", report.Events.Evicted)
	}
	// pending-old was evicted (was pending -> -1); synced-old was evicted
	// (was not pending -> no change). Only pending-new (pending) survives.
	if got := store.BacklogCount(); got != 1 {
		t.Fatalf("expected backlog count 1 after eviction, got %d", got)
	}
}

// Case: shared capture not evicted while referenced by a surviving event (F-A).
func TestRetentionManager_SharedCaptureProtectedWhileReferenced(t *testing.T) {
	dir := t.TempDir()
	store := newRetentionEventStore(t, dir)
	now := time.Now()

	capturePath := writeCaptureFile(t, dir, "shared-capture.jpg", 10, now.Add(-5*time.Hour))
	relPath, err := filepath.Rel(dir, capturePath)
	if err != nil {
		t.Fatal(err)
	}
	evRef := &EvidenceRef{Path: relPath}

	// 3 event JSONs point at the same JPEG.
	writeTestEvent(t, store, "e1", now.Add(-3*time.Hour), SyncStatusSynced, evRef)
	writeTestEvent(t, store, "e2", now.Add(-2*time.Hour), SyncStatusSynced, evRef)
	writeTestEvent(t, store, "e3", now.Add(-1*time.Hour), SyncStatusSynced, evRef)

	r, err := NewRetentionManager(RetentionConfig{
		DataDir:       dir,
		MaxCaptures:   0,         // no count bound: a bug would delete via TTL/refcount ignore
		MaxCaptureAge: time.Hour, // capture is 5h old, would be evicted if unprotected
	}, store)
	if err != nil {
		t.Fatalf("NewRetentionManager: %v", err)
	}
	report := r.LastReport()
	if report.Captures.Evicted != 0 {
		t.Fatalf("expected the shared capture kept while 3 events still reference it, evicted=%d", report.Captures.Evicted)
	}
	if _, err := os.Stat(capturePath); err != nil {
		t.Fatalf("shared capture must survive on disk: %v", err)
	}
}

// Case: pending evidence (capture AND clip) never evicted (F-B), even when
// every bound would otherwise remove it.
func TestRetentionManager_PendingEvidenceNeverEvicted(t *testing.T) {
	dir := t.TempDir()
	store := newRetentionEventStore(t, dir)
	now := time.Now()

	capPath := writeCaptureFile(t, dir, "pending-capture.jpg", 10, now.Add(-48*time.Hour))
	clipPath := writeClipFile(t, dir, "pending-event", 10, now.Add(-48*time.Hour))
	writePendingRecord(t, dir, 1, "pending-event", capPath, clipPath)

	r, err := NewRetentionManager(RetentionConfig{
		DataDir:       dir,
		MaxCaptureAge: time.Hour,
		MaxClipAge:    time.Hour,
	}, store)
	if err != nil {
		t.Fatalf("NewRetentionManager: %v", err)
	}
	report := r.LastReport()
	if report.Captures.Evicted != 0 {
		t.Errorf("expected the pending capture kept, evicted=%d", report.Captures.Evicted)
	}
	if report.Clips.Evicted != 0 {
		t.Errorf("expected the pending clip kept, evicted=%d", report.Clips.Evicted)
	}
	if _, err := os.Stat(capPath); err != nil {
		t.Errorf("pending capture must survive: %v", err)
	}
	if _, err := os.Stat(clipPath); err != nil {
		t.Errorf("pending clip must survive: %v", err)
	}
}

// Case: pending event JSON is protected by default (EvictPending=false),
// and only evicted when the operator explicitly opts in.
func TestRetentionManager_PendingEventJSONProtectedUnlessOptedIn(t *testing.T) {
	dir := t.TempDir()
	store := newRetentionEventStore(t, dir)
	now := time.Now()

	writeTestEvent(t, store, "pending-evt", now.Add(-48*time.Hour), SyncStatusPending, nil)
	writePendingRecord(t, dir, 1, "pending-evt", "", "")

	r, err := NewRetentionManager(RetentionConfig{DataDir: dir, MaxEventAge: time.Hour}, store)
	if err != nil {
		t.Fatalf("NewRetentionManager: %v", err)
	}
	if r.LastReport().Events.Evicted != 0 {
		t.Fatal("expected the pending event JSON kept by default")
	}
	if _, err := os.Stat(filepath.Join(dir, "events", "pending-evt.json")); err != nil {
		t.Fatalf("pending event JSON must survive by default: %v", err)
	}

	// Now opt in.
	r2, err := NewRetentionManager(RetentionConfig{DataDir: dir, MaxEventAge: time.Hour, EvictPending: true}, store)
	if err != nil {
		t.Fatalf("NewRetentionManager: %v", err)
	}
	if r2.LastReport().Events.Evicted != 1 {
		t.Fatal("expected the pending event JSON evicted once EvictPending is set")
	}
}

// Case: no path traversal / symlink escape.
func TestRetentionManager_SymlinkEscapeRefusedAndTargetIntact(t *testing.T) {
	dir := t.TempDir()
	store := newRetentionEventStore(t, dir)

	capturesDir := filepath.Join(dir, "evidence", "captures")
	if err := os.MkdirAll(capturesDir, 0o750); err != nil {
		t.Fatal(err)
	}

	outsideDir := t.TempDir()
	outsideFile := filepath.Join(outsideDir, "secret.txt")
	if err := os.WriteFile(outsideFile, []byte("do not touch"), 0o600); err != nil {
		t.Fatal(err)
	}

	symlink := filepath.Join(capturesDir, "escape.jpg")
	if err := os.Symlink(outsideFile, symlink); err != nil {
		t.Skipf("symlinks unsupported in this environment: %v", err)
	}

	r, err := NewRetentionManager(RetentionConfig{DataDir: dir, MaxCaptureAge: time.Nanosecond}, store)
	if err != nil {
		t.Fatalf("NewRetentionManager: %v", err)
	}
	report := r.LastReport()
	if report.Captures.RefusedUnsafe == 0 {
		t.Error("expected the symlink to be refused and reported")
	}
	if _, err := os.Lstat(symlink); err != nil {
		t.Error("expected the symlink itself to remain untouched")
	}
	if data, err := os.ReadFile(outsideFile); err != nil || string(data) != "do not touch" {
		t.Fatalf("expected the symlink target intact and unread, got data=%q err=%v", data, err)
	}
}

// Case: concurrent create+evict with -race.
func TestRetentionManager_ConcurrentCreateAndSweepRace(t *testing.T) {
	dir := t.TempDir()
	store := newRetentionEventStore(t, dir)

	r, err := NewRetentionManager(RetentionConfig{DataDir: dir, MaxEvents: 5}, store)
	if err != nil {
		t.Fatalf("NewRetentionManager: %v", err)
	}

	// Bounded on both sides: this proves concurrent create+evict is race-free
	// (run with -race), not throughput — an unbounded writer racing a fixed
	// number of sweeps would make the events dir grow without limit between
	// sweeps and turn each Sweep's O(n) scan into an ever-slower scan.
	const writes = 200

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < writes; i++ {
			writeTestEvent(t, store, fmt.Sprintf("race-%d", i), time.Now(), SyncStatusSynced, nil)
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			if _, err := r.Sweep(time.Now()); err != nil {
				t.Errorf("concurrent Sweep: %v", err)
			}
		}
	}()

	wg.Wait()
}

// Case: restart/reopen accounting is deterministic.
func TestRetentionManager_RestartReopenIsDeterministic(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()

	store1 := newRetentionEventStore(t, dir)
	for i := 0; i < 5; i++ {
		writeTestEvent(t, store1, fmt.Sprintf("r-%d", i), now.Add(-time.Duration(5-i)*time.Hour), SyncStatusSynced, nil)
	}

	// First process: sweep bounds to 3.
	if _, err := NewRetentionManager(RetentionConfig{DataDir: dir, MaxEvents: 3}, store1); err != nil {
		t.Fatalf("NewRetentionManager: %v", err)
	}

	entries, err := os.ReadDir(filepath.Join(dir, "events"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 surviving events after first sweep, got %d", len(entries))
	}

	// Simulate a restart: reopen the store and RetentionManager on the same dir.
	store2 := newRetentionEventStore(t, dir)
	r2, err := NewRetentionManager(RetentionConfig{DataDir: dir, MaxEvents: 3}, store2)
	if err != nil {
		t.Fatalf("NewRetentionManager (reopen): %v", err)
	}
	if r2.LastReport().Events.Evicted != 0 {
		t.Fatalf("reopening at the already-satisfied bound must evict nothing more, got %d", r2.LastReport().Events.Evicted)
	}
	entries2, err := os.ReadDir(filepath.Join(dir, "events"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries2) != 3 {
		t.Fatalf("expected the same 3 events to survive reopen, got %d", len(entries2))
	}
}

// Case: delete failure does not corrupt the store and stops that tree's eviction.
func TestRetentionManager_DeleteFailureStopsTreeWithoutCorrupting(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	writeCaptureFile(t, dir, "cap-0.jpg", 1, now.Add(-3*time.Hour))
	writeCaptureFile(t, dir, "cap-1.jpg", 1, now.Add(-2*time.Hour))
	writeCaptureFile(t, dir, "cap-2.jpg", 1, now.Add(-1*time.Hour))
	store := newRetentionEventStore(t, dir)

	// Make the captures directory read-only so os.Remove fails on its
	// entries (portable "inject a failing remove" without root: a
	// non-writable parent directory prevents unlink on POSIX filesystems).
	capturesDir := filepath.Join(dir, "evidence", "captures")
	if err := os.Chmod(capturesDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(capturesDir, 0o750) })

	r, err := NewRetentionManager(RetentionConfig{DataDir: dir, MaxCaptures: 0, MaxCaptureAge: time.Nanosecond}, store)
	if err != nil {
		t.Fatalf("NewRetentionManager: %v", err)
	}
	report := r.LastReport()
	if report.Captures.Failed == 0 {
		t.Fatal("expected at least one recorded delete failure")
	}

	_ = os.Chmod(capturesDir, 0o750)
	for _, name := range []string{"cap-0.jpg", "cap-1.jpg", "cap-2.jpg"} {
		if _, err := os.Stat(filepath.Join(capturesDir, name)); err != nil {
			t.Errorf("expected %s to still exist (deletion must have stopped, not partially corrupted the tree): %v", name, err)
		}
	}
}

// Case: repeated writes with bounds set do not grow the tree unboundedly.
func TestRetentionManager_RepeatedWritesDoNotGrowUnbounded(t *testing.T) {
	dir := t.TempDir()
	store := newRetentionEventStore(t, dir)

	r, err := NewRetentionManager(RetentionConfig{DataDir: dir, MaxEvents: 3}, store)
	if err != nil {
		t.Fatalf("NewRetentionManager: %v", err)
	}

	for i := 0; i < 20; i++ {
		writeTestEvent(t, store, fmt.Sprintf("plateau-%d", i), time.Now(), SyncStatusSynced, nil)
		r.MaybeSweepAfterWrite(time.Now())
	}

	entries, err := os.ReadDir(filepath.Join(dir, "events"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) > 3 {
		t.Fatalf("expected the events tree to plateau at the bound (3), got %d files", len(entries))
	}
}

// Case: min-free-disk gate is honored (JPEG path via LimitsManager already
// covered by fulledge_test.go; this covers the retention config surface: a
// disabled bound performs no sweep-time deletion, independent of disk
// pressure — retention and the free-disk write-gate are two separate
// mechanisms per the design doc).
func TestRetentionManager_DisabledBoundsPerformNoDeletion(t *testing.T) {
	dir := t.TempDir()
	store := newRetentionEventStore(t, dir)
	now := time.Now()
	writeTestEvent(t, store, "e1", now.Add(-999*time.Hour), SyncStatusSynced, nil)
	writeCaptureFile(t, dir, "c1.jpg", 1, now.Add(-999*time.Hour))
	writeClipFile(t, dir, "e1", 1, now.Add(-999*time.Hour))

	r, err := NewRetentionManager(RetentionConfig{DataDir: dir}, store)
	if err != nil {
		t.Fatalf("NewRetentionManager: %v", err)
	}
	report := r.LastReport()
	if report.Events.Evicted != 0 || report.Captures.Evicted != 0 || report.Clips.Evicted != 0 {
		t.Fatalf("all-disabled config must never evict anything, got %+v", report)
	}
}

// Temp orphan sweep: an old .tmp file is removed; a fresh one (still being
// written) is left alone.
func TestRetentionManager_TempOrphanSweep(t *testing.T) {
	dir := t.TempDir()
	store := newRetentionEventStore(t, dir)
	eventsDir := filepath.Join(dir, "events")
	if err := os.MkdirAll(eventsDir, 0o750); err != nil {
		t.Fatal(err)
	}

	oldTmp := filepath.Join(eventsDir, ".event-old.tmp")
	if err := os.WriteFile(oldTmp, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * tempOrphanGrace)
	if err := os.Chtimes(oldTmp, old, old); err != nil {
		t.Fatal(err)
	}

	freshTmp := filepath.Join(eventsDir, ".event-fresh.tmp")
	if err := os.WriteFile(freshTmp, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	r, err := NewRetentionManager(RetentionConfig{DataDir: dir}, store)
	if err != nil {
		t.Fatalf("NewRetentionManager: %v", err)
	}
	report := r.LastReport()
	if report.Events.TempOrphans != 1 {
		t.Fatalf("expected exactly 1 temp orphan removed, got %d", report.Events.TempOrphans)
	}
	if _, err := os.Stat(oldTmp); !os.IsNotExist(err) {
		t.Error("expected the old orphaned temp file removed")
	}
	if _, err := os.Stat(freshTmp); err != nil {
		t.Error("expected the fresh temp file left alone (still within grace period)")
	}
}

// --- edgebacklog.LoadPendingReferences (used by RetentionManager) ---------

func TestLoadPendingReferences_EmptyDirNoError(t *testing.T) {
	dir := t.TempDir()
	refs, err := edgebacklog.LoadPendingReferences(filepath.Join(dir, "local-event-backlog"))
	if err != nil {
		t.Fatalf("unexpected error on a dir with no pending/: %v", err)
	}
	if len(refs.EvidencePaths) != 0 || len(refs.EventUUIDs) != 0 {
		t.Fatalf("expected empty references, got %+v", refs)
	}
}

func TestLoadPendingReferences_CorruptRecordIsAnError(t *testing.T) {
	dir := t.TempDir()
	pendingDir := filepath.Join(dir, "local-event-backlog", "pending")
	if err := os.MkdirAll(pendingDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pendingDir, "00000000000000000001.json"), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := edgebacklog.LoadPendingReferences(filepath.Join(dir, "local-event-backlog")); err == nil {
		t.Fatal("expected an unparsable pending record to be a hard error, not silently skipped")
	}
}

// A corrupt pending record must abort the entire sweep (no deletions
// anywhere) rather than silently proceeding as if nothing were pending.
func TestRetentionManager_CorruptPendingRecordAbortsSweepEntirely(t *testing.T) {
	dir := t.TempDir()
	store := newRetentionEventStore(t, dir)
	now := time.Now()
	writeTestEvent(t, store, "e1", now.Add(-999*time.Hour), SyncStatusSynced, nil)

	pendingDir := filepath.Join(dir, "local-event-backlog", "pending")
	if err := os.MkdirAll(pendingDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pendingDir, "00000000000000000001.json"), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := NewRetentionManager(RetentionConfig{DataDir: dir, MaxEventAge: time.Hour}, store); err == nil {
		t.Fatal("expected construction to fail when the pending spool cannot be trusted")
	}
	if _, err := os.Stat(filepath.Join(dir, "events", "e1.json")); err != nil {
		t.Fatalf("expected e1 untouched when the sweep aborts: %v", err)
	}
}
