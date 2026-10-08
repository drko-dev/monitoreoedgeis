package agent

import (
	"math"
	"sync"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/transport"
	"github.com/drko-dev/monitoreoedgeis/internal/vision"
)

// wallDetectionMaxAge bounds how old the last local inference may be and
// still be drawn on a display frame: at the 2 FPS inference rate a box older
// than this belongs to an object that has likely moved or left.
const wallDetectionMaxAge = 2 * time.Second

// maxWallDetections caps what one display frame carries (header size).
const maxWallDetections = 20

// recentDetections keeps, per camera, the detections of the latest local
// inference so display-only frames (preview and Live View) can carry them to
// the SaaS wall. Display metadata only: it never creates events and never
// triggers inference. One entry per camera, overwritten each inference.
type recentDetections struct {
	mu  sync.Mutex
	m   map[string]recentDetectionEntry
	now func() time.Time
}

type recentDetectionEntry struct {
	at   time.Time
	dets []transport.VideoDetection
}

func newRecentDetections() *recentDetections {
	return &recentDetections{m: make(map[string]recentDetectionEntry), now: time.Now}
}

// Set records one inference result; an empty result clears the camera.
func (r *recentDetections) Set(candidateKey string, dets []vision.Detection) {
	out := make([]transport.VideoDetection, 0, min(len(dets), maxWallDetections))
	for _, d := range dets[:min(len(dets), maxWallDetections)] {
		out = append(out, transport.VideoDetection{
			ClassID:    d.ClassID,
			Label:      d.Label,
			Type:       d.Type,
			Confidence: math.Round(d.Confidence*1000) / 1000,
			BBox:       [4]float64{math.Round(d.BBox[0]), math.Round(d.BBox[1]), math.Round(d.BBox[2]), math.Round(d.BBox[3])},
		})
	}
	r.mu.Lock()
	r.m[candidateKey] = recentDetectionEntry{at: r.now(), dets: out}
	r.mu.Unlock()
}

// Get returns the camera's detections if the latest inference is fresh.
func (r *recentDetections) Get(candidateKey string) []transport.VideoDetection {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.m[candidateKey]
	if !ok || r.now().Sub(e.at) > wallDetectionMaxAge {
		return nil
	}
	return e.dets
}
