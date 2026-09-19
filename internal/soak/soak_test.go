package soak_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/cloudsink"
	"github.com/drko-dev/monitoreoedgeis/internal/heartbeat"
	"github.com/drko-dev/monitoreoedgeis/internal/transport"
)

type sender struct {
	calls atomic.Int64
}

func (s *sender) Heartbeat(context.Context, string, string, transport.HeartbeatRequest) error {
	s.calls.Add(1)
	return nil
}

type processSample struct {
	goroutines int
	fdCount    int
	rssBytes   int64
}

func TestSoak(t *testing.T) {
	if os.Getenv("GEOCAM_SOAK") != "1" {
		t.Skip("set GEOCAM_SOAK=1 to run the soak harness")
	}

	duration := 15 * time.Second
	if raw := os.Getenv("GEOCAM_SOAK_DURATION"); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil || parsed <= 0 {
			t.Fatalf("invalid GEOCAM_SOAK_DURATION %q", raw)
		}
		duration = parsed
	}

	start := sampleProcess()
	peak := start

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s := sampleProcess()
				if s.goroutines > peak.goroutines {
					peak.goroutines = s.goroutines
				}
				if s.fdCount > peak.fdCount {
					peak.fdCount = s.fdCount
				}
				if s.rssBytes > peak.rssBytes {
					peak.rssBytes = s.rssBytes
				}
			}
		}
	}()

	hbSender := &sender{}
	hb, err := heartbeat.New(heartbeat.Options{
		Sender:     hbSender,
		DeviceID:   "soak-device",
		Credential: "soak-credential",
		Build: func() transport.HeartbeatRequest {
			return transport.HeartbeatRequest{EdgeID: "soak-edge"}
		},
		Interval: 5 * time.Millisecond,
		Log:      slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
		Rand:     func() float64 { return 0 },
	})
	if err != nil {
		t.Fatalf("heartbeat.New: %v", err)
	}
	if err := hb.Start(context.Background()); err != nil {
		t.Fatalf("heartbeat.Start: %v", err)
	}

	buffer, err := cloudsink.OpenBuffer(t.TempDir(), 64*1024, 8, 0)
	if err != nil {
		t.Fatalf("OpenBuffer: %v", err)
	}

	deadline := time.Now().Add(duration)
	var cycles int64
	for time.Now().Before(deadline) {
		cycles++
		frame := cloudsink.BufferedFrame{
			CandidateKey: "soak-camera",
			Seq:          uint64(cycles),
			Timestamp:    time.Now().UTC(),
			JPEG:         []byte("deterministic-soak-frame"),
		}
		if err := buffer.Enqueue(frame); err != nil && !errors.Is(err, cloudsink.ErrBufferFull) {
			t.Fatalf("buffer enqueue: %v", err)
		}

		// Exercise both bounded-full and drain paths. Keeping the drain slower
		// than enqueue deliberately reaches capacity without unbounded growth.
		if cycles%3 == 0 {
			if _, ok, err := buffer.Peek(); err != nil {
				t.Fatalf("buffer peek: %v", err)
			} else if ok {
				buffer.Advance()
			}
		}
		time.Sleep(2 * time.Millisecond)
	}

	// A soak must leave the bounded queue drainable and shut down cleanly.
	for {
		if _, ok, err := buffer.Peek(); err != nil {
			t.Fatalf("buffer final peek: %v", err)
		} else if !ok {
			break
		}
		buffer.Advance()
	}

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopCancel()
	if err := hb.Stop(stopCtx); err != nil {
		t.Fatalf("heartbeat.Stop: %v", err)
	}
	cancel()
	time.Sleep(20 * time.Millisecond)

	final := sampleProcess()
	stats := buffer.Stats()
	heartbeats := hbSender.calls.Load()

	if cycles == 0 {
		t.Fatal("soak completed zero cycles")
	}
	if heartbeats == 0 {
		t.Fatal("soak completed zero heartbeats")
	}
	if stats.BufferedFrames != 0 {
		t.Fatalf("buffer did not drain: depth=%d", stats.BufferedFrames)
	}
	if stats.Capacity != 8 {
		t.Fatalf("unexpected buffer capacity: %d", stats.Capacity)
	}
	if final.goroutines > start.goroutines+2 {
		t.Fatalf("goroutine growth did not settle: start=%d final=%d peak=%d", start.goroutines, final.goroutines, peak.goroutines)
	}
	if start.fdCount >= 0 && final.fdCount > start.fdCount+2 {
		t.Fatalf("file descriptor growth did not settle: start=%d final=%d peak=%d", start.fdCount, final.fdCount, peak.fdCount)
	}

	t.Logf("soak duration=%s cycles=%d heartbeats=%d", duration, cycles, heartbeats)
	t.Logf("goroutines start=%d final=%d peak=%d", start.goroutines, final.goroutines, peak.goroutines)
	t.Logf("queue final_depth=%d capacity=%d drops=%d replayed=%d", stats.BufferedFrames, stats.Capacity, stats.DroppedFull, stats.ReplayedFrames)
	if start.fdCount >= 0 {
		t.Logf("fd start=%d final=%d peak=%d", start.fdCount, final.fdCount, peak.fdCount)
	} else {
		t.Log("fd unavailable on this runner")
	}
	if start.rssBytes >= 0 {
		t.Logf("rss_bytes start=%d final=%d peak=%d", start.rssBytes, final.rssBytes, peak.rssBytes)
	} else {
		t.Log("rss unavailable on this runner")
	}
}

func sampleProcess() processSample {
	return processSample{
		goroutines: runtime.NumGoroutine(),
		fdCount:    procFDCount(),
		rssBytes:   procRSSBytes(),
	}
}

func procFDCount() int {
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return -1
	}
	return len(entries)
}

func procRSSBytes() int64 {
	data, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return -1
	}
	fields := strings.Fields(string(data))
	if len(fields) < 2 {
		return -1
	}
	pages, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return -1
	}
	pageSize := int64(os.Getpagesize())
	if pages > (1<<63-1)/pageSize {
		panic(fmt.Sprintf("rss page count overflow: %d", pages))
	}
	return pages * pageSize
}
