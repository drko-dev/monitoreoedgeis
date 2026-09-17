// Package cloudsink implements processing.Sink to push sampled video frames
// to the SaaS's Cloud Vision Worker (Milestone I).
package cloudsink

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"
)

// DefaultMinBurstBytes is the minimum burst capacity applied when auto-computing
// burst size for a positive byte rate limit. A single 640x360 JPEG frame at default
// quality is typically 20-60 KB; 256 KB guarantees that at least 1-2 frames can
// be accommodated without being rejected immediately on startup.
const DefaultMinBurstBytes int64 = 256 * 1024

// TokenBucket implements a thread-safe token bucket rate limiter for bytes
// and optionally frames per second (Milestone I7). It supports bounded burst,
// context-aware cancellation, and zero busy-waiting.
type TokenBucket struct {
	mu sync.Mutex

	bytesPerSec    int64
	burstBytes     int64
	byteTokens     float64
	lastByteRefill time.Time

	fps             float64
	burstFrames     float64
	frameTokens     float64
	lastFrameRefill time.Time

	nowFunc func() time.Time
}

// NewLimiter creates a TokenBucket rate limiter.
//   - bytesPerSec: max upload bytes per second (<= 0 means unlimited bytes)
//   - burstBytes: max burst capacity in bytes (<= 0 auto-calculates to max(bytesPerSec, DefaultMinBurstBytes))
//   - fps: max frames per second (<= 0 means unlimited frames)
func NewLimiter(bytesPerSec, burstBytes int64, fps float64) *TokenBucket {
	now := time.Now()
	l := &TokenBucket{
		bytesPerSec:     bytesPerSec,
		fps:             fps,
		nowFunc:         time.Now,
		lastByteRefill:  now,
		lastFrameRefill: now,
	}

	if bytesPerSec > 0 {
		if burstBytes <= 0 {
			burstBytes = bytesPerSec
			if burstBytes < DefaultMinBurstBytes {
				burstBytes = DefaultMinBurstBytes
			}
		}
		l.burstBytes = burstBytes
		l.byteTokens = float64(burstBytes)
	}

	if fps > 0 {
		bf := math.Ceil(fps)
		if bf < 1 {
			bf = 1
		}
		l.burstFrames = bf
		l.frameTokens = bf
	}

	return l
}

// ConfiguredLimit returns the configured bytes per second limit (0 if unlimited).
func (l *TokenBucket) ConfiguredLimit() int64 {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.bytesPerSec
}

// ConfiguredFPS returns the configured frames per second limit (0 if unlimited).
func (l *TokenBucket) ConfiguredFPS() float64 {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.fps
}

// BurstBytes returns the effective burst capacity in bytes (0 if unlimited).
func (l *TokenBucket) BurstBytes() int64 {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.burstBytes
}

func (l *TokenBucket) now() time.Time {
	if l.nowFunc != nil {
		return l.nowFunc()
	}
	return time.Now()
}

func (l *TokenBucket) enabled() bool {
	return l.bytesPerSec > 0 || l.fps > 0
}

func (l *TokenBucket) refillLocked(now time.Time) {
	if l.bytesPerSec > 0 {
		elapsed := now.Sub(l.lastByteRefill).Seconds()
		if elapsed > 0 {
			l.byteTokens += elapsed * float64(l.bytesPerSec)
			if l.byteTokens > float64(l.burstBytes) {
				l.byteTokens = float64(l.burstBytes)
			}
			l.lastByteRefill = now
		} else if elapsed < 0 {
			// Clock stepped backward
			l.lastByteRefill = now
		}
	}

	if l.fps > 0 {
		elapsed := now.Sub(l.lastFrameRefill).Seconds()
		if elapsed > 0 {
			l.frameTokens += elapsed * l.fps
			if l.frameTokens > l.burstFrames {
				l.frameTokens = l.burstFrames
			}
			l.lastFrameRefill = now
		} else if elapsed < 0 {
			l.lastFrameRefill = now
		}
	}
}

// Allow reports whether an event consuming bytes is permitted right now.
// If permitted, tokens are consumed and Allow returns true.
// If not permitted, tokens are not consumed and Allow returns false.
func (l *TokenBucket) Allow(bytes int64) bool {
	if l == nil || !l.enabled() {
		return true
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	l.refillLocked(now)

	byteOK := l.bytesPerSec <= 0 || (bytes <= l.burstBytes && l.byteTokens >= float64(bytes))
	frameOK := l.fps <= 0 || l.frameTokens >= 1.0

	if byteOK && frameOK {
		if l.bytesPerSec > 0 {
			l.byteTokens -= float64(bytes)
		}
		if l.fps > 0 {
			l.frameTokens -= 1.0
		}
		return true
	}

	return false
}

// Wait blocks until enough tokens are available to permit an event of size
// bytes, or until ctx is cancelled. It calculates the exact wait duration and
// uses a timer without busy-looping.
func (l *TokenBucket) Wait(ctx context.Context, bytes int64) error {
	if l == nil || !l.enabled() {
		return ctx.Err()
	}

	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		l.mu.Lock()
		now := l.now()
		if l.bytesPerSec > 0 && l.burstBytes > 0 && bytes > l.burstBytes {
			l.mu.Unlock()
			return fmt.Errorf("cloudsink: requested %d bytes exceeds burst capacity %d", bytes, l.burstBytes)
		}

		l.refillLocked(now)

		byteOK := l.bytesPerSec <= 0 || l.byteTokens >= float64(bytes)
		frameOK := l.fps <= 0 || l.frameTokens >= 1.0

		if byteOK && frameOK {
			if l.bytesPerSec > 0 {
				l.byteTokens -= float64(bytes)
			}
			if l.fps > 0 {
				l.frameTokens -= 1.0
			}
			l.mu.Unlock()
			return nil
		}

		var waitBytes time.Duration
		if !byteOK && l.bytesPerSec > 0 {
			needed := float64(bytes) - l.byteTokens
			waitSec := needed / float64(l.bytesPerSec)
			waitBytes = time.Duration(waitSec * float64(time.Second))
		}

		var waitFrames time.Duration
		if !frameOK && l.fps > 0 {
			needed := 1.0 - l.frameTokens
			waitSec := needed / l.fps
			waitFrames = time.Duration(waitSec * float64(time.Second))
		}

		waitDur := waitBytes
		if waitFrames > waitDur {
			waitDur = waitFrames
		}
		if waitDur <= 0 {
			waitDur = time.Millisecond
		}
		l.mu.Unlock()

		timer := time.NewTimer(waitDur)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
