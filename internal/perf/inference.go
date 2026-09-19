package perf

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/processing"
	"github.com/drko-dev/monitoreoedgeis/internal/vision"
)

// WorkerKind labels what actually produced the inference numbers, so a
// harness-only run can never be mistaken for a real YOLO measurement.
type WorkerKind string

const (
	// WorkerRealYOLO is the production path: deploy/vision-worker/worker.py
	// under a Python interpreter with Ultralytics/PyTorch installed, loading
	// the two model files this repo documents (yolo11s-pose.pt, yolo11n.pt).
	WorkerRealYOLO WorkerKind = "real-yolo"
	// WorkerFake is any stand-in that speaks the wire protocol but runs no
	// model. It validates the harness end to end and measures nothing else.
	WorkerFake WorkerKind = "fake-worker"
)

// InferenceOptions configures one inference measurement.
type InferenceOptions struct {
	// WorkerCmd/WorkerArgs are exactly the fields the agent's own
	// internal/agent.vision_module.buildVisionSink passes to
	// vision.NewWorker.
	WorkerCmd  string
	WorkerArgs []string
	Kind       WorkerKind

	ModelsDir         string
	PersonModel       string
	VehicleModel      string
	Device            string
	ImgSize           int
	PersonConfidence  float64
	VehicleConfidence float64
	NMSIoU            float64

	SocketPath   string
	StartTimeout time.Duration
	InferTimeout time.Duration

	// YUV/YUVWidth/YUVHeight are the real decoded frames the pipeline
	// produced. When empty, JPEG may be supplied instead; when both are
	// empty the harness falls back to synthetic frames and says so.
	YUV                 [][]byte
	YUVWidth, YUVHeight int
	JPEG                [][]byte

	FrameSourceLabel string
	Warmup           int
	Samples          int
	// InferTimeoutPerSample bounds each individual inference; a timeout is
	// counted as an error rather than silently shortening the sample set.
	PythonPath string
	Logger     *slog.Logger
}

// InferenceResult is the machine-readable outcome of one inference
// measurement. Every latency statistic states its own sample count.
type InferenceResult struct {
	Kind WorkerKind `json:"kind"`
	// Real is true only when a real worker reached ready with real model
	// files loaded. Harness-only runs are explicitly not real.
	Real bool `json:"real_inference_performance_validated"`

	// DeviceRequested is what the agent config asked for; DeviceEffective is
	// what the worker's health handshake reported. DeviceResolutionLabel
	// states how trustworthy the second one is — the worker has historically
	// echoed the request rather than resolving it, and a report must not
	// present an echo as a verified effective device.
	DeviceRequested       string            `json:"device_requested"`
	DeviceEffective       string            `json:"device_effective"`
	DeviceResolutionLabel string            `json:"device_effective_source"`
	WorkerState           string            `json:"worker_state"`
	ModelsLoaded          []string          `json:"models_loaded,omitempty"`
	ModelSHA256           map[string]string `json:"model_sha256,omitempty"`
	ImgSize               int               `json:"imgsz"`
	PersonConfidence      float64           `json:"person_confidence"`
	VehicleConfidence     float64           `json:"vehicle_confidence"`
	NMSIoU                float64           `json:"nms_iou"`
	JPEGQuality           int               `json:"jpeg_quality"`

	FrameSource string `json:"frame_source"`
	FrameWidth  int    `json:"frame_width"`
	FrameHeight int    `json:"frame_height"`

	Warmup     int       `json:"warmup_inferences"`
	WarmupMS   []float64 `json:"warmup_ms,omitempty"`
	WarmupNote string    `json:"warmup_note,omitempty"`
	Samples    int       `json:"measured_samples"`

	// WorkerInferenceMS is the worker's own per-inference timing for the
	// isolated pass (worker.Infer with a pre-encoded JPEG), i.e. model
	// forward-pass time as the worker measures it. SinkRouteMS is the full
	// production vision.Sink.Route: yuv420p -> image -> JPEG -> socket ->
	// worker -> YOLO, which is what an Edge actually pays per sampled frame.
	WorkerInferenceMS LatencyStats `json:"worker_inference_latency"`
	SinkRouteMS       LatencyStats `json:"sink_route_latency"`
	// EncodeAndIPCMS is SinkRoute p50 minus WorkerInference p50: the median
	// cost of JPEG encoding plus the round trip, derived from two medians and
	// therefore an estimate, not a per-frame decomposition.
	EncodeAndIPCMS float64 `json:"encode_and_ipc_p50_estimate_ms"`

	// WorkerInferenceFPS is measured samples per second of summed worker
	// inference time (model throughput). SinkRouteFPS is measured samples per
	// second of wall clock through the production sink (edge throughput,
	// including encoding). They are reported separately rather than blended.
	WorkerInferenceFPS float64 `json:"worker_inference_fps"`
	SinkRouteFPS       float64 `json:"sink_route_fps"`
	WorkerWallMS       float64 `json:"worker_pass_wall_ms"`
	SinkWallMS         float64 `json:"sink_pass_wall_ms"`

	InferenceErrors      int64 `json:"inference_errors"`
	WorkerReportedErrors int64 `json:"worker_reported_inference_errors"`
	DetectionsTotal      int   `json:"detections_total"`
	FramesWithDetections int   `json:"frames_with_detections"`

	Host []string `json:"host_device_notes,omitempty"`
	// Limitations are per-run honesty notes (a fake worker, synthetic frames,
	// a device value the worker only echoed).
	Limitations []string `json:"limitations,omitempty"`
}

// RunInference measures inference throughput through the production
// internal/vision.Worker and internal/vision.Sink.
func RunInference(ctx context.Context, opts InferenceOptions) (InferenceResult, error) {
	if opts.Logger == nil {
		opts.Logger = discardLogger()
	}
	if opts.WorkerCmd == "" {
		return InferenceResult{}, fmt.Errorf("perf: no worker command configured")
	}
	if opts.Samples <= 0 {
		opts.Samples = 50
	}
	if opts.Warmup < 0 {
		opts.Warmup = 0
	}
	if opts.InferTimeout <= 0 {
		opts.InferTimeout = 30 * time.Second
	}
	if opts.StartTimeout <= 0 {
		opts.StartTimeout = 120 * time.Second
	}
	if opts.Kind == "" {
		opts.Kind = WorkerRealYOLO
	}
	if opts.SocketPath == "" {
		opts.SocketPath = tempSocketPath()
	}

	res := InferenceResult{
		Kind:              opts.Kind,
		DeviceRequested:   opts.Device,
		ImgSize:           opts.ImgSize,
		PersonConfidence:  opts.PersonConfidence,
		VehicleConfidence: opts.VehicleConfidence,
		NMSIoU:            opts.NMSIoU,
		JPEGQuality:       JPEGQuality,
		Warmup:            opts.Warmup,
	}

	models := vision.NewModelManager(opts.ModelsDir, opts.PersonModel, opts.VehicleModel)
	if !models.Ready() {
		return res, fmt.Errorf("perf: model weights are not both present in %s (%s, %s): real inference cannot be measured",
			opts.ModelsDir, opts.PersonModel, opts.VehicleModel)
	}
	res.ModelSHA256 = make(map[string]string, 2)
	for _, name := range []string{opts.PersonModel, opts.VehicleModel} {
		if sum, err := models.Checksum(name); err == nil {
			res.ModelSHA256[name] = sum
		}
	}

	worker := vision.NewWorker(vision.Config{
		WorkerCmd:         opts.WorkerCmd,
		WorkerArgs:        opts.WorkerArgs,
		ModelsDir:         opts.ModelsDir,
		PersonModel:       opts.PersonModel,
		VehicleModel:      opts.VehicleModel,
		PersonConfidence:  opts.PersonConfidence,
		VehicleConfidence: opts.VehicleConfidence,
		NMSIoU:            opts.NMSIoU,
		Device:            opts.Device,
		ImgSize:           opts.ImgSize,
		SocketPath:        opts.SocketPath,
		StartTimeout:      opts.StartTimeout,
		InferTimeout:      opts.InferTimeout,
	}, models, opts.Logger)

	sink := vision.NewSink(worker, models, nil, nil, opts.Logger)

	if err := worker.Start(ctx); err != nil {
		return res, fmt.Errorf("perf: start vision worker: %w", err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = worker.Stop(stopCtx)
	}()

	readyCtx, cancelReady := context.WithTimeout(ctx, opts.StartTimeout)
	readyErr := worker.WaitForReady(readyCtx)
	cancelReady()
	status := worker.Status()
	res.WorkerState = status.State
	res.DeviceEffective = status.Device
	// Prefer what the worker says it was asked for: the caller's option only
	// records the harness's intent, while the handshake value is what actually
	// reached the worker.
	if status.DeviceRequested != "" {
		res.DeviceRequested = status.DeviceRequested
	}
	res.DeviceResolutionLabel = DeviceResolutionLabel(res.DeviceRequested, status.Device)
	res.ModelsLoaded = status.ModelsLoaded
	res.Real = opts.Kind == WorkerRealYOLO && readyErr == nil && status.State == vision.StateReady

	if readyErr != nil {
		res.Limitations = append(res.Limitations,
			"vision worker never reached ready: "+readyErr.Error()+"; no inference FPS was produced")
		return res, fmt.Errorf("perf: vision worker not ready: %w", readyErr)
	}

	frames, err := inferenceFrames(opts)
	if err != nil {
		return res, err
	}
	res.FrameSource = opts.FrameSourceLabel
	res.FrameWidth, res.FrameHeight = opts.YUVWidth, opts.YUVHeight
	if res.FrameSource == "" {
		res.FrameSource = "synthetic (harness-generated, not camera footage)"
	}

	// Warm-up is measured and reported separately, never folded into the
	// steady-state statistic: the first Ultralytics call pays one-time model
	// and backend initialization, which on a CPU-only host is orders of
	// magnitude larger than a steady-state inference.
	var warmupTimes []float64
	for i := 0; i < opts.Warmup; i++ {
		f := frameFor(i, opts)
		start := time.Now()
		if err := sink.Route(f); err != nil {
			res.Limitations = append(res.Limitations, "warm-up inference failed: "+err.Error())
			break
		}
		warmupTimes = append(warmupTimes, RoundMS(MS(time.Since(start))))
	}
	res.WarmupMS = warmupTimes
	if len(warmupTimes) > 0 {
		res.WarmupNote = "warm-up samples are reported separately and excluded from every latency statistic; " +
			"the first call includes one-time model/backend initialization"
	}

	// Pass A: isolated worker inference with a pre-encoded JPEG, so the
	// worker's own inference_ms is available per frame (Sink.Route does not
	// surface it).
	inferLatency := make([]float64, 0, opts.Samples)
	var inferSum time.Duration
	passAStart := time.Now()
	for i := 0; i < opts.Samples; i++ {
		req := vision.InferRequest{
			CandidateKey: "perf-infer",
			FrameSeq:     uint64(i),
			Timestamp:    time.Now(),
			Width:        opts.YUVWidth,
			Height:       opts.YUVHeight,
			JPEG:         frames[i%len(frames)],
		}
		start := time.Now()
		out, err := worker.Infer(ctx, req)
		elapsed := time.Since(start)
		if err != nil {
			res.InferenceErrors++
			continue
		}
		inferSum += elapsed
		if out.InferenceMS > 0 {
			inferLatency = append(inferLatency, out.InferenceMS)
		}
		res.DetectionsTotal += len(out.Detections)
		if len(out.Detections) > 0 {
			res.FramesWithDetections++
		}
	}
	res.WorkerWallMS = RoundMS(MS(time.Since(passAStart)))
	res.WorkerInferenceMS = SummarizeMS(inferLatency)
	res.WorkerInferenceFPS = FPS(int64(len(inferLatency)), inferSum)

	// Pass B: the full production Sink.Route path on raw yuv420p frames.
	routeLatency := make([]float64, 0, opts.Samples)
	passBStart := time.Now()
	for i := 0; i < opts.Samples; i++ {
		f := frameFor(i, opts)
		start := time.Now()
		err := sink.Route(f)
		routeLatency = append(routeLatency, RoundMS(MS(time.Since(start))))
		if err != nil {
			res.InferenceErrors++
		}
	}
	res.SinkWallMS = RoundMS(MS(time.Since(passBStart)))
	res.SinkRouteMS = SummarizeMS(routeLatency)
	res.SinkRouteFPS = FPS(int64(len(routeLatency)), time.Since(passBStart))
	res.Samples = opts.Samples
	res.WorkerReportedErrors = worker.Status().InferenceErrors
	if res.WorkerInferenceMS.Count > 0 && res.SinkRouteMS.Count > 0 {
		res.EncodeAndIPCMS = RoundMS(res.SinkRouteMS.P50MS - res.WorkerInferenceMS.P50MS)
	}

	host := ProbeHost(ctx, opts.PythonPath)
	res.Host = append([]string{
		fmt.Sprintf("host %s/%s, %d CPUs", host.GOOS, host.GOARCH, host.NumCPU),
		fmt.Sprintf("torch %s, ultralytics %s, cuda %s, mps %s",
			orUnknown(host.TorchVersion), orUnknown(host.UltralyticsVersion), host.CUDAReported, host.MPSReported),
	}, host.Notes...)

	if opts.Kind != WorkerRealYOLO {
		res.Limitations = append(res.Limitations,
			"fake worker: these numbers validate the harness and describe no model's performance")
	}
	if len(opts.YUV) == 0 {
		res.Limitations = append(res.Limitations,
			"frames are synthetic, so the reported latency does not reflect real camera content")
	}
	if host.CUDAReported == "unavailable" {
		res.Limitations = append(res.Limitations,
			"CUDA is unavailable on this host: no GPU number was measured and CPU numbers must not be extrapolated to a GPU")
	}
	if res.DeviceEffective == "" {
		res.Limitations = append(res.Limitations, "the worker did not report an effective device")
	}
	return res, nil
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

// DeviceResolutionLabel states how much the worker's reported device can be
// trusted. It is deliberately conservative: a value identical to the request
// is labelled an echo, because a worker that never resolves the request would
// report exactly the same thing.
func DeviceResolutionLabel(requested, effective string) string {
	switch {
	case effective == "":
		return "not reported by the worker"
	case effective != requested:
		return "worker-resolved (differs from the request, so the worker really resolved it)"
	case requested == "cpu" || requested == "cuda":
		return "worker-reported concrete device, equal to the request; Ultralytics is driven with exactly this value"
	default:
		return "worker echoed the requested value; the effective device is NOT independently verified by this harness"
	}
}

// inferenceFrames returns the JPEG frames the worker will be fed, preferring
// real decoded footage.
func inferenceFrames(opts InferenceOptions) ([][]byte, error) {
	if len(opts.JPEG) > 0 {
		return opts.JPEG, nil
	}
	if len(opts.YUV) > 0 {
		w, h := opts.YUVWidth, opts.YUVHeight
		if w <= 0 || h <= 0 {
			return nil, fmt.Errorf("perf: raw frames supplied without width/height")
		}
		out := make([][]byte, 0, len(opts.YUV))
		for i, raw := range opts.YUV {
			jpg, err := FrameJPEG(raw, w, h)
			if err != nil {
				return nil, fmt.Errorf("perf: encode decoded frame %d: %w", i, err)
			}
			out = append(out, jpg)
		}
		return out, nil
	}
	return SyntheticFrames(8, 640, 360)
}

func frameFor(i int, opts InferenceOptions) processing.Frame {
	if len(opts.YUV) > 0 && len(opts.JPEG) == 0 {
		// Sink.Route re-encodes from raw yuv420p; give it a real decoded
		// frame so the measured encode cost is the production one.
		raw := opts.YUV[i%len(opts.YUV)]
		return processing.Frame{
			CandidateKey:  "perf-infer",
			Timestamp:     time.Now(),
			Seq:           uint64(i),
			OutputWidth:   opts.YUVWidth,
			OutputHeight:  opts.YUVHeight,
			SourceWidth:   opts.YUVWidth,
			SourceHeight:  opts.YUVHeight,
			Codec:         "H264",
			StreamRole:    "sub",
			Data:          raw,
			CorrelationID: fmt.Sprintf("perf-infer-%d", i),
		}
	}
	// JPEG-only or synthetic: Sink.Route re-encodes from raw yuv420p, so it
	// needs a raw frame. The synthetic buffer below carries the same kind of
	// content the synthetic JPEGs do (a moving gradient), so pass B's encode
	// cost stays representative even though the exact pixels differ from the
	// isolated pass.
	return processing.Frame{
		CandidateKey:  "perf-infer",
		Timestamp:     time.Now(),
		Seq:           uint64(i),
		OutputWidth:   opts.YUVWidth,
		OutputHeight:  opts.YUVHeight,
		Codec:         "H264",
		StreamRole:    "sub",
		Data:          syntheticYUV(opts.YUVWidth, opts.YUVHeight, i),
		CorrelationID: fmt.Sprintf("perf-infer-%d", i),
	}
}

func syntheticYUV(width, height, i int) []byte {
	if width <= 0 {
		width = 640
	}
	if height <= 0 {
		height = 360
	}
	ySize := width * height
	cSize := (width / 2) * (height / 2)
	buf := make([]byte, ySize+2*cSize)
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			buf[y*width+x] = byte((x + y + i*3) % 256)
		}
	}
	for j := 0; j < cSize; j++ {
		buf[ySize+j] = byte(96 + (j+i)%64)
	}
	return buf
}
