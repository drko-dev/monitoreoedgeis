package vision

import (
	"context"
	"testing"

	"github.com/drko-dev/monitoreoedgeis/internal/processing"
)

// fakeHealthReporter records every Status pushed, letting a test assert
// /readyz's edge-mode gate (see internal/health.EdgeVisionReady) would see a
// fresh push on every worker state transition, not only after a frame.
type fakeHealthReporter struct {
	pushes []Status
}

func (f *fakeHealthReporter) SetVisionStatus(s Status) { f.pushes = append(f.pushes, s) }

// TestSink_DroppedWhenNotReady covers K4's backpressure/NOT_READY path: a
// frame routed while the worker isn't ready must be counted as dropped, not
// silently discarded or (worse) blocked forever.
func TestSink_DroppedWhenNotReady(t *testing.T) {
	models := newTestModels(t)
	w := NewWorker(Config{}, models, nil) // never started -> stays not_configured
	reporter := &fakeHealthReporter{}
	sink := NewSink(w, models, reporter, nil, nil)

	err := sink.Route(processing.Frame{CandidateKey: "cam-1", OutputWidth: 640, OutputHeight: 360})
	if err == nil {
		t.Fatal("Route succeeded against a not-ready worker")
	}
	st := sink.Status()
	if st.DroppedNotReady != 1 {
		t.Fatalf("DroppedNotReady = %d, want 1", st.DroppedNotReady)
	}
	if st.Worker.State != StateNotConfigured {
		t.Fatalf("worker state = %q, want %q", st.Worker.State, StateNotConfigured)
	}
}

// TestSink_PublishesOnStateChangeAlone covers the readyz-gate correctness
// fix: a health push must happen on every worker state transition, even if
// Route is never called (a camera that never streams a frame must not wedge
// /readyz at 503 forever once the worker itself becomes ready).
func TestSink_PublishesOnStateChangeAlone(t *testing.T) {
	reporter := &fakeHealthReporter{}
	models := newTestModels(t)
	w := NewWorker(Config{}, models, nil)
	_ = NewSink(w, models, reporter, nil, nil)

	if err := w.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if len(reporter.pushes) == 0 {
		t.Fatal("no status pushed after a worker state transition")
	}
	last := reporter.pushes[len(reporter.pushes)-1]
	if last.Worker.State != StateNotConfigured {
		t.Fatalf("last pushed state = %q, want %q", last.Worker.State, StateNotConfigured)
	}
}

// TestSink_Name covers Sink implementing processing.Sink correctly (K1: it
// is a real routing destination, not a stub).
func TestSink_Name(t *testing.T) {
	models := newTestModels(t)
	w := NewWorker(Config{}, models, nil)
	sink := NewSink(w, models, nil, nil, nil)
	if sink.Name() != "edge-vision" {
		t.Fatalf("Name() = %q, want %q", sink.Name(), "edge-vision")
	}
	var _ processing.Sink = sink // compile-time interface check
}
