package processing

import (
	"context"
	"sync"
	"time"
)

// rateWindow is the sliding window effective-FPS figures are computed over.
const rateWindow = 5 * time.Second

// statusPublishInterval bounds how often the wrappers below push their
// per-camera snapshot to the health reporter.
const statusPublishInterval = 500 * time.Millisecond

// windowRate counts events inside rateWindow to report a measured FPS.
type windowRate struct{ at []time.Time }

func (w *windowRate) add(t time.Time) {
	w.at = append(w.at, t)
	w.trim(t)
}

func (w *windowRate) trim(now time.Time) {
	cut := 0
	for cut < len(w.at) && now.Sub(w.at[cut]) > rateWindow {
		cut++
	}
	w.at = w.at[cut:]
}

func (w *windowRate) fps(now time.Time) float64 {
	w.trim(now)
	return float64(len(w.at)) / rateWindow.Seconds()
}

// FreshCameraStatus is FreshSink's per-camera measurement: what was asked
// for (ConfiguredFPS, the camera's sampler target) next to what the inner
// sink actually processed.
type FreshCameraStatus struct {
	ConfiguredFPS     float64 `json:"configured_fps"`
	EffectiveFPS      float64 `json:"effective_fps"`
	Processed         int64   `json:"processed"`
	Errors            int64   `json:"errors"`
	LastLatencyMS     float64 `json:"last_latency_ms"`
	AvgLatencyMS      float64 `json:"avg_latency_ms"`
	LastFrameAgeMS    float64 `json:"last_frame_age_ms"`
	DroppedSuperseded int64   `json:"dropped_superseded"`
	DroppedStale      int64   `json:"dropped_stale"`
}

type freshCamera struct {
	FreshCameraStatus
	rate windowRate
}

// FreshSink sits in front of a slow, serial sink shared by every camera
// (local YOLO) and replaces the Router's FIFO for it with:
//
//   - freshness: at most one pending frame per camera; a newer frame
//     supersedes the pending one, and a frame older than maxAge when its turn
//     comes is dropped instead of processed;
//   - fairness: cameras with a pending frame are served round-robin, so a
//     camera sampled at 20 FPS can only take its turn, never the others'.
//
// Route never blocks, so the Router queue in front of it never fills.
type FreshSink struct {
	inner      Sink
	maxAge     time.Duration
	configured func(candidateKey string) (float64, bool)
	onStatus   func(map[string]FreshCameraStatus)
	now        func() time.Time

	mu          sync.Mutex
	pending     map[string]Frame
	order       []string // round-robin order, one entry per camera ever seen
	next        int      // index after the last served camera
	cams        map[string]*freshCamera
	lastPublish time.Time

	wake     chan struct{}
	stop     chan struct{}
	done     chan struct{}
	stopOnce sync.Once
}

// NewFreshSink wraps inner. configured (optional) reports a camera's
// configured FPS for status; onStatus (optional) receives throttled
// per-camera snapshots.
func NewFreshSink(inner Sink, maxAge time.Duration, configured func(string) (float64, bool), onStatus func(map[string]FreshCameraStatus)) *FreshSink {
	s := &FreshSink{
		inner:      inner,
		maxAge:     maxAge,
		configured: configured,
		onStatus:   onStatus,
		now:        time.Now,
		pending:    map[string]Frame{},
		cams:       map[string]*freshCamera{},
		wake:       make(chan struct{}, 1),
		stop:       make(chan struct{}),
		done:       make(chan struct{}),
	}
	go s.loop()
	return s
}

// Inner returns the wrapped sink.
func (s *FreshSink) Inner() Sink { return s.inner }

// Name keeps the inner sink's name so router/queue status is unchanged.
func (s *FreshSink) Name() string { return s.inner.Name() }

// WaitForReady and Ready forward to inner when it has them, so callers that
// wait on a vision worker through the sink (remote mode switch) still can.
func (s *FreshSink) WaitForReady(ctx context.Context) error {
	if r, ok := s.inner.(interface{ WaitForReady(context.Context) error }); ok {
		return r.WaitForReady(ctx)
	}
	return nil
}

func (s *FreshSink) Ready() bool {
	if r, ok := s.inner.(interface{ Ready() bool }); ok {
		return r.Ready()
	}
	return true
}

// Route parks f as its camera's pending frame and returns immediately.
func (s *FreshSink) Route(f Frame) error {
	s.mu.Lock()
	cam := s.camLocked(f.CandidateKey)
	if _, ok := s.pending[f.CandidateKey]; ok {
		cam.DroppedSuperseded++
	}
	s.pending[f.CandidateKey] = f
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
	return nil
}

func (s *FreshSink) camLocked(key string) *freshCamera {
	c, ok := s.cams[key]
	if !ok {
		c = &freshCamera{}
		s.cams[key] = c
		s.order = append(s.order, key)
	}
	return c
}

// takeLocked pops the next camera's pending frame in round-robin order.
func (s *FreshSink) takeLocked() (Frame, bool) {
	// next is kept unreduced: a camera appended to order after the last
	// served one is the very next in turn, not skipped by a wrap to 0.
	for i := 0; i < len(s.order); i++ {
		idx := (s.next + i) % len(s.order)
		key := s.order[idx]
		if f, ok := s.pending[key]; ok {
			delete(s.pending, key)
			s.next = idx + 1
			return f, true
		}
	}
	return Frame{}, false
}

func (s *FreshSink) loop() {
	defer close(s.done)
	for {
		select {
		case <-s.stop:
			return
		case <-s.wake:
		}
		for {
			select {
			case <-s.stop:
				return
			default:
			}
			s.mu.Lock()
			f, ok := s.takeLocked()
			s.mu.Unlock()
			if !ok {
				break
			}
			s.process(f)
		}
	}
}

func (s *FreshSink) process(f Frame) {
	if s.maxAge > 0 && s.now().Sub(f.Timestamp) > s.maxAge {
		s.mu.Lock()
		s.camLocked(f.CandidateKey).DroppedStale++
		s.mu.Unlock()
		s.publish(false)
		return
	}
	started := s.now()
	err := s.inner.Route(f)
	end := s.now()
	latency := float64(end.Sub(started)) / float64(time.Millisecond)

	s.mu.Lock()
	cam := s.camLocked(f.CandidateKey)
	if err != nil {
		cam.Errors++
	} else {
		cam.Processed++
		cam.rate.add(end)
		cam.LastLatencyMS = latency
		if cam.AvgLatencyMS == 0 {
			cam.AvgLatencyMS = latency
		} else {
			cam.AvgLatencyMS = 0.9*cam.AvgLatencyMS + 0.1*latency
		}
		cam.LastFrameAgeMS = float64(end.Sub(f.Timestamp)) / float64(time.Millisecond)
	}
	s.mu.Unlock()
	s.publish(false)
}

// Status returns a per-camera snapshot keyed by candidate_key.
func (s *FreshSink) Status() map[string]FreshCameraStatus {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]FreshCameraStatus, len(s.cams))
	for key, c := range s.cams {
		st := c.FreshCameraStatus
		st.EffectiveFPS = c.rate.fps(now)
		if s.configured != nil {
			if fps, ok := s.configured(key); ok {
				st.ConfiguredFPS = fps
			}
		}
		out[key] = st
	}
	return out
}

func (s *FreshSink) publish(force bool) {
	if s.onStatus == nil {
		return
	}
	now := s.now()
	s.mu.Lock()
	due := force || now.Sub(s.lastPublish) >= statusPublishInterval
	if due {
		s.lastPublish = now
	}
	s.mu.Unlock()
	if due {
		s.onStatus(s.Status())
	}
}

// Close stops the scheduling goroutine, then closes the inner sink if it
// is closable (same contract the Router applies to every sink).
func (s *FreshSink) Close() {
	s.stopOnce.Do(func() {
		close(s.stop)
		<-s.done
		if c, ok := s.inner.(interface{ Close() }); ok {
			c.Close()
		}
	})
}

// PacedCameraStatus is PacedSink's per-camera measurement.
type PacedCameraStatus struct {
	ConfiguredFPS float64 `json:"configured_fps"`
	EffectiveFPS  float64 `json:"effective_fps"`
	Forwarded     int64   `json:"forwarded"`
	Throttled     int64   `json:"throttled"`
}

type pacedCamera struct {
	PacedCameraStatus
	nextDue time.Time
	rate    windowRate
}

// PacedSink caps each camera's frames to fps before inner, independently
// of how fast the pipeline samples that camera. Pacing is drift-free (the
// next due time advances by exactly one interval), so a 15 FPS source
// paced to 2 FPS really yields 2 FPS rather than the 1.875 a plain
// minimum-interval gate gives.
type PacedSink struct {
	inner    Sink
	fps      float64
	interval time.Duration
	onStatus func(map[string]PacedCameraStatus)
	now      func() time.Time

	mu          sync.Mutex
	cams        map[string]*pacedCamera
	lastPublish time.Time
}

// NewPacedSink wraps inner. fps<=0 disables pacing.
func NewPacedSink(inner Sink, fps float64, onStatus func(map[string]PacedCameraStatus)) *PacedSink {
	s := &PacedSink{inner: inner, fps: fps, onStatus: onStatus, now: time.Now, cams: map[string]*pacedCamera{}}
	if fps > 0 {
		s.interval = time.Duration(float64(time.Second) / fps)
	}
	return s
}

// Name keeps the inner sink's name.
func (s *PacedSink) Name() string { return s.inner.Name() }

// Route forwards f to inner only when its camera is due.
func (s *PacedSink) Route(f Frame) error {
	s.mu.Lock()
	cam, ok := s.cams[f.CandidateKey]
	if !ok {
		cam = &pacedCamera{}
		s.cams[f.CandidateKey] = cam
	}
	t := f.Timestamp
	if s.interval > 0 && !cam.nextDue.IsZero() && t.Before(cam.nextDue) {
		cam.Throttled++
		s.mu.Unlock()
		s.publish()
		return nil
	}
	if s.interval > 0 {
		// Advance from the previous due time to stay drift-free, but never
		// let a gap (camera stalled, Live View active) bank a burst.
		next := cam.nextDue.Add(s.interval)
		if cam.nextDue.IsZero() || next.Before(t) {
			next = t.Add(s.interval)
		}
		cam.nextDue = next
	}
	cam.Forwarded++
	cam.rate.add(s.now())
	s.mu.Unlock()
	s.publish()
	return s.inner.Route(f)
}

// Status returns a per-camera snapshot keyed by candidate_key.
func (s *PacedSink) Status() map[string]PacedCameraStatus {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]PacedCameraStatus, len(s.cams))
	for key, c := range s.cams {
		st := c.PacedCameraStatus
		st.ConfiguredFPS = s.fps
		st.EffectiveFPS = c.rate.fps(now)
		out[key] = st
	}
	return out
}

func (s *PacedSink) publish() {
	if s.onStatus == nil {
		return
	}
	now := s.now()
	s.mu.Lock()
	due := now.Sub(s.lastPublish) >= statusPublishInterval
	if due {
		s.lastPublish = now
	}
	s.mu.Unlock()
	if due {
		s.onStatus(s.Status())
	}
}

// Close closes the inner sink if it is closable.
func (s *PacedSink) Close() {
	if c, ok := s.inner.(interface{ Close() }); ok {
		c.Close()
	}
}
