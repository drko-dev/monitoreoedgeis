package fulledge

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 7, 21, 0, 0, 0, time.UTC)

func person(x float64) LocalDetection {
	return LocalDetection{ClassID: 0, Label: "person", Tipo: "person", Confidence: 0.9, BBox: BoundingBox{X1: x, Y1: 50, X2: x + 100, Y2: 300}}
}

func car(x float64) LocalDetection {
	return LocalDetection{ClassID: 2, Label: "car", Tipo: "vehicle", Confidence: 0.9, BBox: BoundingBox{X1: x, Y1: 200, X2: x + 200, Y2: 330}}
}

func withTrack(d LocalDetection, id int64) LocalDetection { d.TrackID = &id; return d }

func countNew(admitted []bool) int {
	n := 0
	for _, a := range admitted {
		if a {
			n++
		}
	}
	return n
}

// 1. Same track over many frames => one event (with and without track_id).
func TestDedupSameTrackManyFramesIsOneEvent(t *testing.T) {
	for name, det := range map[string]LocalDetection{"track_id": withTrack(person(100), 7), "iou": person(100)} {
		d := NewDeduper(0, 0, 0)
		events := 0
		for i := 0; i < 500; i++ { // 250 s at 2 FPS, never unseen for 10 s
			events += countNew(d.Admit("cam-a", t0.Add(time.Duration(i)*500*time.Millisecond), []LocalDetection{det}))
		}
		if events != 1 {
			t.Errorf("%s: %d events for one still object over 500 frames, want 1", name, events)
		}
	}
}

// 2. A moving track stays one event: by track_id even across large jumps,
// by IoU while consecutive frames overlap.
func TestDedupMovingTrackStaysOneEvent(t *testing.T) {
	d := NewDeduper(0, 0, 0)
	events := 0
	for i := 0; i < 40; i++ {
		events += countNew(d.Admit("cam-a", t0.Add(time.Duration(i)*500*time.Millisecond), []LocalDetection{withTrack(person(float64(i*60)), 3)}))
	}
	if events != 1 {
		t.Errorf("track_id: %d events for one walking person, want 1", events)
	}

	d, events = NewDeduper(0, 0, 0), 0
	for i := 0; i < 40; i++ { // 15 px/frame on a 100 px wide box
		events += countNew(d.Admit("cam-a", t0.Add(time.Duration(i)*500*time.Millisecond), []LocalDetection{person(float64(i * 15))}))
	}
	if events != 1 {
		t.Errorf("iou: %d events for one walking person, want 1", events)
	}
}

// 3. Unseen for more than the 10 s gap, then back => a second event.
func TestDedupTrackReappearsAfterGap(t *testing.T) {
	for name, det := range map[string]LocalDetection{"track_id": withTrack(person(100), 9), "iou": person(100)} {
		d := NewDeduper(10*time.Second, 0, 0)
		events := countNew(d.Admit("cam-a", t0, []LocalDetection{det}))
		events += countNew(d.Admit("cam-a", t0.Add(9*time.Second), []LocalDetection{det})) // within gap
		events += countNew(d.Admit("cam-a", t0.Add(20*time.Second), []LocalDetection{det}))
		events += countNew(d.Admit("cam-a", t0.Add(21*time.Second), []LocalDetection{det}))
		if events != 2 {
			t.Errorf("%s: %d events, want 2 (first sighting + reappearance after >10s)", name, events)
		}
	}
}

// 4. Two simultaneous people => two independent events, even when their
// boxes overlap enough to both match a single track by IoU.
func TestDedupTwoSimultaneousPeople(t *testing.T) {
	cases := map[string][]LocalDetection{
		"track_id":        {withTrack(person(100), 1), withTrack(person(100), 2)},
		"iou_apart":       {person(0), person(400)},
		"iou_overlapping": {person(100), person(130)},
	}
	for name, dets := range cases {
		d := NewDeduper(0, 0, 0)
		if got := countNew(d.Admit("cam-a", t0, dets)); got != 2 {
			t.Errorf("%s: first frame %d events, want 2", name, got)
		}
		if got := countNew(d.Admit("cam-a", t0.Add(500*time.Millisecond), dets)); got != 0 {
			t.Errorf("%s: second frame %d events, want 0", name, got)
		}
	}
}

// 5. Person + vehicle in the same spot => independent events per class.
func TestDedupPersonAndVehicleIndependent(t *testing.T) {
	d := NewDeduper(0, 0, 0)
	p, c := person(100), car(100)
	c.BBox = p.BBox // identical box: only the class separates them
	if got := countNew(d.Admit("cam-a", t0, []LocalDetection{p, c})); got != 2 {
		t.Fatalf("%d events for person+vehicle, want 2", got)
	}
	if got := countNew(d.Admit("cam-a", t0.Add(time.Second), []LocalDetection{p, c})); got != 0 {
		t.Fatalf("%d events on repeat, want 0", got)
	}
}

// 6. Camera A and camera B never share tracks.
func TestDedupCamerasIndependent(t *testing.T) {
	d := NewDeduper(0, 0, 0)
	det := withTrack(person(100), 5)
	got := countNew(d.Admit("cam-a", t0, []LocalDetection{det})) + countNew(d.Admit("cam-b", t0, []LocalDetection{det}))
	if got != 2 {
		t.Fatalf("%d events for the same track_id on two cameras, want 2", got)
	}
}

// 7. Expiry frees memory, and the state never exceeds maxTracks.
func TestDedupExpiryAndBound(t *testing.T) {
	d := NewDeduper(10*time.Second, 0, 8)
	for i := 0; i < 100; i++ {
		d.Admit(fmt.Sprintf("cam-%d", i), t0, []LocalDetection{person(100)})
		if d.Len() > 8 {
			t.Fatalf("Len = %d after %d cameras, want <= 8", d.Len(), i+1)
		}
	}
	// Silent cameras are reaped by wall clock once the gap elapses.
	d.now = func() time.Time { return time.Now().Add(11 * time.Second) }
	d.Admit("cam-other", t0, nil)
	if d.Len() != 0 {
		t.Fatalf("Len = %d after the gap elapsed, want 0", d.Len())
	}
}

// 8. Concurrent admission from many cameras: race-free and exact.
func TestDedupConcurrent(t *testing.T) {
	d := NewDeduper(0, 0, 0)
	var wg sync.WaitGroup
	var mu sync.Mutex
	events := 0
	for w := 0; w < 16; w++ {
		wg.Add(1)
		go func(cam string) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				n := countNew(d.Admit(cam, t0.Add(time.Duration(i)*100*time.Millisecond), []LocalDetection{person(100), car(300)}))
				mu.Lock()
				events += n
				mu.Unlock()
			}
		}(fmt.Sprintf("cam-%d", w))
	}
	wg.Wait()
	if events != 32 {
		t.Fatalf("%d events, want 32 (2 objects x 16 cameras)", events)
	}
}
