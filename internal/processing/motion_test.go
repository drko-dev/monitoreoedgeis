package processing

import (
	"testing"
	"time"
)

// makeYPlane returns an 8x8 Y plane filled with `base`, with the 4x4 block
// at (bx,by) (block coordinates, blockSize=4) set to `val` instead.
func makeYPlane(base, val byte, bx, by int) []byte {
	const w, h = 8, 8
	y := make([]byte, w*h)
	for i := range y {
		y[i] = base
	}
	if bx >= 0 {
		x0, y0 := bx*4, by*4
		for yy := y0; yy < y0+4; yy++ {
			for xx := x0; xx < x0+4; xx++ {
				y[yy*w+xx] = val
			}
		}
	}
	return y
}

func baseHybridConfig() HybridConfig {
	return HybridConfig{
		Enabled:         true,
		MotionThreshold: 10,
		MinChangedArea:  0.2,
		BlockSize:       4,
	}
}

// TestMotionDetector_StableFrameNoCandidate: identical consecutive frames
// must never be flagged as motion (J2/J8 acceptance).
func TestMotionDetector_StableFrameNoCandidate(t *testing.T) {
	m := NewMotionDetector(baseHybridConfig())
	frame := makeYPlane(100, 0, -1, -1)

	m.Evaluate(frame, 8, 8, 1, time.Now()) // warm-up: no previous frame yet
	res := m.Evaluate(frame, 8, 8, 2, time.Now())

	if res.Candidate {
		t.Fatalf("stable frame flagged as motion candidate: %+v", res)
	}
	if res.ChangedArea != 0 {
		t.Fatalf("ChangedArea = %v, want 0 for an identical frame", res.ChangedArea)
	}
}

// TestMotionDetector_SufficientMotionIsCandidate: a large enough luma
// change in one block over the configured threshold, covering more than
// MinChangedArea of the blocks, must be flagged (J2/J8 acceptance).
func TestMotionDetector_SufficientMotionIsCandidate(t *testing.T) {
	m := NewMotionDetector(baseHybridConfig())
	base := makeYPlane(100, 0, -1, -1)
	moved := makeYPlane(100, 220, 0, 0) // one of four blocks changes by 120

	m.Evaluate(base, 8, 8, 1, time.Now())
	res := m.Evaluate(moved, 8, 8, 2, time.Now())

	if !res.Candidate {
		t.Fatalf("expected motion candidate, got %+v", res)
	}
	if res.ChangedArea < 0.2 {
		t.Fatalf("ChangedArea = %v, want >= MinChangedArea (0.2)", res.ChangedArea)
	}
}

// TestMotionDetector_ThresholdControlsDecision verifies that raising
// MotionThreshold above the actual diff flips a would-be candidate frame
// back to non-candidate (J8 acceptance: "threshold: variar el threshold y
// verificar el cambio de decisión").
func TestMotionDetector_ThresholdControlsDecision(t *testing.T) {
	base := makeYPlane(100, 0, -1, -1)
	moved := makeYPlane(100, 130, 0, 0) // diff of 30 in one block

	lowThreshold := baseHybridConfig()
	lowThreshold.MotionThreshold = 10
	mLow := NewMotionDetector(lowThreshold)
	mLow.Evaluate(base, 8, 8, 1, time.Now())
	resLow := mLow.Evaluate(moved, 8, 8, 2, time.Now())
	if !resLow.Candidate {
		t.Fatalf("with low threshold (10), expected candidate, got %+v", resLow)
	}

	highThreshold := baseHybridConfig()
	highThreshold.MotionThreshold = 100
	mHigh := NewMotionDetector(highThreshold)
	mHigh.Evaluate(base, 8, 8, 1, time.Now())
	resHigh := mHigh.Evaluate(moved, 8, 8, 2, time.Now())
	if resHigh.Candidate {
		t.Fatalf("with high threshold (100), expected no candidate, got %+v", resHigh)
	}
}

// TestMotionDetector_ROIExcludesMotionOutsideZone: a frame whose only
// change is outside every configured ROI must not be a candidate (J4/J8
// acceptance).
func TestMotionDetector_ROIExcludesMotionOutsideZone(t *testing.T) {
	cfg := baseHybridConfig()
	// ROI covers only the bottom-right block (block (1,1) of a 2x2 grid).
	cfg.ROIs = []ROI{{XMin: 0.5, YMin: 0.5, XMax: 1.0, YMax: 1.0}}
	m := NewMotionDetector(cfg)

	base := makeYPlane(100, 0, -1, -1)
	movedOutsideROI := makeYPlane(100, 220, 0, 0) // top-left block, outside ROI

	m.Evaluate(base, 8, 8, 1, time.Now())
	res := m.Evaluate(movedOutsideROI, 8, 8, 2, time.Now())

	if res.Candidate {
		t.Fatalf("motion outside the configured ROI was flagged as candidate: %+v", res)
	}
}

// TestMotionDetector_ROIIncludesMotionInsideZone: a frame whose change
// falls inside the configured ROI must be flagged (J4/J8 acceptance).
func TestMotionDetector_ROIIncludesMotionInsideZone(t *testing.T) {
	cfg := baseHybridConfig()
	cfg.ROIs = []ROI{{XMin: 0.5, YMin: 0.5, XMax: 1.0, YMax: 1.0}}
	// MinChangedArea is a fraction of the blocks *evaluated* (inside ROI),
	// so with only 1 block in the ROI, any single changed block clears it.
	m := NewMotionDetector(cfg)

	base := makeYPlane(100, 0, -1, -1)
	movedInsideROI := makeYPlane(100, 220, 1, 1) // bottom-right block, inside ROI

	m.Evaluate(base, 8, 8, 1, time.Now())
	res := m.Evaluate(movedInsideROI, 8, 8, 2, time.Now())

	if !res.Candidate {
		t.Fatalf("motion inside the configured ROI was not flagged as candidate: %+v", res)
	}
}

// TestMotionDetector_NoROIAnalyzesWholeFrame: with no ROI configured, the
// default is to analyze the entire frame (J4 acceptance).
func TestMotionDetector_NoROIAnalyzesWholeFrame(t *testing.T) {
	cfg := baseHybridConfig()
	cfg.ROIs = nil
	m := NewMotionDetector(cfg)

	base := makeYPlane(100, 0, -1, -1)
	moved := makeYPlane(100, 220, 1, 1) // any block, no ROI restriction

	m.Evaluate(base, 8, 8, 1, time.Now())
	res := m.Evaluate(moved, 8, 8, 2, time.Now())

	if !res.Candidate {
		t.Fatalf("expected candidate with no ROI configured (whole frame analyzed): %+v", res)
	}
}

// TestMotionDetector_ResolutionChangeResetsBoundedState ensures a
// resolution change never panics and never leaks state beyond exactly one
// prior frame (bounded-memory rule).
func TestMotionDetector_ResolutionChangeResetsBoundedState(t *testing.T) {
	m := NewMotionDetector(baseHybridConfig())
	m.Evaluate(makeYPlane(100, 0, -1, -1), 8, 8, 1, time.Now())
	// Switch resolution: must not panic, and must not treat mismatched
	// dimensions as "no change".
	res := m.Evaluate(makeYPlane(50, 0, -1, -1), 4, 4, 2, time.Now())
	if res.Candidate {
		t.Fatalf("first frame at a new resolution must never be a candidate (no valid previous frame yet): %+v", res)
	}
}
