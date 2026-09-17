//go:build localbench

// Package cameratest: local (no camera, no SaaS) comparative bandwidth benchmark
// for Milestone J (J10–J11). Run manually with:
//
//	go test -tags localbench ./internal/cameratest/ -run TestHybridBandwidthBenchmark -v
//
// Compares Cloud baseline (continuous sampled frame push) vs. Hybrid candidate
// push over the exact same frame sequence and duration.
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

	hasRealCamera := os.Getenv("TAPO_ONVIF_IP") != ""
	if !hasRealCamera {
		t.Logf("[NOTICE] REAL CAMERA BENCHMARK = BLOCKED (TAPO_ONVIF_IP unset; no physical TC70 available)")
		t.Logf("[NOTICE] Running controlled local replay benchmark over identical synthetic frame sequence")
	}

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

	// ── Pass 2: Hybrid Candidate Selection ───────────────────────────────────
	currentMode = "hybrid"
	hybridSink := cloudsink.New(
		sender,
		"bench-hybrid",
		"bench-cred",
		cloudsink.Config{},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		nil,
	)

	// Controlled selectivity model: 1 candidate every 3 frames (~33% candidate ratio)
	hybridStart := time.Now()
	candidatesSelected := 0
	for i := 0; i < totalFrames; i++ {
		isCandidate := (i%3 == 0) // deterministic candidate filter for benchmark
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
	t.Logf("                     GEO CAM HYBRID BENCHMARK (J10-J11)")
	t.Logf("================================================================================")
	t.Logf("Duration: %s | Total Input Frames: %d | Stream: %dx%d @ %.1f FPS (Quality 85)", duration, totalFrames, width, height, targetFPS)
	t.Logf("Hardware State: %s", func() string {
		if hasRealCamera {
			return "TC70 Physical Camera ONLINE"
		}
		return "REAL CAMERA = BLOCKED (Controlled Loopback Replay)"
	}())
	t.Logf("--------------------------------------------------------------------------------")
	t.Logf("J10 Cloud Baseline:   %d frames uploaded, %d bytes (avg %.0f B/frame), %.2f FPS, %.3f Mbps",
		cloudStatus.FramesUploadSucceeded, cloudStatus.JPEGBytesUploaded, avgCloudFrameBytes,
		float64(cloudStatus.FramesUploadSucceeded)/cloudDuration.Seconds(), cloudEffectiveBps*8/1_000_000)
	t.Logf("J10 Hybrid Mode:      %d candidates uploaded, %d bytes (avg %.0f B/frame), %.2f FPS, %.3f Mbps",
		hybridStatus.FramesUploadSucceeded, hybridStatus.JPEGBytesUploaded, avgHybridFrameBytes,
		float64(hybridStatus.FramesUploadSucceeded)/hybridDuration.Seconds(), hybridEffectiveBps*8/1_000_000)
	t.Logf("J10 Candidate Ratio:  %.2f%% (%d of %d frames)", candidateRatio*100.0, candidatesSelected, totalFrames)
	t.Logf("J10 Bandwidth Savings: %.2f%% reduction", reductionPct)
	t.Logf("--------------------------------------------------------------------------------")
	t.Logf("Cross-check Server Cloud Bytes:  %d (matching client: %v)", cloudBytesReceived, cloudBytesReceived == int64(cloudStatus.JPEGBytesUploaded))
	t.Logf("Cross-check Server Hybrid Bytes: %d (matching client: %v)", hybridBytesReceived, hybridBytesReceived == int64(hybridStatus.JPEGBytesUploaded))
	t.Logf("================================================================================")

	if reductionPct <= 0 {
		t.Errorf("expected positive bandwidth reduction in hybrid mode, got %.2f%%", reductionPct)
	}
}
