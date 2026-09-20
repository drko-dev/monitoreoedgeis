package fulledge

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Retention deletes evidence permanently, so these tests are the contract:
// every safety rule the design document names has a case that fails if the rule
// is removed.

const retTestUUID = "11111111-2222-3333-4444-555555555555"

// writeEvent writes an event JSON and returns its path.
func writeEvent(t *testing.T, dir, uuid string, status SyncStatus, sourceTS time.Time, capturePath string) string {
	t.Helper()
	eventsDir := filepath.Join(dir, eventsSubdir)
	if err := os.MkdirAll(eventsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	evt := LocalEvent{
		EventUUID:       uuid,
		SourceTimestamp: sourceTS,
		SyncStatus:      status,
	}
	if capturePath != "" {
		evt.Evidence = &EvidenceRef{Path: capturePath, SHA256: "deadbeef", SizeBytes: 3}
	}
	raw, err := json.MarshalIndent(&evt, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(eventsDir, uuid+".json")
	if err := os.WriteFile(p, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// writeCapture writes a JPEG of the given size and returns its basename.
func writeCapture(t *testing.T, dir, uuid string, size int, mtime time.Time) string {
	t.Helper()
	capsDir := filepath.Join(dir, evidenceSubdir, capturesSubdir)
	if err := os.MkdirAll(capsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	name := uuid + ".jpg"
	p := filepath.Join(capsDir, name)
	if err := os.WriteFile(p, make([]byte, size), 0o600); err != nil {
		t.Fatal(err)
	}
	if !mtime.IsZero() {
		if err := os.Chtimes(p, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	return name
}

func newRetention(t *testing.T, dir string, events, captures RetentionBounds) *RetentionManager {
	t.Helper()
	m, err := NewRetentionManager(RetentionConfig{DataDir: dir, Events: events, Captures: captures})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func eventFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dir, eventsSubdir))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".json") && !strings.HasPrefix(e.Name(), ".") {
			names = append(names, e.Name())
		}
	}
	return names
}

func captureFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dir, evidenceSubdir, capturesSubdir))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".jpg") {
			names = append(names, e.Name())
		}
	}
	return names
}

// --- 1. count bound -------------------------------------------------------

func TestRetention_EventCountBound(t *testing.T) {
	dir := t.TempDir()
	base := time.Now().UTC().Add(-time.Hour)
	for i := 0; i < 5; i++ {
		writeEvent(t, dir, fmt.Sprintf("%s-%d", retTestUUID, i), SyncStatusSynced, base.Add(time.Duration(i)*time.Minute), "")
	}
	m := newRetention(t, dir, RetentionBounds{MaxCount: 2}, RetentionBounds{})

	rep, err := m.Sweep(time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if rep.EventsEvicted != 3 {
		t.Fatalf("evicted %d events, want 3", rep.EventsEvicted)
	}
	if got := len(eventFiles(t, dir)); got != 2 {
		t.Fatalf("retained %d events, want 2", got)
	}
}

// --- 2. byte bound --------------------------------------------------------

func TestRetention_EventByteBound(t *testing.T) {
	dir := t.TempDir()
	base := time.Now().UTC().Add(-time.Hour)
	var sizes []int64
	for i := 0; i < 4; i++ {
		p := writeEvent(t, dir, fmt.Sprintf("%s-%d", retTestUUID, i), SyncStatusSynced, base.Add(time.Duration(i)*time.Minute), "")
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		sizes = append(sizes, info.Size())
	}
	// A bound that fits only the two newest.
	m := newRetention(t, dir, RetentionBounds{MaxBytes: sizes[2] + sizes[3]}, RetentionBounds{})
	if _, err := m.Sweep(time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if got := len(eventFiles(t, dir)); got != 2 {
		t.Fatalf("retained %d events under a byte bound, want 2", got)
	}
}

// --- 3. TTL bound ---------------------------------------------------------

func TestRetention_EventTTLBound(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	writeEvent(t, dir, retTestUUID+"-old", SyncStatusSynced, now.Add(-48*time.Hour), "")
	writeEvent(t, dir, retTestUUID+"-new", SyncStatusSynced, now.Add(-time.Minute), "")

	m := newRetention(t, dir, RetentionBounds{MaxAge: 24 * time.Hour}, RetentionBounds{})
	rep, err := m.Sweep(now)
	if err != nil {
		t.Fatal(err)
	}
	if rep.EventsEvicted != 1 {
		t.Fatalf("evicted %d, want 1 (only the aged event)", rep.EventsEvicted)
	}
	files := eventFiles(t, dir)
	if len(files) != 1 || !strings.Contains(files[0], "-new") {
		t.Fatalf("wrong survivor: %v", files)
	}
}

func TestRetention_CaptureTTLBound(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	writeCapture(t, dir, "old-capture", 10, now.Add(-48*time.Hour))
	writeCapture(t, dir, "new-capture", 10, now.Add(-time.Minute))

	m := newRetention(t, dir, RetentionBounds{}, RetentionBounds{MaxAge: 24 * time.Hour})
	rep, err := m.Sweep(now)
	if err != nil {
		t.Fatal(err)
	}
	if rep.CapturesEvicted != 1 {
		t.Fatalf("evicted %d captures, want 1", rep.CapturesEvicted)
	}
	files := captureFiles(t, dir)
	if len(files) != 1 || files[0] != "new-capture.jpg" {
		t.Fatalf("wrong capture survivor: %v", files)
	}
}

// --- 4. oldest-first ------------------------------------------------------

func TestRetention_EvictsOldestFirst(t *testing.T) {
	dir := t.TempDir()
	base := time.Now().UTC().Add(-time.Hour)
	// Deliberately insert out of chronological order.
	order := []struct {
		suffix string
		offset time.Duration
	}{
		{"-third", 30 * time.Minute},
		{"-first", 0},
		{"-fourth", 45 * time.Minute},
		{"-second", 15 * time.Minute},
	}
	for _, o := range order {
		writeEvent(t, dir, retTestUUID+o.suffix, SyncStatusSynced, base.Add(o.offset), "")
	}
	m := newRetention(t, dir, RetentionBounds{MaxCount: 2}, RetentionBounds{})
	if _, err := m.Sweep(time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	files := eventFiles(t, dir)
	joined := strings.Join(files, ",")
	for _, want := range []string{"-third", "-fourth"} {
		if !strings.Contains(joined, want) {
			t.Errorf("expected the two NEWEST (%s) to survive, got %v", want, files)
		}
	}
	for _, gone := range []string{"-first", "-second"} {
		if strings.Contains(joined, gone) {
			t.Errorf("oldest-first violated: %s survived; got %v", gone, files)
		}
	}
}

// --- 5. shared JPEG referenced by N events (F-A) --------------------------

func TestRetention_SharedCaptureSurvivesWhileAnyEventReferencesIt(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	capture := writeCapture(t, dir, "shared-capture", 32, now.Add(-72*time.Hour))
	ref := filepath.Join(evidenceSubdir, capturesSubdir, capture)

	// Three event records share ONE capture; a fourth is older and references
	// nothing.
	writeEvent(t, dir, retTestUUID+"-a", SyncStatusSynced, now.Add(-10*time.Minute), ref)
	writeEvent(t, dir, retTestUUID+"-b", SyncStatusSynced, now.Add(-9*time.Minute), ref)
	writeEvent(t, dir, retTestUUID+"-c", SyncStatusSynced, now.Add(-8*time.Minute), ref)
	writeEvent(t, dir, retTestUUID+"-orphan", SyncStatusSynced, now.Add(-50*time.Hour), "")

	// Retention is aggressive on captures: only 1 may remain. The shared
	// capture is far older than any bound but is still referenced, so it MUST
	// survive; the event count bound is satisfied separately.
	m := newRetention(t, dir,
		RetentionBounds{MaxCount: 2},
		RetentionBounds{MaxCount: 1, MaxAge: time.Minute})

	rep, err := m.Sweep(now)
	if err != nil {
		t.Fatal(err)
	}
	if rep.CapturesEvicted != 0 {
		t.Fatalf("evicted %d captures, want 0: the only capture is referenced by surviving events", rep.CapturesEvicted)
	}
	files := captureFiles(t, dir)
	if len(files) != 1 || files[0] != capture {
		t.Fatalf("shared capture was not preserved: %v", files)
	}
	if rep.Protected == 0 {
		t.Error("report does not record that something was protected by the reference rule")
	}
}

func TestRetention_OrphanedCaptureIsReclaimedAfterItsEventsAreEvicted(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	capture := writeCapture(t, dir, "soon-orphan", 16, now.Add(-4*time.Hour))
	ref := filepath.Join(evidenceSubdir, capturesSubdir, capture)
	writeEvent(t, dir, retTestUUID+"-only", SyncStatusSynced, now.Add(-3*time.Hour), ref)

	// Both trees bounded: the single event goes, so the capture becomes
	// unreferenced and may then be reclaimed in the same sweep.
	m := newRetention(t, dir,
		RetentionBounds{MaxCount: 0, MaxAge: time.Hour},
		RetentionBounds{MaxAge: time.Hour})

	rep, err := m.Sweep(now)
	if err != nil {
		t.Fatal(err)
	}
	if rep.EventsEvicted != 1 {
		t.Fatalf("events evicted = %d, want 1", rep.EventsEvicted)
	}
	if rep.CapturesEvicted != 1 {
		t.Fatalf("captures evicted = %d, want 1 (it became unreferenced)", rep.CapturesEvicted)
	}
	if len(captureFiles(t, dir)) != 0 {
		t.Fatal("orphaned capture was not reclaimed")
	}
}

// --- 6. pending event metadata protected (F-B) ---------------------------

func TestRetention_PendingEventMetadataIsNeverEvicted(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	writeEvent(t, dir, retTestUUID+"-pending", SyncStatusPending, now.Add(-72*time.Hour), "")
	writeEvent(t, dir, retTestUUID+"-synced", SyncStatusSynced, now.Add(-48*time.Hour), "")

	// Bounds so tight that both would otherwise go.
	m := newRetention(t, dir, RetentionBounds{MaxCount: 0, MaxBytes: 0, MaxAge: time.Nanosecond}, RetentionBounds{})
	rep, err := m.Sweep(now)
	if err != nil {
		t.Fatal(err)
	}
	files := eventFiles(t, dir)
	if len(files) != 1 || !strings.Contains(files[0], "-pending") {
		t.Fatalf("pending event metadata was evicted; survivors = %v", files)
	}
	if rep.EventsEvicted != 1 {
		t.Fatalf("evicted %d, want exactly the synced one", rep.EventsEvicted)
	}
}

// --- 7. pending capture protected (F-B) ----------------------------------

// writePendingRecord writes an edgebacklog-shaped pending record referencing an
// absolute evidence path, and points the manager at it.
func writePendingRecord(t *testing.T, dir string, seq int, captureAbs string) string {
	t.Helper()
	pendingDir := filepath.Join(dir, pendingBacklogSubdir, "pending")
	if err := os.MkdirAll(pendingDir, 0o750); err != nil {
		t.Fatal(err)
	}
	rec := map[string]any{
		"sequence":   seq,
		"created_at": time.Now().UTC().Format(time.RFC3339),
		"stage":      "capture",
		"submission": map[string]any{
			"event":   map[string]any{"event_uuid": retTestUUID},
			"capture": map[string]any{"path": captureAbs, "sha256": "x", "size": 8},
		},
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(pendingDir, fmt.Sprintf("%020d.json", seq))
	if err := os.WriteFile(p, raw, 0o640); err != nil {
		t.Fatal(err)
	}
	return pendingDir
}

func TestRetention_PendingBacklogEvidenceIsNeverEvicted(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	// Two captures, both ancient; only one is referenced by a pending record.
	referenced := writeCapture(t, dir, "pending-ref", 8, now.Add(-96*time.Hour))
	unreferenced := writeCapture(t, dir, "free-orphan", 8, now.Add(-96*time.Hour))
	absRef := filepath.Join(dir, evidenceSubdir, capturesSubdir, referenced)
	writePendingRecord(t, dir, 1, absRef)

	m := newRetention(t, dir, RetentionBounds{}, RetentionBounds{MaxCount: 1, MaxAge: time.Minute})
	rep, err := m.Sweep(now)
	if err != nil {
		t.Fatal(err)
	}
	files := captureFiles(t, dir)
	if len(files) != 1 || files[0] != referenced {
		t.Fatalf("pending-referenced capture was evicted; survivors = %v", files)
	}
	if strings.Contains(strings.Join(files, ","), unreferenced) {
		t.Error("the unreferenced capture should have been reclaimed")
	}
	if rep.CapturesEvicted != 1 {
		t.Fatalf("evicted %d, want 1", rep.CapturesEvicted)
	}
}

func TestRetention_PendingRecordWithForeignKeyCannotWidenScope(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	outside := filepath.Join(dir, "identity.json")
	if err := os.WriteFile(outside, []byte("must-survive"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A pending record pointing at a file OUTSIDE the captures tree must not
	// protect it, and must never cause it to be considered for deletion.
	outside2 := writeCapture(t, dir, "real", 8, now.Add(-96*time.Hour))
	_ = outside2
	writePendingRecord(t, dir, 1, outside)

	m := newRetention(t, dir, RetentionBounds{}, RetentionBounds{MaxAge: time.Minute})
	if _, err := m.Sweep(now); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(outside); err != nil || string(got) != "must-survive" {
		t.Fatalf("a file outside the captures tree was touched: %v %q", err, got)
	}
}

// --- 8. symlink / traversal safety ---------------------------------------

func TestRetention_DoesNotFollowSymlinksOrEscapeTheTree(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()

	// A precious file outside the owned trees, and a symlink inside the
	// captures tree pointing at it.
	precious := filepath.Join(dir, "identity.json")
	if err := os.WriteFile(precious, []byte("precious"), 0o600); err != nil {
		t.Fatal(err)
	}
	capsDir := filepath.Join(dir, evidenceSubdir, capturesSubdir)
	if err := os.MkdirAll(capsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(capsDir, "evil.jpg")
	if err := os.Symlink(precious, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	// Also a real capture that SHOULD be evicted.
	writeCapture(t, dir, "real-capture", 8, now.Add(-96*time.Hour))

	m := newRetention(t, dir, RetentionBounds{}, RetentionBounds{MaxAge: time.Minute})
	rep, err := m.Sweep(now)
	if err != nil {
		t.Fatal(err)
	}
	// The symlink target must be untouched.
	if got, err := os.ReadFile(precious); err != nil || string(got) != "precious" {
		t.Fatalf("retention followed a symlink out of the tree: %v %q", err, got)
	}
	// The symlink itself must still be there (we skip it rather than delete it).
	if _, err := os.Lstat(link); err != nil {
		t.Fatalf("retention deleted a symlink entry: %v", err)
	}
	// The REAL capture was aged out and evicted; the symlink entry was skipped
	// (not deleted, not followed), so only evil.jpg remains in the listing.
	if _, err := os.Stat(filepath.Join(capsDir, "real-capture.jpg")); !os.IsNotExist(err) {
		t.Errorf("the unreferenced real capture should have been evicted: %v", err)
	}
	if rep.CapturesEvicted != 1 {
		t.Errorf("captures evicted = %d, want 1 (only the real file)", rep.CapturesEvicted)
	}
}

func TestRetention_IgnoresFilesOutsideTheOwnedTrees(t *testing.T) {
	dir := t.TempDir()
	// Neighbours that must never be touched, even under an aggressive bound.
	others := map[string]string{
		"identity.json":       "identity",
		"credentials.json":    "credentials",
		"camera_master.key":   "masterkey",
		"control_ledger.json": "ledger",
	}
	for name, body := range others {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	modelsDir := filepath.Join(dir, "models")
	if err := os.MkdirAll(modelsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	weight := filepath.Join(modelsDir, "yolo11n.pt")
	if err := os.WriteFile(weight, []byte("weights"), 0o600); err != nil {
		t.Fatal(err)
	}

	m := newRetention(t, dir,
		RetentionBounds{MaxCount: 0, MaxBytes: 0, MaxAge: time.Nanosecond},
		RetentionBounds{MaxCount: 0, MaxBytes: 0, MaxAge: time.Nanosecond})
	if _, err := m.Sweep(time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	for name, body := range others {
		if got, err := os.ReadFile(filepath.Join(dir, name)); err != nil || string(got) != body {
			t.Errorf("retention touched %s: %v %q", name, err, got)
		}
	}
	if got, err := os.ReadFile(weight); err != nil || string(got) != "weights" {
		t.Errorf("retention touched models/: %v %q", err, got)
	}
}

// --- 9. delete failure ---------------------------------------------------

func TestRetention_DeleteFailureIsReportedAndStopsEviction(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root bypasses directory permission checks")
	}
	dir := t.TempDir()
	now := time.Now().UTC()
	base := now.Add(-time.Hour)
	for i := 0; i < 3; i++ {
		writeEvent(t, dir, fmt.Sprintf("%s-%d", retTestUUID, i), SyncStatusSynced, base.Add(time.Duration(i)*time.Minute), "")
	}
	eventsDir := filepath.Join(dir, eventsSubdir)
	// Make the directory read-only so os.Remove fails.
	if err := os.Chmod(eventsDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(eventsDir, 0o700) })

	m := newRetention(t, dir, RetentionBounds{MaxCount: 1}, RetentionBounds{})
	rep, err := m.Sweep(now)
	if err == nil {
		t.Fatal("expected Sweep to report the delete failure")
	}
	if rep.Failed == 0 {
		t.Error("report does not count the failure")
	}
	if rep.EventsEvicted != 0 {
		t.Errorf("report claims %d evictions while deletes were failing", rep.EventsEvicted)
	}
	// Accounting must not claim a bound was met while files remain.
	if got := len(eventFiles(t, dir)); got != 3 {
		t.Errorf("files = %d, want 3 (nothing actually removed)", got)
	}
}

// --- 10. restart accounting ----------------------------------------------

func TestRetention_RestartProducesDeterministicAccounting(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	base := now.Add(-time.Hour)
	for i := 0; i < 6; i++ {
		writeEvent(t, dir, fmt.Sprintf("%s-%d", retTestUUID, i), SyncStatusSynced, base.Add(time.Duration(i)*time.Minute), "")
	}
	bounds := RetentionBounds{MaxCount: 3}

	first := newRetention(t, dir, bounds, RetentionBounds{})
	rep1, err := first.Sweep(now)
	if err != nil {
		t.Fatal(err)
	}
	after1 := len(eventFiles(t, dir))

	// Re-open on the same directory: a second sweep must be a no-op, not a
	// different answer.
	second := newRetention(t, dir, bounds, RetentionBounds{})
	rep2, err := second.Sweep(now)
	if err != nil {
		t.Fatal(err)
	}
	if rep2.EventsEvicted != 0 {
		t.Errorf("a re-sweep after restart evicted %d more; accounting is not deterministic", rep2.EventsEvicted)
	}
	if got := len(eventFiles(t, dir)); got != after1 {
		t.Errorf("file count drifted across restart: %d -> %d", after1, got)
	}
	if rep1.EventsEvicted != 3 {
		t.Errorf("first sweep evicted %d, want 3", rep1.EventsEvicted)
	}
}

// --- 11. repeated writes stay bounded ------------------------------------

func TestRetention_RepeatedWritesStayBounded(t *testing.T) {
	dir := t.TempDir()
	m := newRetention(t, dir, RetentionBounds{MaxCount: 5}, RetentionBounds{MaxCount: 5})
	now := time.Now().UTC()

	for i := 0; i < 40; i++ {
		writeEvent(t, dir, fmt.Sprintf("%s-%d", retTestUUID, i), SyncStatusSynced, now.Add(time.Duration(i)*time.Second), "")
		if _, err := m.Sweep(now); err != nil {
			t.Fatal(err)
		}
	}
	if got := len(eventFiles(t, dir)); got > 5 {
		t.Fatalf("event tree grew past its bound: %d > 5", got)
	}
}

// --- 12. concurrent write + evict ----------------------------------------

func TestRetention_ConcurrentWriteAndEvictIsRaceFree(t *testing.T) {
	dir := t.TempDir()
	m := newRetention(t, dir, RetentionBounds{MaxCount: 8}, RetentionBounds{MaxCount: 8})
	now := time.Now().UTC()

	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 25; i++ {
				writeEvent(t, dir, fmt.Sprintf("%s-%d-%d", retTestUUID, w, i),
					SyncStatusSynced, now.Add(time.Duration(i)*time.Second), "")
			}
		}(w)
	}
	for w := 0; w < 2; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 25; i++ {
				_, _ = m.Sweep(now)
			}
		}()
	}
	wg.Wait()
	// Nothing here may panic or corrupt; the bound is best-effort under
	// concurrency but must not be wildly exceeded.
	if _, err := m.Sweep(now); err != nil {
		t.Fatal(err)
	}
	if got := len(eventFiles(t, dir)); got > 8 {
		t.Fatalf("bound not enforced after concurrent churn: %d > 8", got)
	}
}

// --- backlogCount coherence ----------------------------------------------

func TestRetention_PreservesEventStoreBacklogCount(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	// One pending (must survive) and three synced (evictable).
	writeEvent(t, dir, retTestUUID+"-p", SyncStatusPending, now.Add(-10*time.Hour), "")
	for i := 0; i < 3; i++ {
		writeEvent(t, dir, fmt.Sprintf("%s-s%d", retTestUUID, i), SyncStatusSynced, now.Add(-time.Duration(i+1)*time.Hour), "")
	}
	store, err := NewEventStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	before := store.BacklogCount()
	if before != 1 {
		t.Fatalf("precondition: backlogCount = %d, want 1", before)
	}

	m := newRetention(t, dir, RetentionBounds{MaxCount: 1}, RetentionBounds{})
	if _, err := m.Sweep(now); err != nil {
		t.Fatal(err)
	}
	// Retention only removes non-pending records, so the pending count is
	// untouched both in memory and after a reopen.
	if got := store.BacklogCount(); got != before {
		t.Errorf("in-memory backlogCount changed: %d -> %d", before, got)
	}
	reopened, err := NewEventStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := reopened.BacklogCount(); got != before {
		t.Errorf("recomputed backlogCount = %d, want %d", got, before)
	}
}

// --- disabled / config surface -------------------------------------------

func TestRetention_DisabledByDefaultRetainsEverything(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	for i := 0; i < 5; i++ {
		writeEvent(t, dir, fmt.Sprintf("%s-%d", retTestUUID, i), SyncStatusSynced, now.Add(-1000*time.Hour), "")
	}
	writeCapture(t, dir, "cap", 8, now.Add(-1000*time.Hour))

	m := newRetention(t, dir, RetentionBounds{}, RetentionBounds{})
	if m.RetentionEnabled() {
		t.Fatal("retention reports enabled with no bounds configured")
	}
	rep, err := m.Sweep(now)
	if err != nil {
		t.Fatal(err)
	}
	if rep.EventsEvicted != 0 || rep.CapturesEvicted != 0 {
		t.Fatalf("nothing may be evicted with all bounds disabled: %+v", rep)
	}
	if len(eventFiles(t, dir)) != 5 || len(captureFiles(t, dir)) != 1 {
		t.Fatal("disabled retention did not retain everything")
	}
}

func TestRetention_TempOrphansAreSweptOnlyWhenAbandoned(t *testing.T) {
	dir := t.TempDir()
	eventsDir := filepath.Join(dir, eventsSubdir)
	if err := os.MkdirAll(eventsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()

	abandoned := filepath.Join(eventsDir, ".event-abandoned.tmp")
	fresh := filepath.Join(eventsDir, ".event-fresh.tmp")
	for _, p := range []string{abandoned, fresh} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	old := now.Add(-2 * tempFileGracePeriod)
	if err := os.Chtimes(abandoned, old, old); err != nil {
		t.Fatal(err)
	}

	m := newRetention(t, dir, RetentionBounds{}, RetentionBounds{})
	rep, err := m.Sweep(now)
	if err != nil {
		t.Fatal(err)
	}
	if rep.TempOrphansRemoved != 1 {
		t.Fatalf("removed %d temp orphans, want 1", rep.TempOrphansRemoved)
	}
	if _, err := os.Stat(abandoned); !os.IsNotExist(err) {
		t.Error("abandoned temp file was not removed")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Error("a fresh temp file was removed; it may belong to a write in flight")
	}
}

func TestNewRetentionManager_RequiresDataDir(t *testing.T) {
	if _, err := NewRetentionManager(RetentionConfig{}); err == nil {
		t.Fatal("expected an error for a missing data dir")
	}
}

// TestSecureListDir_ExcludesEverythingRetentionMustNotDelete tests the security
// primitive directly. The end-to-end symlink test above cannot reach this path
// deterministically, because a freshly created link is too new to be an
// eviction candidate — so the exclusion rule is pinned here instead, where it
// fails the moment the guard is removed.
func TestSecureListDir_ExcludesEverythingRetentionMustNotDelete(t *testing.T) {
	dir := t.TempDir()

	real := filepath.Join(dir, "keep.jpg")
	if err := os.WriteFile(real, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "target-outside")
	if err := os.WriteFile(target, []byte("precious"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.jpg")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	// A symlink to a DIRECTORY named like a capture, and a real subdirectory.
	if err := os.MkdirAll(filepath.Join(dir, "subdir.jpg"), 0o700); err != nil {
		t.Fatal(err)
	}
	// A dot-prefixed temp file, and a wrong-suffix file.
	for _, name := range []string{".evidence-inflight.tmp", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	got, err := secureListDir(dir, ".jpg", "evidence")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].name != "keep.jpg" {
		names := make([]string, 0, len(got))
		for _, e := range got {
			names = append(names, e.name)
		}
		t.Fatalf("secureListDir returned %v, want exactly [keep.jpg] — a symlink, a directory, a temp file or a wrong suffix must never become an eviction candidate", names)
	}
}

// TestPathInside_RejectsEscapes pins the containment check itself.
func TestPathInside_RejectsEscapes(t *testing.T) {
	root := filepath.Join(t.TempDir(), "captures")
	cases := []struct {
		p    string
		want bool
	}{
		{filepath.Join(root, "a.jpg"), true},
		{filepath.Join(root, "nested", "a.jpg"), true},
		{root, false},
		{filepath.Join(root, "..", "identity.json"), false},
		{filepath.Join(root, "..", "..", "etc", "passwd"), false},
		{"/etc/passwd", false},
	}
	for _, tc := range cases {
		if got := pathInside(root, tc.p); got != tc.want {
			t.Errorf("pathInside(%q, %q) = %v, want %v", root, tc.p, got, tc.want)
		}
	}
}

// TestServiceWiresRetentionFromConfig proves the manager is actually reachable
// from the production construction path — an unconstructed retention manager
// would be dead code, exactly the defect class B3 exists to avoid.
func TestServiceWiresRetentionFromConfig(t *testing.T) {
	dir := t.TempDir()
	store, err := NewEventStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	rm, err := NewRetentionManager(RetentionConfig{
		DataDir:  dir,
		Events:   RetentionBounds{MaxCount: 2},
		Captures: RetentionBounds{MaxCount: 2},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Pre-existing growth, aged well past any bound.
	now := time.Now().UTC()
	for i := 0; i < 6; i++ {
		writeEvent(t, dir, fmt.Sprintf("%s-%d", retTestUUID, i), SyncStatusSynced, now.Add(-time.Duration(i+1)*time.Hour), "")
	}

	svc := NewService(ServiceConfig{DataDir: dir, Retention: rm}, store, nil, nil, nil, nil, nil)
	if svc.Retention() == nil {
		t.Fatal("Service does not expose the retention manager")
	}
	// Construction runs one sweep, so pre-existing growth is already bounded.
	if got := len(eventFiles(t, dir)); got > 2 {
		t.Fatalf("startup sweep did not bound pre-existing growth: %d events", got)
	}
}

func TestServiceWithoutRetentionIsUnaffected(t *testing.T) {
	dir := t.TempDir()
	store, err := NewEventStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for i := 0; i < 5; i++ {
		writeEvent(t, dir, fmt.Sprintf("%s-%d", retTestUUID, i), SyncStatusSynced, now.Add(-1000*time.Hour), "")
	}
	svc := NewService(ServiceConfig{DataDir: dir}, store, nil, nil, nil, nil, nil)
	if svc.Retention() != nil {
		t.Fatal("expected a nil retention manager when unconfigured")
	}
	if got := len(eventFiles(t, dir)); got != 5 {
		t.Fatalf("unconfigured service deleted events: %d remain, want 5", got)
	}
}

func TestMaybeSweep_IsRateLimited(t *testing.T) {
	dir := t.TempDir()
	m := newRetention(t, dir, RetentionBounds{MaxCount: 1}, RetentionBounds{})
	now := time.Now().UTC()

	if _, ran := m.MaybeSweep(now, time.Hour); !ran {
		t.Fatal("first MaybeSweep should run")
	}
	if _, ran := m.MaybeSweep(now.Add(time.Minute), time.Hour); ran {
		t.Fatal("a second sweep inside the interval must be skipped")
	}
	if _, ran := m.MaybeSweep(now.Add(2*time.Hour), time.Hour); !ran {
		t.Fatal("a sweep after the interval should run")
	}
}

func TestMaybeSweep_NoOpWhenDisabled(t *testing.T) {
	dir := t.TempDir()
	m := newRetention(t, dir, RetentionBounds{}, RetentionBounds{})
	if _, ran := m.MaybeSweep(time.Now().UTC(), 0); ran {
		t.Fatal("MaybeSweep must not scan when no bound is configured")
	}
}
