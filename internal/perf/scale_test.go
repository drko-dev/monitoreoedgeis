package perf_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/perf"
)

func TestParseConfig_Defaults(t *testing.T) {
	cfg, enabled, err := perf.ParseConfig()
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if enabled {
		t.Fatal("expected disabled by default (GEOCAM_PERF unset)")
	}
	if cfg.Cameras != 1 {
		t.Errorf("expected default 1 camera, got %d", cfg.Cameras)
	}
	if cfg.Duration != 10*time.Second {
		t.Errorf("expected default 10s duration, got %s", cfg.Duration)
	}
}

func TestParseConfig_Overrides(t *testing.T) {
	t.Setenv("GEOCAM_PERF", "1")
	t.Setenv("GEOCAM_PERF_CAMERAS", "25")
	t.Setenv("GEOCAM_PERF_DURATION", "2s")

	cfg, enabled, err := perf.ParseConfig()
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if !enabled {
		t.Fatal("expected enabled when GEOCAM_PERF=1")
	}
	if cfg.Cameras != 25 {
		t.Errorf("expected 25 cameras, got %d", cfg.Cameras)
	}
	if cfg.Duration != 2*time.Second {
		t.Errorf("expected 2s duration, got %s", cfg.Duration)
	}
}

func TestParseConfig_InvalidCameras(t *testing.T) {
	for _, raw := range []string{"0", "-1", "abc"} {
		t.Setenv("GEOCAM_PERF_CAMERAS", raw)
		if _, _, err := perf.ParseConfig(); err == nil {
			t.Errorf("expected error for GEOCAM_PERF_CAMERAS=%q", raw)
		}
	}
}

func TestParseConfig_InvalidDuration(t *testing.T) {
	for _, raw := range []string{"0s", "-1s", "notaduration"} {
		t.Setenv("GEOCAM_PERF_DURATION", raw)
		if _, _, err := perf.ParseConfig(); err == nil {
			t.Errorf("expected error for GEOCAM_PERF_DURATION=%q", raw)
		}
	}
}

func TestDefaultProfiles(t *testing.T) {
	want := []int{1, 5, 10, 25, 50}
	got := perf.DefaultProfiles()
	if len(got) != len(want) {
		t.Fatalf("expected %d profiles, got %d", len(want), len(got))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("profile[%d]: expected %d, got %d", i, want[i], got[i])
		}
	}
}

func TestRun_InvalidConfig(t *testing.T) {
	ctx := context.Background()
	if _, err := perf.Run(ctx, perf.Config{Cameras: 0, Duration: time.Second}); err == nil {
		t.Error("expected error for cameras=0")
	}
	if _, err := perf.Run(ctx, perf.Config{Cameras: 1, Duration: 0}); err == nil {
		t.Error("expected error for duration=0")
	}
}

// TestScaleSmoke is the cheap, deterministic CI smoke: it proves the harness
// itself works end to end without running the expensive manual profiles.
// It always runs as part of `go test ./...`.
func TestScaleSmoke(t *testing.T) {
	for _, n := range []int{1, 5} {
		t.Run(fmt.Sprintf("cameras=%d", n), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			res, err := perf.Run(ctx, perf.Config{Cameras: n, Duration: 300 * time.Millisecond})
			if err != nil {
				t.Fatalf("Run: %v", err)
			}

			if res.CameraTarget != n {
				t.Errorf("expected camera_target=%d, got %d", n, res.CameraTarget)
			}
			if res.CameraActive != n {
				t.Errorf("expected all %d cameras active, got %d", n, res.CameraActive)
			}
			if res.PacketsReceived == 0 {
				t.Error("expected packets to flow during the run")
			}
			if res.Errors != 0 {
				t.Errorf("unexpected errors/reconnects in a clean synthetic run: %d", res.Errors)
			}
			if res.GoroutinesEnd > res.GoroutinesStart+5 {
				t.Errorf("possible goroutine leak: start=%d end=%d", res.GoroutinesStart, res.GoroutinesEnd)
			}

			data, err := json.Marshal(res)
			if err != nil {
				t.Fatalf("json.Marshal: %v", err)
			}
			t.Logf("PERF_RESULT_JSON: %s", data)
		})
	}
}

// TestScaleFull is the manual/full benchmark entry point:
//
//	GEOCAM_PERF=1 GEOCAM_PERF_CAMERAS=50 GEOCAM_PERF_DURATION=60s \
//	  go test ./internal/perf/... -run TestScaleFull -v
//
// It is skipped by default so it never runs in a normal `go test ./...`.
func TestScaleFull(t *testing.T) {
	cfg, enabled, err := perf.ParseConfig()
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if !enabled {
		t.Skip("set GEOCAM_PERF=1 to run the full camera-scale benchmark")
	}

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration+30*time.Second)
	defer cancel()

	res, err := perf.Run(ctx, cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	data, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	t.Logf("PERF_RESULT_JSON:\n%s", data)

	t.Logf("cameras target=%d active=%d duration=%.2fs", res.CameraTarget, res.CameraActive, res.DurationSeconds)
	t.Logf("cpu_seconds_used=%.3f available=%v", res.CPUSecondsUsed, res.CPUAvailable)
	t.Logf("rss bytes start=%d peak=%d end=%d (-1=UNAVAILABLE)", res.RSSStartBytes, res.RSSPeakBytes, res.RSSEndBytes)
	t.Logf("goroutines start=%d peak=%d end=%d", res.GoroutinesStart, res.GoroutinesPeak, res.GoroutinesEnd)
	t.Logf("fd start=%d peak=%d end=%d (-1=UNAVAILABLE)", res.FDStart, res.FDPeak, res.FDEnd)
	t.Logf("errors=%d packets=%d throughput_pps=%.2f", res.Errors, res.PacketsReceived, res.ThroughputPPS)
	t.Logf("final_state_counts=%v", res.FinalStateCounts)

	if res.CameraActive != cfg.Cameras {
		t.Errorf("not all cameras reached active state: target=%d active=%d", cfg.Cameras, res.CameraActive)
	}
}
