package inference

import (
	"context"
	"fmt"
	"math"
	"sort"
	"sync"
	"time"
)

// Video is what the Manager drives: the camera pipelines' samplers and
// motion gates (implemented over processing.Manager by the agent).
type Video interface {
	CandidateKeys() []string
	CurrentTargetFPS(key string) (float64, bool)
	SetTargetFPS(key string, fps float64) error
	// MotionState reports the camera's MotionGate state ("" if none).
	MotionState(key string) string
	SetMotionSensitivity(key, sensitivity string)
}

// Measured is one camera's measured inference figures (from the scheduler
// in front of the worker).
type Measured struct {
	EffectiveFPS float64
	LatencyMS    float64 // average service time per inference (encode+IPC+YOLO)
	Dropped      int64
}

// TickInterval is how often the Manager re-evaluates: it bounds how late
// a MotionGate activation reaches the sampler.
const TickInterval = 100 * time.Millisecond

// CameraStatus is the per-camera /status block.
type CameraStatus struct {
	Mode             string  `json:"inference_mode"`
	ControllerState  string  `json:"controller_state"`
	MotionState      string  `json:"motion_state,omitempty"`
	Priority         string  `json:"priority"`
	MinFPS           float64 `json:"min_fps"`
	RequestedFPS     float64 `json:"requested_fps"`
	BurstFPS         float64 `json:"burst_fps,omitempty"`
	AllocatedFPS     float64 `json:"allocated_fps"`
	EffectiveFPS     float64 `json:"effective_fps"`
	LatencyMS        float64 `json:"inference_latency_ms"`
	Dropped          int64   `json:"dropped_frames"`
	SaturationReason string  `json:"saturation_reason,omitempty"`
}

// DeviceStatus is the Edge-level /status block. Capacity figures say
// where they come from: CapacitySource is "measured_latency" (1000 /
// measured average service time of the serial worker) or "unmeasured".
type DeviceStatus struct {
	CapacityFPS        float64 `json:"total_inference_capacity_fps"`
	CapacitySource     string  `json:"capacity_source"`
	ReserveFraction    float64 `json:"reserve_fraction"`
	AvailableFPS       float64 `json:"available_capacity_fps"`
	AllocatedFPS       float64 `json:"allocated_capacity_fps"`
	MeasuredThroughput float64 `json:"measured_throughput_fps"`
	Utilization        float64 `json:"utilization"` // measured busy fraction of the worker
	HeadroomFPS        float64 `json:"available_headroom_fps"`
	Saturated          bool    `json:"saturated"`
	ActiveCameras      int     `json:"active_cameras"`
	OverloadedCameras  int     `json:"overloaded_cameras"`
	Cameras            int     `json:"cameras"`
}

// Status is the Manager's full snapshot.
type Status struct {
	Device  DeviceStatus            `json:"device"`
	Cameras map[string]CameraStatus `json:"cameras"`
}

// Options configure a Manager.
type Options struct {
	GlobalTargetFPS float64      // FIXED fallback (GEOCAM_VIDEO_TARGET_FPS)
	Default         CameraConfig // policy for cameras without an override
	Reserve         float64      // capacity fraction never allocated (0..0.9)
	Measure         func() map[string]Measured
	OnStatus        func(Status)
	// Active reports whether the Manager may drive the samplers right now
	// (processing mode is edge). nil = always.
	Active func() bool
}

// Manager is the only component that sets Full Edge sampler rates while
// enabled: per tick it advances every camera's Controller, adds ANPR
// burst demand, divides measured worker capacity and applies the result.
type Manager struct {
	video Video
	opt   Options
	now   func() time.Time

	mu        sync.Mutex
	global    float64
	overrides map[string]CameraConfig
	ctrls     map[string]*Controller
	sens      map[string]string
	bursts    map[string]float64
	status    Status
}

// NewManager builds a Manager; Run starts it.
func NewManager(video Video, opt Options) *Manager {
	if opt.Reserve < 0 || opt.Reserve > 0.9 {
		opt.Reserve = 0.15
	}
	return &Manager{
		video:     video,
		opt:       opt,
		now:       time.Now,
		global:    opt.GlobalTargetFPS,
		overrides: map[string]CameraConfig{},
		ctrls:     map[string]*Controller{},
		sens:      map[string]string{},
		bursts:    map[string]float64{},
	}
}

// Run ticks until ctx is done.
func (m *Manager) Run(ctx context.Context) {
	t := time.NewTicker(TickInterval)
	defer t.Stop()
	for {
		m.Tick()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// SetGlobalTargetFPS changes the FIXED fallback (remote global target_fps).
func (m *Manager) SetGlobalTargetFPS(fps float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.global = fps
	for key, c := range m.ctrls {
		c.Configure(m.configForLocked(key))
	}
}

// SetCameraConfig sets (or, with nil, clears) a camera's policy override.
// It is validated first; an invalid policy is rejected and nothing changes.
func (m *Manager) SetCameraConfig(key string, cfg *CameraConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if cfg == nil {
		delete(m.overrides, key)
	} else {
		if err := cfg.WithDefaults(m.global).Validate(); err != nil {
			return err
		}
		m.overrides[key] = *cfg
	}
	if c, ok := m.ctrls[key]; ok {
		c.Configure(m.configForLocked(key))
	}
	return nil
}

// ApplyRemote replaces every remote per-camera policy at once (cameras
// absent from the map lose their override) and, when global is non-nil,
// the FIXED fallback. All policies are validated first: on any error
// nothing changes.
func (m *Manager) ApplyRemote(global *float64, cameras map[string]CameraConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	g := m.global
	if global != nil {
		g = *global
	}
	for key, cfg := range cameras {
		if err := mergeDefault(m.opt.Default, cfg).WithDefaults(g).Validate(); err != nil {
			return fmt.Errorf("camera %q: %w", key, err)
		}
	}
	m.global = g
	m.overrides = make(map[string]CameraConfig, len(cameras))
	for key, cfg := range cameras {
		m.overrides[key] = cfg
	}
	for key, c := range m.ctrls {
		c.Configure(m.configForLocked(key))
	}
	return nil
}

// SetCameraTargetFPS is the remote per-camera target_fps: it becomes the
// camera's FIXED rate (keeping any other override fields).
func (m *Manager) SetCameraTargetFPS(key string, fps float64) error {
	m.mu.Lock()
	cfg := m.overrides[key]
	m.mu.Unlock()
	cfg.TargetFPS = fps
	return m.SetCameraConfig(key, &cfg)
}

func (m *Manager) configForLocked(key string) CameraConfig {
	return mergeDefault(m.opt.Default, m.overrides[key]).WithDefaults(m.global)
}

func mergeDefault(def, o CameraConfig) CameraConfig {
	out := def
	if o.Mode != "" {
		out.Mode = o.Mode
	}
	if o.TargetFPS > 0 {
		out.TargetFPS = o.TargetFPS
	}
	if o.IdleFPS > 0 {
		out.IdleFPS = o.IdleFPS
	}
	if o.ActiveFPS > 0 {
		out.ActiveFPS = o.ActiveFPS
	}
	if o.MaxFPS > 0 {
		out.MaxFPS = o.MaxFPS
	}
	if o.MotionSensitivity != "" {
		out.MotionSensitivity = o.MotionSensitivity
	}
	if o.IdleTimeout > 0 {
		out.IdleTimeout = o.IdleTimeout
	}
	if o.Priority != "" {
		out.Priority = o.Priority
	}
	return out
}

// SetBurstFPS is ANPR's burst demand for a camera (0 releases it). The
// burst raises the camera's demand up to its ceiling; it competes for
// capacity like any other demand and never bypasses the Manager.
func (m *Manager) SetBurstFPS(key string, fps float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if fps <= 0 {
		delete(m.bursts, key)
		return
	}
	m.bursts[key] = fps
}

// CurrentTargetFPS reports the camera's applied sampler rate (ANPR uses it
// to know the camera is live).
func (m *Manager) CurrentTargetFPS(key string) (float64, bool) {
	return m.video.CurrentTargetFPS(key)
}

// Status returns the last snapshot.
func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := m.status
	out.Cameras = make(map[string]CameraStatus, len(m.status.Cameras))
	for k, v := range m.status.Cameras {
		out.Cameras[k] = v
	}
	return out
}

// demand is one camera's input to allocation.
type demand struct {
	key    string
	min    float64
	want   float64
	weight float64
}

// Tick runs one evaluation (exported for tests).
func (m *Manager) Tick() {
	if m.opt.Active != nil && !m.opt.Active() {
		return
	}
	now := m.now()
	keys := m.video.CandidateKeys()
	sort.Strings(keys)
	var measured map[string]Measured
	if m.opt.Measure != nil {
		measured = m.opt.Measure()
	}

	m.mu.Lock()
	seen := make(map[string]bool, len(keys))
	demands := make([]demand, 0, len(keys))
	cams := make(map[string]CameraStatus, len(keys))
	type sensChange struct{ key, s string }
	var sensChanges []sensChange
	for _, key := range keys {
		seen[key] = true
		c, ok := m.ctrls[key]
		if !ok {
			c = NewController(m.configForLocked(key))
			m.ctrls[key] = c
		}
		cfg := c.Config()
		motion := m.video.MotionState(key)
		want := c.Update(now, motion == "active")
		burst := m.bursts[key]
		if burst > want {
			want = math.Min(burst, c.Ceiling())
		}
		if cfg.Mode == ModeAdaptive && m.sens[key] != cfg.MotionSensitivity {
			m.sens[key] = cfg.MotionSensitivity
			sensChanges = append(sensChanges, sensChange{key, cfg.MotionSensitivity})
		}
		demands = append(demands, demand{key: key, min: math.Min(c.Min(), want), want: want, weight: priorityWeight(cfg.Priority)})
		cams[key] = CameraStatus{
			Mode: cfg.Mode, ControllerState: c.State(), MotionState: motion, Priority: cfg.Priority,
			MinFPS: c.Min(), RequestedFPS: want, BurstFPS: burst,
			EffectiveFPS: measured[key].EffectiveFPS, LatencyMS: measured[key].LatencyMS, Dropped: measured[key].Dropped,
		}
	}
	for key := range m.ctrls {
		if !seen[key] {
			delete(m.ctrls, key)
			delete(m.sens, key)
		}
	}
	reserve := m.opt.Reserve
	m.mu.Unlock()

	for _, sc := range sensChanges {
		m.video.SetMotionSensitivity(sc.key, sc.s)
	}

	dev := measureDevice(measured, reserve)
	alloc, reasons := allocate(demands, dev.AvailableFPS, dev.CapacitySource == "measured_latency")
	for _, d := range demands {
		a := alloc[d.key]
		if cur, ok := m.video.CurrentTargetFPS(d.key); ok && math.Abs(cur-a) > 0.01 {
			_ = m.video.SetTargetFPS(d.key, a)
		}
		cs := cams[d.key]
		cs.AllocatedFPS = a
		cs.SaturationReason = reasons[d.key]
		cams[d.key] = cs
		dev.AllocatedFPS += a
		if cs.ControllerState == StateActive || cs.ControllerState == StateCooldown || cs.BurstFPS > 0 {
			dev.ActiveCameras++
		}
		if cs.SaturationReason != "" {
			dev.OverloadedCameras++
		}
	}
	dev.Cameras = len(demands)
	dev.Saturated = dev.OverloadedCameras > 0
	if dev.CapacitySource == "measured_latency" {
		dev.HeadroomFPS = math.Max(0, dev.AvailableFPS-dev.AllocatedFPS)
	}

	st := Status{Device: dev, Cameras: cams}
	m.mu.Lock()
	m.status = st
	m.mu.Unlock()
	if m.opt.OnStatus != nil {
		m.opt.OnStatus(st)
	}
}
