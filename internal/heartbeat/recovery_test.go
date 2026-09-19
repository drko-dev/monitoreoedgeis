package heartbeat

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/transport"
)

// TestOnRecoveredFiresOncePerRecovery pins the exactly-once contract of the
// recovery hook, which is the counterpart OnUnauthorized needed: the agent marks
// itself DEGRADED on a 401 and, without this, had no way back to READY.
//
// "Once per recovery" matters in both directions. Firing on every successful
// heartbeat would rewrite agent health on every beat, and never firing leaves
// the Edge DEGRADED — and /readyz at 503 — until an operator restarts it.
func TestOnRecoveredFiresOncePerRecovery(t *testing.T) {
	const cycles = 10

	// A scripted sequence: 401, success, 401, success, ... The fake sender
	// returns the last result once the script runs out.
	results := make([]error, 0, cycles*2)
	for i := 0; i < cycles; i++ {
		results = append(results,
			fmt.Errorf("%w (status 401)", transport.ErrUnauthorized),
			nil,
		)
	}
	sender := newFakeSender(results...)

	var unauthorized, recovered int
	m, err := New(Options{
		Sender:         sender,
		DeviceID:       "device-1",
		Credential:     "credential-1",
		Build:          func() transport.HeartbeatRequest { return transport.HeartbeatRequest{} },
		Interval:       time.Millisecond,
		Log:            quietLogger(),
		OnUnauthorized: func() { unauthorized++ },
		OnRecovered:    func() { recovered++ },
		// Skip the real five-minute post-401 wait; the loop's scheduling is
		// not what this test is about.
		AuthFailureInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Start(ctx)
	defer m.Stop(context.Background())

	deadline := time.After(20 * time.Second)
	for {
		if len(sender.snapshot()) >= cycles*2 && recovered >= cycles {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("timed out: %d heartbeats, unauthorized=%d recovered=%d (want >=%d each)",
				len(sender.snapshot()), unauthorized, recovered, cycles)
		case <-sender.fired:
		}
	}
	// Allow any further (buggy) hook calls to surface before asserting.
	time.Sleep(50 * time.Millisecond)

	if unauthorized != cycles {
		t.Errorf("OnUnauthorized fired %d times, want exactly %d (once per new 401)", unauthorized, cycles)
	}
	if recovered != cycles {
		t.Errorf("OnRecovered fired %d times, want exactly %d (once per recovery, not once per successful heartbeat)",
			recovered, cycles)
	}
}

// TestOnRecoveredNotFiredOnConsecutiveSuccesses is the negative half: a run of
// healthy heartbeats must not keep firing the recovery hook.
func TestOnRecoveredNotFiredOnConsecutiveSuccesses(t *testing.T) {
	sender := newFakeSender(
		fmt.Errorf("%w (status 401)", transport.ErrUnauthorized),
		nil, nil, nil, nil, nil,
	)

	var recovered int
	m, err := New(Options{
		Sender:              sender,
		DeviceID:            "device-1",
		Credential:          "credential-1",
		Build:               func() transport.HeartbeatRequest { return transport.HeartbeatRequest{} },
		Interval:            time.Millisecond,
		Log:                 quietLogger(),
		OnRecovered:         func() { recovered++ },
		AuthFailureInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Start(ctx)
	defer m.Stop(context.Background())

	deadline := time.After(10 * time.Second)
	for len(sender.snapshot()) < 6 {
		select {
		case <-deadline:
			t.Fatalf("timed out with %d heartbeats", len(sender.snapshot()))
		case <-sender.fired:
		}
	}
	time.Sleep(50 * time.Millisecond)

	if recovered != 1 {
		t.Errorf("OnRecovered fired %d times across 1 recovery and 5 successes, want exactly 1", recovered)
	}
}

// TestOnRecoveredFiresAfterATransientOutage covers the other path into
// wasFailing: a 5xx run followed by a success. The module's own status already
// self-healed here, but the agent-level hook had to learn about it too.
func TestOnRecoveredFiresAfterATransientOutage(t *testing.T) {
	sender := newFakeSender(
		errors.New("boom"),
		fmt.Errorf("%w: status 503", transport.ErrUnexpectedStatus),
		nil,
	)

	var recovered int
	m, err := New(Options{
		Sender:      sender,
		DeviceID:    "device-1",
		Credential:  "credential-1",
		Build:       func() transport.HeartbeatRequest { return transport.HeartbeatRequest{} },
		Interval:    time.Millisecond,
		Log:         quietLogger(),
		OnRecovered: func() { recovered++ },
		// The backoff after a transient failure starts at BaseBackoff (1s);
		// jitter keeps it near that, which is fast enough for the deadline.
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Start(ctx)
	defer m.Stop(context.Background())

	deadline := time.After(15 * time.Second)
	for len(sender.snapshot()) < 3 {
		select {
		case <-deadline:
			t.Fatalf("timed out with %d heartbeats", len(sender.snapshot()))
		case <-sender.fired:
		}
	}
	time.Sleep(50 * time.Millisecond)

	if recovered != 1 {
		t.Errorf("OnRecovered fired %d times after a transient outage, want exactly 1", recovered)
	}
	if got := m.Status().State; got != StateRunning {
		t.Errorf("module state = %q, want %q", got, StateRunning)
	}
}
