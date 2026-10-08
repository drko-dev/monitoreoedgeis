package processing

import (
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/rtsp"
)

func emitted(s *Sampler, srcFPS float64, d time.Duration, start time.Time) int {
	step := time.Duration(float64(time.Second) / srcFPS)
	n := 0
	for t := start; t.Before(start.Add(d)); t = t.Add(step) {
		if s.ShouldEmit(t) {
			n++
		}
	}
	return n
}

// A 15 FPS source sampled at 2/5/10/15 must yield exactly that rate in Full
// Edge (paced), where target_fps is the inference rate.
func TestPacedSamplerHitsConfiguredRate(t *testing.T) {
	start := time.Unix(1000, 0)
	for _, fps := range []float64{2, 5, 10, 15} {
		got := emitted(NewPacedSampler(fps), 15, 10*time.Second, start)
		if want := int(fps * 10); got < want-1 || got > want+1 {
			t.Errorf("paced %.0f FPS over 10 s of 15 FPS source = %d frames, want %d", fps, got, want)
		}
	}
}

// Cloud/hybrid keep the minimum-interval gate: unchanged behavior.
func TestUnpacedSamplerKeepsMinimumIntervalBehavior(t *testing.T) {
	start := time.Unix(1000, 0)
	if got := emitted(NewSampler(10), 15, 10*time.Second, start); got < 74 || got > 76 {
		t.Fatalf("unpaced 10 FPS over 15 FPS source = %d, want 75 (7.5 FPS, pre-existing behavior)", got)
	}
}

func TestPacedSamplerFollowsSetTargetFPS(t *testing.T) {
	s := NewPacedSampler(2)
	start := time.Unix(1000, 0)
	emitted(s, 15, 5*time.Second, start)
	s.SetTargetFPS(15) // e.g. ANPR burst or remote per-camera target_fps
	if got := emitted(s, 15, 10*time.Second, start.Add(5*time.Second)); got < 149 {
		t.Fatalf("after SetTargetFPS(15) got %d frames in 10 s, want ~150", got)
	}
}

func TestRingCapacityCoversMinHistoryAtTargetFPS(t *testing.T) {
	cfg := Config{RingBufferSize: 30, TargetFPS: 2, MinHistory: 4 * time.Second}
	if got := ringCapacity(cfg); got != 30 {
		t.Fatalf("2 FPS capacity = %d, want floor 30", got)
	}
	cfg.TargetFPS = 15
	if got := ringCapacity(cfg); got != 61 {
		t.Fatalf("15 FPS capacity = %d, want 61 (4 s + 1)", got)
	}
	cfg.TargetFPS = 30
	cfg.MinHistory = time.Minute
	if got := ringCapacity(cfg); got != MaxRingBufferFrames {
		t.Fatalf("capacity = %d, want ceiling %d", got, MaxRingBufferFrames)
	}
	cfg.MinHistory = 0
	if got := ringCapacity(cfg); got != 30 {
		t.Fatalf("no MinHistory capacity = %d, want RingBufferSize (unchanged behavior)", got)
	}
}

func TestPipelineSetTargetFPSResizesRing(t *testing.T) {
	p := newCameraPipeline("cam", rtsp.StreamDescriptor{CandidateKey: "cam", Codec: "H264", Width: 4, Height: 4}, Config{RingBufferSize: 30, TargetFPS: 2, MinHistory: 4 * time.Second, PacedSampling: true, QueueDepth: 4}, nil, nil)
	p.SetTargetFPS(15)
	if _, c := p.ring.Usage(); c != 61 {
		t.Fatalf("ring capacity after 15 FPS = %d, want 61", c)
	}
	p.SetTargetFPS(2)
	if _, c := p.ring.Usage(); c != 30 {
		t.Fatalf("ring capacity back at 2 FPS = %d, want 30", c)
	}
}

func TestRingBufferResizeKeepsNewest(t *testing.T) {
	r := NewRingBuffer(4)
	for i := 1; i <= 6; i++ {
		r.Push(Frame{Seq: uint64(i)})
	}
	r.Resize(2)
	snap := r.Snapshot()
	if len(snap) != 2 || snap[0].Seq != 5 || snap[1].Seq != 6 {
		t.Fatalf("after shrink = %+v, want seqs 5,6", snap)
	}
	r.Resize(5)
	r.Push(Frame{Seq: 7})
	snap = r.Snapshot()
	if len(snap) != 3 || snap[0].Seq != 5 || snap[2].Seq != 7 {
		t.Fatalf("after grow = %+v, want seqs 5,6,7", snap)
	}
}
