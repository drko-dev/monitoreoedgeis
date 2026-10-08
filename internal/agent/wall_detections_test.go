package agent

import (
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/vision"
)

func TestRecentDetectionsFreshnessAndClear(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	r := newRecentDetections()
	r.now = func() time.Time { return now }
	person := vision.Detection{ClassID: 0, Label: "person", Type: "person", Confidence: 0.8437, BBox: [4]float64{10.4, 20.6, 110, 220}}

	r.Set("cam-a", []vision.Detection{person})
	got := r.Get("cam-a")
	if len(got) != 1 || got[0].Label != "person" || got[0].BBox != [4]float64{10, 21, 110, 220} || got[0].Confidence != 0.844 {
		t.Fatalf("Get = %+v", got)
	}
	if r.Get("cam-b") != nil {
		t.Fatal("cameras must not share detections")
	}
	now = now.Add(wallDetectionMaxAge + time.Millisecond)
	if r.Get("cam-a") != nil {
		t.Fatal("stale detections must not be drawn")
	}
	r.Set("cam-a", []vision.Detection{person})
	r.Set("cam-a", nil) // person left: next inference has no detections
	if r.Get("cam-a") != nil && len(r.Get("cam-a")) != 0 {
		t.Fatal("an empty inference must clear the camera's boxes")
	}
	many := make([]vision.Detection, 50)
	r.Set("cam-a", many)
	if n := len(r.Get("cam-a")); n != maxWallDetections {
		t.Fatalf("carried %d detections, want cap %d", n, maxWallDetections)
	}
	var nilCache *recentDetections
	if nilCache.Get("cam-a") != nil {
		t.Fatal("nil cache must send nothing")
	}
}
