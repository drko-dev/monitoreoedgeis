// Package livevideo implements Full Edge on-demand Live View.
//
// Preview/wall frames keep flowing through the router at
// GEOCAM_VIDEO_TARGET_FPS (the edge-video sink). When the SaaS answers one of
// those uploads with live_requested=true -- a user opened "En vivo" for that
// camera -- the Controller starts tapping the camera's already-decoded frames
// at GEOCAM_LIVE_TARGET_FPS and uploads them over the same display-only path.
// Every live upload's answer renews the demand; once the SaaS reports zero
// viewers the demand lapses after GEOCAM_LIVE_IDLE_TIMEOUT and the camera
// falls back to preview.
//
// The tap sits ahead of the preview/inference sampler and never feeds the
// router, so live never duplicates inference and edge-vision is untouched.
// Transport lives behind processing.Sink: a future WebRTC uplink replaces
// the sink, not this demand logic.
package livevideo

import (
	"sort"
	"sync"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/cloudsink"
	"github.com/drko-dev/monitoreoedgeis/internal/processing"
)

// queueDepth bounds frames waiting for upload. Live favors freshness: when
// the uplink is slower than the camera, new frames are dropped, never queued.
const queueDepth = 2

// fpsWindow is the window effective_fps is measured over.
const fpsWindow = 2 * time.Second

// Status is the /status "live_video" block. Never carries URLs or secrets.
type Status struct {
	Active        bool       `json:"active"`
	ActiveViewers int        `json:"active_viewers"`
	TargetFPS     float64    `json:"target_fps"`
	EffectiveFPS  float64    `json:"effective_fps"`
	IdleTimeoutS  float64    `json:"idle_timeout_seconds"`
	FramesSent    int64      `json:"frames_sent"`
	FramesFailed  int64      `json:"frames_failed"`
	FramesDropped int64      `json:"frames_dropped"`
	BytesSent     int64      `json:"bytes_sent"`
	LastFrameAt   *time.Time `json:"last_frame_at,omitempty"`
	CandidateKeys []string   `json:"candidate_keys,omitempty"`
}

type camera struct {
	until   time.Time
	viewers int
	sampler *processing.Sampler
}

// Controller is a processing.LiveTap. Safe for concurrent use.
type Controller struct {
	targetFPS float64
	idle      time.Duration
	now       func() time.Time
	publish   func(Status)
	queue     chan processing.Frame
	stop      chan struct{}
	wg        sync.WaitGroup

	mu      sync.Mutex
	sink    processing.Sink
	cams    map[string]*camera
	sentAt  []time.Time
	sent    int64
	failed  int64
	dropped int64
	bytes   int64
	lastAt  *time.Time
}

// New returns a Controller emitting at targetFPS (0 = source FPS, capped by
// maxFPS) for idle after the last demand. publish (may be nil) receives the
// status whenever it changes. Call SetSink before demand can be served.
func New(targetFPS, maxFPS float64, idle time.Duration, publish func(Status)) *Controller {
	return newWithClock(targetFPS, maxFPS, idle, publish, time.Now)
}

func newWithClock(targetFPS, maxFPS float64, idle time.Duration, publish func(Status), now func() time.Time) *Controller {
	if targetFPS <= 0 || targetFPS > maxFPS {
		targetFPS = maxFPS
	}
	if publish == nil {
		publish = func(Status) {}
	}
	c := &Controller{
		targetFPS: targetFPS,
		idle:      idle,
		now:       now,
		publish:   publish,
		queue:     make(chan processing.Frame, queueDepth),
		stop:      make(chan struct{}),
		cams:      make(map[string]*camera),
	}
	c.wg.Add(1)
	go c.uploadLoop()
	return c
}

// SetSink installs (or, with nil, removes) the live uplink. Removing it --
// e.g. the Edge left ModeEdge -- also drops all pending demand.
func (c *Controller) SetSink(s processing.Sink) {
	c.mu.Lock()
	c.sink = s
	if s == nil {
		c.cams = make(map[string]*camera)
	}
	c.mu.Unlock()
	c.publishStatus()
}

// Demand records the SaaS's live-view answer for one camera. viewers > 0
// (re)arms live until now+idle; viewers == 0 only updates the count, so the
// camera falls back to preview once the current window lapses.
func (c *Controller) Demand(candidateKey string, viewers int) {
	c.mu.Lock()
	cam := c.cams[candidateKey]
	if cam == nil {
		if viewers <= 0 || c.sink == nil {
			c.mu.Unlock()
			return
		}
		cam = &camera{sampler: processing.NewSampler(c.targetFPS)}
		c.cams[candidateKey] = cam
	}
	changed := cam.viewers != viewers
	cam.viewers = viewers
	if viewers > 0 {
		changed = changed || !c.now().Before(cam.until)
		cam.until = c.now().Add(c.idle)
	}
	c.mu.Unlock()
	if changed {
		c.publishStatus()
	}
}

// Active reports whether candidateKey currently has live demand.
func (c *Controller) Active(candidateKey string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	cam := c.cams[candidateKey]
	return cam != nil && c.now().Before(cam.until)
}

// Wants implements processing.LiveTap.
func (c *Controller) Wants(candidateKey string, at time.Time) bool {
	c.mu.Lock()
	cam := c.cams[candidateKey]
	if cam == nil {
		c.mu.Unlock()
		return false
	}
	if !c.now().Before(cam.until) {
		// Idle timeout elapsed: back to preview only.
		delete(c.cams, candidateKey)
		c.mu.Unlock()
		c.publishStatus()
		return false
	}
	ok := cam.sampler.ShouldEmit(at)
	c.mu.Unlock()
	return ok
}

// Offer implements processing.LiveTap. Never blocks the decode loop.
func (c *Controller) Offer(f processing.Frame) {
	select {
	case c.queue <- f:
	default:
		c.mu.Lock()
		c.dropped++
		c.mu.Unlock()
	}
}

// Close stops the upload goroutine.
func (c *Controller) Close() {
	close(c.stop)
	c.wg.Wait()
}

func (c *Controller) uploadLoop() {
	defer c.wg.Done()
	for {
		select {
		case <-c.stop:
			return
		case f := <-c.queue:
			c.mu.Lock()
			sink := c.sink
			c.mu.Unlock()
			if sink == nil {
				continue
			}
			err := sink.Route(f)
			now := c.now()
			c.mu.Lock()
			if err != nil {
				c.failed++
			} else {
				c.sent++
				c.lastAt = &now
				c.sentAt = append(c.sentAt, now)
			}
			c.mu.Unlock()
			c.publishStatus()
		}
	}
}

// SetCloudStatus lets the Controller be its own CloudSink's health reporter,
// so bytes_sent comes from the encoder that actually produced the JPEGs.
func (c *Controller) SetCloudStatus(s cloudsink.Status) {
	c.mu.Lock()
	c.bytes = int64(s.JPEGBytesUploaded)
	c.mu.Unlock()
}

// Status returns the current live_video block.
func (c *Controller) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	cut := 0
	for cut < len(c.sentAt) && now.Sub(c.sentAt[cut]) > fpsWindow {
		cut++
	}
	c.sentAt = c.sentAt[cut:]
	st := Status{
		TargetFPS:     c.targetFPS,
		EffectiveFPS:  float64(len(c.sentAt)) / fpsWindow.Seconds(),
		IdleTimeoutS:  c.idle.Seconds(),
		FramesSent:    c.sent,
		FramesFailed:  c.failed,
		FramesDropped: c.dropped,
		BytesSent:     c.bytes,
		LastFrameAt:   c.lastAt,
	}
	for key, cam := range c.cams {
		if now.Before(cam.until) {
			st.Active = true
			st.ActiveViewers += cam.viewers
			st.CandidateKeys = append(st.CandidateKeys, key)
		}
	}
	sort.Strings(st.CandidateKeys)
	return st
}

func (c *Controller) publishStatus() { c.publish(c.Status()) }
