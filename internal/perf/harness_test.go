package perf

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/processing"
)

// fakeDecoder is the harness's boundary fake: it replaces the ffmpeg
// subprocess — an external process — and nothing else. The depacketizer,
// pipeline, sampler, router and every counter under test are the production
// ones.
//
// It emits exactly one frame per pushed access unit. Close() closes Done (the
// real decoder does too when its process exits), which is what lets a drain
// loop finish; an unclosed Done models a decoder that neither fails nor exits,
// which is the state a measurement window needs.
type fakeDecoder struct {
	width, height int
	frames        chan processing.DecodedFrame
	done          chan struct{}
	pushDelay     time.Duration

	closeOnce sync.Once
	decoded   atomic.Int64
	dropped   atomic.Int64
	seq       atomic.Uint64
	closed    atomic.Bool
}

func newFakeDecoder(width, height, queue int) *fakeDecoder {
	if queue <= 0 {
		queue = 4
	}
	return &fakeDecoder{
		width:  width,
		height: height,
		frames: make(chan processing.DecodedFrame, queue),
		done:   make(chan struct{}),
	}
}

func (d *fakeDecoder) Push(au processing.AccessUnit) error {
	if d.closed.Load() {
		return errors.New("perf test fake decoder: closed")
	}
	if d.pushDelay > 0 {
		time.Sleep(d.pushDelay)
	}
	d.decoded.Add(1)
	seq := d.seq.Add(1)
	frame := processing.DecodedFrame{
		Data:             patternYUV420P(d.width, d.height, int(seq)),
		Width:            d.width,
		Height:           d.height,
		PipelineSeq:      seq,
		SourceReceivedAt: au.ReceivedAt,
		DecodedAt:        time.Now(),
	}
	select {
	case d.frames <- frame:
	default:
		// Same backpressure semantics as processing.FFmpegDecoder: a slow
		// drainer drops frames rather than stalling the decoder.
		d.dropped.Add(1)
	}
	return nil
}

func (d *fakeDecoder) Frames() <-chan processing.DecodedFrame { return d.frames }
func (d *fakeDecoder) Done() <-chan struct{}                  { return d.done }
func (d *fakeDecoder) DecodedCount() int64                    { return d.decoded.Load() }
func (d *fakeDecoder) DroppedCount() int64                    { return d.dropped.Load() }

func (d *fakeDecoder) Close() error {
	d.closeOnce.Do(func() {
		d.closed.Store(true)
		close(d.done)
	})
	return nil
}

func patternYUV420P(width, height, seed int) []byte {
	ySize := width * height
	cSize := (width / 2) * (height / 2)
	buf := make([]byte, ySize+2*cSize)
	for i := range buf {
		buf[i] = byte(i + seed)
	}
	return buf
}

// syntheticAUs builds access units shaped like a real encoder's output: an
// access unit delimiter plus several slices per picture, which is what makes
// the AUD-based grouping (rather than one-unit-per-slice) observable.
func syntheticAUs(pictures, slicesPerPicture int) []AccessUnit {
	aus := make([]AccessUnit, 0, pictures)
	for p := 0; p < pictures; p++ {
		nalus := []NALUnit{{Type: NALTypeAUD, Bytes: nal(NALTypeAUD, 0, 3)}}
		if p%12 == 0 {
			nalus = append(nalus,
				NALUnit{Type: NALTypeSPS, Bytes: nal(NALTypeSPS, 0x60, 20)},
				NALUnit{Type: NALTypePPS, Bytes: nal(NALTypePPS, 0x61, 8)},
			)
		}
		sliceType := byte(NALTypeSlice)
		if p%12 == 0 {
			sliceType = NALTypeSliceIDR
		}
		for s := 0; s < slicesPerPicture; s++ {
			nalus = append(nalus, NALUnit{Type: sliceType, Bytes: nal(sliceType, byte(p+s), 1200)})
		}
		aus = append(aus, AccessUnit{NALUs: nalus})
	}
	return aus
}

func smokeClip(pictures int, fps float64) ClipInfo {
	return ClipInfo{
		Path:                   "(in-memory synthetic access units, no video file)",
		Container:              "in-memory synthetic access units",
		Codec:                  "h264",
		Width:                  320,
		Height:                 180,
		NominalSourceFPS:       fps,
		NominalSourceFPSSource: "harness-constructed access units",
		CodedFrames:            int64(pictures),
		AccessUnitBoundary:     "aud",
	}
}

// TestDecodeDirectHarnessSmoke proves the direct-mode measurement machinery —
// counting, per-frame latency summarisation, FPS derivation, report fields —
// works without ffmpeg. The decoder is faked; no throughput claim is made.
func TestDecodeDirectHarnessSmoke(t *testing.T) {
	const pictures = 24
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	res, err := RunDecode(ctx, DecodeOptions{
		Mode:          DecodeModeDirect,
		Clip:          smokeClip(pictures, 60),
		AUs:           syntheticAUs(pictures, 2),
		Pacing:        PacingRealtime,
		PrimingFrames: 4,
		// A fresh fake per call: the injection restarts the pipeline, which
		// closes the previous decoder, so a single shared instance would be
		// handed to the second pipeline already closed.
		DecoderFactory: func() (processing.VideoDecoder, error) { return newFakeDecoder(320, 180, 64), nil },
		Stability:      150 * time.Millisecond,
		Timeout:        10 * time.Second,
	})
	if err != nil {
		t.Fatalf("RunDecode: %v", err)
	}
	if !res.DecoderFake {
		t.Error("a run with an injected decoder factory must be reported as fake")
	}
	if res.FramesFed != pictures {
		t.Errorf("frames_fed = %d, want %d", res.FramesFed, pictures)
	}
	if res.MeasuredFrames+res.PrimingFrames != pictures {
		t.Errorf("measured (%d) + priming (%d) must equal the %d access units fed", res.MeasuredFrames, res.PrimingFrames, pictures)
	}
	if d := res.FramesDecoded - int64(res.MeasuredFrames); d < -1 || d > 1 {
		t.Errorf("frames_decoded = %d, want %d (one frame per measured access unit, priming excluded; the counters are read at phase boundaries so a one-frame tolerance applies)",
			res.FramesDecoded, res.MeasuredFrames)
	}
	if res.DecodeLatency.Count != pictures {
		t.Errorf("decode latency samples = %d, want %d (latency is kept for every frame, priming included, because it is a distribution rather than a rate)",
			res.DecodeLatency.Count, pictures)
	}
	if res.DecodeFPS <= 0 || res.HarnessFeedFPS <= 0 || res.IngestFPS <= 0 {
		t.Errorf("rates must be positive: decode_fps=%v harness_feed_fps=%v ingest_fps=%v", res.DecodeFPS, res.HarnessFeedFPS, res.IngestFPS)
	}
	if res.DecodeFPSEndToEnd <= 0 || res.DecodeFPSEndToEnd > res.DecodeFPS {
		t.Errorf("decode_fps_end_to_end = %v must be positive and no greater than decode_fps = %v",
			res.DecodeFPSEndToEnd, res.DecodeFPS)
	}
	if res.PrimingFrames <= 0 || res.MeasuredFrames != pictures-res.PrimingFrames {
		t.Errorf("priming=%d measured=%d, want a positive priming phase and measured = %d - priming",
			res.PrimingFrames, res.MeasuredFrames, pictures)
	}
	// A zero SourceReceivedAt would make every per-frame latency meaningless;
	// the harness must stamp the ingest time itself in direct mode.
	if res.DecodeLatency.MaxMS > 5000 {
		t.Errorf("decode latency max = %vms: the ingest timestamp is not being set, so latency is measured against the zero time", res.DecodeLatency.MaxMS)
	}
	if res.ElapsedMS <= 0 || res.FeedElapsedMS <= 0 {
		t.Errorf("durations must be positive: elapsed=%v feed=%v", res.ElapsedMS, res.FeedElapsedMS)
	}
	if res.DecodeLatency.P95MS < res.DecodeLatency.P50MS {
		t.Errorf("p95 (%v) must not be below p50 (%v)", res.DecodeLatency.P95MS, res.DecodeLatency.P50MS)
	}
	if res.TimeToFirstFrameMS <= 0 {
		t.Errorf("time_to_first_frame_ms = %v, want a positive value", res.TimeToFirstFrameMS)
	}
	if len(res.Errors) != 0 {
		t.Errorf("unexpected errors: %v", res.Errors)
	}
}

// TestDecodePipelineHarnessSmoke runs the whole production chain — a real
// RTSP session against internal/rtsptest.Simulator, a real rtsp.Manager, a
// real processing.Manager with its real depacketizer, sampler and router —
// with only the ffmpeg subprocess replaced. It is the CI-visible proof that
// the harness's plumbing and counters work, and it needs neither ffmpeg nor a
// camera.
func TestDecodePipelineHarnessSmoke(t *testing.T) {
	const pictures = 24
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	res, err := RunDecode(ctx, DecodeOptions{
		Mode:              DecodeModePipeline,
		Clip:              smokeClip(pictures, 20),
		AUs:               syntheticAUs(pictures, 2),
		Pacing:            PacingRealtime,
		PrimingFrames:     8,
		OutputWidth:       320,
		OutputHeight:      180,
		SampleFPS:         10,
		Stability:         400 * time.Millisecond,
		Timeout:           30 * time.Second,
		RTSPPacketTimeout: 3 * time.Second,
		DecoderFactory:    func() (processing.VideoDecoder, error) { return newFakeDecoder(320, 180, 64), nil },
	})
	if err != nil {
		t.Fatalf("RunDecode: %v", err)
	}
	if !res.DecoderFake {
		t.Error("a run with an injected decoder factory must be reported as fake")
	}
	// A failure here is a timing/boundary question, so the diagnostics name the
	// phase boundaries rather than only the derived number that tripped.
	defer func() {
		if t.Failed() {
			t.Logf("diagnostic: priming=%d measured=%d pacing_fps=%v send=%.1fms feed=%.1fms",
				res.PrimingFrames, res.MeasuredFrames, res.PacingFPS, res.SendElapsedMS, res.FeedElapsedMS)
			t.Logf("diagnostic before=%+v", res.StatusBefore)
			t.Logf("diagnostic mid=%+v", res.StatusMid)
			t.Logf("diagnostic after=%+v", res.StatusAfter)
		}
	}()
	if res.StatusBefore == nil || res.StatusAfter == nil {
		t.Fatal("pipeline status snapshots must be captured for auditability")
	}
	if res.StatusAfter.State != "running" {
		t.Errorf("pipeline state = %q, want running", res.StatusAfter.State)
	}
	if res.RTPPacketsSent == 0 {
		t.Fatal("no RTP packets were sent")
	}
	// Only a SHORTFALL is meaningful: loopback TCP is lossless, so fewer packets
	// received than sent would mean the harness lost frames and the measurement
	// would blame the decoder for it. An excess is expected and harmless — it
	// counts the pre-window access unit this test's injected decoder needs to
	// make the pipeline exist at all.
	if res.TransportDeliveryRatio < 0.98 {
		t.Errorf("transport delivery ratio = %v: the loopback transport delivered fewer packets than the harness sent, so the measurement is losing frames before the decoder",
			res.TransportDeliveryRatio)
	}
	if res.FramesFed != pictures {
		t.Errorf("frames_fed = %d, want %d", res.FramesFed, pictures)
	}
	if d := res.FramesDecoded - int64(res.MeasuredFrames); d < -1 || d > 1 {
		t.Errorf("frames_decoded through the real chain = %d, want %d measured access units (one-frame boundary tolerance)", res.FramesDecoded, res.MeasuredFrames)
	}
	if d := res.FramesReceived - int64(res.MeasuredFrames); d < -1 || d > 1 {
		t.Errorf("access units completed by the production depacketizer = %d, want %d (one-frame boundary tolerance)", res.FramesReceived, res.MeasuredFrames)
	}
	if res.FramesSampled <= 0 || res.FramesSampled > res.FramesReceived {
		t.Errorf("frames_sampled = %d, want a positive value no greater than the %d ingested", res.FramesSampled, res.FramesReceived)
	}
	if res.SinkFrames == 0 {
		t.Error("the harness's own sink received no frames, so nothing was routed end to end")
	}
	if res.SinkFrames > res.FramesSampled {
		t.Errorf("sink saw %d frames but the pipeline only sampled %d", res.SinkFrames, res.FramesSampled)
	}
	if res.PacketsPerAU <= 0 {
		t.Errorf("packets per access unit = %v, want a positive value", res.PacketsPerAU)
	}
	if res.DecodeFPS <= 0 {
		t.Errorf("decode_fps = %v, want a positive value", res.DecodeFPS)
	}
	if len(res.Errors) != 0 {
		t.Errorf("unexpected errors: %v", res.Errors)
	}
	// The transport cross-check is pipeline-only and must survive into JSON.
	blob, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(blob), `"transport_delivery_ratio"`) {
		t.Error("the pipeline decode result must carry its transport delivery cross-check")
	}
}

// TestDecodePipelineSmoke_RealtimePacingKeepsUpWithTheSourceRate checks the
// harness's own pacing arithmetic: feeding a 60 fps source in real time must
// take about as long as the clip's nominal duration, not less.
func TestDecodePipelineSmoke_RealtimePacingKeepsUpWithTheSourceRate(t *testing.T) {
	if testing.Short() {
		t.Skip("timing smoke test skipped in -short mode")
	}
	const pictures = 90
	const fps = 60
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	res, err := RunDecode(ctx, DecodeOptions{
		Mode:              DecodeModePipeline,
		Clip:              smokeClip(pictures, fps),
		AUs:               syntheticAUs(pictures, 1),
		Pacing:            PacingRealtime,
		OutputWidth:       160,
		OutputHeight:      90,
		SampleFPS:         10,
		Stability:         400 * time.Millisecond,
		Timeout:           30 * time.Second,
		RTSPPacketTimeout: 3 * time.Second,
		DecoderFactory:    func() (processing.VideoDecoder, error) { return newFakeDecoder(320, 180, 64), nil },
	})
	if err != nil {
		t.Fatalf("RunDecode: %v", err)
	}
	// Both phases must be paced at the source's cadence: the measured feed
	// window for MeasuredFrames, and the priming window for PrimingFrames.
	wantFeedMS := float64(res.MeasuredFrames) / fps * 1000
	if res.FeedElapsedMS < wantFeedMS*0.8 || res.FeedElapsedMS > wantFeedMS*1.5 {
		t.Errorf("measured feed window = %vms, want about %vms for %d frames at %d fps: realtime pacing is not pacing",
			res.FeedElapsedMS, wantFeedMS, res.MeasuredFrames, fps)
	}
	if res.HarnessFeedFPS < fps*0.8 || res.HarnessFeedFPS > fps*1.3 {
		t.Errorf("harness_feed_fps = %v, want approximately %d", res.HarnessFeedFPS, fps)
	}
	if res.PrimingMS <= 0 {
		t.Errorf("priming_ms = %v, want a positive value", res.PrimingMS)
	}
}

// TestReportJSONRoundTrip checks that a report is valid, complete JSON with
// the fields a consumer needs — Hito X requires machine-readable results.
func TestReportJSONRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	res, err := RunDecode(ctx, DecodeOptions{
		Mode:          DecodeModeDirect,
		Clip:          smokeClip(4, 30),
		AUs:           syntheticAUs(4, 1),
		Pacing:        PacingRealtime,
		PrimingFrames: 1,
		// A fresh fake per call: the injection restarts the pipeline, which
		// closes the previous decoder, so a single shared instance would be
		// handed to the second pipeline already closed.
		DecoderFactory: func() (processing.VideoDecoder, error) { return newFakeDecoder(320, 180, 64), nil },
		Stability:      150 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("RunDecode: %v", err)
	}
	clip := res.Clip
	report := Report{
		SchemaVersion: SchemaVersion,
		Kind:          "harness-smoke",
		GeneratedAt:   time.Now().UTC(),
		Harness:       "internal/perf",
		Host:          HostInfo{GOOS: "test", GOARCH: "test", NumCPU: 1},
		Clip:          &clip,
		Decode:        []DecodeResult{res},
		Environment:   CaptureEnvironment(),
		Limitations:   []string{"fake decoder: harness validation only"},
	}
	out := filepath.Join(t.TempDir(), "nested", "report.json")
	if err := report.WriteJSON(out); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, want := range []string{`"schema_version"`, `"decode"`, `"frames_decoded"`, `"decode_fps"`, `"p50_ms"`, `"p99_ms"`, `"access_unit_boundary"`, `"nominal_source_fps"`, `"decode_latency"`} {
		if !strings.Contains(text, want) {
			t.Errorf("report JSON is missing %s", want)
		}
	}
	var buf strings.Builder
	report.Summary(&buf)
	if !strings.Contains(buf.String(), "decode_fps=") {
		t.Error("the human summary does not mention decode_fps")
	}
	if !strings.Contains(buf.String(), "FAKE DECODER") {
		t.Error("the human summary must warn when the decoder was faked")
	}
}

func TestSummarizeMS_NearestRankAndCount(t *testing.T) {
	got := SummarizeMS([]float64{5, 1, 4, 2, 3})
	if got.Count != 5 {
		t.Fatalf("count = %d, want 5", got.Count)
	}
	if got.MinMS != 1 || got.MaxMS != 5 || got.P50MS != 3 {
		t.Errorf("min/p50/max = %v/%v/%v, want 1/3/5", got.MinMS, got.P50MS, got.MaxMS)
	}
	if got.P95MS != 5 || got.P99MS != 5 {
		t.Errorf("p95/p99 = %v/%v, want the observed maximum 5", got.P95MS, got.P99MS)
	}
	if empty := SummarizeMS(nil); empty.Count != 0 || empty.P95MS != 0 {
		t.Errorf("an empty sample must summarise to zeroes, got %+v", empty)
	}
}

func TestDeviceResolutionLabel_DoesNotDressUpAnEcho(t *testing.T) {
	cases := []struct {
		requested, effective string
		wantSubstring        string
	}{
		{"cpu", "cpu", "concrete device"},
		{"cuda", "cuda", "concrete device"},
		{"auto", "cpu", "worker-resolved"},
		{"auto", "auto", "NOT independently verified"},
		{"cpu", "", "not reported"},
	}
	for _, c := range cases {
		got := DeviceResolutionLabel(c.requested, c.effective)
		if !strings.Contains(got, c.wantSubstring) {
			t.Errorf("DeviceResolutionLabel(%q, %q) = %q, want it to contain %q", c.requested, c.effective, got, c.wantSubstring)
		}
	}
}

func TestParseClipSpec_OverridesAndValidation(t *testing.T) {
	def, err := ParseClipSpec("")
	if err != nil {
		t.Fatal(err)
	}
	if def != DefaultClipSpec() {
		t.Errorf("empty spec = %+v, want the default %+v", def, DefaultClipSpec())
	}
	got, err := ParseClipSpec("1280x720@20x300")
	if err != nil {
		t.Fatal(err)
	}
	if got.Width != 1280 || got.Height != 720 || got.FPS != 20 || got.Frames != 300 {
		t.Errorf("parsed %+v, want 1280x720@20fps x300", got)
	}
	if _, err := ParseClipSpec("641x361"); err == nil {
		t.Error("odd dimensions must be rejected: yuv420p requires even width/height")
	}
	if _, err := ParseClipSpec("nonsense"); err == nil {
		t.Error("a malformed spec must be rejected rather than silently defaulted")
	}
}
