// Package inference decides each Full Edge camera's inference rate: a
// per-camera controller (FIXED or ADAPTIVE on MotionGate activity) states
// what the camera wants, and the Manager turns those wants into sampler
// rates. Edge mode only; cloud and hybrid never use it.
package inference

import (
	"fmt"
	"math"
	"time"
)

// Modes.
const (
	ModeFixed    = "fixed"
	ModeAdaptive = "adaptive"
)

// Priorities and their allocation weights.
const (
	PriorityLow    = "low"
	PriorityNormal = "normal"
	PriorityHigh   = "high"
)

func priorityWeight(p string) float64 {
	switch p {
	case PriorityLow:
		return 1
	case PriorityHigh:
		return 4
	default:
		return 2
	}
}

// Defaults for ADAPTIVE (initial values, subject to calibration).
const (
	DefaultIdleFPS     = 2.0
	DefaultActiveFPS   = 10.0
	DefaultMaxFPS      = 15.0
	DefaultIdleTimeout = 10 * time.Second
	// MinFPS/MaxFPS bound every rate, same range as target_fps.
	MinFPS = 0.1
	MaxFPS = 30.0
	// rampStep is how often the rate steps down after IdleTimeout, and
	// rampFraction how much of (active - idle) each step removes: back to
	// idle in 4 s.
	rampStep     = time.Second
	rampFraction = 0.25
)

// CameraConfig is one camera's inference policy. Zero values take the
// defaults; Mode "" means FIXED at the camera's target_fps.
type CameraConfig struct {
	Mode              string        `json:"mode,omitempty"`
	TargetFPS         float64       `json:"target_fps,omitempty"` // FIXED rate; 0 = global target_fps
	IdleFPS           float64       `json:"idle_fps,omitempty"`
	ActiveFPS         float64       `json:"active_fps,omitempty"`
	MaxFPS            float64       `json:"max_fps,omitempty"`
	MotionSensitivity string        `json:"motion_sensitivity,omitempty"`
	IdleTimeout       time.Duration `json:"-"`
	Priority          string        `json:"priority,omitempty"`
}

// WithDefaults fills unset fields; globalTarget is the FIXED fallback.
func (c CameraConfig) WithDefaults(globalTarget float64) CameraConfig {
	if c.Mode == "" {
		c.Mode = ModeFixed
	}
	if c.TargetFPS <= 0 {
		c.TargetFPS = globalTarget
	}
	if c.IdleFPS <= 0 {
		c.IdleFPS = DefaultIdleFPS
	}
	if c.ActiveFPS <= 0 {
		c.ActiveFPS = DefaultActiveFPS
	}
	if c.MaxFPS <= 0 {
		c.MaxFPS = math.Max(DefaultMaxFPS, c.ActiveFPS)
	}
	if c.MotionSensitivity == "" {
		c.MotionSensitivity = "medium"
	}
	if c.IdleTimeout <= 0 {
		c.IdleTimeout = DefaultIdleTimeout
	}
	if c.Priority == "" {
		c.Priority = PriorityNormal
	}
	return c
}

// Validate enforces the contract strictly (after WithDefaults).
func (c CameraConfig) Validate() error {
	switch c.Mode {
	case ModeFixed, ModeAdaptive:
	default:
		return fmt.Errorf("inference: mode %q must be fixed or adaptive", c.Mode)
	}
	for name, v := range map[string]float64{"target_fps": c.TargetFPS, "idle_fps": c.IdleFPS, "active_fps": c.ActiveFPS, "max_fps": c.MaxFPS} {
		if v < MinFPS || v > MaxFPS || math.IsNaN(v) {
			return fmt.Errorf("inference: %s %.2f out of range [%.1f, %.0f]", name, v, MinFPS, MaxFPS)
		}
	}
	if c.Mode == ModeAdaptive && !(c.IdleFPS <= c.ActiveFPS && c.ActiveFPS <= c.MaxFPS) {
		return fmt.Errorf("inference: need idle_fps <= active_fps <= max_fps, got %.1f/%.1f/%.1f", c.IdleFPS, c.ActiveFPS, c.MaxFPS)
	}
	switch c.MotionSensitivity {
	case "low", "medium", "high":
	default:
		return fmt.Errorf("inference: motion_sensitivity %q must be low, medium or high", c.MotionSensitivity)
	}
	if c.IdleTimeout < time.Second || c.IdleTimeout > 10*time.Minute {
		return fmt.Errorf("inference: idle_timeout %s out of range [1s, 10m]", c.IdleTimeout)
	}
	switch c.Priority {
	case PriorityLow, PriorityNormal, PriorityHigh:
	default:
		return fmt.Errorf("inference: priority %q must be low, normal or high", c.Priority)
	}
	return nil
}

// Controller states.
const (
	StateFixed    = "fixed"
	StateIdle     = "idle"
	StateActive   = "active"
	StateCooldown = "cooldown" // stepping back down to idle
)

// Controller is one camera's policy state machine. Not safe for concurrent
// use: the Manager owns it.
type Controller struct {
	cfg        CameraConfig
	state      string
	lastMotion time.Time
	rampAt     time.Time
	want       float64
}

// NewController starts in FIXED, or ADAPTIVE idle.
func NewController(cfg CameraConfig) *Controller {
	c := &Controller{}
	c.Configure(cfg)
	return c
}

// Configure applies a new policy, keeping motion history.
func (c *Controller) Configure(cfg CameraConfig) {
	c.cfg = cfg
	if cfg.Mode == ModeFixed {
		c.state, c.want = StateFixed, cfg.TargetFPS
		return
	}
	if c.state == StateFixed || c.state == "" {
		c.state, c.want = StateIdle, cfg.IdleFPS
	}
	c.want = math.Min(math.Max(c.want, cfg.IdleFPS), cfg.MaxFPS)
}

// Config returns the policy in force.
func (c *Controller) Config() CameraConfig { return c.cfg }

// State returns the controller state.
func (c *Controller) State() string { return c.state }

// Update advances the state machine at now with the camera's current
// motion (MotionGate active) and returns the wanted rate. Activation is
// immediate; after the last motion the rate holds for IdleTimeout, then
// steps down to IdleFPS. ADAPTIVE never wants less than IdleFPS: YOLO
// never stops.
func (c *Controller) Update(now time.Time, motion bool) float64 {
	if c.cfg.Mode == ModeFixed {
		c.state, c.want = StateFixed, c.cfg.TargetFPS
		return c.want
	}
	if motion {
		c.lastMotion = now
		c.state, c.want = StateActive, c.cfg.ActiveFPS
		return c.want
	}
	switch c.state {
	case StateActive:
		if now.Sub(c.lastMotion) >= c.cfg.IdleTimeout {
			c.state, c.rampAt = StateCooldown, now
			c.stepDown()
		}
	case StateCooldown:
		if now.Sub(c.rampAt) >= rampStep {
			c.rampAt = now
			c.stepDown()
		}
	}
	return c.want
}

func (c *Controller) stepDown() {
	c.want -= rampFraction * (c.cfg.ActiveFPS - c.cfg.IdleFPS)
	if c.want <= c.cfg.IdleFPS+1e-9 {
		c.want, c.state = c.cfg.IdleFPS, StateIdle
	}
}

// Min is the rate this camera must keep (FIXED: its target; ADAPTIVE:
// IdleFPS). The Manager guarantees mins first.
func (c *Controller) Min() float64 {
	if c.cfg.Mode == ModeFixed {
		return c.cfg.TargetFPS
	}
	return c.cfg.IdleFPS
}

// Ceiling caps demand including bursts (ADAPTIVE: MaxFPS; FIXED: MaxFPS of
// the contract, 30).
func (c *Controller) Ceiling() float64 {
	if c.cfg.Mode == ModeFixed {
		return MaxFPS
	}
	return c.cfg.MaxFPS
}
