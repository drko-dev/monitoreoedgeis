package perf

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// SchemaVersion identifies the JSON shape of a report. It changes whenever a
// field is removed or its meaning changes, so a stored result can never be
// silently misread by a newer tool.
const SchemaVersion = "geocam-perf/hito-x/1"

// Report is the complete, machine-readable evidence of one benchmark
// invocation. It is the artifact that makes a number auditable: it carries
// the host, the exact video source, every tunable, and the component
// counters the derived rates came from.
type Report struct {
	SchemaVersion string    `json:"schema_version"`
	Kind          string    `json:"kind"`
	GeneratedAt   time.Time `json:"generated_at"`
	Harness       string    `json:"harness"`

	Host HostInfo  `json:"host"`
	Clip *ClipInfo `json:"clip,omitempty"`

	// Decode holds one entry per measured configuration (for example the
	// production pipeline at real-time pacing, the same pipeline at maximum
	// pacing, and the decoder driven directly), so a reader can see both
	// "does it keep up" and "where does it top out".
	Decode    []DecodeResult   `json:"decode,omitempty"`
	Inference *InferenceResult `json:"inference,omitempty"`

	// Environment records the harness-relevant environment variables that
	// were set, so a stored report can be reproduced exactly.
	Environment map[string]string `json:"environment,omitempty"`
	// Limitations are the honest caveats for this run as a whole.
	Limitations []string `json:"limitations,omitempty"`
}

// perfEnvVars are the variables the harness itself reads. Recording them
// makes a stored report reproducible without guessing.
var perfEnvVars = []string{
	"GEOCAM_PERF",
	"GEOCAM_PERF_CLIP",
	"GEOCAM_PERF_CLIP_FILE",
	"GEOCAM_PERF_OUT",
	"GEOCAM_PERF_DECODE_ONLY",
	"GEOCAM_PERF_INFER_ONLY",
	"GEOCAM_PERF_INFER_WARMUP",
	"GEOCAM_PERF_INFER_SAMPLES",
	"GEOCAM_PERF_KEEP_FRAMES",
	"GEOCAM_PERF_PYTHON",
	"GEOCAM_PERF_WORKER_CMD",
	"GEOCAM_PERF_MODELS_DIR",
	"GEOCAM_PERF_DEVICE",
	"GEOCAM_PERF_IMGSZ",
	"GEOCAM_PERF_SAMPLE_FPS",
	"GEOCAM_PERF_FFMPEG",
	"GEOCAM_PERF_FFPROBE",
}

// CaptureEnvironment records the harness-relevant environment.
func CaptureEnvironment() map[string]string {
	out := make(map[string]string)
	for _, k := range perfEnvVars {
		if v, ok := os.LookupEnv(k); ok && strings.TrimSpace(v) != "" {
			out[k] = v
		}
	}
	return out
}

// WriteJSON writes the report as indented JSON, creating the parent
// directory. JSON is the primary artifact format because Hito X's metrics
// must be machine-readable.
func (r Report) WriteJSON(path string) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return os.WriteFile(path, b, 0o644)
}

// Summary renders a short human-readable block for `go test -v` logs. It
// never replaces the JSON: the log is for a person watching a run, the JSON
// is the evidence.
func (r Report) Summary(w io.Writer) {
	fmt.Fprintf(w, "=== GEO CAM Edge — Hito X performance report (%s) ===\n", r.SchemaVersion)
	fmt.Fprintf(w, "kind:      %s\n", r.Kind)
	fmt.Fprintf(w, "generated: %s\n", r.GeneratedAt.Format(time.RFC3339))
	fmt.Fprintf(w, "host:      %s/%s  %d CPUs  %s  mem=%s\n",
		r.Host.GOOS, r.Host.GOARCH, r.Host.NumCPU, orUnknown(r.Host.CPUModel), humanBytes(r.Host.MemoryBytes))
	fmt.Fprintf(w, "toolchain: %s | %s | %s\n", orUnknown(r.Host.FFmpegVersion), orUnknown(r.Host.PythonVersion), orUnknown(r.Host.TorchVersion))
	fmt.Fprintf(w, "accelerat: cuda=%s mps=%s\n", r.Host.CUDAReported, r.Host.MPSReported)

	if c := r.Clip; c != nil {
		fmt.Fprintf(w, "\n-- video source --\n")
		fmt.Fprintf(w, "  codec=%s profile=%s %dx%d nominal_fps=%g frames=%d bytes=%d\n",
			c.Codec, orUnknown(c.Profile), c.Width, c.Height, c.NominalSourceFPS, c.CodedFrames, c.Bytes)
		fmt.Fprintf(w, "  sha256=%s\n", c.SHA256)
		fmt.Fprintf(w, "  access_unit_boundary=%s regenerated_identical=%t\n", c.AccessUnitBoundary, c.RegeneratedIdentical)
	}

	for _, d := range r.Decode {
		pacing := string(d.Pacing)
		if d.PacingFPS > 0 {
			pacing = fmt.Sprintf("%s @%g fps", d.Pacing, d.PacingFPS)
		}
		fmt.Fprintf(w, "\n-- decode (%s / pacing=%s) --\n", d.Mode, pacing)
		if d.DecoderFake {
			fmt.Fprintf(w, "  FAKE DECODER: these numbers validate the harness, not ffmpeg\n")
		}
		fmt.Fprintf(w, "  frames_fed=%d rtp_packets_sent=%d rtp_packets_received=%d",
			d.FramesFed, d.RTPPacketsSent, d.RTPPacketsReceived)
		if d.Mode == DecodeModePipeline {
			fmt.Fprintf(w, " delivery_ratio=%.4f", d.TransportDeliveryRatio)
		}
		fmt.Fprintf(w, "\n")
		fmt.Fprintf(w, "  frames_decoded=%d frames_sampled=%d frames_dropped=%d sink_frames=%d\n",
			d.FramesDecoded, d.FramesSampled, d.FramesDropped, d.SinkFrames)
		fmt.Fprintf(w, "  priming: frames=%d priming_ms=%.1f ttff=%.1fms decoder_start=%.1fms\n",
			d.PrimingFrames, d.PrimingMS, d.TimeToFirstFrameMS, d.DecoderStartMS)
		fmt.Fprintf(w, "  feed=%.1fms elapsed=%.1fms harness_feed_fps=%.2f ingest_fps=%.2f decode_fps=%.2f sampled_fps=%.2f\n",
			d.FeedElapsedMS, d.ElapsedMS, d.HarnessFeedFPS, d.IngestFPS, d.DecodeFPS, d.SampleFPS)
		fmt.Fprintf(w, "  decoder_buffered_lag_frames=%d tail_flushed=%d tail_flush_ms=%.1f decode_fps_end_to_end=%.2f\n",
			d.BufferedLagFrames, d.TailFramesFlushed, d.TailFlushMS, d.DecodeFPSEndToEnd)
		fmt.Fprintf(w, "  steady_state=%t decode/ingest=%.3f\n", d.SteadyStateAchieved, d.DecodeToIngestRatio)
		if !d.SteadyStateAchieved && d.Mode == DecodeModePipeline {
			fmt.Fprintf(w, "  NOTE: this window was not steady state (the decoder was still catching up while measured),\n"+
				"        so decode_fps describes a warming decoder. Read decode_fps_end_to_end instead.\n")
		}
		if d.DecodeLatency.Count > 0 {
			fmt.Fprintf(w, "  decode_latency_ms: n=%d p50=%.2f p95=%.2f p99=%.2f max=%.2f\n",
				d.DecodeLatency.Count, d.DecodeLatency.P50MS, d.DecodeLatency.P95MS, d.DecodeLatency.P99MS, d.DecodeLatency.MaxMS)
		}
		fmt.Fprintf(w, "  errors=%d %v\n", len(d.Errors), d.Errors)
	}

	if i := r.Inference; i != nil {
		fmt.Fprintf(w, "\n-- inference (%s) --\n", i.Kind)
		fmt.Fprintf(w, "  real_inference_performance_validated=%t state=%s\n", i.Real, i.WorkerState)
		fmt.Fprintf(w, "  device_requested=%s device_effective=%s (%s)\n", i.DeviceRequested, i.DeviceEffective, i.DeviceResolutionLabel)
		fmt.Fprintf(w, "  models=%v imgsz=%d warmup=%d samples=%d\n", i.ModelsLoaded, i.ImgSize, i.Warmup, i.Samples)
		if i.WorkerInferenceMS.Count > 0 {
			fmt.Fprintf(w, "  worker_inference_ms: n=%d p50=%.2f p95=%.2f p99=%.2f  -> %.2f inferences/s\n",
				i.WorkerInferenceMS.Count, i.WorkerInferenceMS.P50MS, i.WorkerInferenceMS.P95MS, i.WorkerInferenceMS.P99MS, i.WorkerInferenceFPS)
		}
		if i.SinkRouteMS.Count > 0 {
			fmt.Fprintf(w, "  sink_route_ms:       n=%d p50=%.2f p95=%.2f p99=%.2f  -> %.2f frames/s end to end\n",
				i.SinkRouteMS.Count, i.SinkRouteMS.P50MS, i.SinkRouteMS.P95MS, i.SinkRouteMS.P99MS, i.SinkRouteFPS)
		}
		fmt.Fprintf(w, "  encode+ipc p50 estimate=%.2fms errors=%d detections=%d\n",
			i.EncodeAndIPCMS, i.InferenceErrors, i.DetectionsTotal)
		for _, l := range i.Limitations {
			fmt.Fprintf(w, "  limitation: %s\n", l)
		}
	}

	if len(r.Limitations) > 0 {
		fmt.Fprintf(w, "\n-- limits of this run --\n")
		for _, l := range r.Limitations {
			fmt.Fprintf(w, "  - %s\n", l)
		}
	}
	if len(r.Environment) > 0 {
		fmt.Fprintf(w, "\n-- environment --\n")
		keys := make([]string, 0, len(r.Environment))
		for k := range r.Environment {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(w, "  %s=%s\n", k, r.Environment[k])
		}
	}
	fmt.Fprintf(w, "=== end report ===\n")
}

func humanBytes(b uint64) string {
	if b == 0 {
		return "unknown"
	}
	const gib = 1 << 30
	const mib = 1 << 20
	if b >= gib {
		return fmt.Sprintf("%.1f GiB", float64(b)/gib)
	}
	return fmt.Sprintf("%.0f MiB", float64(b)/mib)
}
