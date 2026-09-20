package control

import (
	"context"
	"fmt"
	"testing"

	"github.com/drko-dev/monitoreoedgeis/internal/transport"
)

// newStatusCommand builds a command the allowlist accepts and whose executor
// does nothing observable, so a test can dispatch thousands cheaply.
func newStatusCommand(i int) *transport.ControlCommand {
	return &transport.ControlCommand{ID: fmt.Sprintf("cmd-%05d", i), CommandType: "request_status"}
}

// TestExecutedMapIsBounded is the regression test for the in-memory idempotency
// map that had no delete path: every distinct control command the SaaS ever
// dispatched added a permanent entry, so a long-lived Edge accumulated them for
// the whole process lifetime. The durable ledger was bounded; this map was not.
func TestExecutedMapIsBounded(t *testing.T) {
	exec := &fakeExecutor{}
	m := New(nil, exec, "device-1", "credential-1")

	total := defaultMaxTrackedExecutions + 200
	for i := 0; i < total; i++ {
		if _, _, _, err := m.ExecuteCommand(context.Background(), newStatusCommand(i)); err != nil {
			t.Fatalf("command %d: %v", i, err)
		}
	}

	m.mu.Lock()
	size := len(m.executed)
	order := len(m.executedOrder)
	m.mu.Unlock()

	if size > defaultMaxTrackedExecutions {
		t.Errorf("executed map holds %d entries, want <= %d: it is still unbounded", size, defaultMaxTrackedExecutions)
	}
	if size == 0 {
		t.Error("executed map is empty; the bound must not evict everything")
	}
	if order != size {
		t.Errorf("executedOrder holds %d entries but executed holds %d; they must stay in step or the bound drifts", order, size)
	}

	// The newest commands must still be remembered — drop oldest, not drop all.
	newest := fmt.Sprintf("cmd-%05d", total-1)
	m.mu.Lock()
	_, seen := m.executed[newest]
	m.mu.Unlock()
	if !seen {
		t.Error("the most recently executed command was evicted; the bound must drop oldest-first")
	}
}

// TestExecutedMapBoundFollowsLedger covers the interaction with the durable
// ledger: the in-memory bound must never be smaller than the ledger's, or a
// command the ledger still remembers could execute a second time.
func TestExecutedMapBoundFollowsLedger(t *testing.T) {
	l, err := OpenLedger(t.TempDir(), defaultMaxTrackedExecutions+500)
	if err != nil {
		t.Fatal(err)
	}
	exec := &fakeExecutor{}
	m := New(nil, exec, "device-1", "credential-1", WithLedger(l))

	// Drive trackExecutedLocked directly: this test is about how the in-memory
	// bound is sized, and going through ExecuteCommand would also write the
	// ledger to disk once per command for no extra coverage.
	total := m.ledger.MaxEntries() + 100
	m.mu.Lock()
	for i := 0; i < total; i++ {
		m.trackExecutedLocked(fmt.Sprintf("cmd-%05d", i), commandExecution{status: StatusSucceeded})
	}
	m.mu.Unlock()

	m.mu.Lock()
	size := len(m.executed)
	m.mu.Unlock()
	if size <= defaultMaxTrackedExecutions {
		t.Errorf("executed map holds %d entries, want more than the default bound %d because the ledger retains more",
			size, defaultMaxTrackedExecutions)
	}
	if want := l.MaxEntries(); size > want {
		t.Errorf("executed map holds %d entries, want <= the ledger's %d", size, want)
	}
}

// TestExecutedOrderDoesNotDuplicateOnRedelivery guards the bound's own
// bookkeeping: re-delivering a known command updates its recorded outcome, and
// must not append a second order entry (which would evict live entries early).
func TestExecutedOrderDoesNotDuplicateOnRedelivery(t *testing.T) {
	exec := &fakeExecutor{}
	m := New(nil, exec, "device-1", "credential-1")

	cmd := newStatusCommand(1)
	for i := 0; i < 5; i++ {
		if _, _, _, err := m.ExecuteCommand(context.Background(), cmd); err != nil {
			t.Fatalf("delivery %d: %v", i, err)
		}
	}
	m.mu.Lock()
	order := len(m.executedOrder)
	size := len(m.executed)
	m.mu.Unlock()
	if order != 1 || size != 1 {
		t.Errorf("after 5 deliveries of one command: executedOrder=%d executed=%d, want 1/1", order, size)
	}
}
