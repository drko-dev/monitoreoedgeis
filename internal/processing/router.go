package processing

import (
	"log/slog"
	"sync"
	"sync/atomic"
)

// Sink is a routing destination for frames (H8). Hito H ships only
// DebugSink; Milestone I adds internal/cloudsink.CloudSink as a second
// implementation. Hybrid/Edge-YOLO sinks remain later milestones (J/K).
type Sink interface {
	Name() string
	// Route delivers a frame. Implementations must return quickly — a slow
	// sink is Router's problem to bound (via its own queue), not the frame
	// producer's.
	Route(f Frame) error
}

// Router fans a frame out to every registered sink through one bounded
// queue and exactly one worker goroutine per sink (never per frame). A full
// queue drops the frame for that sink only, so one slow sink never blocks
// the others.
type Router struct {
	logger  *slog.Logger
	sinks   []Sink
	queues  []chan Frame
	dropped []atomic.Int64
	wg      sync.WaitGroup
	stop    chan struct{}
}

// NewRouter creates a Router with one bounded queue (depth queueDepth) per
// sink and starts its worker goroutines.
func NewRouter(sinks []Sink, queueDepth int, logger *slog.Logger) *Router {
	if logger == nil {
		logger = slog.Default()
	}
	if queueDepth <= 0 {
		queueDepth = 1
	}
	r := &Router{
		logger:  logger,
		sinks:   sinks,
		queues:  make([]chan Frame, len(sinks)),
		dropped: make([]atomic.Int64, len(sinks)),
		stop:    make(chan struct{}),
	}
	for i, sink := range sinks {
		r.queues[i] = make(chan Frame, queueDepth)
		r.wg.Add(1)
		go r.worker(i, sink)
	}
	return r
}

func (r *Router) worker(i int, sink Sink) {
	defer r.wg.Done()
	for {
		select {
		case <-r.stop:
			return
		case f := <-r.queues[i]:
			if err := sink.Route(f); err != nil {
				r.logger.Warn("sink route failed",
					"sink", sink.Name(),
					"candidate_key", f.CandidateKey,
					"seq", f.Seq,
					"correlation_id", f.CorrelationID,
					"error", err)
			}
		}
	}
}

// Dispatch enqueues f to every sink's queue, dropping it (and counting the
// drop) for any sink whose queue is currently full, rather than blocking.
func (r *Router) Dispatch(f Frame) {
	for i, q := range r.queues {
		select {
		case q <- f:
		default:
			r.dropped[i].Add(1)
		}
	}
}

// Dropped returns how many frames were dropped for the named sink because
// its queue was full.
func (r *Router) Dropped(sinkName string) int64 {
	for i, s := range r.sinks {
		if s.Name() == sinkName {
			return r.dropped[i].Load()
		}
	}
	return 0
}

// sinkCloser is implemented by a Sink holding background resources that
// need an orderly shutdown (e.g. cloudsink.CloudSink's I6 offline-buffer
// drain goroutine). Checked with a type assertion so ordinary sinks (ones
// with no such resource, like DebugSink) need nothing extra.
type sinkCloser interface {
	Close()
}

// Stop terminates every worker goroutine, waits for them to exit, then
// closes any sink that holds its own background resources.
func (r *Router) Stop() {
	close(r.stop)
	r.wg.Wait()
	for _, s := range r.sinks {
		if c, ok := s.(sinkCloser); ok {
			c.Close()
		}
	}
}

// DebugSink is the only Sink shipped in Hito H: it records a count and the
// most recent frame's metadata, with no per-frame history — bounded memory
// by construction. Real sinks (Cloud/Hybrid/Edge) are I/J/K's job.
type DebugSink struct {
	mu    sync.Mutex
	count int64
	last  Frame
}

// NewDebugSink creates an empty DebugSink.
func NewDebugSink() *DebugSink { return &DebugSink{} }

// Name implements Sink.
func (d *DebugSink) Name() string { return "debug" }

// Route implements Sink.
func (d *DebugSink) Route(f Frame) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.count++
	d.last = f
	return nil
}

// Count returns how many frames this sink has received.
func (d *DebugSink) Count() int64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.count
}

// Last returns the most recently routed frame.
func (d *DebugSink) Last() Frame {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.last
}
