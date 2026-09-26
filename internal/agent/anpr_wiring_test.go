package agent

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/anpr"
	"github.com/drko-dev/monitoreoedgeis/internal/cloudsink"
	"github.com/drko-dev/monitoreoedgeis/internal/edgebacklog"
	"github.com/drko-dev/monitoreoedgeis/internal/fulledge"
	"github.com/drko-dev/monitoreoedgeis/internal/remoteconfig"
	"github.com/drko-dev/monitoreoedgeis/internal/transport"
	"github.com/drko-dev/monitoreoedgeis/internal/vision"
)

func realFixtureJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 128, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatalf("encode fixture jpeg: %v", err)
	}
	return buf.Bytes()
}

func newTestConsumerForAnpr(t *testing.T) *fullEdgeEventConsumer {
	t.Helper()
	dataDir := t.TempDir()
	logger := slog.New(slog.DiscardHandler)

	store, err := fulledge.NewEventStore(dataDir)
	if err != nil {
		t.Fatalf("NewEventStore: %v", err)
	}
	limits := fulledge.NewLimitsManager(fulledge.LimitsConfig{MaxConcurrentInference: 1, QueueDepth: 8}, nil, nil, logger)
	hw := fulledge.NewHardwareManager(fulledge.DeviceCPU, nil, logger)
	evidenceMgr, err := fulledge.NewEvidenceManager(dataDir, limits, 85, logger)
	if err != nil {
		t.Fatalf("NewEvidenceManager: %v", err)
	}
	svc := fulledge.NewService(fulledge.ServiceConfig{
		EdgeID: "edge-test", DataDir: dataDir, ModelName: "yolo11s-pose.pt+yolo11n.pt",
		DeviceMode: fulledge.DeviceCPU, Limits: fulledge.LimitsConfig{MaxConcurrentInference: 1, QueueDepth: 8},
		JPEGQuality: 85,
	}, store, evidenceMgr, hw, limits, nil, logger)

	backlog, err := edgebacklog.Open(edgebacklog.Config{Dir: t.TempDir(), MaxOperations: 10, MaxBytes: 10 << 20})
	if err != nil {
		t.Fatalf("edgebacklog.Open: %v", err)
	}

	return newFullEdgeEventConsumer(svc, backlog, nil, nil, dataDir, logger)
}

func testAnprConfig() anpr.Config {
	return anpr.Config{
		Enabled: true, MaxActiveBurstsPerCamera: 4, MaxFramesPerBurst: 5,
		MaxCandidateBytes: 2 << 20, MaxContextFrames: 5, MaxCameras: 8,
		BurstTTL: 3 * time.Second, CropPolicy: anpr.CropVehicleContext,
		FrameSelection: anpr.SelectFirst, MaxEncodedCropBytes: 2 << 20, DedupeCacheSize: 128,
	}
}

// Requirement #12/#32: a vehicle detection, with ANPR authorized, produces
// exactly one transport hand-off carrying a real, non-empty crop -- and
// this never happens for a PERSON detection or when unauthorized.
func TestConsumeAnprCandidates_AcceptedVehicleTransports(t *testing.T) {
	consumer := newTestConsumerForAnpr(t)
	registry := anpr.NewRegistry(testAnprConfig(), anpr.WithAuthorizer(anpr.AllowAllAuthorizer{}))
	consumer.SetAnprRegistry(registry)

	var transported []anpr.PlateCandidate
	var crops [][]byte
	consumer.SetAnprTransport(func(candidate anpr.PlateCandidate, cropJPEG []byte) {
		transported = append(transported, candidate)
		crops = append(crops, cropJPEG)
	})

	jpegBytes := realFixtureJPEG(t, 200, 150)
	result := vision.InferenceResult{
		CandidateKey: "cam-1", FrameSeq: 1, Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		CorrelationID: "corr-1",
		Detections: []vision.Detection{
			{ClassID: vision.ClassIDCar, Label: "car", Type: vision.DetectionTypeVehicle, Confidence: 0.9, BBox: [4]float64{10, 10, 100, 100}},
			{ClassID: 0, Label: "person", Type: vision.DetectionTypePerson, Confidence: 0.9, BBox: [4]float64{5, 5, 20, 20}},
		},
	}

	consumer.ConsumeInference(result, jpegBytes)

	if len(transported) != 1 {
		t.Fatalf("expected exactly 1 ANPR transport call (vehicle only), got %d", len(transported))
	}
	if transported[0].CameraKey != "cam-1" || transported[0].CorrelationID != "corr-1" {
		t.Fatalf("unexpected candidate metadata: %+v", transported[0])
	}
	if len(crops[0]) == 0 {
		t.Fatal("expected a non-empty crop JPEG")
	}
}

func TestConsumeAnprCandidates_UnauthorizedProducesZeroTransport(t *testing.T) {
	consumer := newTestConsumerForAnpr(t)
	registry := anpr.NewRegistry(testAnprConfig(), anpr.WithAuthorizer(anpr.DenyAllAuthorizer{}))
	consumer.SetAnprRegistry(registry)

	called := false
	consumer.SetAnprTransport(func(anpr.PlateCandidate, []byte) { called = true })

	result := vision.InferenceResult{
		CandidateKey: "cam-1", FrameSeq: 1, Timestamp: time.Now(),
		Detections: []vision.Detection{
			{ClassID: vision.ClassIDCar, Type: vision.DetectionTypeVehicle, Confidence: 0.9, BBox: [4]float64{10, 10, 100, 100}},
		},
	}

	consumer.ConsumeInference(result, realFixtureJPEG(t, 200, 150))

	if called {
		t.Fatal("expected zero ANPR transport calls when unauthorized")
	}
}

func TestConsumeAnprCandidates_NilRegistryIsNoOp(t *testing.T) {
	consumer := newTestConsumerForAnpr(t)
	// anprRegistry left nil -- must not panic, must not call transport.
	called := false
	consumer.SetAnprTransport(func(anpr.PlateCandidate, []byte) { called = true })

	result := vision.InferenceResult{
		CandidateKey: "cam-1", FrameSeq: 1, Timestamp: time.Now(),
		Detections: []vision.Detection{
			{ClassID: vision.ClassIDCar, Type: vision.DetectionTypeVehicle, Confidence: 0.9, BBox: [4]float64{10, 10, 100, 100}},
		},
	}

	consumer.ConsumeInference(result, realFixtureJPEG(t, 200, 150))

	if called {
		t.Fatal("expected zero ANPR transport calls with a nil registry")
	}
}

// remoteConfigAnprAuthorizer (item 37/38): fail-closed default, enabled
// once the live snapshot says so, denies again once it flips back.
func TestRemoteConfigAnprAuthorizer_FailClosedAndReactsToConfig(t *testing.T) {
	var current remoteconfig.RuntimeConfig
	auth := &remoteConfigAnprAuthorizer{current: func() remoteconfig.RuntimeConfig { return current }}

	if auth.ANPRAllowed("cam-1") {
		t.Fatal("expected deny with no config at all")
	}

	current = remoteconfig.RuntimeConfig{Cameras: map[string]remoteconfig.CameraConfig{
		"cam-1": {ANPR: &remoteconfig.CameraANPRConfig{Enabled: true}},
	}}
	if !auth.ANPRAllowed("cam-1") {
		t.Fatal("expected allow once config enables this camera")
	}
	if auth.ANPRAllowed("cam-2") {
		t.Fatal("expected deny for a camera with no config entry")
	}

	current.Cameras["cam-1"] = remoteconfig.CameraConfig{ANPR: &remoteconfig.CameraANPRConfig{Enabled: false}}
	if auth.ANPRAllowed("cam-1") {
		t.Fatal("expected deny once config flips back to disabled")
	}
}

func TestRemoteConfigAnprAuthorizer_NilCurrentFuncFailsClosed(t *testing.T) {
	auth := &remoteConfigAnprAuthorizer{current: nil}
	if auth.ANPRAllowed("cam-1") {
		t.Fatal("expected deny with a nil current func")
	}
}

// End-to-end proof that anprCloudTransport really reaches a live CloudSink
// and a real HTTP server, with a correctly built envelope (item #17's wire
// contract fields, sha256/size matching the actual crop bytes).
func TestAnprCloudTransport_Send_RealCloudSinkRealHTTP(t *testing.T) {
	var gotMetadata map[string]any
	var gotCropLen int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != transport.AnprCandidatesPath {
			t.Errorf("path = %q, want %q", r.URL.Path, transport.AnprCandidatesPath)
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("ParseMultipartForm: %v", err)
		}
		if err := json.Unmarshal([]byte(r.FormValue("metadata")), &gotMetadata); err != nil {
			t.Fatalf("unmarshal metadata: %v", err)
		}
		file, _, err := r.FormFile("crop")
		if err != nil {
			t.Fatalf("FormFile(crop): %v", err)
		}
		data, _ := io.ReadAll(file)
		gotCropLen = len(data)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	client, err := transport.New(srv.URL, true, 2*time.Second, "test")
	if err != nil {
		t.Fatalf("transport.New: %v", err)
	}
	sink := cloudsink.New(client, "device-1", "cred-1", cloudsink.Config{}, slog.Default(), nil)
	t.Cleanup(sink.Close)

	xport := &anprCloudTransport{logger: slog.Default()}
	xport.SetSink(sink)

	candidate := anpr.PlateCandidate{
		CandidateID: "cand-1", CameraKey: "cam-1", FrameSeq: 42,
		Timestamp:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		VehicleBBox: anpr.BBox{X0: 10, Y0: 10, X1: 60, Y1: 90}, BurstID: "burst-1",
	}
	cropJPEG := []byte{0xFF, 0xD8, 0xFF, 0xD9, 0x01, 0x02, 0x03}

	xport.Send(candidate, cropJPEG)

	if gotMetadata["candidate_id"] != "cand-1" {
		t.Errorf("candidate_id = %v, want cand-1", gotMetadata["candidate_id"])
	}
	if gotMetadata["camera_key"] != "cam-1" {
		t.Errorf("camera_key = %v, want cam-1", gotMetadata["camera_key"])
	}
	if gotMetadata["schema_version"] != anprEnvelopeSchemaVersion {
		t.Errorf("schema_version = %v, want %v", gotMetadata["schema_version"], anprEnvelopeSchemaVersion)
	}
	wantHash := sha256.Sum256(cropJPEG)
	if gotMetadata["crop_sha256"] != hex.EncodeToString(wantHash[:]) {
		t.Errorf("crop_sha256 = %v, want a hash of the actual crop bytes", gotMetadata["crop_sha256"])
	}
	if gotCropLen != len(cropJPEG) {
		t.Errorf("received crop length = %d, want %d", gotCropLen, len(cropJPEG))
	}
}

// A nil sink (no CloudSink built yet, or Edge-only mode) must never panic
// and must never fabricate a send.
func TestAnprCloudTransport_Send_NilSinkIsNoOp(t *testing.T) {
	xport := &anprCloudTransport{logger: slog.Default()}
	xport.Send(anpr.PlateCandidate{CandidateID: "cand-1", CameraKey: "cam-1"}, []byte{0x01})
	// No assertion beyond "did not panic" -- there is nothing else to observe.
}
