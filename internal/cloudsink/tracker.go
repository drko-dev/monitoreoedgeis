package cloudsink

import (
	"sync"
	"time"
)

// uploadTracker tracks recent uploads using a sliding window to compute
// effective upload rate in bytes per second (Milestone I7).
type uploadTracker struct {
	mu          sync.Mutex
	firstUpload time.Time
	buckets     [10]int64
	bucketSecs  [10]int64
	nowFunc     func() time.Time
}

func newUploadTracker() *uploadTracker {
	return &uploadTracker{
		nowFunc: time.Now,
	}
}

func (t *uploadTracker) record(bytes int64, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.firstUpload.IsZero() {
		t.firstUpload = now
	}
	sec := now.Unix()
	idx := int(sec % 10)
	if idx < 0 {
		idx = -idx
	}
	if t.bucketSecs[idx] != sec {
		t.bucketSecs[idx] = sec
		t.buckets[idx] = 0
	}
	t.buckets[idx] += bytes
}

func (t *uploadTracker) rate(now time.Time) float64 {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.firstUpload.IsZero() {
		return 0
	}

	sec := now.Unix()
	var sum int64
	var activeSecs int64
	for i := 0; i < 10; i++ {
		bSec := t.bucketSecs[i]
		if bSec > 0 && sec-bSec < 10 && sec >= bSec {
			sum += t.buckets[i]
			activeSecs++
		}
	}
	if activeSecs == 0 {
		return 0
	}

	elapsed := now.Sub(t.firstUpload).Seconds()
	if elapsed < 10 {
		if elapsed < 1 {
			elapsed = 1
		}
		return float64(sum) / elapsed
	}
	return float64(sum) / 10.0
}
