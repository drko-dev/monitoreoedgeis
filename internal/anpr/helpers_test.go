package anpr

import "time"

// testConfig returns a permissive-but-bounded Config for tests that don't
// care about exercising a specific bound.
func testConfig() Config {
	return Config{
		Enabled:                  true,
		MaxActiveBurstsPerCamera: 4,
		MaxFramesPerBurst:        5,
		MaxCandidateBytes:        1 << 20,
		MaxContextFrames:         5,
		MaxCameras:               10,
		BurstTTL:                 10 * time.Second,
		CropPolicy:               CropVehicleContext,
		FrameSelection:           SelectFirst,
		MaxEncodedCropBytes:      1 << 20,
		DedupeCacheSize:          1000,
	}
}

func testVehicleCandidate(cameraKey string, frameSeq uint64) VehicleCandidate {
	return VehicleCandidate{
		CameraKey:         cameraKey,
		FrameSeq:          frameSeq,
		Timestamp:         time.Unix(int64(frameSeq), 0),
		VehicleClassID:    2,
		VehicleConfidence: 0.8,
		VehicleBBox:       BBox{X0: 10, Y0: 10, X1: 110, Y1: 90},
		CorrelationID:     "",
		ProcessingMode:    "hybrid",
		SourceWidth:       640,
		SourceHeight:      360,
	}
}

// fakeClock is a simple injectable, manually-advanced clock for burst TTL
// tests — no sleeps anywhere in this package's tests.
type fakeClock struct{ t time.Time }

func newFakeClock(start time.Time) *fakeClock { return &fakeClock{t: start} }
func (c *fakeClock) Now() time.Time           { return c.t }
func (c *fakeClock) Advance(d time.Duration)  { c.t = c.t.Add(d) }
