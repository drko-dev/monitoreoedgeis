package agent

import (
	"context"
	"errors"
	"testing"
)

func TestModuleManagerOrderedStartAndStop(t *testing.T) {
	var started, stopped []string
	states := map[string]string{}
	report := func(name, state string) { states[name] = state }

	mgr := newModuleManager(report,
		&fakeModule{name: "a", startedAt: &started, stoppedAt: &stopped},
		&fakeModule{name: "b", startedAt: &started, stoppedAt: &stopped},
		&fakeModule{name: "c", startedAt: &started, stoppedAt: &stopped},
	)

	if err := mgr.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if got, want := started, []string{"a", "b", "c"}; !equalSlices(got, want) {
		t.Errorf("start order = %v, want %v", got, want)
	}
	for _, name := range []string{"a", "b", "c"} {
		if states[name] != "running" {
			t.Errorf("state[%s] = %q, want running", name, states[name])
		}
	}

	if err := mgr.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if got, want := stopped, []string{"c", "b", "a"}; !equalSlices(got, want) {
		t.Errorf("stop order = %v, want %v (reverse of start)", got, want)
	}
	for _, name := range []string{"a", "b", "c"} {
		if states[name] != "stopped" {
			t.Errorf("state[%s] = %q, want stopped", name, states[name])
		}
	}
}

func TestModuleManagerStartFailureUnwindsStarted(t *testing.T) {
	var started, stopped []string

	mgr := newModuleManager(nil,
		&fakeModule{name: "a", startedAt: &started, stoppedAt: &stopped},
		&fakeModule{name: "b", startedAt: &started, stoppedAt: &stopped, startErr: errBoom},
		&fakeModule{name: "c", startedAt: &started, stoppedAt: &stopped},
	)

	err := mgr.Start(context.Background())
	if err == nil || !errors.Is(err, errBoom) {
		t.Fatalf("Start() error = %v, want wrapped errBoom", err)
	}

	if got, want := started, []string{"a", "b"}; !equalSlices(got, want) {
		t.Errorf("started = %v, want %v (c must never start)", got, want)
	}
	// Only "a" actually started successfully and must be unwound.
	if got, want := stopped, []string{"a"}; !equalSlices(got, want) {
		t.Errorf("stopped = %v, want %v", got, want)
	}
}

func equalSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
