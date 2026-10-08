package inference

import (
	"testing"
	"time"
)

// measuredAt returns a Measure func reporting latencyMS per inference, so
// capacity = 1000/latencyMS, with each camera's effective = its rate.
func measuredAt(v *fakeVideo, latencyMS float64) func() map[string]Measured {
	return func() map[string]Measured {
		out := map[string]Measured{}
		for _, k := range v.CandidateKeys() {
			out[k] = Measured{EffectiveFPS: v.rate(k), LatencyMS: latencyMS, Samples: 32}
		}
		return out
	}
}

func TestUnmeasuredCapacityPassesDemandThrough(t *testing.T) {
	v := newFakeVideo("a")
	m, _ := newTestManager(v, Options{GlobalTargetFPS: 2, Default: adaptive()})
	v.setMotion("a", "active")
	m.Tick()
	if v.rate("a") != 10 {
		t.Fatalf("rate = %v, want 10 before any measurement", v.rate("a"))
	}
	if d := m.Status().Device; d.CapacitySource != "unmeasured" || d.Saturated {
		t.Fatalf("device = %+v", d)
	}
}

func TestCapacitySharedAcrossSimultaneouslyActiveCameras(t *testing.T) {
	// 50 ms/inference -> 20/s capacity, 15 % reserve -> 17/s available.
	v := newFakeVideo("a", "b", "c")
	m, c := newTestManager(v, Options{GlobalTargetFPS: 2, Default: adaptive(), Reserve: 0.15})
	m.opt.Measure = measuredAt(v, 50)
	for _, k := range []string{"a", "b", "c"} {
		v.setMotion(k, "active")
	}
	m.Tick()
	c.adv(TickInterval)
	m.Tick()
	var sum float64
	for _, k := range []string{"a", "b", "c"} {
		r := v.rate(k)
		sum += r
		if !near(r, 17.0/3) {
			t.Errorf("%s = %v, want equal share %.2f", k, r, 17.0/3)
		}
	}
	if !near(sum, 17) {
		t.Fatalf("allocated %v, want available 17 (reserve kept)", sum)
	}
	d := m.Status().Device
	if !near(d.CapacityFPS, 20) || d.CapacitySource != "measured_latency" || d.ActiveCameras != 3 || d.Saturated {
		t.Fatalf("device = %+v", d)
	}
	if st := m.Status().Cameras["a"]; st.SaturationReason != "capacity_limited" {
		t.Fatalf("a reason = %q, want capacity_limited", st.SaturationReason)
	}
}

func TestPrioritiesWeightTheExtra(t *testing.T) {
	v := newFakeVideo("hi", "lo")
	m, c := newTestManager(v, Options{GlobalTargetFPS: 2, Default: adaptive(), Reserve: 0.15})
	m.opt.Measure = measuredAt(v, 1000.0/16) // 16/s -> 13.6 available
	_ = m.SetCameraConfig("hi", &CameraConfig{Mode: ModeAdaptive, Priority: PriorityHigh})
	_ = m.SetCameraConfig("lo", &CameraConfig{Mode: ModeAdaptive, Priority: PriorityLow})
	v.setMotion("hi", "active")
	v.setMotion("lo", "active")
	m.Tick()
	c.adv(TickInterval)
	m.Tick()
	// mins 2+2, extra 9.6 split 4:1 -> hi 2+7.68, lo 2+1.92.
	if !near(v.rate("hi"), 9.68) || !near(v.rate("lo"), 3.92) {
		t.Fatalf("hi=%v lo=%v, want 9.68 / 3.92", v.rate("hi"), v.rate("lo"))
	}
}

func TestOneFastCameraCannotStarveOthers(t *testing.T) {
	// A FIXED camera asking 30 FPS next to two idle adaptive ones.
	v := newFakeVideo("greedy", "x", "y")
	m, c := newTestManager(v, Options{GlobalTargetFPS: 2, Default: adaptive(), Reserve: 0.15})
	m.opt.Measure = measuredAt(v, 50) // 17 available
	_ = m.SetCameraConfig("greedy", &CameraConfig{Mode: ModeFixed, TargetFPS: 30})
	m.Tick()
	c.adv(TickInterval)
	m.Tick()
	if v.rate("x") < 2 || v.rate("y") < 2 {
		t.Fatalf("idle cameras starved: x=%v y=%v", v.rate("x"), v.rate("y"))
	}
	if st := m.Status().Cameras["greedy"]; st.SaturationReason != "min_demand_exceeds_capacity" {
		t.Fatalf("greedy = %+v, want reported as exceeding capacity", st)
	}
}

func TestSaturationWhenMinsDoNotFit(t *testing.T) {
	// 5 FIXED cameras at 5 FPS = 25 > 17 available: never a promise the
	// worker cannot keep, every camera reported saturated, shares by weight.
	keys := []string{"c1", "c2", "c3", "c4", "c5"}
	v := newFakeVideo(keys...)
	m, c := newTestManager(v, Options{GlobalTargetFPS: 5, Reserve: 0.15})
	m.opt.Measure = measuredAt(v, 50)
	m.Tick()
	c.adv(TickInterval)
	m.Tick()
	var sum float64
	for _, k := range keys {
		sum += v.rate(k)
		if st := m.Status().Cameras[k]; st.SaturationReason != "min_demand_exceeds_capacity" {
			t.Fatalf("%s reason = %q", k, st.SaturationReason)
		}
	}
	if sum > 17.01 {
		t.Fatalf("allocated %v > available 17", sum)
	}
	d := m.Status().Device
	if !d.Saturated || d.OverloadedCameras != 5 || d.HeadroomFPS != 0 {
		t.Fatalf("device = %+v", d)
	}
}

func TestBurstCompetesForCapacity(t *testing.T) {
	v := newFakeVideo("anpr", "other")
	m, c := newTestManager(v, Options{GlobalTargetFPS: 2, Reserve: 0.15})
	m.opt.Measure = measuredAt(v, 1000.0/12) // 10.2 available
	m.SetBurstFPS("anpr", 25)
	m.Tick()
	c.adv(TickInterval)
	m.Tick()
	if v.rate("other") < 2 {
		t.Fatalf("burst starved the other camera: %v", v.rate("other"))
	}
	if !near(v.rate("anpr")+v.rate("other"), 10.2) {
		t.Fatalf("total = %v, want 10.2", v.rate("anpr")+v.rate("other"))
	}
}

func TestFixedSingleCameraUnaffectedWithinCapacity(t *testing.T) {
	// TC70 today: one FIXED camera at 2 FPS, capacity ~15/s.
	v := newFakeVideo("tc70")
	m, c := newTestManager(v, Options{GlobalTargetFPS: 2, Reserve: 0.15})
	m.opt.Measure = measuredAt(v, 65)
	for i := 0; i < 50; i++ {
		c.adv(TickInterval)
		m.Tick()
	}
	if v.rate("tc70") != 2 || v.sets != 0 {
		t.Fatalf("FIXED 2 FPS changed: rate=%v sets=%d", v.rate("tc70"), v.sets)
	}
	d := m.Status().Device
	if d.Saturated || !near(d.HeadroomFPS, 1000.0/65*0.85-2) {
		t.Fatalf("device = %+v", d)
	}
}

func TestWaterFillRedistributesUnusedShare(t *testing.T) {
	got := waterFill([]demand{{key: "a", want: 1, weight: 1}, {key: "b", want: 10, weight: 1}}, 6)
	if !near(got["a"], 1) || !near(got["b"], 5) {
		t.Fatalf("got %v, want a=1 b=5", got)
	}
	_ = time.Second
}

// Regression (TC70, 2026-10-08): the worker's first inference takes seconds;
// before minLatencySamples the capacity is "unmeasured" and demand passes
// through, instead of collapsing the allocation below even the minimum.
func TestFewSamplesDoNotCountAsCapacity(t *testing.T) {
	v := newFakeVideo("a")
	m, _ := newTestManager(v, Options{GlobalTargetFPS: 2, Default: adaptive()})
	m.opt.Measure = func() map[string]Measured {
		return map[string]Measured{"a": {EffectiveFPS: 2, LatencyMS: 1900, Samples: 3}}
	}
	v.setMotion("a", "active")
	m.Tick()
	if v.rate("a") != 10 {
		t.Fatalf("rate = %v, want 10 (warm-up latency must not count)", v.rate("a"))
	}
	if d := m.Status().Device; d.CapacitySource != "unmeasured" {
		t.Fatalf("capacity source = %q", d.CapacitySource)
	}
}
