package processing

import (
	"context"
	"math"
	"sort"
	"sync"
	"time"
)

// MotionGate grid: fixed cell count, independent of the decoded resolution,
// so cost and sensitivity do not change with the camera's stream size.
const (
	motionGateCellsX = 32
	motionGateCellsY = 18
	// motionGateStride samples every Nth pixel/row inside a cell: a cell's
	// mean luma is just as stable from a quarter of its pixels.
	motionGateStride = 2
)

// MotionGateConfig tunes the passive activity detector (Full Edge stage 1).
// Zero values take the defaults documented on each field.
type MotionGateConfig struct {
	// Sensitivity multiplies each cell's noise floor into its change
	// threshold: "high" 3, "medium" 4 (default), "low" 6.
	Sensitivity string
	// MinDelta is the absolute luma change (0..255) below which a cell
	// never counts as changed, however quiet its noise floor. Default 4.
	MinDelta float64
	// MinCells is how many changed cells make a frame a motion candidate.
	// Default 2 (of 576).
	MinCells int
	// Persist is how many consecutive candidate frames activate. Default 2.
	Persist int
	// Hold keeps the state active this long after the last candidate
	// (hysteresis). Default 1 s.
	Hold time.Duration
	// LightingShift is the median per-cell luma shift (0..255) treated as a
	// global illumination change and compensated, not reported as motion.
	// Default 8.
	LightingShift float64
	// GlobalChangeFraction: when more than this fraction of cells changes
	// at once (IR switch, camera moved, non-uniform lighting), the
	// background is re-seeded and the frame is not a candidate. Default 0.6.
	GlobalChangeFraction float64
	// WarmupFrames seed the background before evaluating. Default 10.
	WarmupFrames int
	// StillAbsorb: a cell that differs from the background but has not
	// changed frame-to-frame for this long is presence, not motion (a
	// parked car, a seated person, or the ghost an object leaves behind):
	// it is absorbed into the background. YOLO keeps covering presence at
	// its own rate. Default 1 s.
	StillAbsorb time.Duration
	// NewAppearanceGap: a detection with no other detection on the camera
	// for this long counts as a new appearance (correlation metric).
	// Default 5 s.
	NewAppearanceGap time.Duration
	// ROIs restricts evaluation to these normalized regions (cell centers).
	ROIs []ROI
}

func (c MotionGateConfig) withDefaults() MotionGateConfig {
	if c.MinDelta <= 0 {
		c.MinDelta = 4
	}
	if c.MinCells <= 0 {
		c.MinCells = 2
	}
	if c.Persist <= 0 {
		c.Persist = 2
	}
	if c.Hold <= 0 {
		c.Hold = time.Second
	}
	if c.LightingShift <= 0 {
		c.LightingShift = 8
	}
	if c.GlobalChangeFraction <= 0 {
		c.GlobalChangeFraction = 0.6
	}
	if c.WarmupFrames <= 0 {
		c.WarmupFrames = 10
	}
	if c.StillAbsorb <= 0 {
		c.StillAbsorb = time.Second
	}
	if c.NewAppearanceGap <= 0 {
		c.NewAppearanceGap = 5 * time.Second
	}
	return c
}

func (c MotionGateConfig) noiseFactor() float64 {
	switch c.Sensitivity {
	case "high":
		return 3
	case "low":
		return 6
	default:
		return 4
	}
}

// Motion gate states.
const (
	MotionStateWarming = "warming"
	MotionStateIdle    = "idle"
	MotionStateActive  = "active"
)

// MotionGateStatus is the per-camera /status block. Every figure is
// measured on the decoded stream; nothing is estimated.
type MotionGateStatus struct {
	State       string  `json:"motion_state"`
	Score       float64 `json:"motion_score"` // changed fraction of evaluated cells, last frame
	ActiveCells int     `json:"active_cells"`

	FramesEvaluated int64   `json:"frames_evaluated"`
	FramesSkipped   int64   `json:"frames_skipped"` // gate busy: frame not evaluated, capture never waited
	EvaluatedFPS    float64 `json:"evaluated_fps"`
	AvgCostUS       float64 `json:"avg_cost_us"`
	MaxCostUS       float64 `json:"max_cost_us"`
	NoiseFloorAvg   float64 `json:"noise_floor_avg"`
	LightingShift   float64 `json:"last_lighting_shift"`

	Activations             int64   `json:"activations"`
	AbsorbedCells           int64   `json:"absorbed_cells"` // still changed cells taken into the background
	LightingCompensations   int64   `json:"lighting_compensations"`
	GlobalChanges           int64   `json:"global_changes"`
	LastActivationAt        string  `json:"last_activation_at,omitempty"`
	LastActivationLatencyMS float64 `json:"last_activation_latency_ms"` // first candidate frame -> activation
	MaxActivationLatencyMS  float64 `json:"max_activation_latency_ms"`
	ActiveSecondsTotal      float64 `json:"active_seconds_total"`

	// Correlation with local YOLO (observation only: never gates anything).
	DetectionsObserved       int64   `json:"detections_observed"`
	DetectionsWhileActive    int64   `json:"detections_while_active"`
	NewAppearances           int64   `json:"new_appearances"`
	NewAppearancesWithMotion int64   `json:"new_appearances_with_motion"`
	LastAppearanceLeadMS     float64 `json:"last_appearance_lead_ms"` // detection - activation; > 0 = gate first
	EpisodesWithDetection    int64   `json:"episodes_with_detection"`
	EpisodesWithoutDetection int64   `json:"episodes_without_detection"` // false-activation proxy
}

type motionEpisode struct {
	start, end time.Time // end zero while open
	detected   bool
}

// MotionGate is a passive, per-camera activity detector fed every decoded
// frame ahead of the inference sampler. Stage 1 is observation only: it
// never touches the sampler or any FPS; it only reports.
//
// Each frame is reduced to a 32x18 grid of mean luma (Y plane, already
// decoded) and compared with an EMA background. Per cell, an EMA noise
// floor sets the change threshold (handles night/IR grain and swaying
// foliage); the median shift of all cells is removed first, so a uniform
// lighting change is compensated instead of reported as motion.
type MotionGate struct {
	cfg   MotionGateConfig
	inbox chan DecodedFrame
	now   func() time.Time

	mu       sync.Mutex
	roiMask  []bool
	bg       []float64
	noise    []float64
	prev     []float64   // previous frame's cell means
	still    []time.Time // since when a changed cell stopped changing; zero = moving
	warm     int
	state    string
	streak   int
	streakAt time.Time
	lastCand time.Time
	episodes []motionEpisode // newest last, bounded
	lastDet  time.Time
	st       MotionGateStatus
	rate     windowRate
	costSum  float64
}

// NewMotionGate creates a gate. Run must be started for Offer'd frames to
// be evaluated.
func NewMotionGate(cfg MotionGateConfig) *MotionGate {
	g := &MotionGate{
		cfg:   cfg.withDefaults(),
		inbox: make(chan DecodedFrame, 1),
		now:   time.Now,
		state: MotionStateWarming,
	}
	g.roiMask = gateROIMask(g.cfg.ROIs)
	return g
}

// Offer hands a decoded frame to the gate without ever blocking: if the
// previous frame is still being evaluated the frame is skipped and counted.
func (g *MotionGate) Offer(f DecodedFrame) {
	select {
	case g.inbox <- f:
	default:
		g.mu.Lock()
		g.st.FramesSkipped++
		g.mu.Unlock()
	}
}

// Run evaluates offered frames until ctx is done.
func (g *MotionGate) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case f := <-g.inbox:
			g.Evaluate(f)
		}
	}
}

// SetROIs replaces the evaluated region; the background is kept.
func (g *MotionGate) SetROIs(rois []ROI) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.cfg.ROIs = append([]ROI(nil), rois...)
	g.roiMask = gateROIMask(rois)
}

func gateROIMask(rois []ROI) []bool {
	if len(rois) == 0 {
		return nil
	}
	mask := make([]bool, motionGateCellsX*motionGateCellsY)
	for cy := 0; cy < motionGateCellsY; cy++ {
		for cx := 0; cx < motionGateCellsX; cx++ {
			nx := (float64(cx) + 0.5) / motionGateCellsX
			ny := (float64(cy) + 0.5) / motionGateCellsY
			for _, r := range rois {
				if nx >= r.XMin && nx <= r.XMax && ny >= r.YMin && ny <= r.YMax {
					mask[cy*motionGateCellsX+cx] = true
					break
				}
			}
		}
	}
	return mask
}

// cellMeans reduces the Y plane to the grid's mean luma per cell.
func cellMeans(y []byte, w, h int, out []float64) bool {
	if w < motionGateCellsX || h < motionGateCellsY || len(y) < w*h {
		return false
	}
	for cy := 0; cy < motionGateCellsY; cy++ {
		y0, y1 := cy*h/motionGateCellsY, (cy+1)*h/motionGateCellsY
		for cx := 0; cx < motionGateCellsX; cx++ {
			x0, x1 := cx*w/motionGateCellsX, (cx+1)*w/motionGateCellsX
			sum, n := 0, 0
			for yy := y0; yy < y1; yy += motionGateStride {
				row := y[yy*w : yy*w+w]
				for xx := x0; xx < x1; xx += motionGateStride {
					sum += int(row[xx])
					n++
				}
			}
			out[cy*motionGateCellsX+cx] = float64(sum) / float64(n)
		}
	}
	return true
}

// Evaluate runs the detector on one decoded frame (synchronously; Run
// calls it off the capture path). Exported for deterministic tests.
func (g *MotionGate) Evaluate(f DecodedFrame) {
	started := time.Now()
	n := motionGateCellsX * motionGateCellsY
	means := make([]float64, n)
	ok := cellMeans(f.Data, f.Width, f.Height, means)

	g.mu.Lock()
	defer g.mu.Unlock()
	if !ok {
		return
	}
	t := f.DecodedAt
	g.st.FramesEvaluated++
	g.rate.add(t)

	if g.bg == nil || g.warm < g.cfg.WarmupFrames {
		g.seedLocked(means)
		g.warm++
		g.recordCostLocked(started)
		return
	}
	if g.state == MotionStateWarming {
		g.state = MotionStateIdle
	}

	// Global illumination: the median cell shift moves the whole scene;
	// a moving object only moves a minority of cells.
	diffs := make([]float64, 0, n)
	for i := 0; i < n; i++ {
		if g.roiMask == nil || g.roiMask[i] {
			diffs = append(diffs, means[i]-g.bg[i])
		}
	}
	if len(diffs) == 0 {
		g.recordCostLocked(started)
		return
	}
	shift := median(diffs)
	g.st.LightingShift = shift
	if math.Abs(shift) >= g.cfg.LightingShift {
		g.st.LightingCompensations++
	}

	// Frame-to-frame shift, to tell a still cell from a moving one under a
	// lighting ramp.
	for i, j := 0, 0; i < n; i++ {
		if g.roiMask == nil || g.roiMask[i] {
			diffs[j] = means[i] - g.prev[i]
			j++
		}
	}
	frameShift := median(diffs)

	k := g.cfg.noiseFactor()
	evaluated := 0
	changed := make([]bool, n)
	for i := 0; i < n; i++ {
		if g.roiMask != nil && !g.roiMask[i] {
			continue
		}
		evaluated++
		thr := math.Max(k*g.noise[i], g.cfg.MinDelta)
		if math.Abs(means[i]-g.bg[i]-shift) <= thr {
			g.still[i] = time.Time{}
			continue
		}
		if math.Abs(means[i]-g.prev[i]-frameShift) > thr {
			g.still[i] = time.Time{} // moving
		} else if g.still[i].IsZero() {
			g.still[i] = t
		} else if t.Sub(g.still[i]) >= g.cfg.StillAbsorb {
			g.bg[i] = means[i] - shift // absorbed below (bg += shift)
			g.still[i] = time.Time{}
			g.st.AbsorbedCells++
			continue
		}
		changed[i] = true
	}
	// Spatial coherence: an object spans neighbouring cells; sensor or
	// codec noise lights up isolated ones.
	active := 0
	clustered := make([]bool, n)
	for i := 0; i < n; i++ {
		if !changed[i] {
			continue
		}
		cx, cy := i%motionGateCellsX, i/motionGateCellsX
		if (cx > 0 && changed[i-1]) || (cx < motionGateCellsX-1 && changed[i+1]) ||
			(cy > 0 && changed[i-motionGateCellsX]) || (cy < motionGateCellsY-1 && changed[i+motionGateCellsX]) {
			clustered[i] = true
			active++
		}
	}

	frac := float64(active) / float64(evaluated)
	if frac > g.cfg.GlobalChangeFraction {
		// IR switch, camera moved, non-uniform lighting: not an object.
		g.st.GlobalChanges++
		g.seedLocked(means)
		for i := range g.still {
			g.still[i] = time.Time{}
		}
		g.st.Score, g.st.ActiveCells = 0, 0
		g.advanceLocked(t, false)
		g.recordCostLocked(started)
		return
	}

	// Learn: quiet cells track the background and their noise quickly;
	// changed cells adapt slowly, so a parked object is absorbed in
	// seconds-to-tens-of-seconds instead of reading as motion forever.
	var noiseSum float64
	for i := 0; i < n; i++ {
		d := means[i] - g.bg[i] - shift
		g.bg[i] += shift
		if clustered[i] {
			g.bg[i] += 0.01 * d
		} else {
			g.bg[i] += 0.05 * d
			g.noise[i] += 0.05 * (math.Abs(d) - g.noise[i])
			if g.noise[i] < 0.5 {
				g.noise[i] = 0.5
			}
		}
		noiseSum += g.noise[i]
	}
	g.st.NoiseFloorAvg = noiseSum / float64(n)
	copy(g.prev, means)
	g.st.Score, g.st.ActiveCells = frac, active
	g.advanceLocked(t, active >= g.cfg.MinCells)
	g.recordCostLocked(started)
}

func (g *MotionGate) seedLocked(means []float64) {
	n := len(means)
	if g.bg == nil {
		g.bg = make([]float64, n)
		g.noise = make([]float64, n)
		g.prev = make([]float64, n)
		g.still = make([]time.Time, n)
		for i := range g.noise {
			g.noise[i] = 1
		}
	}
	copy(g.bg, means)
	copy(g.prev, means)
}

// advanceLocked runs the observation state machine for a frame at t.
func (g *MotionGate) advanceLocked(t time.Time, candidate bool) {
	if candidate {
		if g.streak == 0 {
			g.streakAt = t
		}
		g.streak++
		g.lastCand = t
	} else {
		g.streak = 0
	}
	switch g.state {
	case MotionStateIdle:
		if g.streak >= g.cfg.Persist {
			g.state = MotionStateActive
			g.st.Activations++
			g.st.LastActivationAt = t.UTC().Format(time.RFC3339Nano)
			lat := float64(t.Sub(g.streakAt)) / float64(time.Millisecond)
			g.st.LastActivationLatencyMS = lat
			if lat > g.st.MaxActivationLatencyMS {
				g.st.MaxActivationLatencyMS = lat
			}
			g.episodes = append(g.episodes, motionEpisode{start: t})
			if len(g.episodes) > 64 {
				g.closeOldestLocked()
			}
		}
	case MotionStateActive:
		if !candidate && t.Sub(g.lastCand) >= g.cfg.Hold {
			g.state = MotionStateIdle
			ep := &g.episodes[len(g.episodes)-1]
			ep.end = t
			g.st.ActiveSecondsTotal += t.Sub(ep.start).Seconds()
			if ep.detected {
				g.st.EpisodesWithDetection++
			} else {
				g.st.EpisodesWithoutDetection++
			}
		}
	}
}

// closeOldestLocked drops the oldest episode from the correlation window.
func (g *MotionGate) closeOldestLocked() { g.episodes = g.episodes[1:] }

func (g *MotionGate) recordCostLocked(started time.Time) {
	us := float64(time.Since(started)) / float64(time.Microsecond)
	g.costSum += us
	g.st.AvgCostUS = g.costSum / float64(g.st.FramesEvaluated)
	if us > g.st.MaxCostUS {
		g.st.MaxCostUS = us
	}
}

// ObserveDetection records that local YOLO detected an object on a frame
// captured at t (correlation only; the gate never influences inference).
func (g *MotionGate) ObserveDetection(t time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.st.DetectionsObserved++
	ep := g.episodeAtLocked(t)
	if ep != nil {
		g.st.DetectionsWhileActive++
		ep.detected = true
	}
	if g.lastDet.IsZero() || t.Sub(g.lastDet) >= g.cfg.NewAppearanceGap {
		g.st.NewAppearances++
		if ep != nil {
			g.st.NewAppearancesWithMotion++
			g.st.LastAppearanceLeadMS = float64(t.Sub(ep.start)) / float64(time.Millisecond)
		}
	}
	if t.After(g.lastDet) {
		g.lastDet = t
	}
}

// episodeAtLocked returns the episode active at t, allowing the frame to
// precede activation by up to the gate's own persistence delay (YOLO may
// sample the very frame that started the motion).
func (g *MotionGate) episodeAtLocked(t time.Time) *motionEpisode {
	const slack = 500 * time.Millisecond
	for i := len(g.episodes) - 1; i >= 0; i-- {
		ep := &g.episodes[i]
		if t.Before(ep.start.Add(-slack)) {
			continue
		}
		if ep.end.IsZero() || !t.After(ep.end) {
			return ep
		}
		return nil
	}
	return nil
}

// Status returns a snapshot.
func (g *MotionGate) Status() MotionGateStatus {
	g.mu.Lock()
	defer g.mu.Unlock()
	st := g.st
	st.State = g.state
	st.EvaluatedFPS = g.rate.fps(g.now())
	return st
}

func median(v []float64) float64 {
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	m := len(s) / 2
	if len(s)%2 == 1 {
		return s[m]
	}
	return (s[m-1] + s[m]) / 2
}
