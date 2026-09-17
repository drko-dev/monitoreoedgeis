package cloudsink

import (
	"sync"
	"testing"
	"time"
)

func TestEffectiveRate_RecentUploadsProduceRate(t *testing.T) {
	r := newEffectiveRate(2 * time.Second)
	now := time.Now()
	r.record(1000, now)
	r.record(1000, now)

	bytesPerSec, framesPerSec := r.rate(now)
	if bytesPerSec <= 0 {
		t.Fatalf("bytesPerSec = %v, want > 0", bytesPerSec)
	}
	if framesPerSec <= 0 {
		t.Fatalf("framesPerSec = %v, want > 0", framesPerSec)
	}
}

func TestEffectiveRate_DecaysToZeroAfterWindowExpires(t *testing.T) {
	r := newEffectiveRate(2 * time.Second)
	base := time.Now()
	r.record(5000, base)

	bytesPerSec, framesPerSec := r.rate(base.Add(5 * time.Second))
	if bytesPerSec != 0 || framesPerSec != 0 {
		t.Fatalf("rate after window expired = (%v, %v), want (0, 0)", bytesPerSec, framesPerSec)
	}
}

// TestEffectiveRate_HistoricalOutageDoesNotDiluteRecentBurst is the P1 fix
// itself: a since-process-start average would report a near-zero rate here
// (one byte diluted over an hour); the rolling window must not.
func TestEffectiveRate_HistoricalOutageDoesNotDiluteRecentBurst(t *testing.T) {
	r := newEffectiveRate(10 * time.Second)
	base := time.Now()

	r.record(1, base) // a single upload long before the recent burst

	now := base.Add(1 * time.Hour) // simulated long outage in between
	for i := 0; i < 10; i++ {
		r.record(100000, now)
	}

	bytesPerSec, _ := r.rate(now)
	const wantApprox = 10 * 100000 / 10 // 10 uploads of 100000B over a 10s window
	if bytesPerSec < wantApprox*0.9 {
		t.Fatalf("bytesPerSec = %v, want close to %v (recent burst, not diluted by an hour of prior idle time)", bytesPerSec, wantApprox)
	}
}

func TestEffectiveRate_ReplayCountsSameAsDirect(t *testing.T) {
	direct := newEffectiveRate(2 * time.Second)
	replay := newEffectiveRate(2 * time.Second)
	now := time.Now()

	// record() has no notion of "direct" vs "replay" caller — both upload
	// paths in cloudsink.upload() call it identically.
	direct.record(500, now)
	replay.record(500, now)

	db, df := direct.rate(now)
	rb, rf := replay.rate(now)
	if db != rb || df != rf {
		t.Fatalf("direct rate (%v,%v) != replay rate (%v,%v)", db, df, rb, rf)
	}
}

func TestEffectiveRate_ConcurrentRecordAndRate(t *testing.T) {
	r := newEffectiveRate(2 * time.Second)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			r.record(10, time.Now())
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			_, _ = r.rate(time.Now())
		}
	}()
	wg.Wait()
}
