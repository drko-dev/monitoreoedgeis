//go:build localbench

// Package cameratest: local (no camera, no SaaS) comparative bandwidth benchmark
// for Milestone J (J10–J11). Run manually with:
//
//	go test -tags localbench ./internal/cameratest/ -run TestHybridBandwidthBenchmark -v
//
// Compares Cloud baseline (continuous sampled frame push) vs. a SYNTHETIC
// SELECTIVITY / TRANSPORT SENSITIVITY candidate push over the exact same
// frame sequence and duration.
//
// IMPORTANT: the "candidate" selection used here (selectSyntheticCandidates,
// default pattern i%3==0) is a deterministic, artificial pattern used to
// exercise the CloudSink byte-accounting/transport path. It is NOT the real
// Hybrid J1-J5 motion/ROI candidate algorithm (internal/processing, PR #19).
// The bandwidth reduction reported by this benchmark is a controlled,
// synthetic result derived from the fixed 1-in-3 ratio; it validates that
// byte counting and transport behave correctly under reduced upload volume,
// it is NOT evidence of real-world savings from the Hybrid motion detector.
//
// candidateSelector is exposed so a future integration test can inject a
// real candidate/non-candidate decision sequence (e.g. derived from
// internal/processing.MotionResult.Candidate) without changing the rest of
// this harness.
package cameratest

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/cloudsink"
	"github.com/drko-dev/monitoreoedgeis/internal/processing"
)

func TestHybridBandwidthBenchmark(t *testing.T) {
	const (
		width     = 640 // config.DefaultVideoOutputWidth
		height    = 360 // config.DefaultVideoOutputHeight
		targetFPS = 5.0 // config.DefaultVideoTargetFPS
		duration  = 10 * time.Second
	)

	// This harness never opens or consumes a real RTSP/ONVIF stream — every
	// frame below is synthetic (noisyYUV420p). TAPO_ONVIF_IP being set only
	// means a camera is CONFIGURED in the environment; it does not mean this
	// benchmark exercised it. Hardware status is therefore always
	// NOT_EXECUTED for this harness, regardless of the env var.
	cameraConfigured := os.Getenv("TAPO_ONVIF_IP") != ""
	hardwareStatus := "REAL CAMERA = NOT_EXECUTED (Controlled Loopback Replay; no RTSP capture in this harness)"
	if cameraConfigured {
		hardwareStatus = "REAL CAMERA = CONFIGURED but NOT_EXECUTED (TAPO_ONVIF_IP set; this harness has no RTSP capture path, Controlled Loopback Replay used instead)"
	}
	t.Logf("[NOTICE] %s", hardwareStatus)
	t.Logf("[NOTICE] Running controlled local replay benchmark over identical synthetic frame sequence")

	// 1. Setup local receiver server
	var cloudBytesReceived, hybridBytesReceived int64
	var currentMode string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, _ := io.Copy(io.Discard, r.Body)
		if currentMode == "cloud" {
			cloudBytesReceived += n
		} else {
			hybridBytesReceived += n
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	sender := &httpFrameSender{baseURL: srv.URL}

	// 2. Pre-generate identical deterministic frame sequence (apple-to-apple)
	totalFrames := int(duration.Seconds() * targetFPS)
	if totalFrames < 1 {
		totalFrames = 1
	}

	frames := make([][]byte, totalFrames)
	for i := 0; i < totalFrames; i++ {
		frames[i] = noisyYUV420p(t, width, height)
	}

	// ── Pass 1: Cloud Baseline ───────────────────────────────────────────────
	currentMode = "cloud"
	cloudSink := cloudsink.New(
		sender,
		"bench-cloud",
		"bench-cred",
		cloudsink.Config{},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		nil,
	)

	cloudStart := time.Now()
	for i := 0; i < totalFrames; i++ {
		f := processing.Frame{
			CandidateKey: "bench-cam",
			Timestamp:    cloudStart.Add(time.Duration(float64(i) * float64(time.Second) / targetFPS)),
			Seq:          uint64(i + 1),
			OutputWidth:  width,
			OutputHeight: height,
			Data:         frames[i],
		}
		if err := cloudSink.Route(f); err != nil {
			t.Fatalf("CloudSink.Route error: %v", err)
		}
	}
	cloudDuration := time.Since(cloudStart)
	cloudStatus := cloudSink.Status()

	// ── Pass 2: Synthetic Selectivity / Transport Sensitivity ────────────────
	// NOT the real Hybrid J1-J5 candidate algorithm. See package doc comment.
	currentMode = "hybrid"
	hybridSink := cloudsink.New(
		sender,
		"bench-hybrid",
		"bench-cred",
		cloudsink.Config{},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		nil,
	)

	// candidateSelector picks which frames count as "candidates" for this
	// synthetic transport-sensitivity benchmark. Default is the deterministic
	// pattern below (1 candidate every 3 frames, ~33% ratio). A future test
	// can replace this with a function/slice fed by real Hybrid J1-J5
	// candidate decisions (internal/processing.MotionResult.Candidate)
	// without touching anything else in this harness.
	candidateSelector := selectSyntheticCandidates

	hybridStart := time.Now()
	candidatesSelected := 0
	for i := 0; i < totalFrames; i++ {
		isCandidate := candidateSelector(i)
		if isCandidate {
			candidatesSelected++
			f := processing.Frame{
				CandidateKey: "bench-cam",
				Timestamp:    hybridStart.Add(time.Duration(float64(i) * float64(time.Second) / targetFPS)),
				Seq:          uint64(i + 1),
				OutputWidth:  width,
				OutputHeight: height,
				Data:         frames[i],
			}
			if err := hybridSink.Route(f); err != nil {
				t.Fatalf("HybridSink.Route error: %v", err)
			}
		}
	}
	hybridDuration := time.Since(hybridStart)
	hybridStatus := hybridSink.Status()

	// ── Calculations & Comparison ────────────────────────────────────────────
	if cloudStatus.FramesUploadSucceeded == 0 || cloudStatus.JPEGBytesUploaded == 0 {
		t.Fatalf("Cloud baseline uploaded 0 frames or bytes; cannot compare")
	}

	reductionPct := (1.0 - (float64(hybridStatus.JPEGBytesUploaded) / float64(cloudStatus.JPEGBytesUploaded))) * 100.0
	candidateRatio := float64(candidatesSelected) / float64(totalFrames)

	avgCloudFrameBytes := float64(cloudStatus.JPEGBytesUploaded) / float64(cloudStatus.FramesUploadSucceeded)
	var avgHybridFrameBytes float64
	if hybridStatus.FramesUploadSucceeded > 0 {
		avgHybridFrameBytes = float64(hybridStatus.JPEGBytesUploaded) / float64(hybridStatus.FramesUploadSucceeded)
	}

	cloudEffectiveBps := float64(cloudStatus.JPEGBytesUploaded) / cloudDuration.Seconds()
	hybridEffectiveBps := float64(hybridStatus.JPEGBytesUploaded) / hybridDuration.Seconds()

	t.Logf("================================================================================")
	t.Logf("       GEO CAM SYNTHETIC SELECTIVITY / TRANSPORT SENSITIVITY BENCHMARK (J10-J11)")
	t.Logf("================================================================================")
	t.Logf("Duration: %s | Total Input Frames: %d | Stream: %dx%d @ %.1f FPS (Quality 85)", duration, totalFrames, width, height, targetFPS)
	t.Logf("Hardware State: %s", hardwareStatus)
	t.Logf("--------------------------------------------------------------------------------")
	t.Logf("J10 Cloud Baseline:               %d frames uploaded, %d bytes (avg %.0f B/frame), %.2f FPS, %.3f Mbps",
		cloudStatus.FramesUploadSucceeded, cloudStatus.JPEGBytesUploaded, avgCloudFrameBytes,
		float64(cloudStatus.FramesUploadSucceeded)/cloudDuration.Seconds(), cloudEffectiveBps*8/1_000_000)
	t.Logf("J10 Synthetic Selectivity Mode:   %d candidates uploaded, %d bytes (avg %.0f B/frame), %.2f FPS, %.3f Mbps",
		hybridStatus.FramesUploadSucceeded, hybridStatus.JPEGBytesUploaded, avgHybridFrameBytes,
		float64(hybridStatus.FramesUploadSucceeded)/hybridDuration.Seconds(), hybridEffectiveBps*8/1_000_000)
	t.Logf("J10 Candidate Ratio:              %.2f%% (%d of %d frames, fixed synthetic i%%3==0 pattern)", candidateRatio*100.0, candidatesSelected, totalFrames)
	t.Logf("J10 Bandwidth Delta:              %.2f%% reduction (SYNTHETIC result)", reductionPct)
	t.Logf("                                  Derived deliberately from the fixed 1/3 selection ratio; useful only to")
	t.Logf("                                  validate byte/transport accounting. NOT evidence of real Hybrid J1-J5")
	t.Logf("                                  motion-detector savings — that requires INTEGRATED HYBRID benchmarking,")
	t.Logf("                                  not yet implemented here.")
	t.Logf("--------------------------------------------------------------------------------")
	t.Logf("Cross-check Server Cloud Bytes:      %d (matching client: %v)", cloudBytesReceived, cloudBytesReceived == int64(cloudStatus.JPEGBytesUploaded))
	t.Logf("Cross-check Server Synthetic Bytes:  %d (matching client: %v)", hybridBytesReceived, hybridBytesReceived == int64(hybridStatus.JPEGBytesUploaded))
	t.Logf("================================================================================")

	if reductionPct <= 0 {
		t.Errorf("expected positive bandwidth reduction in synthetic selectivity mode, got %.2f%%", reductionPct)
	}
}

// selectSyntheticCandidates is the default candidate selector for the
// SYNTHETIC SELECTIVITY / TRANSPORT SENSITIVITY benchmark above: a fixed,
// deterministic pattern (1 candidate every 3 frames, ~33% ratio) with no
// relationship to real motion/event detection. It exists only to exercise
// CloudSink's byte accounting under reduced upload volume.
//
// It is intentionally a plain func(int) bool so a future test can swap it
// for a selector backed by real Hybrid J1-J5 candidate decisions (see
// internal/processing.MotionResult.Candidate in PR #19) without changing
// anything else in this harness.
func selectSyntheticCandidates(i int) bool {
	return i%3 == 0
}
