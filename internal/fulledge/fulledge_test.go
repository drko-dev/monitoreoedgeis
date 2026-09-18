package fulledge

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/processing"
)

// mockHealthSink records the latest Status.
type mockHealthSink struct {
	mu     sync.Mutex
	status Status
}

func (m *mockHealthSink) SetFullEdgeStatus(s Status) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.status = s
}

func (m *mockHealthSink) get() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.status
}

// mockHardwareDetector allows mocking CUDA availability.
type mockHardwareDetector struct {
	cudaAvailable bool
}

func (m mockHardwareDetector) DetectCUDA() bool {
	return m.cudaAvailable
}

// mockDiskChecker allows simulating disk capacity.
type mockDiskChecker struct {
	freeBytes uint64
	err       error
}

func (m mockDiskChecker) FreeBytes(_ string) (uint64, error) {
	return m.freeBytes, m.err
}

// mockMemoryChecker allows simulating memory usage percentage.
type mockMemoryChecker struct {
	percent float64
	ok      bool
}

func (m mockMemoryChecker) MemoryUsagePercent() (float64, bool) {
	return m.percent, m.ok
}

// createTestYUVFrame generates a valid 64x64 yuv420p frame.
func createTestYUVFrame(candidateKey string, seq uint64) processing.Frame {
	w, h := 64, 64
	ySize := w * h
	cSize := (w / 2) * (h / 2)
	data := make([]byte, ySize+2*cSize)
	for i := range data {
		data[i] = 128
	}
	return processing.Frame{
		CandidateKey:     candidateKey,
		Timestamp:        time.Now().UTC(),
		SourceReceivedAt: time.Now().UTC(),
		Seq:              seq,
		OutputWidth:      w,
		OutputHeight:     h,
		Data:             data,
	}
}

// createTestJPEG creates a simple valid JPEG byte slice.
func createTestJPEG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 32, 32))
	for y := 0; y < 32; y++ {
		for x := 0; x < 32; x++ {
			img.Set(x, y, color.RGBA{R: 255, G: 0, B: 0, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80}); err != nil {
		t.Fatalf("failed to encode test jpeg: %v", err)
	}
	return buf.Bytes()
}

func TestDetectionToLocalEvent(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewEventStore(tmpDir)
	if err != nil {
		t.Fatalf("NewEventStore: %v", err)
	}

	evMgr, err := NewEvidenceManager(tmpDir, nil, 85, nil)
	if err != nil {
		t.Fatalf("NewEvidenceManager: %v", err)
	}

	hs := &mockHealthSink{}
	hw := NewHardwareManager(DeviceAuto, mockHardwareDetector{cudaAvailable: false}, nil)
	limits := NewLimitsManager(LimitsConfig{MaxConcurrentInference: 2, QueueDepth: 16}, nil, nil, nil)

	cfg := ServiceConfig{
		EdgeID:     "edge-test-1",
		TenantID:   "tenant-100",
		SiteID:     "site-200",
		DataDir:    tmpDir,
		ModelName:  "yolo11s-pose.pt+yolo11n.pt",
		DeviceMode: DeviceAuto,
	}
	svc := NewService(cfg, store, evMgr, hw, limits, hs, nil)

	frame := createTestYUVFrame("cam-front", 42)
	res := InferenceResult{
		CandidateKey:       "cam-front",
		FrameSeq:           42,
		FrameTimestamp:     frame.Timestamp,
		InferenceTimestamp: time.Now().UTC(),
		InferenceMs:        12.5,
		Detections: []LocalDetection{
			{
				ClassID:    0,
				Label:      "person",
				Tipo:       "persona",
				Confidence: 0.92,
				BBox:       BoundingBox{X1: 10, Y1: 20, X2: 50, Y2: 80},
			},
		},
	}

	events, err := svc.ProcessInference(res, &frame)
	if err != nil {
		t.Fatalf("ProcessInference failed: %v", err)
	}

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}

	evt := events[0]
	if evt.EventUUID == "" {
		t.Error("event_uuid must not be empty")
	}
	if evt.EdgeID != "edge-test-1" {
		t.Errorf("expected edge_id edge-test-1, got %s", evt.EdgeID)
	}
	if evt.TenantID != "tenant-100" || evt.SiteID != "site-200" {
		t.Errorf("unexpected tenant/site: %s / %s", evt.TenantID, evt.SiteID)
	}
	if evt.Tipo != "persona" || evt.ClassID != 0 || evt.Confidence != 0.92 {
		t.Errorf("detection fields mismatch: tipo=%s class=%d conf=%f", evt.Tipo, evt.ClassID, evt.Confidence)
	}
	if evt.ProcessingMode != "edge" {
		t.Errorf("processing_mode must be edge, got %s", evt.ProcessingMode)
	}
	if evt.SyncStatus != SyncStatusPending {
		t.Errorf("sync_status must be pending, got %s", evt.SyncStatus)
	}
	if evt.Evidence == nil {
		t.Fatal("evidence must not be nil")
	}
	if evt.Evidence.Path == "" || evt.Evidence.SHA256 == "" || evt.Evidence.SizeBytes <= 0 {
		t.Errorf("invalid evidence metadata: %+v", evt.Evidence)
	}

	// Verify event file persisted on disk
	persisted, err := store.Get(evt.EventUUID)
	if err != nil {
		t.Fatalf("failed to read persisted event: %v", err)
	}
	if persisted.EventUUID != evt.EventUUID {
		t.Errorf("uuid mismatch: %s vs %s", persisted.EventUUID, evt.EventUUID)
	}

	// Verify health status updated
	st := hs.get()
	if st.LocalDetections != 1 || st.LocalEventsCreated != 1 || st.EvidenceSaved != 1 {
		t.Errorf("health status metrics mismatch: %+v", st)
	}
}

func TestZeroDetectionZeroEvent(t *testing.T) {
	tmpDir := t.TempDir()
	store, _ := NewEventStore(tmpDir)
	evMgr, _ := NewEvidenceManager(tmpDir, nil, 85, nil)
	hs := &mockHealthSink{}

	svc := NewService(ServiceConfig{DataDir: tmpDir}, store, evMgr, nil, nil, hs, nil)

	res := InferenceResult{
		CandidateKey: "cam-1",
		FrameSeq:     10,
		Detections:   nil, // ZERO detections
	}

	frame := createTestYUVFrame("cam-1", 10)
	events, err := svc.ProcessInference(res, &frame)
	if !errors.Is(err, ErrZeroDetections) {
		t.Fatalf("expected ErrZeroDetections, got %v", err)
	}
	if len(events) != 0 {
		t.Errorf("expected 0 events, got %d", len(events))
	}

	st := hs.get()
	if st.LocalEventsCreated != 0 || st.EvidenceSaved != 0 {
		t.Errorf("expected 0 events created and 0 evidence saved, got: %+v", st)
	}
}

func TestDeterministicUniqueEventIDs(t *testing.T) {
	tmpDir := t.TempDir()
	store, _ := NewEventStore(tmpDir)
	svc := NewService(ServiceConfig{DataDir: tmpDir}, store, nil, nil, nil, nil, nil)

	res := InferenceResult{
		CandidateKey: "cam-1",
		FrameSeq:     1,
		Detections: []LocalDetection{
			{ClassID: 1, Label: "car", Tipo: "vehiculo", Confidence: 0.85, BBox: BoundingBox{X1: 10, Y1: 10, X2: 50, Y2: 50}},
			{ClassID: 2, Label: "truck", Tipo: "vehiculo", Confidence: 0.75, BBox: BoundingBox{X1: 60, Y1: 60, X2: 100, Y2: 100}},
		},
	}

	events, err := svc.ProcessInference(res, nil)
	if err != nil {
		t.Fatalf("ProcessInference: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}

	if events[0].EventUUID == events[1].EventUUID {
		t.Errorf("event UUIDs must be unique, got duplicate: %s", events[0].EventUUID)
	}
}

func TestBBoxAndConfidencePreserved(t *testing.T) {
	tmpDir := t.TempDir()
	store, _ := NewEventStore(tmpDir)
	svc := NewService(ServiceConfig{DataDir: tmpDir}, store, nil, nil, nil, nil, nil)

	wantBBox := BoundingBox{X1: 12.345, Y1: 23.456, X2: 87.654, Y2: 98.765}
	wantConf := 0.887766

	res := InferenceResult{
		CandidateKey: "cam-roi",
		FrameSeq:     55,
		Detections: []LocalDetection{
			{ClassID: 3, Label: "motorcycle", Tipo: "vehiculo", Confidence: wantConf, BBox: wantBBox},
		},
	}

	events, err := svc.ProcessInference(res, nil)
	if err != nil {
		t.Fatalf("ProcessInference: %v", err)
	}

	evt := events[0]
	if evt.Confidence != wantConf {
		t.Errorf("confidence mismatch: got %f, want %f", evt.Confidence, wantConf)
	}
	if evt.BBox != wantBBox {
		t.Errorf("bbox mismatch: got %+v, want %+v", evt.BBox, wantBBox)
	}

	persisted, err := store.Get(evt.EventUUID)
	if err != nil {
		t.Fatalf("Get event: %v", err)
	}
	if persisted.Confidence != wantConf || persisted.BBox != wantBBox {
		t.Errorf("persisted bbox or confidence altered: %+v", persisted)
	}
}

func TestEvidencePublicationAndChecksum(t *testing.T) {
	tmpDir := t.TempDir()
	evMgr, err := NewEvidenceManager(tmpDir, nil, 85, nil)
	if err != nil {
		t.Fatalf("NewEvidenceManager: %v", err)
	}

	rawJPEG := createTestJPEG(t)
	expectedHash := sha256.Sum256(rawJPEG)
	expectedHex := hex.EncodeToString(expectedHash[:])

	const testEventUUID = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	ref, err := evMgr.SaveJPEG(testEventUUID, time.Now().UTC(), rawJPEG)
	if err != nil {
		t.Fatalf("SaveJPEG: %v", err)
	}

	if ref.SHA256 != expectedHex {
		t.Errorf("checksum mismatch: got %s, want %s", ref.SHA256, expectedHex)
	}
	if ref.SizeBytes != int64(len(rawJPEG)) {
		t.Errorf("size mismatch: got %d, want %d", ref.SizeBytes, len(rawJPEG))
	}

	// Verify file on disk
	fullPath := filepath.Join(tmpDir, ref.Path)
	data, err := os.ReadFile(fullPath)
	if err != nil {
		t.Fatalf("failed to read written evidence: %v", err)
	}
	if !bytes.Equal(data, rawJPEG) {
		t.Errorf("saved bytes do not match original jpeg")
	}

	// Verify atomic protection against overwrite
	_, err = evMgr.SaveJPEG(testEventUUID, time.Now().UTC(), rawJPEG)
	if !errors.Is(err, ErrEvidenceAlreadyExists) {
		t.Errorf("expected ErrEvidenceAlreadyExists on duplicate event uuid, got %v", err)
	}
}

func TestRestartPreservesEventsAndEvidence(t *testing.T) {
	tmpDir := t.TempDir()

	// 1. First run creates 3 events
	store1, err := NewEventStore(tmpDir)
	if err != nil {
		t.Fatalf("store1: %v", err)
	}

	for i := 0; i < 3; i++ {
		evt, err := NewLocalEvent("edge-1", "t-1", "s-1", "yolov8n",
			InferenceResult{CandidateKey: "cam-1", FrameSeq: uint64(i)},
			LocalDetection{ClassID: 0, Label: "person", Confidence: 0.9}, nil)
		if err != nil {
			t.Fatalf("NewLocalEvent: %v", err)
		}
		if err := store1.Save(evt); err != nil {
			t.Fatalf("Save event %d: %v", i, err)
		}
	}

	if store1.BacklogCount() != 3 {
		t.Fatalf("store1 backlog: got %d, want 3", store1.BacklogCount())
	}

	// 2. Simulate restart: instantiate new store pointing to same dataDir
	store2, err := NewEventStore(tmpDir)
	if err != nil {
		t.Fatalf("store2: %v", err)
	}

	if store2.BacklogCount() != 3 {
		t.Errorf("store2 backlog after restart: got %d, want 3", store2.BacklogCount())
	}

	pending, err := store2.ListPending()
	if err != nil {
		t.Fatalf("ListPending: %v", err)
	}
	if len(pending) != 3 {
		t.Errorf("expected 3 pending events, got %d", len(pending))
	}
}

func TestDiskFailureHandling(t *testing.T) {
	tmpDir := t.TempDir()
	store, _ := NewEventStore(tmpDir)

	// Inject disk checker reporting free space below threshold (e.g. 5MB free, threshold 100MB)
	mockDC := mockDiskChecker{freeBytes: 5 * 1024 * 1024}
	limits := NewLimitsManager(LimitsConfig{
		MinFreeDiskBytes: 100 * 1024 * 1024,
	}, mockDC, nil, nil)

	evMgr, _ := NewEvidenceManager(tmpDir, limits, 85, nil)
	hs := &mockHealthSink{}

	svc := NewService(ServiceConfig{DataDir: tmpDir}, store, evMgr, nil, limits, hs, nil)

	frame := createTestYUVFrame("cam-full", 99)
	res := InferenceResult{
		CandidateKey: "cam-full",
		FrameSeq:     99,
		Detections: []LocalDetection{
			{ClassID: 0, Label: "person", Confidence: 0.95, BBox: BoundingBox{X1: 10, Y1: 10, X2: 50, Y2: 50}},
		},
	}

	// Must NOT panic! Must degrade safely, create event with error in evidence ref
	events, err := svc.ProcessInference(res, &frame)
	if err != nil {
		t.Fatalf("ProcessInference should succeed even when evidence fails: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}

	evt := events[0]
	if evt.Evidence == nil {
		t.Fatal("expected evidence ref to record error, got nil")
	}
	if evt.Evidence.ErrorMessage == "" {
		t.Error("expected non-empty ErrorMessage in evidence ref")
	}

	st := hs.get()
	if st.EvidenceFailures != 1 || st.EvidenceSaved != 0 {
		t.Errorf("expected EvidenceFailures=1 and EvidenceSaved=0, got: %+v", st)
	}
	if !st.Limits.DiskSaturated {
		t.Error("expected DiskSaturated=true")
	}
}

func TestCUDAUnavailableFallbackToCPU(t *testing.T) {
	detector := mockHardwareDetector{cudaAvailable: false}
	hw := NewHardwareManager(DeviceCUDA, detector, nil)

	if hw.CurrentDevice() != string(DeviceCPU) {
		t.Errorf("expected fallback to CPU, got %s", hw.CurrentDevice())
	}
	if hw.FallbackCount() != 1 {
		t.Errorf("expected fallback count 1, got %d", hw.FallbackCount())
	}

	st := hw.Status()
	if st.ConfiguredDevice != "cuda" || st.CurrentDevice != "cpu" || st.CUDAAvailable != false {
		t.Errorf("unexpected hardware status: %+v", st)
	}
	if st.NPU.Status != NPUStatusMessage {
		t.Errorf("unexpected NPU status: %s", st.NPU.Status)
	}
}

func TestCUDAAvailableSelection(t *testing.T) {
	detector := mockHardwareDetector{cudaAvailable: true}

	// Auto mode with CUDA -> CUDA
	hwAuto := NewHardwareManager(DeviceAuto, detector, nil)
	if hwAuto.CurrentDevice() != string(DeviceCUDA) {
		t.Errorf("expected CUDA in auto mode, got %s", hwAuto.CurrentDevice())
	}
	if hwAuto.FallbackCount() != 0 {
		t.Errorf("expected 0 fallbacks, got %d", hwAuto.FallbackCount())
	}

	// Explicit CUDA mode with CUDA -> CUDA
	hwCUDA := NewHardwareManager(DeviceCUDA, detector, nil)
	if hwCUDA.CurrentDevice() != string(DeviceCUDA) {
		t.Errorf("expected CUDA in cuda mode, got %s", hwCUDA.CurrentDevice())
	}
}

func TestInvalidDeviceConfig(t *testing.T) {
	_, err := ParseDeviceMode("tensorrt")
	if !errors.Is(err, ErrInvalidDevice) {
		t.Errorf("expected ErrInvalidDevice, got %v", err)
	}

	_, err = ParseDeviceMode("openvino")
	if !errors.Is(err, ErrInvalidDevice) {
		t.Errorf("expected ErrInvalidDevice, got %v", err)
	}

	valid, err := ParseDeviceMode("cuda")
	if err != nil || valid != DeviceCUDA {
		t.Errorf("expected valid cuda, got %v, err: %v", valid, err)
	}
}

func TestBoundedResourceBehavior(t *testing.T) {
	limits := NewLimitsManager(LimitsConfig{
		MaxConcurrentInference: 2,
		QueueDepth:             4,
	}, nil, nil, nil)

	// Acquire 2 slots
	if !limits.TryAcquireInference() {
		t.Fatal("failed to acquire first slot")
	}
	if !limits.TryAcquireInference() {
		t.Fatal("failed to acquire second slot")
	}

	// Third acquire must fail (bounded concurrency)
	if limits.TryAcquireInference() {
		t.Fatal("expected failure on 3rd acquire (max 2)")
	}

	// Release 1 slot
	limits.ReleaseInference()

	// Now acquisition should succeed
	if !limits.TryAcquireInference() {
		t.Fatal("failed to acquire slot after release")
	}

	// Drop tracking
	limits.RecordQueueDrop()
	st := limits.Status("")
	if st.QueueDropped != 1 {
		t.Errorf("expected QueueDropped=1, got %d", st.QueueDropped)
	}
}

func TestMemoryPressureSignaling(t *testing.T) {
	mockMC := mockMemoryChecker{percent: 88.5, ok: true}
	limits := NewLimitsManager(LimitsConfig{
		MaxMemoryPercent: 80.0,
	}, nil, mockMC, nil)

	pressure, pct := limits.CheckMemoryPressure()
	if !pressure {
		t.Errorf("expected memory pressure at 88.5%% (limit 80%%)")
	}
	if pct != 88.5 {
		t.Errorf("expected 88.5%%, got %f", pct)
	}
}

func TestConcurrentProcessInferenceRace(t *testing.T) {
	tmpDir := t.TempDir()
	store, _ := NewEventStore(tmpDir)
	evMgr, _ := NewEvidenceManager(tmpDir, nil, 85, nil)
	hs := &mockHealthSink{}
	svc := NewService(ServiceConfig{DataDir: tmpDir}, store, evMgr, nil, nil, hs, nil)

	var wg sync.WaitGroup
	workers := 8
	iterations := 10

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				frame := createTestYUVFrame("cam-race", uint64(workerID*100+i))
				res := InferenceResult{
					CandidateKey: "cam-race",
					FrameSeq:     uint64(workerID*100 + i),
					Detections: []LocalDetection{
						{ClassID: 0, Label: "person", Confidence: 0.9, BBox: BoundingBox{X1: 10, Y1: 10, X2: 50, Y2: 50}},
					},
				}
				_, err := svc.ProcessInference(res, &frame)
				if err != nil {
					t.Errorf("worker %d iteration %d failed: %v", workerID, i, err)
				}
			}
		}(w)
	}

	wg.Wait()

	st := hs.get()
	expectedTotal := int64(workers * iterations)
	if st.LocalEventsCreated != expectedTotal {
		t.Errorf("expected %d events created, got %d", expectedTotal, st.LocalEventsCreated)
	}
}
