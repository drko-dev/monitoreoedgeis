package fulledge

import (
	"sync"
	"time"
)

// Event deduplication defaults. A tracked object that stays in view yields one
// event; it yields a new one only after it has been unseen for DedupGap.
const (
	DefaultDedupGap       = 10 * time.Second
	DefaultDedupIoU       = 0.3
	DefaultDedupMaxTracks = 512
)

// Deduper decides whether a detection starts a new event or continues an
// object that already produced one. Identity is the detector's track_id when
// present; otherwise camera + class + IoU against tracks seen within the gap.
//
// State is ephemeral and bounded: tracks expire DedupGap after their last
// sighting, and the oldest one is evicted beyond maxTracks. Losing it on an
// Edge restart only means the next sighting creates one extra event -- the
// durable backlog stays the source of truth for events already created.
type Deduper struct {
	gap       time.Duration
	minIoU    float64
	maxTracks int
	now       func() time.Time

	mu     sync.Mutex
	tracks map[dedupKey][]*dedupTrack
	n      int
	// round numbers Admit calls so a track matched by one detection of a
	// frame is never matched again by a second, simultaneous detection.
	round uint64
}

type dedupKey struct{ camera, class string }

type dedupTrack struct {
	trackID  int64
	hasID    bool
	bbox     BoundingBox
	lastSeen time.Time // frame time of the last sighting (the camera's own timeline)
	seenWall time.Time // wall time of the last sighting, to reap cameras that went silent
	frame    uint64    // admission round that last matched it, so one round never matches it twice
}

// NewDeduper returns a Deduper; zero or negative arguments take the defaults.
func NewDeduper(gap time.Duration, minIoU float64, maxTracks int) *Deduper {
	if gap <= 0 {
		gap = DefaultDedupGap
	}
	if minIoU <= 0 {
		minIoU = DefaultDedupIoU
	}
	if maxTracks <= 0 {
		maxTracks = DefaultDedupMaxTracks
	}
	return &Deduper{gap: gap, minIoU: minIoU, maxTracks: maxTracks, now: time.Now, tracks: make(map[dedupKey][]*dedupTrack)}
}

// Admit reports, per detection of one frame of one camera, whether it starts
// a new event (true) or continues a known object (false).
func (d *Deduper) Admit(camera string, at time.Time, dets []LocalDetection) []bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.round++
	round := d.round
	wall := d.now()
	d.expireLocked(camera, at, wall)

	out := make([]bool, len(dets))
	for i, det := range dets {
		key := dedupKey{camera, detectionClass(det)}
		if t := d.matchLocked(key, det, round); t != nil {
			t.bbox, t.lastSeen, t.seenWall, t.frame = det.BBox, at, wall, round
			continue
		}
		t := &dedupTrack{bbox: det.BBox, lastSeen: at, seenWall: wall, frame: round}
		if det.TrackID != nil {
			t.trackID, t.hasID = *det.TrackID, true
		}
		d.tracks[key] = append(d.tracks[key], t)
		d.n++
		out[i] = true
	}
	for d.n > d.maxTracks {
		d.evictOldestLocked()
	}
	return out
}

// Len returns the number of live tracks.
func (d *Deduper) Len() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.n
}

func (d *Deduper) matchLocked(key dedupKey, det LocalDetection, round uint64) *dedupTrack {
	var best *dedupTrack
	bestIoU := d.minIoU
	for _, t := range d.tracks[key] {
		if t.frame == round {
			continue
		}
		if det.TrackID != nil {
			if t.hasID && t.trackID == *det.TrackID {
				return t
			}
			continue
		}
		if t.hasID {
			continue
		}
		if v := iou(t.bbox, det.BBox); v >= bestIoU {
			best, bestIoU = t, v
		}
	}
	return best
}

// expireLocked drops tracks unseen for longer than the gap. The calling
// camera's tracks are judged on its own frame timeline; other cameras' only
// by wall clock, so cameras with skewed clocks or uneven frame pacing never
// expire each other's tracks, while a camera that went silent is still reaped.
func (d *Deduper) expireLocked(camera string, at, wall time.Time) {
	for key, ts := range d.tracks {
		kept := ts[:0]
		for _, t := range ts {
			alive := wall.Sub(t.seenWall) <= d.gap
			if key.camera == camera {
				alive = at.Sub(t.lastSeen) <= d.gap
			}
			if alive {
				kept = append(kept, t)
			}
		}
		d.n -= len(ts) - len(kept)
		if len(kept) == 0 {
			delete(d.tracks, key)
		} else {
			d.tracks[key] = kept
		}
	}
}

func (d *Deduper) evictOldestLocked() {
	var oldKey dedupKey
	oldIdx := -1
	var oldest time.Time
	for key, ts := range d.tracks {
		for i, t := range ts {
			if oldIdx < 0 || t.lastSeen.Before(oldest) {
				oldKey, oldIdx, oldest = key, i, t.lastSeen
			}
		}
	}
	if oldIdx < 0 {
		return
	}
	ts := d.tracks[oldKey]
	d.tracks[oldKey] = append(ts[:oldIdx], ts[oldIdx+1:]...)
	if len(d.tracks[oldKey]) == 0 {
		delete(d.tracks, oldKey)
	}
	d.n--
}

func detectionClass(det LocalDetection) string {
	if det.Label != "" {
		return det.Label
	}
	return det.Tipo
}

func iou(a, b BoundingBox) float64 {
	ix := min(a.X2, b.X2) - max(a.X1, b.X1)
	iy := min(a.Y2, b.Y2) - max(a.Y1, b.Y1)
	if ix <= 0 || iy <= 0 {
		return 0
	}
	inter := ix * iy
	union := (a.X2-a.X1)*(a.Y2-a.Y1) + (b.X2-b.X1)*(b.Y2-b.Y1) - inter
	if union <= 0 {
		return 0
	}
	return inter / union
}
