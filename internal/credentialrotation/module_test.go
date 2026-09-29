package credentialrotation

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/credentials"
)

type testTimer struct {
	ch      chan time.Time
	stopped bool
}

func timerFactory(ch chan *testTimer) func(time.Duration) (<-chan time.Time, func() bool) {
	return func(d time.Duration) (<-chan time.Time, func() bool) {
		t := &testTimer{ch: make(chan time.Time, 1)}
		ch <- t
		return t.ch, func() bool { t.stopped = true; return true }
	}
}

func TestSchedulerRestartBeforeDueAndGracefulStop(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	due := now.Add(time.Hour)
	if err := credentials.Save(dir, credentials.Credentials{EdgeID: "edge", DeviceID: "device", Credential: "secret", EnrolledAt: now.Add(-24 * time.Hour), NextRotationDueAt: due, Status: credentials.StatusEnrolled}); err != nil {
		t.Fatal(err)
	}
	store := DiskStore{}
	client := &fakeClient{}
	service, _ := New(Options{DataDir: dir, Client: client, Store: store, NewID: func() (string, error) { return "id", nil }})
	timers := make(chan *testTimer, 4)
	module := NewModule(ModuleOptions{Service: service, Store: store, DataDir: dir, Enabled: true, Interval: 24 * time.Hour, MinimumAge: 24 * time.Hour, RetryBase: time.Second, RetryMax: time.Minute, Now: func() time.Time { return now }, Rand: func() float64 { return 0 }, NewTimer: timerFactory(timers)})
	ctx, cancel := context.WithCancel(context.Background())
	if err := module.Start(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-timers:
	case <-time.After(time.Second):
		t.Fatal("scheduler did not wait for persisted due date")
	}
	if len(client.requests) != 0 {
		t.Fatal("rotation occurred before due")
	}
	stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Second)
	defer stopCancel()
	cancel()
	if err := module.Stop(stopCtx); err != nil {
		t.Fatal(err)
	}
	if len(client.requests) != 0 {
		t.Fatal("rotation occurred on restart before due")
	}
}

func TestSchedulerRestartAfterDueAttemptsOnce(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	_ = credentials.Save(dir, credentials.Credentials{EdgeID: "edge", DeviceID: "device", Credential: "secret", EnrolledAt: now.Add(-48 * time.Hour), NextRotationDueAt: now.Add(-time.Second), Status: credentials.StatusEnrolled})
	store := DiskStore{}
	client := &fakeClient{}
	service, _ := New(Options{DataDir: dir, Client: client, Store: store, NewID: func() (string, error) { return "id", nil }, Interval: 24 * time.Hour, Rand: func() float64 { return 0 }})
	module := NewModule(ModuleOptions{Service: service, Store: store, DataDir: dir, Enabled: true, Interval: 24 * time.Hour, MinimumAge: 24 * time.Hour, RetryBase: time.Second, RetryMax: time.Minute, Now: func() time.Time { return now }, Rand: func() float64 { return 0 }})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := module.Start(ctx); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(time.Second)
	for {
		client.mu.Lock()
		n := len(client.requests)
		client.mu.Unlock()
		if n == 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("rotation attempts=%d", n)
		case <-time.After(time.Millisecond):
		}
	}
	if err := module.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.requests) != 1 {
		t.Fatalf("rotation attempts after stop=%d", len(client.requests))
	}
}

func TestSchedulerClockJumpsDoNotRotateEarlyOrSpin(t *testing.T) {
	for _, jump := range []struct {
		name        string
		delta       time.Duration
		wantAttempt bool
	}{
		{name: "forward", delta: 3 * time.Hour, wantAttempt: true},
		{name: "backward", delta: -3 * time.Hour, wantAttempt: false},
	} {
		t.Run(jump.name, func(t *testing.T) {
			dir := t.TempDir()
			base := time.Now().UTC()
			due := base.Add(2 * time.Hour)
			_ = credentials.Save(dir, credentials.Credentials{EdgeID: "edge", DeviceID: "device", Credential: "secret", EnrolledAt: base.Add(-time.Hour), NextRotationDueAt: due, Status: credentials.StatusEnrolled})
			store := DiskStore{}
			client := &fakeClient{}
			service, _ := New(Options{DataDir: dir, Client: client, Store: store, NewID: func() (string, error) { return "id", nil }, Interval: 24 * time.Hour, Rand: func() float64 { return 0 }})
			var mu sync.Mutex
			now := base
			timers := make(chan *testTimer, 4)
			module := NewModule(ModuleOptions{Service: service, Store: store, DataDir: dir, Enabled: true, Interval: 24 * time.Hour, MinimumAge: time.Hour, RetryBase: time.Second, RetryMax: time.Minute, Now: func() time.Time { mu.Lock(); defer mu.Unlock(); return now }, Rand: func() float64 { return 0 }, NewTimer: timerFactory(timers)})
			ctx, cancel := context.WithCancel(context.Background())
			if err := module.Start(ctx); err != nil {
				t.Fatal(err)
			}
			var first *testTimer
			select {
			case first = <-timers:
			case <-time.After(time.Second):
				t.Fatal("no scheduler timer")
			}
			mu.Lock()
			now = base.Add(jump.delta)
			mu.Unlock()
			first.ch <- now
			if jump.wantAttempt {
				deadline := time.After(time.Second)
				for {
					client.mu.Lock()
					n := len(client.requests)
					client.mu.Unlock()
					if n == 1 {
						break
					}
					select {
					case <-deadline:
						t.Fatal("forward clock jump did not trigger due rotation")
					case <-time.After(time.Millisecond):
					}
				}
			} else {
				select {
				case <-timers:
				case <-time.After(time.Second):
					t.Fatal("backward jump did not re-arm a timer")
				}
				client.mu.Lock()
				n := len(client.requests)
				client.mu.Unlock()
				if n != 0 {
					t.Fatalf("backward jump rotated early: %d", n)
				}
			}
			cancel()
			stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Second)
			defer stopCancel()
			if err := module.Stop(stopCtx); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestJitterIsDeterministicAndNeverAdvancesDue(t *testing.T) {
	window := time.Hour
	if got := Jitter(window, func() float64 { return .25 }); got != 15*time.Minute {
		t.Fatalf("jitter=%s", got)
	}
	if got := Jitter(window, func() float64 { return 2 }); got != window {
		t.Fatalf("clamped jitter=%s", got)
	}
	if got := Jitter(-time.Second, func() float64 { return 1 }); got != 0 {
		t.Fatalf("negative window=%s", got)
	}
}

func TestModuleStatusContainsNoSecrets(t *testing.T) {
	var mu sync.Mutex
	var got Status
	module := NewModule(ModuleOptions{Enabled: false, OnStatus: func(s Status) { mu.Lock(); got = s; mu.Unlock() }})
	module.set(Status{Enabled: false, State: "disabled"})
	mu.Lock()
	defer mu.Unlock()
	if got.State != "disabled" || got.Enabled {
		t.Fatalf("status=%#v", got)
	}
}
