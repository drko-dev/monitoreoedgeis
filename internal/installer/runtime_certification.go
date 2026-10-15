package installer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/vision"
)

const (
	certificationFileName = "full-edge-certification.json"
	certPersonModel       = "yolo11s-pose.pt"
	certVehicleModel      = "yolo11n.pt"
)

// installedWorkerDevice is the inference device the daemon is configured
// with for the installed runtime: Metal on Apple Silicon, CPU elsewhere.
func installedWorkerDevice() string {
	if runtime.GOOS == "darwin" && runtime.GOARCH == "arm64" {
		return "mps"
	}
	return "cpu"
}

// CertifyInstalledRuntime runs a real inference smoke test with the INSTALLED
// runtime, exactly as the daemon would: the installed FFmpeg decodes one
// frame of a known fixture and the vision Worker (installed Python + worker.py
// + persisted model weights) must return at least one detection. Only then is
// a certification issued. Model authorization is asserted by the caller's
// build (owner/lab builds); this never runs for default builds.
func CertifyInstalledRuntime(ctx context.Context, dataDir, fixture, runtimeVersion, manifestSHA256 string) (*RuntimeCertification, error) {
	root := filepath.Join(dataDir, DefaultRuntimePackTargetName)
	ffmpeg := filepath.Join(root, "ffmpeg", "ffmpeg")
	ffprobe := filepath.Join(root, "ffmpeg", "ffprobe")
	modelsDir := filepath.Join(dataDir, "models")
	modelDigest, err := modelsManifestSHA256(modelsDir, certPersonModel, certVehicleModel)
	if err != nil {
		return nil, err
	}
	frame, width, height, err := decodeFixtureFrame(ctx, ffmpeg, ffprobe, fixture)
	if err != nil {
		return nil, err
	}

	sockDir, err := os.MkdirTemp("", "geocam-cert-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(sockDir)
	worker := vision.NewWorker(vision.Config{
		WorkerCmd:         filepath.Join(root, "python", "bin", "python3.11"),
		WorkerArgs:        []string{"-B", filepath.Join(root, "worker", "worker.py")},
		ModelsDir:         modelsDir,
		PersonModel:       certPersonModel,
		VehicleModel:      certVehicleModel,
		PersonConfidence:  0.3,
		VehicleConfidence: 0.3,
		NMSIoU:            0.45,
		Device:            installedWorkerDevice(),
		ImgSize:           640,
		SocketPath:        filepath.Join(sockDir, "v.sock"),
		StartTimeout:      2 * time.Minute,
		InferTimeout:      time.Minute,
	}, vision.NewModelManager(modelsDir, certPersonModel, certVehicleModel), nil)
	if err := worker.Start(ctx); err != nil {
		return nil, err
	}
	defer worker.Stop(context.Background())
	if err := worker.WaitForReady(ctx); err != nil {
		return nil, fmt.Errorf("installed vision worker not ready: %w", err)
	}
	result, err := worker.Infer(ctx, vision.InferRequest{
		CandidateKey: "certification", FrameSeq: 1, Timestamp: time.Now(),
		Width: width, Height: height, JPEG: frame,
	})
	if err != nil {
		return nil, fmt.Errorf("inference smoke test failed: %w", err)
	}
	if len(result.Detections) == 0 {
		return nil, errors.New("inference smoke test returned no detections on the known fixture")
	}

	return &RuntimeCertification{
		CertificationID:          fmt.Sprintf("local-%d", time.Now().Unix()),
		RuntimePackVersion:       runtimeVersion,
		RuntimeManifestSHA256:    manifestSHA256,
		Platform:                 runtime.GOOS + "/" + runtime.GOARCH,
		EvidenceLevel:            "verified",
		RuntimePackVerified:      true,
		DependenciesVerified:     true,
		VisionWorkerReady:        true,
		FFmpegVerified:           true,
		FFprobeVerified:          true,
		ModelPackVersion:         "local-" + modelDigest[:12],
		ModelManifestSHA256:      modelDigest,
		ModelAuthorized:          true,
		ModelIntegrityVerified:   true,
		InferenceSmokeTestPassed: true,
		CertifiedAt:              time.Now().UTC(),
	}, nil
}

// decodeFixtureFrame mirrors the daemon pipeline: the installed (decode-only)
// FFmpeg emits raw RGB24 and Go encodes the JPEG sent to the worker.
func decodeFixtureFrame(ctx context.Context, ffmpeg, ffprobe, fixture string) ([]byte, int, int, error) {
	dims, err := exec.CommandContext(ctx, ffprobe, "-v", "error", "-select_streams", "v:0",
		"-show_entries", "stream=width,height", "-of", "csv=p=0", fixture).Output()
	if err != nil {
		return nil, 0, 0, fmt.Errorf("installed ffprobe could not read fixture: %w", err)
	}
	var width, height int
	if _, err := fmt.Sscanf(strings.TrimSpace(string(dims)), "%d,%d", &width, &height); err != nil || width <= 0 || height <= 0 {
		return nil, 0, 0, fmt.Errorf("unexpected fixture dimensions %q", dims)
	}
	raw, err := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-loglevel", "error", "-i", fixture,
		"-frames:v", "1", "-f", "rawvideo", "-pix_fmt", "rgb24", "-").Output()
	if err != nil || len(raw) != width*height*3 {
		return nil, 0, 0, fmt.Errorf("installed ffmpeg could not decode fixture (%d bytes): %v", len(raw), err)
	}
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for i, j := 0, 0; i < len(raw); i, j = i+3, j+4 {
		img.Pix[j], img.Pix[j+1], img.Pix[j+2], img.Pix[j+3] = raw[i], raw[i+1], raw[i+2], 0xff
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		return nil, 0, 0, err
	}
	return buf.Bytes(), width, height, nil
}

// modelsManifestSHA256 digests "name sha256\n" lines of the required weights.
func modelsManifestSHA256(dir string, names ...string) (string, error) {
	sort.Strings(names)
	var manifest strings.Builder
	for _, name := range names {
		sum, err := hashFileSHA256(filepath.Join(dir, name))
		if err != nil {
			return "", fmt.Errorf("model %s unavailable: %w", name, err)
		}
		fmt.Fprintf(&manifest, "%s %s\n", name, sum)
	}
	digest := sha256.Sum256([]byte(manifest.String()))
	return hex.EncodeToString(digest[:]), nil
}

// SaveRuntimeCertification persists a certification next to the runtime.
func SaveRuntimeCertification(dataDir string, cert *RuntimeCertification) error {
	data, err := json.Marshal(cert)
	if err != nil {
		return err
	}
	path := filepath.Join(dataDir, certificationFileName)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// LoadRuntimeCertification returns a persisted certification only if it is
// valid, bound to the given pinned manifest and its model weights still
// match. Anything else returns nil (not certified).
func LoadRuntimeCertification(dataDir, manifestSHA256 string) *RuntimeCertification {
	data, err := os.ReadFile(filepath.Join(dataDir, certificationFileName))
	if err != nil {
		return nil
	}
	var cert RuntimeCertification
	if json.Unmarshal(data, &cert) != nil || !cert.Valid() || !strings.EqualFold(cert.RuntimeManifestSHA256, manifestSHA256) {
		return nil
	}
	digest, err := modelsManifestSHA256(filepath.Join(dataDir, "models"), certPersonModel, certVehicleModel)
	if err != nil || digest != cert.ModelManifestSHA256 {
		return nil
	}
	return &cert
}

// CertifiedSnapshot is the READY snapshot for a valid certification.
func CertifiedSnapshot(cert *RuntimeCertification) FullEdgePreparationSnapshot {
	components := DefaultComponentProgress()
	for i := range components {
		components[i].Status = ComponentStatusReady
	}
	return FullEdgePreparationSnapshot{
		State: StateReady, Stage: "CERTIFIED", ProgressPercent: 100,
		DiagnosticsAvailable: true, Components: components, Certification: cert,
	}
}
