//go:build localbench

// Package cameratest: local (no camera, no SaaS) bandwidth/cost benchmark
// for Hito I10. Run manually with:
//
//	go test -tags localbench ./internal/cameratest/ -run TestLocalBandwidthBenchmark -v
//
// This is NOT a substitute for the real TC70 -> SaaS E2E acceptance test in
// cloud_e2e_integration_test.go. It exists only for the case that test
// documents: no TC70/SaaS credentials reachable in this environment, so I10
// falls back to measuring the real encode + HTTP path locally (loopback
// httptest.Server), never inventing numbers. Every metric printed here comes
// from CloudSink's own real counters (internal/cloudsink) — this benchmark
// only supplies synthetic frame content and a local HTTP endpoint, which the
// output log makes explicit.
package cameratest

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/cloudsink"
	"github.com/drko-dev/monitoreoedgeis/internal/processing"
)

// noisyYUV420p fills a yuv420p buffer with pseudo-random bytes so JPEG
// compression behaves like real camera footage (a solid-color frame
// compresses to an unrealistically tiny size and would understate real
// bandwidth use).
func noisyYUV420p(t *testing.T, width, height int) []byte {
	t.Helper()
	ySize := width * height
	cSize := (width / 2) * (height / 2)
	buf := make([]byte, ySize+2*cSize)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	return buf
}

func TestLocalBandwidthBenchmark(t *testing.T) {
	const (
		width     = 640 // config.DefaultVideoOutputWidth
		height    = 360 // config.DefaultVideoOutputHeight
		targetFPS = 5.0 // config.DefaultVideoTargetFPS
		duration  = 30 * time.Second
	)

	var serverBytesReceived int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, _ := io.Copy(io.Discard, r.Body)
		serverBytesReceived += n
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	sender := &httpFrameSender{baseURL: srv.URL}
	cSink := cloudsink.New(sender, "bench-device", "bench-credential", cloudsink.Config{}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)

	ticker := time.NewTicker(time.Duration(float64(time.Second) / targetFPS))
	defer ticker.Stop()
	deadline := time.Now().Add(duration)
	start := time.Now()
	var seq uint64
	for time.Now().Before(deadline) {
		<-ticker.C
		seq++
		f := processing.Frame{
			CandidateKey: "bench-cam",
			Timestamp:    time.Now(),
			Seq:          seq,
			OutputWidth:  width,
			OutputHeight: height,
			Data:         noisyYUV420p(t, width, height),
		}
		if err := cSink.Route(f); err != nil {
			t.Logf("Route() error (counted in frames_upload_failed): %v", err)
		}
	}

	status := cSink.Status()
	var avgJPEGBytes float64
	if status.FramesUploadSucceeded > 0 {
		avgJPEGBytes = float64(status.JPEGBytesUploaded) / float64(status.FramesUploadSucceeded)
	}

	t.Logf("LIMITATION: no TC70/SaaS reachable in this environment (TAPO_ONVIF_* / GEOCAM_E2E_* unset) — this is a LOCAL encode+loopback-HTTP measurement, not a real-camera/real-SaaS benchmark")
	t.Logf("I10 LOCAL BENCHMARK: duration=%s stream=%dx%d target_fps=%.1f quality=%d", time.Since(start).Round(time.Second), width, height, targetFPS, cloudsink.JPEGQuality)
	t.Logf("I10 LOCAL BENCHMARK: frames_encoded=%d frames_upload_attempted=%d frames_upload_succeeded=%d frames_upload_failed=%d",
		status.FramesEncoded, status.FramesUploadAttempted, status.FramesUploadSucceeded, status.FramesUploadFailed)
	t.Logf("I10 LOCAL BENCHMARK: jpeg_bytes_uploaded=%d avg_bytes_per_frame=%.0f effective_bytes_per_sec=%.1f effective_mbps=%.3f effective_frames_per_sec=%.2f",
		status.JPEGBytesUploaded, avgJPEGBytes, status.EffectiveBytesPerSec, status.EffectiveBytesPerSec*8/1_000_000, status.EffectiveFramesPerSec)
	t.Logf("I10 LOCAL BENCHMARK: encode_latency_avg_ms=%.2f upload_latency_avg_ms=%.2f (loopback HTTP, not real network)", status.EncodeLatencyAvgMs, status.UploadLatencyAvgMs)
	t.Logf("I10 LOCAL BENCHMARK: server_bytes_received=%d (raw HTTP body bytes seen server-side, cross-check against jpeg_bytes_uploaded)", serverBytesReceived)
}

// httpFrameSender implements cloudsink.FrameSender against a plain HTTP
// endpoint (no auth, no SaaS contract) — only used by this local benchmark.
type httpFrameSender struct {
	baseURL string
}

func (s *httpFrameSender) PostFrame(ctx context.Context, deviceID, credential, candidateKey string, seq uint64, capturedAt time.Time, jpeg []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("%s/bench", s.baseURL), bytes.NewReader(jpeg))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "image/jpeg")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	return nil
}
