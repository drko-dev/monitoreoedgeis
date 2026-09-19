package edgebacklog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/platform"
	"github.com/drko-dev/monitoreoedgeis/internal/transport"
)

// fsFault replaces a Backlog's filesystem seam with one that fails a chosen
// operation with a genuine ENOSPC errno while it is enabled. Injecting at the
// seam — rather than filling a real filesystem, or mounting a small tmpfs,
// which needs root — is what makes this deterministic and portable: the test
// exercises the real writeLocked/recover/quarantine code, and only the
// filesystem's answer changes.
type fsFault struct {
	enabled    atomic.Bool
	failWrite  atomic.Bool
	failRemove atomic.Bool
	renameTo   atomic.Value // string: fail renames whose destination contains it
}

func (f *fsFault) install(b *Backlog) {
	b.writeFile = func(name string, data []byte, perm os.FileMode) error {
		if f.enabled.Load() && f.failWrite.Load() {
			return &os.PathError{Op: "write", Path: name, Err: syscall.ENOSPC}
		}
		return os.WriteFile(name, data, perm)
	}
	b.renameFile = func(oldp, newp string) error {
		if sub, _ := f.renameTo.Load().(string); f.enabled.Load() && sub != "" && strings.Contains(newp, sub) {
			return &os.LinkError{Op: "rename", Old: oldp, New: newp, Err: syscall.ENOSPC}
		}
		return os.Rename(oldp, newp)
	}
	b.removeFile = func(name string) error {
		if f.enabled.Load() && f.failRemove.Load() {
			return &os.PathError{Op: "remove", Path: name, Err: syscall.ENOSPC}
		}
		return os.Remove(name)
	}
}

// openWithFault opens a Backlog and installs the fault seam on it.
func openWithFault(t *testing.T, dir string, maxOps int, maxBytes int64) (*Backlog, *fsFault) {
	t.Helper()
	b, err := Open(Config{Dir: dir, MaxOperations: maxOps, MaxBytes: maxBytes, RetryBase: time.Millisecond, RetryMax: 2 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	f := &fsFault{}
	f.install(b)
	return b, f
}

func pendingFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dir, "pending"))
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func quarantineFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dir, "quarantine"))
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// TestEnqueueDiskFullIsClassifiedAndLeavesNoPartialFile is the core Y6
// property: an out-of-space write must be propagated as a classified error,
// must not enter the in-memory queue, must not leave a partial record behind,
// and must not touch the record that was already safely on disk.
func TestEnqueueDiskFullIsClassifiedAndLeavesNoPartialFile(t *testing.T) {
	dir := t.TempDir()
	b, fault := openWithFault(t, dir, 8, 1<<20)

	if err := b.Enqueue(submission(t, dir, "one")); err != nil {
		t.Fatalf("first enqueue must succeed: %v", err)
	}
	before := pendingFiles(t, dir)
	if len(before) != 1 {
		t.Fatalf("pending files after a successful enqueue = %v, want exactly 1", before)
	}
	validPath := filepath.Join(dir, "pending", before[0])
	validBytes, err := os.ReadFile(validPath)
	if err != nil {
		t.Fatal(err)
	}
	// The file must be valid JSON as stored, not just present.
	var probe record
	if err := json.Unmarshal(validBytes, &probe); err != nil {
		t.Fatalf("the persisted record is not valid JSON: %v", err)
	}

	fault.enabled.Store(true)
	fault.failWrite.Store(true)

	err = b.Enqueue(submission(t, dir, "two"))
	if err == nil {
		t.Fatal("enqueue with a full disk returned nil, want an error")
	}
	if !errors.Is(err, platform.ErrDiskFull) {
		t.Errorf("enqueue error %v does not classify as platform.ErrDiskFull", err)
	}
	if !errors.Is(err, syscall.ENOSPC) {
		t.Errorf("enqueue error %v does not keep the raw ENOSPC reachable", err)
	}
	if !platform.IsDiskFull(err) {
		t.Errorf("platform.IsDiskFull(%v) = false, want true", err)
	}

	if got := b.Status(); got.BacklogCount != 1 {
		t.Errorf("backlog_count = %d after a failed enqueue, want 1 (the refused submission must not be queued)", got.BacklogCount)
	}
	// A write failure is not a capacity drop: Drops counts MaxOperations/
	// MaxBytes refusals and must stay 0 here, or the operator cannot tell
	// which bound actually refused work.
	if got := b.Status(); got.Drops != 0 {
		t.Errorf("drops = %d, want 0: a disk-full write failure is not a capacity drop", got.Drops)
	}
	if got := b.Status(); !got.DiskFull {
		t.Error("status.disk_full = false, want true after an ENOSPC write")
	}
	if got := b.Status(); got.PersistErrors != 1 {
		t.Errorf("persist_errors = %d, want 1", got.PersistErrors)
	}

	// No partial record and no orphan temp file may be left behind.
	after := pendingFiles(t, dir)
	if len(after) != 1 {
		t.Errorf("pending files after a failed enqueue = %v, want exactly the 1 pre-existing file", after)
	}
	for _, name := range after {
		if strings.HasSuffix(name, ".tmp") {
			t.Errorf("orphan temp file %q survived a failed write", name)
		}
	}
	nowBytes, err := os.ReadFile(validPath)
	if err != nil {
		t.Fatalf("the previously valid record was removed by a failed write: %v", err)
	}
	if string(nowBytes) != string(validBytes) {
		t.Errorf("the previously valid record was modified by a failed write")
	}
}

// TestEnqueueRecoversWhenSpaceReturns covers the "recovery when free space
// comes back" requirement without a restart: the same object must start
// accepting submissions again and must stop reporting a full disk.
func TestEnqueueRecoversWhenSpaceReturns(t *testing.T) {
	dir := t.TempDir()
	b, fault := openWithFault(t, dir, 8, 1<<20)

	fault.enabled.Store(true)
	fault.failWrite.Store(true)
	if err := b.Enqueue(submission(t, dir, "one")); err == nil {
		t.Fatal("enqueue on a full disk returned nil, want an error")
	}
	if !b.Status().DiskFull {
		t.Fatal("status.disk_full = false, want true")
	}

	fault.enabled.Store(false)
	if err := b.Enqueue(submission(t, dir, "two")); err != nil {
		t.Fatalf("enqueue after space returned: %v", err)
	}
	st := b.Status()
	if st.DiskFull {
		t.Error("status.disk_full stayed true after a successful write; the flag must not latch")
	}
	if st.BacklogCount != 1 {
		t.Errorf("backlog_count = %d, want 1", st.BacklogCount)
	}
	// The count is cumulative — it is evidence that a failure happened, not
	// a live indicator, and the live indicator is DiskFull.
	if st.PersistErrors != 1 {
		t.Errorf("persist_errors = %d, want 1 (cumulative)", st.PersistErrors)
	}
}

// TestProcessOnePersistFailureIsVisibleNotSilent is the regression test for
// the swallowed `_ = b.writeLocked(r)` calls. The record must stay queued (the
// event is not lost) while the failure is counted and classified.
func TestProcessOnePersistFailureIsVisibleNotSilent(t *testing.T) {
	dir := t.TempDir()
	b, fault := openWithFault(t, dir, 8, 1<<20)
	if err := b.Enqueue(submission(t, dir, "one")); err != nil {
		t.Fatal(err)
	}

	failing := &sender{errs: []error{transport.ErrTimeout, transport.ErrTimeout}}
	fault.enabled.Store(true)
	fault.failWrite.Store(true)

	if !b.ProcessOne(context.Background(), failing, "device-1", "super-secret-credential") {
		t.Fatal("ProcessOne reported no work, want it to attempt the queued record")
	}
	st := b.Status()
	if st.BacklogCount != 1 {
		t.Errorf("backlog_count = %d, want 1: a failed retry-state write must not drop the event", st.BacklogCount)
	}
	if st.PersistErrors != 1 {
		t.Errorf("persist_errors = %d, want 1", st.PersistErrors)
	}
	if !st.DiskFull {
		t.Error("status.disk_full = false, want true")
	}
	if st.LastError == "" {
		t.Error("status.last_error is empty: a persistence failure must be reported")
	}
	// The error text is operator-visible over /status, so it must never carry
	// the credential the send path was handed.
	if strings.Contains(st.LastError, "super-secret-credential") {
		t.Errorf("status.last_error leaked the credential: %q", st.LastError)
	}
	if len(st.LastError) > maxErrorBytes {
		t.Errorf("status.last_error is %d bytes, want it bounded to %d", len(st.LastError), maxErrorBytes)
	}

	// Space comes back: the next persistence succeeds and clears the flag.
	// The record's NextAttempt backoff has to elapse first, otherwise
	// ProcessOne correctly reports no eligible work and nothing is written.
	fault.enabled.Store(false)
	time.Sleep(3 * time.Millisecond)
	b.ProcessOne(context.Background(), failing, "device-1", "super-secret-credential")
	st = b.Status()
	if st.DiskFull {
		t.Error("status.disk_full stayed true after a successful persist")
	}
	if st.PersistErrors != 1 {
		t.Errorf("persist_errors = %d, want 1 (cumulative)", st.PersistErrors)
	}
}

// TestPersistFailureRecoveryRepeats drives the failure/recovery cycle ten
// times, because "it recovers once" does not prove the state machine is not
// one-shot. After every cycle the counter has advanced by exactly one and the
// live flag is false, and the event is still queued exactly once.
func TestPersistFailureRecoveryRepeats(t *testing.T) {
	dir := t.TempDir()
	b, fault := openWithFault(t, dir, 8, 1<<20)
	if err := b.Enqueue(submission(t, dir, "one")); err != nil {
		t.Fatal(err)
	}

	// RetryBase is 1ms in openWithFault, so the record is eligible again
	// after a short sleep and ten cycles stay fast.
	failing := &sender{errs: make([]error, 0)}

	for i := 1; i <= 10; i++ {
		failing.errs = append(failing.errs, transport.ErrTimeout)
		fault.enabled.Store(true)
		fault.failWrite.Store(true)
		time.Sleep(3 * time.Millisecond)
		b.ProcessOne(context.Background(), failing, "d", "c")

		st := b.Status()
		if st.PersistErrors != int64(i) {
			t.Fatalf("cycle %d: persist_errors = %d, want %d", i, st.PersistErrors, i)
		}
		if !st.DiskFull {
			t.Fatalf("cycle %d: status.disk_full = false, want true", i)
		}
		if st.BacklogCount != 1 {
			t.Fatalf("cycle %d: backlog_count = %d, want 1", i, st.BacklogCount)
		}

		failing.errs = append(failing.errs, transport.ErrTimeout)
		fault.enabled.Store(false)
		time.Sleep(3 * time.Millisecond)
		b.ProcessOne(context.Background(), failing, "d", "c")

		st = b.Status()
		if st.DiskFull {
			t.Fatalf("cycle %d: status.disk_full stayed true after recovery", i)
		}
		if st.PersistErrors != int64(i) {
			t.Fatalf("cycle %d: persist_errors = %d after recovery, want %d unchanged", i, st.PersistErrors, i)
		}
		if st.BacklogCount != 1 {
			t.Fatalf("cycle %d: backlog_count = %d after recovery, want 1", i, st.BacklogCount)
		}
	}
}

// TestQuarantineMoveFailureDoesNotResurrectTheRecord covers the swallowed
// rename in quarantineLocked. A record the SaaS permanently rejected must not
// come back after a restart just because the archive move failed.
func TestQuarantineMoveFailureDoesNotResurrectTheRecord(t *testing.T) {
	dir := t.TempDir()
	b, fault := openWithFault(t, dir, 8, 1<<20)
	if err := b.Enqueue(submission(t, dir, "poison")); err != nil {
		t.Fatal(err)
	}

	fault.enabled.Store(true)
	fault.renameTo.Store(string(filepath.Join("quarantine")))
	rejecting := &sender{errs: []error{transport.ErrInvalidRequest}}
	if !b.ProcessOne(context.Background(), rejecting, "d", "c") {
		t.Fatal("ProcessOne reported no work")
	}
	fault.enabled.Store(false)

	st := b.Status()
	if st.BacklogCount != 0 {
		t.Errorf("backlog_count = %d, want 0: the permanently-rejected record must leave the queue", st.BacklogCount)
	}
	if st.PersistErrors == 0 {
		t.Error("persist_errors = 0: a failed quarantine move must be reported, not swallowed")
	}
	if names := pendingFiles(t, dir); len(names) != 0 {
		t.Errorf("pending still holds %v: a failed quarantine move left the poison record to resurrect on restart", names)
	}

	// The real proof: reopening must not bring it back.
	reopened, err := Open(Config{Dir: dir, MaxOperations: 8, MaxBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	if got := reopened.Status().BacklogCount; got != 0 {
		t.Errorf("after restart backlog_count = %d, want 0: the quarantined record resurrected", got)
	}
}

// TestQuarantineArenaIsBoundedAcrossRestart closes the unbounded on-disk
// growth of quarantine/, which nothing ever drains. The bound reuses the
// queue's own MaxOperations rather than inventing a second number.
func TestQuarantineArenaIsBoundedAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	b, _ := openWithFault(t, dir, 2, 1<<20)

	rejecting := &sender{errs: []error{transport.ErrInvalidRequest, transport.ErrInvalidRequest, transport.ErrInvalidRequest}}
	for _, id := range []string{"p1", "p2", "p3"} {
		if err := b.Enqueue(submission(t, dir, id)); err != nil {
			t.Fatal(err)
		}
		b.ProcessOne(context.Background(), rejecting, "d", "c")
	}

	if got := len(quarantineFiles(t, dir)); got > 2 {
		t.Errorf("quarantine holds %d files, want <= MaxOperations (2): the arena is unbounded", got)
	}
	st := b.Status()
	if st.Quarantined != len(quarantineFiles(t, dir)) {
		t.Errorf("status.quarantined = %d, want the real on-disk count %d", st.Quarantined, len(quarantineFiles(t, dir)))
	}
	if st.QuarantineEvicted == 0 {
		t.Error("quarantine_evicted = 0 although the arena was pruned; eviction must be counted, never silent")
	}

	// A restart must re-establish the bound, not reset it: pre-seed more
	// files than the bound and confirm Open prunes them.
	for i := 0; i < 5; i++ {
		path := filepath.Join(dir, "quarantine", pad(i+100)+".json")
		if err := os.WriteFile(path, []byte(`{"sequence":1}`), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	reopened, err := Open(Config{Dir: dir, MaxOperations: 2, MaxBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(quarantineFiles(t, dir)); got > 2 {
		t.Errorf("after restart quarantine holds %d files, want <= 2", got)
	}
	if got := reopened.Status().Quarantined; got > 2 {
		t.Errorf("after restart status.quarantined = %d, want <= 2", got)
	}
}

func pad(n int) string {
	return fmt.Sprintf("%020d", n)
}

// TestRecoveryOverCapacityIsReportedNotDropped covers the recovery-time bound
// hole: recover() loads every pending file, including more than the configured
// bound. Those records are durable, unsynced events, so dropping them silently
// would destroy evidence — the honest behaviour is to keep them, report the
// violation, and refuse new work until the queue drains.
func TestRecoveryOverCapacityIsReportedNotDropped(t *testing.T) {
	dir := t.TempDir()
	seed, err := Open(Config{Dir: dir, MaxOperations: 100, MaxBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b", "c", "d", "e"} {
		if err := seed.Enqueue(submission(t, dir, id)); err != nil {
			t.Fatal(err)
		}
	}

	// Reopen with a much smaller bound: the on-disk spool now exceeds it.
	b, err := Open(Config{Dir: dir, MaxOperations: 2, MaxBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	st := b.Status()
	if st.RecoveredOperations != 5 {
		t.Errorf("recovered_operations = %d, want 5", st.RecoveredOperations)
	}
	if st.BacklogCount != 5 {
		t.Errorf("backlog_count = %d, want 5: recovery must not silently discard durable events", st.BacklogCount)
	}
	if !st.OverCapacity {
		t.Error("over_capacity = false although the recovered spool exceeds MaxOperations; the violation must be explicit")
	}
	// New work is refused, which is the bound actually taking effect.
	if err := b.Enqueue(submission(t, dir, "f")); !errors.Is(err, ErrFull) {
		t.Errorf("enqueue while over capacity = %v, want ErrFull", err)
	}
	if got := b.Status().Drops; got != 1 {
		t.Errorf("drops = %d, want 1", got)
	}
}

// TestStatusSurvivesDiskFullWithNoPanic is the "no panic" half of the Y6
// contract, driven through every persistence path with the seam failing:
// enqueue, a retry-state write, a removal, and a quarantine move.
func TestStatusSurvivesDiskFullWithNoPanic(t *testing.T) {
	dir := t.TempDir()
	b, fault := openWithFault(t, dir, 8, 1<<20)

	fault.enabled.Store(true)
	fault.failWrite.Store(true)
	fault.failRemove.Store(true)
	fault.renameTo.Store("quarantine")

	s := &sender{errs: []error{transport.ErrInvalidRequest, transport.ErrTimeout}}
	for i := 0; i < 4; i++ {
		_ = b.Enqueue(submission(t, dir, "x"))
		b.ProcessOne(context.Background(), s, "d", "c")
		_ = b.Status()
	}
	if got := b.Status(); got.PersistErrors == 0 {
		t.Error("persist_errors = 0 after every persistence path failed")
	}
	// The zero value must still be a coherent status, never a panic.
	empty, err := Open(Config{Dir: t.TempDir(), MaxOperations: 1, MaxBytes: 1})
	if err != nil {
		t.Fatal(err)
	}
	if st := empty.Status(); st.BacklogCount != 0 || st.OverCapacity {
		t.Errorf("empty backlog status = %+v, want a zeroed, non-over-capacity status", st)
	}
}
