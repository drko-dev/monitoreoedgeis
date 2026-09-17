package processing

import "time"

// ROI is a normalized (0..1) region of interest, relative to frame width
// and height, so a configured ROI never needs adjusting when
// GEOCAM_VIDEO_OUTPUT_WIDTH/HEIGHT changes. XMin < XMax and YMin < YMax;
// all four values within [0,1] -- enforced at config-parse time
// (internal/config), never at runtime here.
type ROI struct {
	XMin, YMin, XMax, YMax float64
}

// MotionResult is Milestone J2's structured motion-evaluator output for one
// frame. Never carries frame bytes.
type MotionResult struct {
	// Score and ChangedArea are both the fraction (0..1) of evaluated
	// blocks whose average luma changed beyond MotionThreshold. Kept as
	// two fields (rather than one) so a future refinement (e.g.
	// magnitude-weighted scoring) can diverge Score from ChangedArea
	// without an API break.
	Score       float64
	ChangedArea float64
	Candidate   bool
	Timestamp   time.Time
	Seq         uint64
}

// MotionDetector performs bounded-memory, block-based luminance-diff
// motion detection on the Y (luma) plane of a yuv420p frame. Pure Go, no
// OpenCV, no model (that is Hito K's local YOLO, out of scope here): it
// compares each block's average luma in the current frame against the SAME
// block's average luma in the immediately preceding frame only -- no
// history beyond that single previous frame is ever retained, per Hito J's
// bounded-memory rule.
//
// Block-based (rather than pixel-by-pixel) diffing trades a small amount
// of spatial precision for resilience to per-pixel sensor/codec noise,
// which would otherwise make a static camera "see" motion in every frame.
//
// Not safe for concurrent use: a MotionDetector is owned solely by one
// cameraPipeline.readLoop goroutine, the same goroutine that decodes and
// resizes every frame for that camera.
type MotionDetector struct {
	cfg HybridConfig

	// prevAvg holds exactly one frame's worth of per-block average luma --
	// never a history. nil until the first frame (or after a resolution
	// change resets it).
	prevAvg      []float64
	prevW, prevH int
	blocksX      int
	// roiMask marks, per block index (row-major, blocksX*blocksY), whether
	// that block's center falls inside a configured ROI. nil means "no ROI
	// configured -- analyze every block" (the default).
	roiMask []bool
}

// NewMotionDetector creates a MotionDetector for the given Hybrid tunables.
func NewMotionDetector(cfg HybridConfig) *MotionDetector {
	return &MotionDetector{cfg: cfg}
}

// Evaluate analyzes the Y (luma) plane of a decoded/resized yuv420p frame
// and reports whether it contains motion. y must be at least width*height
// bytes long -- the Y plane always comes first in yuv420p, so callers pass
// frame.Data[:width*height] (or the full buffer; only the prefix is read).
func (m *MotionDetector) Evaluate(y []byte, width, height int, seq uint64, ts time.Time) MotionResult {
	result := MotionResult{Timestamp: ts, Seq: seq}

	block := m.cfg.BlockSize
	if block <= 0 {
		block = 16
	}
	if width <= 0 || height <= 0 || len(y) < width*height {
		// Can't analyze this frame; report "no motion" rather than guess,
		// so a transient bad frame never spuriously triggers a candidate.
		return result
	}

	blocksX := (width + block - 1) / block
	blocksY := (height + block - 1) / block

	if width != m.prevW || height != m.prevH {
		// Resolution changed (or this is the first frame): reset state.
		// Still bounded to exactly one frame's worth of averages.
		m.prevAvg = nil
		m.prevW, m.prevH = width, height
		m.blocksX = blocksX
		m.roiMask = computeROIMask(m.cfg.ROIs, blocksX, blocksY, block, width, height)
	}

	curAvg := make([]float64, blocksX*blocksY)
	for by := 0; by < blocksY; by++ {
		for bx := 0; bx < blocksX; bx++ {
			curAvg[by*blocksX+bx] = blockAvgLuma(y, width, height, bx*block, by*block, block)
		}
	}

	if m.prevAvg != nil {
		var changed, evaluated int
		for i, avg := range curAvg {
			if len(m.roiMask) > 0 && !m.roiMask[i] {
				continue // outside every configured ROI
			}
			evaluated++
			diff := avg - m.prevAvg[i]
			if diff < 0 {
				diff = -diff
			}
			if diff > m.cfg.MotionThreshold {
				changed++
			}
		}
		if evaluated > 0 {
			result.Score = float64(changed) / float64(evaluated)
			result.ChangedArea = result.Score
		}
		result.Candidate = result.ChangedArea >= m.cfg.MinChangedArea
	}

	m.prevAvg = curAvg
	return result
}

// blockAvgLuma averages the Y-plane samples in the block starting at
// (x0,y0), clipped to the frame's actual width/height (blocks at the
// right/bottom edge of a non-multiple-of-block-size frame are partial).
func blockAvgLuma(y []byte, width, height, x0, y0, block int) float64 {
	x1 := min(x0+block, width)
	y1 := min(y0+block, height)
	var sum, count int
	for yy := y0; yy < y1; yy++ {
		row := yy * width
		for xx := x0; xx < x1; xx++ {
			sum += int(y[row+xx])
			count++
		}
	}
	if count == 0 {
		return 0
	}
	return float64(sum) / float64(count)
}

// computeROIMask returns, per block (row-major), whether its center falls
// inside at least one configured ROI. An empty rois slice (no ROI
// configured) returns nil, which Evaluate treats as "every block counts".
func computeROIMask(rois []ROI, blocksX, blocksY, block, width, height int) []bool {
	if len(rois) == 0 {
		return nil
	}
	mask := make([]bool, blocksX*blocksY)
	for by := 0; by < blocksY; by++ {
		for bx := 0; bx < blocksX; bx++ {
			cx := (float64(bx*block) + float64(block)/2) / float64(width)
			cy := (float64(by*block) + float64(block)/2) / float64(height)
			for _, r := range rois {
				if cx >= r.XMin && cx <= r.XMax && cy >= r.YMin && cy <= r.YMax {
					mask[by*blocksX+bx] = true
					break
				}
			}
		}
	}
	return mask
}
