package cloudsink

import (
	"sync"
	"time"
)

// effectiveRateWindow is the width of the rolling window backing
// Status.EffectiveBytesPerSec/EffectiveFramesPerSec. 10s matches Milestone
// I7's original tracker (removed during the I6/I7/I10 integration in favor
// of this single canonical implementation) — short enough to reflect
// current load, not diluted by a historical outage.
const effectiveRateWindow = 10 * time.Second

// effectiveRate is a rolling per-second upload rate (bytes and frames)
// over the last effectiveRateWindow, in O(window seconds) fixed memory —
// never proportional to total upload volume or uptime. record is called on
// every successful upload, direct or replayed alike, so a long historical
// outage followed by a replay burst is reflected as a real recent rate,
// never diluted by time spent idle before it.
type effectiveRate struct {
	mu      sync.Mutex
	windowS int64
	bytes   []int64 // bytes uploaded in each second-bucket
	frames  []int64 // frames uploaded in each second-bucket
	bucketS []int64 // unix-second each bucket was last written for; a
	// bucket whose bucketS falls outside the current window reads as 0
	// without needing to be actively cleared.
}

func newEffectiveRate(window time.Duration) *effectiveRate {
	n := int64(window / time.Second)
	if n < 1 {
		n = 1
	}
	return &effectiveRate{
		windowS: n,
		bytes:   make([]int64, n),
		frames:  make([]int64, n),
		bucketS: make([]int64, n),
	}
}

func (r *effectiveRate) record(uploadedBytes int64, at time.Time) {
	sec := at.Unix()
	idx := sec % r.windowS
	if idx < 0 {
		idx += r.windowS
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.bucketS[idx] != sec {
		// First write to this slot for this second (or it's stale from a
		// previous lap around the ring): reset it before accumulating.
		r.bucketS[idx] = sec
		r.bytes[idx] = 0
		r.frames[idx] = 0
	}
	r.bytes[idx] += uploadedBytes
	r.frames[idx]++
}

// rate returns bytes/sec and frames/sec averaged over the trailing window
// as of now. Buckets last written outside the window (including all of
// them, if nothing uploaded recently) are excluded, so the rate correctly
// decays to 0 once uploads stop for a full window — it never reports a
// number left over from a burst that ended minutes or hours ago.
func (r *effectiveRate) rate(now time.Time) (bytesPerSec, framesPerSec float64) {
	nowSec := now.Unix()

	r.mu.Lock()
	defer r.mu.Unlock()
	var totalBytes, totalFrames int64
	for i := int64(0); i < r.windowS; i++ {
		age := nowSec - r.bucketS[i]
		if age >= 0 && age < r.windowS {
			totalBytes += r.bytes[i]
			totalFrames += r.frames[i]
		}
	}
	window := float64(r.windowS)
	return float64(totalBytes) / window, float64(totalFrames) / window
}
