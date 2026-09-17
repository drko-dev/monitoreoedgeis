package cloudsink

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestTokenBucket_Unconfigured(t *testing.T) {
	var nilLimiter *TokenBucket
	if !nilLimiter.Allow(1000) {
		t.Fatal("nil limiter Allow() = false, want true")
	}
	if err := nilLimiter.Wait(context.Background(), 1000); err != nil {
		t.Fatalf("nil limiter Wait() error = %v, want nil", err)
	}
	if nilLimiter.ConfiguredLimit() != 0 {
		t.Fatalf("ConfiguredLimit() = %d, want 0", nilLimiter.ConfiguredLimit())
	}

	zeroLimiter := NewLimiter(0, 0, 0)
	if !zeroLimiter.Allow(50000) {
		t.Fatal("zero limiter Allow() = false, want true")
	}
	if err := zeroLimiter.Wait(context.Background(), 50000); err != nil {
		t.Fatalf("zero limiter Wait() error = %v, want nil", err)
	}
	if zeroLimiter.ConfiguredLimit() != 0 {
		t.Fatalf("ConfiguredLimit() = %d, want 0", zeroLimiter.ConfiguredLimit())
	}
}

func TestTokenBucket_ByteRateLimiting_Allow(t *testing.T) {
	simTime := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	l := NewLimiter(1000, 2000, 0) // 1000 bytes/sec, 2000 bytes burst
	l.nowFunc = func() time.Time { return simTime }
	l.lastByteRefill = simTime

	if l.ConfiguredLimit() != 1000 {
		t.Fatalf("ConfiguredLimit() = %d, want 1000", l.ConfiguredLimit())
	}
	if l.BurstBytes() != 2000 {
		t.Fatalf("BurstBytes() = %d, want 2000", l.BurstBytes())
	}

	// 1. Initial burst allows up to 2000 bytes.
	if !l.Allow(1200) {
		t.Fatal("Allow(1200) = false, want true")
	}
	// 800 bytes left.
	if !l.Allow(800) {
		t.Fatal("Allow(800) = false, want true")
	}
	// 0 bytes left, now should reject.
	if l.Allow(1) {
		t.Fatal("Allow(1) with empty bucket = true, want false")
	}

	// 2. Advance time by 0.5s -> refilled 500 bytes.
	simTime = simTime.Add(500 * time.Millisecond)
	if l.Allow(600) {
		t.Fatal("Allow(600) with 500 available = true, want false")
	}
	if !l.Allow(500) {
		t.Fatal("Allow(500) with 500 available = false, want true")
	}

	// 3. Advance time by 5s -> should refill up to burst capacity (2000), not more.
	simTime = simTime.Add(5 * time.Second)
	if !l.Allow(2000) {
		t.Fatal("Allow(2000) after long pause = false, want true")
	}
	if l.Allow(1) {
		t.Fatal("Allow(1) after exhausting burst = true, want false")
	}

	// 4. Request exceeding burst capacity is never allowed.
	if l.Allow(2001) {
		t.Fatal("Allow(2001) exceeding burst capacity = true, want false")
	}
}

func TestTokenBucket_FPSLimiting_Allow(t *testing.T) {
	simTime := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	l := NewLimiter(0, 0, 2.0) // 2 FPS, burst 2 frames
	l.nowFunc = func() time.Time { return simTime }
	l.lastFrameRefill = simTime

	if l.ConfiguredFPS() != 2.0 {
		t.Fatalf("ConfiguredFPS() = %g, want 2.0", l.ConfiguredFPS())
	}

	// Consume burst: 2 frames.
	if !l.Allow(0) {
		t.Fatal("frame 1: Allow() = false, want true")
	}
	if !l.Allow(0) {
		t.Fatal("frame 2: Allow() = false, want true")
	}
	// Frame 3 should fail.
	if l.Allow(0) {
		t.Fatal("frame 3 without delay: Allow() = true, want false")
	}

	// Advance 500ms -> refilled 1 frame.
	simTime = simTime.Add(500 * time.Millisecond)
	if !l.Allow(0) {
		t.Fatal("frame 3 after 500ms: Allow() = false, want true")
	}
	if l.Allow(0) {
		t.Fatal("frame 4 immediately: Allow() = true, want false")
	}
}

func TestTokenBucket_CombinedLimits(t *testing.T) {
	simTime := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	l := NewLimiter(1000, 2000, 2.0) // 1000 B/s, 2000 B burst, 2 FPS
	l.nowFunc = func() time.Time { return simTime }
	l.lastByteRefill = simTime
	l.lastFrameRefill = simTime

	// Has both frame and byte tokens.
	if !l.Allow(1500) {
		t.Fatal("first call with both tokens available: want true, got false")
	}
	// 500 bytes left, 1 frame token left.
	// Requesting 600 bytes should fail on bytes even though 1 frame token is available.
	if l.Allow(600) {
		t.Fatal("call exceeding byte limit: want false, got true")
	}
	// Requesting 500 bytes succeeds, exhausting frame tokens.
	if !l.Allow(500) {
		t.Fatal("call within byte and frame limit: want true, got false")
	}
	// Frame tokens exhausted: next call fails even for 0 bytes.
	if l.Allow(0) {
		t.Fatal("call with exhausted frame tokens: want false, got true")
	}
}

func TestTokenBucket_Wait_ContextCancellation(t *testing.T) {
	l := NewLimiter(100, 100, 0)
	if !l.Allow(100) {
		t.Fatal("initial Allow(100) failed")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	err := l.Wait(ctx, 100)
	if err == nil {
		t.Fatal("Wait() with cancelled context: want error, got nil")
	}
	if ctx.Err() == nil {
		t.Fatal("ctx.Err() should be non-nil")
	}
}

func TestTokenBucket_Wait_ExceedsBurst(t *testing.T) {
	l := NewLimiter(1000, 2000, 0)
	err := l.Wait(context.Background(), 3000)
	if err == nil {
		t.Fatal("Wait() exceeding burst capacity: want error, got nil")
	}
}

func TestTokenBucket_Wait_Success(t *testing.T) {
	l := NewLimiter(10000, 10000, 0) // 10 KB/s
	// Exhaust bucket
	if !l.Allow(10000) {
		t.Fatal("initial Allow() failed")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	// Wait for 100 bytes (takes ~10ms at 10KB/s)
	start := time.Now()
	if err := l.Wait(ctx, 100); err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if elapsed := time.Since(start); elapsed < 5*time.Millisecond {
		t.Logf("Wait() completed in %v", elapsed)
	}
}

func TestTokenBucket_AutoBurst(t *testing.T) {
	l := NewLimiter(1000, 0, 0)
	if l.BurstBytes() != DefaultMinBurstBytes {
		t.Fatalf("Auto burst for small rate = %d, want %d", l.BurstBytes(), DefaultMinBurstBytes)
	}

	largeRate := 1024 * 1024 * 5 // 5 MB/s
	l2 := NewLimiter(int64(largeRate), 0, 0)
	if l2.BurstBytes() != int64(largeRate) {
		t.Fatalf("Auto burst for large rate = %d, want %d", l2.BurstBytes(), largeRate)
	}
}

func TestTokenBucket_ThreadSafety(t *testing.T) {
	l := NewLimiter(50000, 50000, 100)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_ = l.Allow(500)
			}
		}()
	}
	wg.Wait()
}

// TestTokenBucket_Wait_BlocksAllowUntilSatisfied is the fairness fix for
// the I6/I7 starvation finding: while a replay is inside Wait(), the
// direct path's Allow() must yield instead of racing it for newly-refilled
// tokens, or a durable buffered frame could be postponed indefinitely
// under sustained direct traffic.
func TestTokenBucket_Wait_BlocksAllowUntilSatisfied(t *testing.T) {
	l := NewLimiter(1000, 100, 0) // 1000 B/s, burst 100 B
	if !l.Allow(100) {
		t.Fatal("initial Allow(100) should succeed (full burst)")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	waitStarted := make(chan struct{})
	waitDone := make(chan error, 1)
	go func() {
		close(waitStarted)
		waitDone <- l.Wait(ctx, 100) // needs the burst to fully refill
	}()
	<-waitStarted
	time.Sleep(20 * time.Millisecond) // let Wait() register as a waiter

	if l.Allow(1) {
		t.Fatal("Allow() succeeded while a Wait() was pending — no fairness against starvation")
	}

	select {
	case err := <-waitDone:
		if err != nil {
			t.Fatalf("Wait() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Wait() did not complete")
	}

	// Wait(100) just consumed the full refilled burst for itself, so an
	// immediate Allow(1) can legitimately fail on token exhaustion alone —
	// that's not what this assertion is checking. Give a few bytes time to
	// refill (1000 B/s), then confirm Allow() is no longer blocked by
	// fairness (a waiters-count bug would still block it here).
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		if l.Allow(1) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("Allow() still blocked after Wait() completed and tokens had time to refill — priority was not released")
}

// TestTokenBucket_Wait_CancellationReleasesPriority ensures a cancelled
// waiter doesn't leave Allow() permanently blocked.
func TestTokenBucket_Wait_CancellationReleasesPriority(t *testing.T) {
	l := NewLimiter(1, 100, 0) // refill so slow Wait(100) will not finish in test time
	if !l.Allow(100) {
		t.Fatal("initial Allow(100) should succeed")
	}

	ctx, cancel := context.WithCancel(context.Background())
	waitDone := make(chan error, 1)
	go func() { waitDone <- l.Wait(ctx, 100) }()

	time.Sleep(20 * time.Millisecond)
	if l.Allow(1) {
		t.Fatal("Allow() succeeded while a Wait() was pending")
	}

	cancel()
	select {
	case err := <-waitDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Wait() error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Wait() did not return after cancellation")
	}

	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		if l.Allow(1) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("Allow() still blocked after the waiting Wait() was cancelled — priority not released")
}
