package edgebacklog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/transport"
)

// expireCamera drives cam-a to "inactive, retention expired, nothing pending"
// through the real 423 path.
func expireCamera(t *testing.T, b *Backlog, d string, retryMax time.Duration) {
	t.Helper()
	enqueueFor(t, b, d, "a1", "cam-a")
	inactive := &sender{errs: []error{transport.ErrCameraInactive, transport.ErrCameraInactive}}
	b.ProcessOne(context.Background(), inactive, "device", "token")
	time.Sleep(retryMax + 10*time.Millisecond)
	b.ProcessOne(context.Background(), inactive, "device", "token")
	if st := b.Status(); st.BacklogCount != 0 || st.InactiveExpired != 1 || len(st.InactiveCandidates) != 1 {
		t.Fatalf("setup: %+v, want cam-a marked with nothing pending", st)
	}
}

// writeInactiveState seeds inactive.json as a previous run would have left it.
func writeInactiveState(t *testing.T, d string, state map[string]inactiveState) {
	t.Helper()
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, inactiveStateFile), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func enqueueErr(t *testing.T, b *Backlog, d, id, key string) error {
	t.Helper()
	s := submission(t, d, id)
	s.Event.CandidateKey = key
	return b.Enqueue(s)
}

func TestInactiveStateSurvivesRestartWithNothingPending(t *testing.T) {
	d := t.TempDir()
	retryMax := 30 * time.Millisecond
	b := openInactive(t, d, retryMax, 20*time.Millisecond)
	expireCamera(t, b, d, retryMax)

	b = openInactive(t, d, retryMax, 20*time.Millisecond)
	st := b.Status()
	if st.BacklogCount != 0 || len(st.InactiveCandidates) != 1 || st.InactiveCandidates[0] != "cam-a" {
		t.Fatalf("after restart: %+v, want cam-a still marked with nothing pending", st)
	}
}

func TestInactiveStateAfterRestartBurstAdmitsOneProbe(t *testing.T) {
	d := t.TempDir()
	writeInactiveState(t, d, map[string]inactiveState{"cam-a": {Since: time.Now().Add(-48 * time.Hour)}})
	b := openInactive(t, d, time.Hour, 24*time.Hour)

	if err := enqueueErr(t, b, d, "p1", "cam-a"); err != nil {
		t.Fatalf("probe admission: %v", err)
	}
	for i := 0; i < 10; i++ {
		if err := enqueueErr(t, b, d, fmt.Sprintf("x%02d", i), "cam-a"); !errors.Is(err, ErrCameraInactive) {
			t.Fatalf("burst event %d: err=%v, want ErrCameraInactive", i, err)
		}
	}
	// Still inactive: the probe is quarantined on its first 423 (retention
	// already passed). The probe cooldown is durable too, so a second restart
	// inside RetryMax admits nothing.
	b.ProcessOne(context.Background(), &sender{errs: []error{transport.ErrCameraInactive}}, "device", "token")
	b = openInactive(t, d, time.Hour, 24*time.Hour)
	if err := enqueueErr(t, b, d, "x10", "cam-a"); !errors.Is(err, ErrCameraInactive) {
		t.Fatalf("event after second restart inside RetryMax: err=%v, want ErrCameraInactive", err)
	}
	if st := b.Status(); st.BacklogCount != 0 || len(st.InactiveCandidates) != 1 {
		t.Fatalf("status = %+v, want empty backlog and cam-a marked", st)
	}
}

func TestInactiveStateRestartThenReactivationClearsState(t *testing.T) {
	d := t.TempDir()
	writeInactiveState(t, d, map[string]inactiveState{"cam-a": {Since: time.Now().Add(-48 * time.Hour)}})
	b := openInactive(t, d, time.Hour, 24*time.Hour)
	if st := b.Status(); len(st.InactiveCandidates) != 1 {
		t.Fatalf("after restart: %+v, want cam-a marked", st)
	}

	enqueueFor(t, b, d, "p1", "cam-a")
	s := &sender{}
	drain(b, s)
	if fmt.Sprint(s.calls) != fmt.Sprint([]string{"metadata:p1", "capture:p1"}) {
		t.Fatalf("calls = %v, want probe p1 synced", s.calls)
	}
	if _, err := os.Stat(filepath.Join(d, inactiveStateFile)); !os.IsNotExist(err) {
		t.Fatalf("inactive state file after reactivation: err=%v, want removed", err)
	}
	enqueueFor(t, b, d, "a2", "cam-a")
	enqueueFor(t, b, d, "a3", "cam-a")
	drain(b, s)
	b = openInactive(t, d, time.Hour, 24*time.Hour)
	if st := b.Status(); st.BacklogCount != 0 || len(st.InactiveCandidates) != 0 || st.InactiveDrops != 0 {
		t.Fatalf("after reactivation + restart: %+v, want no mark, no drops", st)
	}
}

func TestInactiveProbeCooldownOnlyConsumedWhenQueued(t *testing.T) {
	d := t.TempDir()
	// RetryMax is the probe cooldown; the steps below take far less than it.
	retryMax := 500 * time.Millisecond
	b := openInactive(t, d, retryMax, 20*time.Millisecond)
	expireCamera(t, b, d, retryMax)

	// Capacity: the backlog is full of an active camera, the probe is refused.
	for i := 0; i < 8; i++ {
		enqueueFor(t, b, d, fmt.Sprintf("on%02d", i), "cam-on")
	}
	if err := enqueueErr(t, b, d, "p1", "cam-a"); !errors.Is(err, ErrFull) {
		t.Fatalf("probe on full backlog: err=%v, want ErrFull", err)
	}
	drain(b, &sender{})

	// Persistence: the probe's pending write fails with ENOSPC.
	f := &fsFault{}
	f.install(b)
	f.enabled.Store(true)
	f.failWrite.Store(true)
	if err := enqueueErr(t, b, d, "p2", "cam-a"); err == nil {
		t.Fatal("probe with failing disk: want an error")
	}
	f.enabled.Store(false)

	// Neither refusal consumed the RetryMax cooldown: the next event is
	// admitted as the probe right away.
	if err := enqueueErr(t, b, d, "p3", "cam-a"); err != nil {
		t.Fatalf("probe after refused attempts: %v, want admitted", err)
	}
	if st := b.Status(); st.BacklogCount != 1 || st.InactiveDrops != 0 {
		t.Fatalf("status = %+v, want only the probe pending, no inactive drops", st)
	}
}

func readInactiveState(t *testing.T, d string) map[string]inactiveState {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(d, inactiveStateFile))
	if err != nil {
		t.Fatal(err)
	}
	var state map[string]inactiveState
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	return state
}

func TestInactiveStateDirectoryIsSyncedAfterRenameAndRemove(t *testing.T) {
	d := t.TempDir()
	b := openInactive(t, d, time.Hour, 24*time.Hour)
	var ops []string
	b.renameFile = func(oldp, newp string) error {
		ops = append(ops, "rename:"+filepath.Base(newp))
		return os.Rename(oldp, newp)
	}
	b.removeFile = func(name string) error {
		ops = append(ops, "remove:"+filepath.Base(name))
		return os.Remove(name)
	}
	b.syncDir = func(dir string) error {
		ops = append(ops, "syncdir:"+dir)
		return syncDirFS(dir)
	}
	enqueueFor(t, b, d, "a1", "cam-a")
	b.ProcessOne(context.Background(), &sender{errs: []error{transport.ErrCameraInactive}}, "device", "token")
	b.MarkCandidateActive("cam-a")

	var got []string
	for _, op := range ops {
		if strings.Contains(op, inactiveStateFile) || strings.HasPrefix(op, "syncdir:") {
			got = append(got, op)
		}
	}
	want := []string{"rename:" + inactiveStateFile, "syncdir:" + d, "remove:" + inactiveStateFile, "syncdir:" + d}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("ops = %v, want %v (directory fsync after the rename and after the remove)", got, want)
	}
	if st := b.Status(); st.PersistErrors != 0 {
		t.Fatalf("persist_errors = %d, want 0", st.PersistErrors)
	}
}

func TestInactiveStateDirectorySyncFailureIsReported(t *testing.T) {
	d := t.TempDir()
	b := openInactive(t, d, time.Hour, 24*time.Hour)
	b.syncDir = func(dir string) error {
		return &os.PathError{Op: "fsync", Path: dir, Err: syscall.EIO}
	}
	enqueueFor(t, b, d, "a1", "cam-a")
	b.ProcessOne(context.Background(), &sender{errs: []error{transport.ErrCameraInactive}}, "device", "token")
	st := b.Status()
	if st.PersistErrors != 1 || !strings.Contains(st.LastError, "fsync") {
		t.Fatalf("after marking with failing dir sync: persist_errors=%d last_error=%q, want 1 and an fsync error", st.PersistErrors, st.LastError)
	}
	// The mark is still enforced in memory.
	if len(st.InactiveCandidates) != 1 {
		t.Fatalf("inactive_candidates = %v, want [cam-a]", st.InactiveCandidates)
	}
	// Reactivation removes the file; its directory sync failure is reported too.
	b.MarkCandidateActive("cam-a")
	if st := b.Status(); st.PersistErrors != 2 || len(st.InactiveCandidates) != 0 {
		t.Fatalf("after reactivation with failing dir sync: %+v, want persist_errors 2 and no mark", st)
	}
}

// seedCameras writes n marks cam-000..cam-(n-1); a lower index is an older
// mark, and each has its own probe_at so corruption would be visible.
func seedCameras(t *testing.T, d string, n int, base time.Time) map[string]inactiveState {
	t.Helper()
	state := make(map[string]inactiveState, n)
	for i := 0; i < n; i++ {
		state[fmt.Sprintf("cam-%03d", i)] = inactiveState{
			Since:   base.Add(time.Duration(i) * time.Minute),
			ProbeAt: base.Add(time.Duration(i)*time.Minute + 30*time.Second),
		}
	}
	writeInactiveState(t, d, state)
	return state
}

func TestInactiveStateCapEvictsOldestMarkAt257(t *testing.T) {
	d := t.TempDir()
	base := time.Now().Add(-48 * time.Hour).UTC().Truncate(time.Second)
	seeded := seedCameras(t, d, maxInactiveCameras, base)
	b := openInactive(t, d, time.Hour, 24*time.Hour)
	if n := len(b.Status().InactiveCandidates); n != maxInactiveCameras {
		t.Fatalf("after load: %d marks, want %d (256 fit, nothing evicted)", n, maxInactiveCameras)
	}

	// The 257th camera is learned through the real 423 path.
	enqueueFor(t, b, d, "n1", "cam-new")
	b.ProcessOne(context.Background(), &sender{errs: []error{transport.ErrCameraInactive}}, "device", "token")

	st := b.Status()
	if len(st.InactiveCandidates) != maxInactiveCameras {
		t.Fatalf("memory: %d marks, want %d", len(st.InactiveCandidates), maxInactiveCameras)
	}
	onDisk := readInactiveState(t, d)
	if len(onDisk) != maxInactiveCameras {
		t.Fatalf("inactive.json: %d entries, want %d", len(onDisk), maxInactiveCameras)
	}
	if _, ok := onDisk["cam-000"]; ok {
		t.Fatal("cam-000 (oldest mark) must be the one evicted")
	}
	if _, ok := onDisk["cam-new"]; !ok {
		t.Fatal("cam-new must be kept")
	}
	for i := 1; i < maxInactiveCameras; i++ {
		k := fmt.Sprintf("cam-%03d", i)
		got, want := onDisk[k], seeded[k]
		if !got.Since.Equal(want.Since) || !got.ProbeAt.Equal(want.ProbeAt) {
			t.Fatalf("%s = %+v, want unchanged %+v", k, got, want)
		}
	}

	// Known tradeoff: the evicted camera is no longer known inactive, so it
	// admits events again (not probe-limited) until its next 423 re-marks it,
	// which in turn evicts the next oldest mark.
	for _, id := range []string{"e1", "e2"} {
		if err := enqueueErr(t, b, d, id, "cam-000"); err != nil {
			t.Fatalf("evicted camera event %s: %v, want accepted", id, err)
		}
	}
}

func TestInactiveStateCapAppliesOnLoad(t *testing.T) {
	d := t.TempDir()
	base := time.Now().Add(-48 * time.Hour).UTC().Truncate(time.Second)
	seedCameras(t, d, maxInactiveCameras+44, base)
	b := openInactive(t, d, time.Hour, 24*time.Hour)

	marks := b.Status().InactiveCandidates
	if len(marks) != maxInactiveCameras {
		t.Fatalf("after loading 300 entries: %d marks, want %d", len(marks), maxInactiveCameras)
	}
	// Sorted keys: the 44 oldest (cam-000..cam-043) were dropped.
	if marks[0] != "cam-044" || marks[len(marks)-1] != "cam-299" {
		t.Fatalf("kept marks %s..%s, want cam-044..cam-299", marks[0], marks[len(marks)-1])
	}
}
