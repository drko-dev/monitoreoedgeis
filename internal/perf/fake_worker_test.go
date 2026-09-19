package perf

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestMain doubles as the fake vision worker's entry point, following the
// same re-exec pattern internal/vision already uses for its own worker tests:
// when GEOCAM_PERF_FAKE_WORKER=1 the test binary becomes a worker speaking the
// real wire protocol, so the inference harness can be exercised end to end in
// CI with no Python, no torch and no model weights.
func TestMain(m *testing.M) {
	if os.Getenv("GEOCAM_PERF_FAKE_WORKER") == "1" {
		fakeVisionWorkerMain()
		return
	}
	os.Exit(m.Run())
}

// fakeWorkerResponse mirrors internal/vision.wireResponse field for field.
// It is declared here rather than imported because that type is unexported by
// design: the harness must speak the protocol, not reach into the package.
type fakeWorkerResponse struct {
	Type         string            `json:"type"`
	Ready        bool              `json:"ready,omitempty"`
	Device       string            `json:"device,omitempty"`
	DeviceReq    string            `json:"device_requested,omitempty"`
	ModelsLoaded []string          `json:"models_loaded,omitempty"`
	FrameSeq     uint64            `json:"frame_seq,omitempty"`
	InferenceMS  float64           `json:"inference_ms,omitempty"`
	Detections   []json.RawMessage `json:"detections,omitempty"`
	Error        string            `json:"error,omitempty"`
}

func fakeVisionWorkerMain() {
	var socketPath, device string
	for i, a := range os.Args {
		if i+1 >= len(os.Args) {
			continue
		}
		switch a {
		case "--socket":
			socketPath = os.Args[i+1]
		case "--device":
			device = os.Args[i+1]
		}
	}
	if socketPath == "" {
		fmt.Fprintln(os.Stderr, "fake vision worker: no --socket given")
		os.Exit(2)
	}
	_ = os.Remove(socketPath)
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fake vision worker: listen: %v\n", err)
		os.Exit(2)
	}
	defer ln.Close()

	conn, err := ln.Accept()
	if err != nil {
		os.Exit(2)
	}
	defer conn.Close()

	delay := 0 * time.Millisecond
	if raw := os.Getenv("GEOCAM_PERF_FAKE_WORKER_DELAY_MS"); raw != "" {
		if ms, perr := strconv.Atoi(raw); perr == nil {
			delay = time.Duration(ms) * time.Millisecond
		}
	}

	rd := bufio.NewReader(conn)
	for {
		line, err := rd.ReadBytes('\n')
		if err != nil {
			return
		}
		var req struct {
			Type     string `json:"type"`
			FrameSeq uint64 `json:"frame_seq"`
		}
		if err := json.Unmarshal(line, &req); err != nil {
			_, _ = conn.Write([]byte(`{"type":"error","error":"bad json"}` + "\n"))
			continue
		}
		switch req.Type {
		case "health":
			writeFakeResponse(conn, fakeWorkerResponse{
				Type: "health_ok", Ready: true,
				// The fake echoes the requested device, exactly like
				// deploy/vision-worker/backend.py did before it resolved one —
				// which is what makes it useful for pinning the harness's
				// honesty about device_effective.
				Device: device, DeviceReq: device,
				ModelsLoaded: []string{"yolo11s-pose.pt", "yolo11n.pt"},
			})
		case "infer":
			if delay > 0 {
				time.Sleep(delay)
			}
			// Report the delay it actually spent, the way a real worker
			// reports its measured forward-pass time. A constant here would
			// make the harness's own sanity check ("the production path
			// cannot be faster than the isolated inference it contains")
			// meaningless.
			writeFakeResponse(conn, fakeWorkerResponse{
				Type: "result", FrameSeq: req.FrameSeq, InferenceMS: float64(delay) / float64(time.Millisecond),
				Detections: []json.RawMessage{json.RawMessage(
					`{"class_id":0,"label":"person","type":"person","confidence":0.91,"bbox":[1,2,3,4]}`)},
			})
		case "shutdown":
			writeFakeResponse(conn, fakeWorkerResponse{Type: "health_ok"})
			return
		default:
			writeFakeResponse(conn, fakeWorkerResponse{Type: "error", Error: "unknown request type"})
		}
	}
}

func writeFakeResponse(conn net.Conn, resp fakeWorkerResponse) {
	b, err := json.Marshal(resp)
	if err != nil {
		return
	}
	_, _ = conn.Write(append(b, '\n'))
}

// writeFakeModels creates the two weight files a ModelManager only stats.
func writeFakeModels(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"yolo11s-pose.pt", "yolo11n.pt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("not real weights"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func smokeYUVFrames(count, width, height int) [][]byte {
	out := make([][]byte, 0, count)
	for i := 0; i < count; i++ {
		out = append(out, patternYUV420P(width, height, i))
	}
	return out
}

// TestInferenceHarnessSmoke_WithFakeWorker drives the whole production
// inference path — internal/vision.Worker spawning a subprocess, the health
// handshake, one-at-a-time inference over the Unix socket, and
// internal/vision.Sink.Route encoding raw yuv420p frames — with only the
// model replaced. It proves the harness's counters, warm-up separation and
// report fields work, and asserts the run is NOT presented as real inference.
func TestInferenceHarnessSmoke_WithFakeWorker(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a subprocess; skipped in -short mode")
	}
	t.Setenv("GEOCAM_PERF_FAKE_WORKER", "1")
	t.Setenv("GEOCAM_PERF_FAKE_WORKER_DELAY_MS", "8")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	const samples = 4
	res, err := RunInference(ctx, InferenceOptions{
		WorkerCmd:    os.Args[0],
		Kind:         WorkerFake,
		ModelsDir:    writeFakeModels(t),
		PersonModel:  "yolo11s-pose.pt",
		VehicleModel: "yolo11n.pt",
		Device:       "cpu",
		ImgSize:      640,
		SocketPath:   tempSocketPath(),
		Warmup:       1,
		Samples:      samples,
		YUV:          smokeYUVFrames(3, 64, 64),
		YUVWidth:     64,
		YUVHeight:    64,
	})
	if err != nil {
		t.Fatalf("RunInference: %v", err)
	}
	if res.Real {
		t.Error("a fake worker must never be reported as real inference performance")
	}
	if res.WorkerState != "ready" {
		t.Errorf("worker state = %q, want ready", res.WorkerState)
	}
	if res.DeviceRequested != "cpu" || res.DeviceEffective != "cpu" {
		t.Errorf("device requested/effective = %q/%q, want cpu/cpu", res.DeviceRequested, res.DeviceEffective)
	}
	if !strings.Contains(res.DeviceResolutionLabel, "concrete device") {
		t.Errorf("device label = %q, want it to acknowledge a concrete requested device", res.DeviceResolutionLabel)
	}
	if len(res.WarmupMS) != 1 {
		t.Errorf("warm-up samples recorded = %d, want 1", len(res.WarmupMS))
	}
	if res.WarmupNote == "" {
		t.Error("a report with warm-up must state that warm-up is excluded from the statistics")
	}
	if res.Samples != samples {
		t.Errorf("measured samples = %d, want %d", res.Samples, samples)
	}
	if res.WorkerInferenceMS.Count != samples {
		t.Errorf("worker inference latency samples = %d, want %d", res.WorkerInferenceMS.Count, samples)
	}
	if res.SinkRouteMS.Count != samples {
		t.Errorf("sink route latency samples = %d, want %d", res.SinkRouteMS.Count, samples)
	}
	if res.WorkerInferenceMS.P50MS != 8 {
		t.Errorf("worker p50 = %v, want the worker-reported 8ms", res.WorkerInferenceMS.P50MS)
	}
	if res.WorkerInferenceFPS <= 0 || res.SinkRouteFPS <= 0 {
		t.Errorf("inference rates must be positive: worker=%v sink=%v", res.WorkerInferenceFPS, res.SinkRouteFPS)
	}
	if res.SinkRouteMS.P50MS < res.WorkerInferenceMS.P50MS {
		t.Errorf("the production sink path (%.2fms) cannot be faster than the isolated inference (%.2fms): encoding is being skipped",
			res.SinkRouteMS.P50MS, res.WorkerInferenceMS.P50MS)
	}
	if res.InferenceErrors != 0 {
		t.Errorf("inference errors = %d, want 0", res.InferenceErrors)
	}
	if res.DetectionsTotal != samples {
		t.Errorf("detections = %d, want %d (one per isolated inference)", res.DetectionsTotal, samples)
	}
	if len(res.ModelSHA256) != 2 {
		t.Errorf("model checksums recorded = %d, want 2", len(res.ModelSHA256))
	}
	var sawFakeLimitation bool
	for _, l := range res.Limitations {
		if strings.Contains(l, "fake worker") {
			sawFakeLimitation = true
		}
	}
	if !sawFakeLimitation {
		t.Errorf("limitations must state that a fake worker proves nothing about model performance: %v", res.Limitations)
	}

	// The inference block must serialize, including the honesty fields.
	blob, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"real_inference_performance_validated":false`, `"device_effective"`, `"worker_inference_latency"`, `"sink_route_latency"`, `"p95_ms"`, `"model_sha256"`} {
		if !strings.Contains(string(blob), want) {
			t.Errorf("inference JSON is missing %s", want)
		}
	}
}

// TestInferenceHarnessSmoke_DeviceEchoIsNotCalledEffective pins the harness's
// honesty about section 4 of Hito X: when the worker only echoes the
// requested device, the report must say the effective device is not verified
// rather than presenting the echo as a measurement.
func TestInferenceHarnessSmoke_DeviceEchoIsNotCalledEffective(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a subprocess; skipped in -short mode")
	}
	t.Setenv("GEOCAM_PERF_FAKE_WORKER", "1")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	res, err := RunInference(ctx, InferenceOptions{
		WorkerCmd:    os.Args[0],
		Kind:         WorkerFake,
		ModelsDir:    writeFakeModels(t),
		PersonModel:  "yolo11s-pose.pt",
		VehicleModel: "yolo11n.pt",
		Device:       "auto",
		ImgSize:      640,
		SocketPath:   tempSocketPath(),
		Warmup:       0,
		Samples:      1,
		YUV:          smokeYUVFrames(1, 64, 64),
		YUVWidth:     64,
		YUVHeight:    64,
	})
	if err != nil {
		t.Fatalf("RunInference: %v", err)
	}
	if res.DeviceRequested != "auto" || res.DeviceEffective != "auto" {
		t.Fatalf("device requested/effective = %q/%q, want auto/auto (the fake echoes)", res.DeviceRequested, res.DeviceEffective)
	}
	if !strings.Contains(res.DeviceResolutionLabel, "NOT independently verified") {
		t.Errorf("an echoed device must be labelled unverified, got %q", res.DeviceResolutionLabel)
	}
}

// TestRunInference_RefusesToReportWithoutModelWeights documents the
// NOT VALIDATED path: with no weights present the harness must fail loudly
// instead of producing a number for a model that never loaded.
func TestRunInference_RefusesToReportWithoutModelWeights(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := RunInference(ctx, InferenceOptions{
		WorkerCmd:    "/bin/true",
		Kind:         WorkerRealYOLO,
		ModelsDir:    t.TempDir(), // deliberately empty
		PersonModel:  "yolo11s-pose.pt",
		VehicleModel: "yolo11n.pt",
		SocketPath:   tempSocketPath(),
		Samples:      1,
	})
	if err == nil {
		t.Fatal("expected an error when the model weights are absent")
	}
	if !strings.Contains(err.Error(), "model weights") {
		t.Errorf("error should explain that model weights are missing, got %v", err)
	}
}

func TestSyntheticFrames_AreDeterministicAndDecodable(t *testing.T) {
	a, err := SyntheticFrames(2, 64, 64)
	if err != nil {
		t.Fatal(err)
	}
	b, err := SyntheticFrames(2, 64, 64)
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != 2 || len(b) != 2 {
		t.Fatalf("expected 2 frames each, got %d and %d", len(a), len(b))
	}
	for i := range a {
		if string(a[i]) != string(b[i]) {
			t.Fatalf("frame %d is not deterministic", i)
		}
		if len(a[i]) < 100 {
			t.Fatalf("frame %d is only %d bytes: a degenerate frame would understate encoding cost", i, len(a[i]))
		}
	}
	if string(a[0]) == string(a[1]) {
		t.Error("consecutive synthetic frames must differ, otherwise inference sees a static scene")
	}
}
