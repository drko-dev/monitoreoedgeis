package processing

import (
	"math/rand"
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/rtsp"
)

// scene renders reproducible 640x360 Y planes: a textured static
// background, per-pixel sensor noise, a global brightness offset, and
// optional moving rectangles.
type scene struct {
	w, h   int
	base   []byte
	rng    *rand.Rand
	noise  int // +/- per-pixel noise amplitude
	bright int // global luma offset
}

type box struct{ x, y, w, h int }

func newScene(seed int64, noise int) *scene {
	s := &scene{w: 640, h: 360, rng: rand.New(rand.NewSource(seed)), noise: noise}
	s.base = make([]byte, s.w*s.h)
	for y := 0; y < s.h; y++ {
		for x := 0; x < s.w; x++ {
			s.base[y*s.w+x] = byte(60 + (x/40+y/30)%5*20) // static texture
		}
	}
	return s
}

func clamp(v int) byte {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return byte(v)
}

func (s *scene) frame(objs ...box) []byte {
	out := make([]byte, s.w*s.h*3/2)
	for i, b := range s.base {
		v := int(b) + s.bright
		if s.noise > 0 {
			v += s.rng.Intn(2*s.noise+1) - s.noise
		}
		out[i] = clamp(v)
	}
	for _, o := range objs {
		for y := max(o.y, 0); y < min(o.y+o.h, s.h); y++ {
			for x := max(o.x, 0); x < min(o.x+o.w, s.w); x++ {
				out[y*s.w+x] = clamp(200 + s.bright)
			}
		}
	}
	return out
}

type gateRun struct {
	g     *MotionGate
	t     time.Time
	step  time.Duration
	frame int
}

func newGateRun(cfg MotionGateConfig) *gateRun {
	return &gateRun{g: NewMotionGate(cfg), t: time.Unix(1000, 0), step: time.Second / 15}
}

func (r *gateRun) feed(y []byte) {
	r.frame++
	r.g.Evaluate(DecodedFrame{Data: y, Width: 640, Height: 360, PipelineSeq: uint64(r.frame), DecodedAt: r.t})
	r.t = r.t.Add(r.step)
}

func (r *gateRun) quiet(s *scene, n int) {
	for i := 0; i < n; i++ {
		r.feed(s.frame())
	}
}

func TestMotionGateStaysIdleOnStaticNoisyScene(t *testing.T) {
	s := newScene(1, 6)
	r := newGateRun(MotionGateConfig{})
	r.quiet(s, 15*60) // one minute
	st := r.g.Status()
	if st.State != MotionStateIdle || st.Activations != 0 {
		t.Fatalf("static scene: state=%s activations=%d, want idle/0", st.State, st.Activations)
	}
}

func TestMotionGateActivatesWithinTwoFramesForWalkingAndRunning(t *testing.T) {
	for _, tc := range []struct {
		name  string
		speed int // px per frame at 15 FPS
	}{{"walking", 4}, {"running", 16}} {
		t.Run(tc.name, func(t *testing.T) {
			s := newScene(2, 4)
			r := newGateRun(MotionGateConfig{})
			r.quiet(s, 30)
			onset := r.t
			for x := 0; x < 640; x += tc.speed {
				r.feed(s.frame(box{x, 120, 40, 120})) // person-sized, ~6 % of frame
				if r.g.Status().State == MotionStateActive {
					break
				}
			}
			st := r.g.Status()
			if st.State != MotionStateActive {
				t.Fatalf("never activated")
			}
			// Persist=2: activation on the 2nd candidate frame.
			if lag := r.t.Sub(onset); lag > 3*r.step {
				t.Fatalf("activation took %v (>3 frames)", lag)
			}
			if st.LastActivationLatencyMS > 100 {
				t.Fatalf("first-candidate->activation = %.0f ms, want <= 1 frame", st.LastActivationLatencyMS)
			}
		})
	}
}

func TestMotionGateDetectsSlowMovement(t *testing.T) {
	// A vehicle creeping 1 px/frame: frame-to-frame diff is tiny, but the
	// EMA background still sees it.
	s := newScene(3, 4)
	r := newGateRun(MotionGateConfig{})
	r.quiet(s, 30)
	for i := 0; i < 45; i++ { // 3 s
		r.feed(s.frame(box{100 + i, 200, 120, 60}))
	}
	if st := r.g.Status(); st.Activations == 0 {
		t.Fatalf("slow movement not detected: %+v", st)
	}
}

func TestMotionGateCompensatesUniformLightingChange(t *testing.T) {
	s := newScene(4, 4)
	r := newGateRun(MotionGateConfig{})
	r.quiet(s, 30)
	for i := 0; i < 15; i++ { // cloud passing: +2 luma per frame, +30 total
		s.bright += 2
		r.feed(s.frame())
	}
	r.quiet(s, 30)
	st := r.g.Status()
	if st.Activations != 0 {
		t.Fatalf("uniform lighting ramp activated the gate: %+v", st)
	}
	s.bright += 40 // lights switched on: one-frame jump
	r.quiet(s, 30)
	if st = r.g.Status(); st.Activations != 0 {
		t.Fatalf("sudden uniform lighting activated the gate: %+v", st)
	}
	if st.LightingCompensations == 0 {
		t.Fatalf("lighting jump not recorded")
	}
	// The gate still sees a person after the lighting change.
	for x := 0; x < 200; x += 8 {
		r.feed(s.frame(box{x, 120, 40, 120}))
	}
	if st = r.g.Status(); st.Activations == 0 {
		t.Fatalf("person after lighting change not detected")
	}
}

func TestMotionGateReseedsOnGlobalChangeWithoutActivating(t *testing.T) {
	// IR switch: the whole texture changes at once (non-uniform).
	s := newScene(5, 4)
	r := newGateRun(MotionGateConfig{})
	r.quiet(s, 30)
	for i := range s.base {
		s.base[i] = 255 - s.base[i]
	}
	r.quiet(s, 30)
	st := r.g.Status()
	if st.Activations != 0 || st.GlobalChanges == 0 {
		t.Fatalf("IR-like switch: activations=%d global_changes=%d, want 0 and >0", st.Activations, st.GlobalChanges)
	}
}

func TestMotionGateNightNoiseAdaptsFloor(t *testing.T) {
	// Heavy grain (IR night) must not keep the gate active after warm-up.
	s := newScene(6, 25)
	r := newGateRun(MotionGateConfig{})
	r.quiet(s, 15*10)
	before := r.g.Status().Activations
	r.quiet(s, 15*30)
	if st := r.g.Status(); st.Activations != before || st.State != MotionStateIdle {
		t.Fatalf("night noise kept activating: %d -> %d, state %s", before, st.Activations, st.State)
	}
}

func TestMotionGateAmbientMotionOutsideROIIgnored(t *testing.T) {
	// Swaying tree in the top-left corner, ROI excludes it.
	s := newScene(7, 4)
	r := newGateRun(MotionGateConfig{ROIs: []ROI{{XMin: 0.3, YMin: 0.3, XMax: 1, YMax: 1}}})
	r.quiet(s, 30)
	for i := 0; i < 15*20; i++ {
		r.feed(s.frame(box{20 + (i%10)*3, 20, 60, 60}))
	}
	if st := r.g.Status(); st.Activations != 0 {
		t.Fatalf("motion outside ROI activated: %+v", st)
	}
	for x := 250; x < 600; x += 8 {
		r.feed(s.frame(box{x, 200, 40, 120}))
	}
	if st := r.g.Status(); st.Activations == 0 {
		t.Fatalf("person inside ROI not detected")
	}
}

func TestMotionGateHysteresisHoldsThroughShortPauses(t *testing.T) {
	s := newScene(8, 4)
	r := newGateRun(MotionGateConfig{})
	r.quiet(s, 30)
	x := 0
	for burst := 0; burst < 5; burst++ { // walk, pause 0.5 s, walk...
		for i := 0; i < 8; i++ {
			x += 6
			r.feed(s.frame(box{x, 120, 40, 120}))
		}
		for i := 0; i < 7; i++ {
			r.feed(s.frame(box{x, 120, 40, 120}))
		}
	}
	if st := r.g.Status(); st.Activations != 1 {
		t.Fatalf("activations = %d over a walk with short pauses, want 1 (hysteresis)", st.Activations)
	}
	r.quiet(s, 15*3)
	if st := r.g.Status(); st.State != MotionStateIdle {
		t.Fatalf("state after 3 s quiet = %s, want idle", st.State)
	}
}

func TestMotionGateDetectionCorrelation(t *testing.T) {
	s := newScene(9, 4)
	r := newGateRun(MotionGateConfig{})
	r.quiet(s, 30)
	var detAt time.Time
	for x := 0; x < 300; x += 8 {
		r.feed(s.frame(box{x, 120, 40, 120}))
		if x == 80 {
			detAt = r.t
		}
	}
	r.g.ObserveDetection(detAt)
	// The ghost is absorbed after StillAbsorb (1 s), then Hold (1 s): idle
	// about 2.3 s after the object left.
	r.quiet(s, 45)
	r.g.ObserveDetection(r.t) // same object 3 s later, scene static: not new
	st := r.g.Status()
	if st.NewAppearances != 1 || st.NewAppearancesWithMotion != 1 || st.LastAppearanceLeadMS <= 0 {
		t.Fatalf("correlation = %+v, want 1 new appearance seen by the gate first", st)
	}
	if st.EpisodesWithDetection != 1 || st.EpisodesWithoutDetection != 0 {
		t.Fatalf("episodes = %+v", st)
	}
	if st.DetectionsObserved != 2 || st.DetectionsWhileActive != 1 {
		t.Fatalf("detections = %d/%d, want 2 observed, 1 while active", st.DetectionsObserved, st.DetectionsWhileActive)
	}
}

func TestMotionGateOfferNeverBlocks(t *testing.T) {
	g := NewMotionGate(MotionGateConfig{}) // Run not started: inbox fills
	f := DecodedFrame{Data: make([]byte, 640*360*3/2), Width: 640, Height: 360}
	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			g.Offer(f)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Offer blocked")
	}
	if st := g.Status(); st.FramesSkipped != 99 {
		t.Fatalf("skipped = %d, want 99", st.FramesSkipped)
	}
}

func BenchmarkMotionGateEvaluate640x360(b *testing.B) {
	s := newScene(10, 4)
	g := NewMotionGate(MotionGateConfig{})
	frames := [][]byte{s.frame(), s.frame(box{100, 100, 40, 120})}
	t0 := time.Unix(1000, 0)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		g.Evaluate(DecodedFrame{Data: frames[i%2], Width: 640, Height: 360, DecodedAt: t0.Add(time.Duration(i) * time.Second / 15)})
	}
}

func BenchmarkMotionGateEvaluate1920x1080(b *testing.B) {
	y := make([]byte, 1920*1080*3/2)
	g := NewMotionGate(MotionGateConfig{})
	t0 := time.Unix(1000, 0)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		g.Evaluate(DecodedFrame{Data: y, Width: 1920, Height: 1080, DecodedAt: t0.Add(time.Duration(i) * time.Second / 15)})
	}
}

// Stage 1 is observation only: an active gate never changes the sampler.
func TestMotionGateObservationOnlyLeavesSamplerUntouched(t *testing.T) {
	cfg := Config{TargetFPS: 2, PacedSampling: true, RingBufferSize: 30, QueueDepth: 4, MotionGate: &MotionGateConfig{}}
	p := newCameraPipeline("cam", rtsp.StreamDescriptor{CandidateKey: "cam", Codec: "H264", Width: 4, Height: 4}, cfg, nil, nil)
	if p.MotionGate() == nil {
		t.Fatal("gate not built from Config.MotionGate")
	}
	s := newScene(11, 4)
	r := &gateRun{g: p.MotionGate(), t: time.Unix(1000, 0), step: time.Second / 15}
	r.quiet(s, 30)
	for x := 0; x < 400; x += 8 {
		r.feed(s.frame(box{x, 120, 40, 120}))
	}
	if st := p.MotionGate().Status(); st.State != MotionStateActive {
		t.Fatalf("gate state = %s, want active", st.State)
	}
	if got := p.Sampler().TargetFPS(); got != 2 {
		t.Fatalf("sampler target = %v while motion active, want 2 (observation only)", got)
	}
	if p.Status().Motion == nil {
		t.Fatal("pipeline status has no motion block")
	}
}

func TestNoMotionGateWithoutConfig(t *testing.T) {
	p := newCameraPipeline("cam", rtsp.StreamDescriptor{CandidateKey: "cam", Codec: "H264", Width: 4, Height: 4}, Config{TargetFPS: 2, QueueDepth: 4}, nil, nil)
	if p.MotionGate() != nil || p.Status().Motion != nil {
		t.Fatal("cloud/hybrid pipelines must not get a motion gate")
	}
}
