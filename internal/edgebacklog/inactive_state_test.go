package edgebacklog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
