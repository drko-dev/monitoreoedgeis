package inference

import (
	"math"
	"sort"
	"sync"
	"testing"
	"time"
)

// fakeVideo stands in for processing.Manager.
type fakeVideo struct {
	mu     sync.Mutex
	fps    map[string]float64
	motion map[string]string
	sens   map[string]string
	sets   int
}

func newFakeVideo(keys ...string) *fakeVideo {
	v := &fakeVideo{fps: map[string]float64{}, motion: map[string]string{}, sens: map[string]string{}}
	for _, k := range keys {
		v.fps[k] = 2
		v.motion[k] = "idle"
	}
	return v
}

func (v *fakeVideo) CandidateKeys() []string {
	v.mu.Lock()
	defer v.mu.Unlock()
	var ks []string
	for k := range v.fps {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}
func (v *fakeVideo) CurrentTargetFPS(k string) (float64, bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	f, ok := v.fps[k]
	return f, ok
}
func (v *fakeVideo) SetTargetFPS(k string, f float64) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.fps[k] = f
	v.sets++
	return nil
}
func (v *fakeVideo) MotionState(k string) string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.motion[k]
}
func (v *fakeVideo) SetMotionSensitivity(k, s string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.sens[k] = s
}
func (v *fakeVideo) setMotion(k, s string) { v.mu.Lock(); v.motion[k] = s; v.mu.Unlock() }
func (v *fakeVideo) rate(k string) float64 { f, _ := v.CurrentTargetFPS(k); return f }

type clock struct{ t time.Time }

func (c *clock) now() time.Time      { return c.t }
func (c *clock) adv(d time.Duration) { c.t = c.t.Add(d) }

func newTestManager(v *fakeVideo, opt Options) (*Manager, *clock) {
	m := NewManager(v, opt)
	c := &clock{t: time.Unix(1000, 0)}
	m.now = c.now
	return m, c
}

func adaptive() CameraConfig { return CameraConfig{Mode: ModeAdaptive} }

func near(a, b float64) bool { return math.Abs(a-b) < 0.05 }

func TestFixedDefaultKeepsCurrentBehavior(t *testing.T) {
	v := newFakeVideo("cam")
	m, _ := newTestManager(v, Options{GlobalTargetFPS: 2})
	v.setMotion("cam", "active")
	m.Tick()
	if v.rate("cam") != 2 || v.sets != 0 {
		t.Fatalf("FIXED default: rate=%v sets=%d, want 2 untouched", v.rate("cam"), v.sets)
	}
	st := m.Status().Cameras["cam"]
	if st.Mode != ModeFixed || st.AllocatedFPS != 2 {
		t.Fatalf("status %+v", st)
	}
}

func TestAdaptiveIdleActiveHysteresisAndRamp(t *testing.T) {
	v := newFakeVideo("cam")
	m, c := newTestManager(v, Options{GlobalTargetFPS: 2, Default: adaptive()})
	m.Tick()
	if v.rate("cam") != 2 {
		t.Fatalf("idle rate = %v, want 2", v.rate("cam"))
	}
	if v.sens["cam"] != "medium" {
		t.Fatalf("sensitivity not pushed to gate: %q", v.sens["cam"])
	}
	v.setMotion("cam", "active")
	c.adv(TickInterval)
	m.Tick()
	if v.rate("cam") != 10 {
		t.Fatalf("active rate = %v, want 10 on the first tick after motion", v.rate("cam"))
	}
	// Motion stops: rate holds for idle_timeout (10 s), with short gaps.
	v.setMotion("cam", "idle")
	for i := 0; i < 99; i++ {
		c.adv(TickInterval)
		m.Tick()
		if i == 50 { // brief motion again resets the timeout
			v.setMotion("cam", "active")
			c.adv(TickInterval)
			m.Tick()
			v.setMotion("cam", "idle")
		}
	}
	if v.rate("cam") != 10 {
		t.Fatalf("rate dropped before idle_timeout: %v", v.rate("cam"))
	}
	// Past the timeout: steps down 10 -> 8 -> 6 -> 4 -> 2, one step per second.
	var seen []float64
	for i := 0; i < 120; i++ {
		c.adv(TickInterval)
		m.Tick()
		if r := v.rate("cam"); len(seen) == 0 || seen[len(seen)-1] != r {
			seen = append(seen, r)
		}
	}
	want := []float64{10, 8, 6, 4, 2}
	if len(seen) != len(want) {
		t.Fatalf("ramp = %v, want %v", seen, want)
	}
	for i := range want {
		if !near(seen[i], want[i]) {
			t.Fatalf("ramp = %v, want %v", seen, want)
		}
	}
	if st := m.Status().Cameras["cam"]; st.ControllerState != StateIdle {
		t.Fatalf("state = %s, want idle", st.ControllerState)
	}
}

func TestAdaptiveNeverBelowIdle(t *testing.T) {
	v := newFakeVideo("cam")
	m, c := newTestManager(v, Options{Default: CameraConfig{Mode: ModeAdaptive, IdleFPS: 1, ActiveFPS: 5}})
	for i := 0; i < 600; i++ {
		c.adv(TickInterval)
		m.Tick()
	}
	if v.rate("cam") != 1 {
		t.Fatalf("static scene rate = %v, want idle 1 (YOLO never stops)", v.rate("cam"))
	}
}

func TestBurstRaisesDemandAndReleases(t *testing.T) {
	v := newFakeVideo("cam")
	m, c := newTestManager(v, Options{GlobalTargetFPS: 2, Default: adaptive()})
	m.Tick()
	m.SetBurstFPS("cam", 12)
	c.adv(TickInterval)
	m.Tick()
	if v.rate("cam") != 12 {
		t.Fatalf("burst rate = %v, want 12", v.rate("cam"))
	}
	m.SetBurstFPS("cam", 40) // above max_fps 15
	c.adv(TickInterval)
	m.Tick()
	if v.rate("cam") != 15 {
		t.Fatalf("burst capped = %v, want max_fps 15", v.rate("cam"))
	}
	m.SetBurstFPS("cam", 0)
	c.adv(TickInterval)
	m.Tick()
	if v.rate("cam") != 2 {
		t.Fatalf("after release = %v, want idle 2", v.rate("cam"))
	}
}

func TestReconnectReappliesAllocation(t *testing.T) {
	v := newFakeVideo("cam")
	m, c := newTestManager(v, Options{GlobalTargetFPS: 2, Default: adaptive()})
	v.setMotion("cam", "active")
	m.Tick()
	// Pipeline rebuilt (RTSP reconnect): sampler back at the global rate,
	// gate warming.
	v.mu.Lock()
	v.fps["cam"] = 2
	v.motion["cam"] = "warming"
	v.mu.Unlock()
	c.adv(TickInterval)
	m.Tick()
	if v.rate("cam") != 10 {
		t.Fatalf("after reconnect rate = %v, want 10 re-applied (still within idle_timeout)", v.rate("cam"))
	}
}

func TestCameraRemovedAndAddedResetsController(t *testing.T) {
	v := newFakeVideo("a")
	m, c := newTestManager(v, Options{GlobalTargetFPS: 2})
	m.Tick()
	v.mu.Lock()
	delete(v.fps, "a")
	v.fps["b"] = 2
	v.mu.Unlock()
	c.adv(TickInterval)
	m.Tick()
	if _, ok := m.Status().Cameras["a"]; ok {
		t.Fatal("removed camera still reported")
	}
	if _, ok := m.Status().Cameras["b"]; !ok {
		t.Fatal("new camera not reported")
	}
}

func TestSetCameraConfigValidatesStrictly(t *testing.T) {
	v := newFakeVideo("cam")
	m, _ := newTestManager(v, Options{GlobalTargetFPS: 2})
	bad := []CameraConfig{
		{Mode: "turbo"},
		{Mode: ModeAdaptive, IdleFPS: 12, ActiveFPS: 10},
		{Mode: ModeAdaptive, ActiveFPS: 20, MaxFPS: 15},
		{Mode: ModeAdaptive, MaxFPS: 31},
		{Mode: ModeAdaptive, MotionSensitivity: "extreme"},
		{Mode: ModeAdaptive, Priority: "urgent"},
		{Mode: ModeAdaptive, IdleTimeout: 500 * time.Millisecond},
		{Mode: ModeFixed, TargetFPS: 0.01},
	}
	for _, b := range bad {
		b := b
		if err := m.SetCameraConfig("cam", &b); err == nil {
			t.Errorf("accepted invalid %+v", b)
		}
	}
	good := CameraConfig{Mode: ModeAdaptive, IdleFPS: 1, ActiveFPS: 8, MaxFPS: 12, MotionSensitivity: "high", IdleTimeout: 5 * time.Second, Priority: PriorityHigh}
	if err := m.SetCameraConfig("cam", &good); err != nil {
		t.Fatalf("rejected valid: %v", err)
	}
	m.Tick()
	if st := m.Status().Cameras["cam"]; st.Mode != ModeAdaptive || st.Priority != PriorityHigh || v.sens["cam"] != "high" {
		t.Fatalf("override not applied: %+v sens=%q", st, v.sens["cam"])
	}
	if err := m.SetCameraConfig("cam", nil); err != nil {
		t.Fatal(err)
	}
	m.Tick()
	if st := m.Status().Cameras["cam"]; st.Mode != ModeFixed {
		t.Fatalf("clearing override did not restore default: %+v", st)
	}
}

func TestRemoteTargetFPSIsFixedRate(t *testing.T) {
	v := newFakeVideo("cam")
	m, _ := newTestManager(v, Options{GlobalTargetFPS: 2})
	if err := m.SetCameraTargetFPS("cam", 5); err != nil {
		t.Fatal(err)
	}
	m.Tick()
	if v.rate("cam") != 5 {
		t.Fatalf("per-camera target_fps = %v, want 5", v.rate("cam"))
	}
	m.SetGlobalTargetFPS(3)
	m.Tick()
	if v.rate("cam") != 5 {
		t.Fatalf("global change overrode per-camera target: %v", v.rate("cam"))
	}
}
