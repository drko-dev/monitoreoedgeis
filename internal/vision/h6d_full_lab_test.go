package vision

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/processing"
	"github.com/drko-dev/monitoreoedgeis/internal/rtsp"
	"github.com/drko-dev/monitoreoedgeis/internal/rtsptest"
	"github.com/drko-dev/monitoreoedgeis/internal/workerbootstrap"
)

type labEvents struct{ results chan InferenceResult }

func (l *labEvents) ConsumeInference(r InferenceResult, _ []byte) {
	select {
	case l.results <- r:
	default:
	}
}

func labNals(data []byte) [][]byte {
	starts := []int{}
	for i := 0; i+3 < len(data); i++ {
		if data[i] == 0 && data[i+1] == 0 && (data[i+2] == 1 || (data[i+2] == 0 && data[i+3] == 1)) {
			starts = append(starts, i)
		}
	}
	out := [][]byte{}
	for i, s := range starts {
		end := len(data)
		if i+1 < len(starts) {
			end = starts[i+1]
		}
		prefix := 3
		if data[s+2] == 0 {
			prefix = 4
		}
		if s+prefix < end {
			out = append(out, data[s+prefix:end])
		}
	}
	return out
}
func labPackets(n []byte) [][]byte {
	if len(n) <= 1000 {
		return [][]byte{n}
	}
	out := [][]byte{}
	typ := n[0] & 31
	fuIndicator := n[0]&0xe0 | 28
	for off := 1; off < len(n); {
		end := off + 1000
		if end > len(n) {
			end = len(n)
		}
		h := typ
		if off == 1 {
			h |= 0x80
		}
		if end == len(n) {
			h |= 0x40
		}
		pkt := append([]byte{fuIndicator, h}, n[off:end]...)
		out = append(out, pkt)
		off = end
	}
	return out
}

func TestLabRTSPToRealYOLO(t *testing.T) {
	py := os.Getenv("H6D_LAB_PYTHON")
	models := os.Getenv("H6D_LAB_MODELS")
	img := os.Getenv("H6D_LAB_IMAGE")
	h264Fixture := os.Getenv("H6D_LAB_H264")
	pack := os.Getenv("H6D_LAB_PACK")
	if (pack == "" && (py == "" || img == "")) || (pack != "" && h264Fixture == "") || models == "" {
		t.Skip("explicit H6D local opt-in paths required")
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Dir(filepath.Dir(wd))
	workerPath := filepath.Join(root, "deploy/vision-worker/worker.py")
	decoderPath := os.Getenv("H6D_LAB_FFMPEG_DECODER")
	workerArgs := []string{workerPath}
	if pack != "" {
		if !filepath.IsAbs(pack) {
			t.Fatal("H6D_LAB_PACK must be an absolute path")
		}
		manifest, manifestErr := readH6DLabPackManifest(pack)
		if manifestErr != nil {
			t.Fatal(manifestErr)
		}
		if manifest.PackType != "laboratory-only" || manifest.ReleaseEligible || manifest.RuntimeReady || manifest.InferenceReady || manifest.ModelRedistributionApproved {
			t.Fatal("lab pack manifest must remain explicitly non-release and non-ready")
		}
		py = verifiedH6DLabArtifact(t, pack, manifest, "python/bin/python3.11")
		workerPath = verifiedH6DLabArtifact(t, pack, manifest, "worker/worker.py")
		_ = verifiedH6DLabArtifact(t, pack, manifest, "worker/backend.py")
		decoderPath = verifiedH6DLabArtifact(t, pack, manifest, "ffmpeg/ffmpeg")
		workerArgs = workerbootstrap.Args(workerPath)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
	defer cancel()
	dir := t.TempDir()
	device := os.Getenv("H6D_LAB_DEVICE")
	if device == "" {
		device = "cpu"
	}
	cfg := Config{WorkerCmd: py, WorkerArgs: workerArgs, SocketPath: filepath.Join(dir, "v.sock"), Device: device, ImgSize: 320, PersonConfidence: 0.3, VehicleConfidence: 0.3, NMSIoU: 0.45, InferTimeout: 15 * time.Second, StartTimeout: 40 * time.Second}
	if pack != "" {
		// Test-only authorization uses operator-supplied LOCAL models and never
		// changes model redistribution or production readiness gates.
		manifest, err := readH6DLabPackManifest(pack)
		if err != nil {
			t.Fatal(err)
		}
		ffprobePath := verifiedH6DLabArtifact(t, pack, manifest, "ffmpeg/ffprobe")
		tmpDir := filepath.Join(dir, "tmp")
		if err := os.MkdirAll(tmpDir, 0o700); err != nil {
			t.Fatal(err)
		}
		cfg.Hermetic = true
		cfg.RuntimePackRoot = pack
		cfg.ModelPackAuthorized = true // local lab only, never product provisioning
		cfg.ModelsDir = models
		cfg.ProcessEnv = []string{
			"PATH=" + filepath.Dir(decoderPath),
			"HOME=" + dir,
			"YOLO_CONFIG_DIR=" + dir,
			"TMPDIR=" + tmpDir,
			"GEOCAM_VIDEO_FFMPEG_PATH=" + decoderPath,
			"GEOCAM_VIDEO_FFPROBE_PATH=" + ffprobePath,
			"GEOCAM_EDGE_YOLO_MODELS_DIR=" + models,
		}
	}
	w := NewWorker(cfg, NewModelManager(models, "yolo11s-pose.pt", "yolo11n.pt"), nil)
	events := &labEvents{results: make(chan InferenceResult, 10)}
	sink := NewSink(w, w.ModelManager(), nil, events, nil)
	if err = w.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err = w.WaitForReady(ctx); err != nil {
		t.Fatal(err)
	}
	sim, err := rtsptest.NewSimulator(rtsptest.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer sim.Close()
	rm := rtsp.NewManager(rtsp.DefaultConfig(), nil, nil)
	if err = rm.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if decoderPath != "" {
		if !filepath.IsAbs(decoderPath) {
			t.Fatal("H6D_LAB_FFMPEG_DECODER must be an absolute path")
		}
		if _, err := os.Stat(decoderPath); err != nil {
			t.Fatal(err)
		}
	}
	pm := processing.NewManager(processing.Config{Enabled: true, TargetFPS: 5, RingBufferSize: 10, QueueDepth: 64, DecodeQueueDepth: 8, MaxConcurrentPipelines: 2, FFmpegPath: decoderPath}, rm, nil, nil, sink)
	if err = pm.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() {
		c, k := context.WithTimeout(context.Background(), 5*time.Second)
		defer k()
		_ = pm.Stop(c)
		_ = rm.Stop(c)
	}()
	rm.SetTargets([]rtsp.CameraTarget{{CandidateKey: "h6d-real", Addr: sim.Addr(), RTSPPath: "/live", StreamRole: "sub", Codec: "H264", Width: 320, Height: 320, FPS: 5}})
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		ss := rm.Snapshot()
		if len(ss) > 0 && ss[0].Status == rtsp.StateOnline {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	var data []byte
	if pack != "" {
		if !filepath.IsAbs(h264Fixture) {
			t.Fatal("H6D_LAB_H264 must be an absolute path to the external pre-generated test fixture")
		}
		data, err = os.ReadFile(h264Fixture)
		if err != nil {
			t.Fatalf("read external H264 lab fixture: %v", err)
		}
		sum := sha256.Sum256(data)
		t.Logf("external fixture H264 bytes=%d sha256=%s", len(data), hex.EncodeToString(sum[:]))
	} else {
		fixtureFFmpeg := os.Getenv("H6D_LAB_FIXTURE_FFMPEG")
		if fixtureFFmpeg == "" {
			fixtureFFmpeg = "ffmpeg"
		}
		cmd := exec.CommandContext(ctx, fixtureFFmpeg, "-hide_banner", "-loglevel", "error", "-loop", "1", "-framerate", "5", "-i", img, "-frames:v", "30", "-vf", "scale=320:320", "-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency", "-g", "5", "-bf", "0", "-pix_fmt", "yuv420p", "-f", "h264", "-")
		data, err = cmd.Output()
		if err != nil {
			t.Fatalf("lab fixture ffmpeg: %v", err)
		}
	}
	nals := labNals(data)
	if len(nals) == 0 {
		t.Fatal("no H264 NAL units")
	}
	var seq uint16
	var stamp uint32 = 90000
	// Repeat the finite 4-frame fixture in a bounded local-only simulator:
	// FFmpeg can buffer a few H264 frames before emitting rawvideo over pipe.
	// Without a live stream or EOF a single short fixture may yield zero frames.
	for repeat := 0; repeat < 12; repeat++ {
		for _, n := range nals {
			if len(n) == 0 {
				continue
			}
			for j, p := range labPackets(n) {
				seq++
				r := make([]byte, 12+len(p))
				r[0] = 0x80
				r[1] = 96
				if j == len(labPackets(n))-1 {
					r[1] |= 0x80
				}
				binary.BigEndian.PutUint16(r[2:4], seq)
				binary.BigEndian.PutUint32(r[4:8], stamp)
				binary.BigEndian.PutUint32(r[8:12], 0x1)
				copy(r[12:], p)
				if e := sim.SendPacket(r); e != nil {
					t.Fatal(e)
				}
				time.Sleep(time.Millisecond)
			}
			stamp += 18000
		}
	}
	select {
	case result := <-events.results:
		if len(result.Detections) == 0 {
			t.Fatal("no detections")
		}
		var person bool
		for _, d := range result.Detections {
			if d.Type == "person" {
				person = true
			}
		}
		if !person {
			t.Fatalf("no person in detection result: %v", result.Detections)
		}
		t.Logf("RTSP_GO_FFMPEG_YOLO_PASS frame=%d detections=%d", result.FrameSeq, len(result.Detections))
	case <-time.After(12 * time.Second):
		if p := pm.Pipeline("h6d-real"); p != nil {
			t.Fatalf("no real detection; worker=%+v pipeline=%+v", w.Status(), p.Status())
		}
		t.Fatalf("no real detection; worker=%+v pipeline missing", w.Status())
	}
}

type h6dLabPackManifest struct {
	PackType                    string `json:"pack_type"`
	ReleaseEligible             bool   `json:"release_eligible"`
	RuntimeReady                bool   `json:"runtime_ready"`
	InferenceReady              bool   `json:"inference_ready"`
	ModelRedistributionApproved bool   `json:"model_redistribution_approved"`
	Files                       []struct {
		Path      string `json:"path"`
		SizeBytes int64  `json:"size_bytes"`
		SHA256    string `json:"sha256"`
	} `json:"files"`
}

func readH6DLabPackManifest(root string) (h6dLabPackManifest, error) {
	var manifest h6dLabPackManifest
	data, err := os.ReadFile(filepath.Join(root, "lab-manifest.json"))
	if err != nil {
		return manifest, fmt.Errorf("read lab pack manifest: %w", err)
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return manifest, fmt.Errorf("decode lab pack manifest: %w", err)
	}
	return manifest, nil
}

func verifiedH6DLabArtifact(t *testing.T, root string, manifest h6dLabPackManifest, relative string) string {
	t.Helper()
	clean := filepath.Clean(relative)
	if filepath.IsAbs(clean) || clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		t.Fatalf("unsafe lab artifact path %q", relative)
	}
	path := filepath.Join(root, clean)
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("resolve lab artifact %s: %v", relative, err)
	}
	packRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(packRoot, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		t.Fatalf("lab artifact escapes pack: %s", relative)
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() {
		t.Fatalf("lab artifact is not a regular file: %s", relative)
	}
	for _, item := range manifest.Files {
		if item.Path != filepath.ToSlash(clean) {
			continue
		}
		if info.Size() != item.SizeBytes {
			t.Fatalf("lab artifact size mismatch: %s", relative)
		}
		data, err := os.ReadFile(resolved)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != item.SHA256 {
			t.Fatalf("lab artifact SHA-256 mismatch: %s", relative)
		}
		return resolved
	}
	t.Fatalf("lab manifest does not inventory artifact %s", relative)
	return ""
}
