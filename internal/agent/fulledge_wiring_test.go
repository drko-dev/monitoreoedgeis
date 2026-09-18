package agent

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/edgebacklog"
	"github.com/drko-dev/monitoreoedgeis/internal/evidence"
	"github.com/drko-dev/monitoreoedgeis/internal/fulledge"
	"github.com/drko-dev/monitoreoedgeis/internal/processing"
	"github.com/drko-dev/monitoreoedgeis/internal/transport"
	"github.com/drko-dev/monitoreoedgeis/internal/vision"
)

// fakeLocalEventSender implements transport.LocalEventSender entirely
// in-memory, matching the pattern internal/edgebacklog's own tests use.
type fakeLocalEventSender struct {
	calls  []string
	events []transport.LocalEvent
}

func (s *fakeLocalEventSender) PostLocalEvent(_ context.Context, _, _ string, e transport.LocalEvent) error {
	s.calls = append(s.calls, "metadata:"+e.EventUUID)
	s.events = append(s.events, e)
	return nil
}

func (s *fakeLocalEventSender) PutLocalEventEvidence(_ context.Context, _, _, id, _, kind string, _ []byte, _ string, _ int64) error {
	s.calls = append(s.calls, kind+":"+id)
	return nil
}

// TestFullEdgeWiring_VisionToSyncedEndToEnd is the short integration test
// this batch's instructions ask for: fake vision inference -> fulledge
// event -> capture evidence -> edgebacklog enqueue -> fake SaaS sender ->
// marked synced. It exercises the real internal/fulledge and
// internal/edgebacklog code, faking only the vision.InferenceResult input
// and the transport.LocalEventSender at the very edge — the two disconnected
// trees this integration pass wired together (K1->K8->K12).
func TestFullEdgeWiring_VisionToSyncedEndToEnd(t *testing.T) {
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
		EdgeID:      "edge-test",
		DataDir:     dataDir,
		ModelName:   "yolo11s-pose.pt+yolo11n.pt",
		DeviceMode:  fulledge.DeviceCPU,
		Limits:      fulledge.LimitsConfig{MaxConcurrentInference: 1, QueueDepth: 8},
		JPEGQuality: 85,
	}, store, evidenceMgr, hw, limits, nil, logger)

	backlog, err := edgebacklog.Open(edgebacklog.Config{Dir: t.TempDir(), MaxOperations: 10, MaxBytes: 10 << 20})
	if err != nil {
		t.Fatalf("edgebacklog.Open: %v", err)
	}
	backlog.SetSyncCallbacks(
		func(eventUUID string) { _ = store.MarkSynced(eventUUID) },
		func(eventUUID, reason string) { _ = store.MarkQuarantined(eventUUID, reason) },
	)

	consumer := newFullEdgeEventConsumer(svc, backlog, nil, nil, dataDir, logger)

	// Fake vision inference result with one real detection — the same
	// shape internal/vision.Sink.Route produces from a real worker. Uses a
	// fixed, non-"now" frame timestamp (M2) and a vehicle detection whose
	// Label ("car") is more specific than its Type ("vehicle") (M4), the
	// same shape deploy/vision-worker/backend.py's VEHICLE_CLASS_IDS emits.
	frameTimestamp := time.Date(2026, 1, 15, 10, 30, 0, 0, time.UTC)
	result := vision.InferenceResult{
		CandidateKey: "cam-1",
		FrameSeq:     42,
		Timestamp:    frameTimestamp,
		InferenceMS:  12.5,
		Device:       "cpu",
		Detections: []vision.Detection{
			{ClassID: vision.ClassIDCar, Label: "car", Type: vision.DetectionTypeVehicle, Confidence: 0.91, BBox: [4]float64{10, 20, 110, 220}},
		},
	}
	fakeJPEG := []byte("fake-jpeg-bytes-not-a-real-image")

	consumer.ConsumeInference(result, fakeJPEG)

	if got := svc.Status().LocalEventsCreated; got != 1 {
		t.Fatalf("LocalEventsCreated = %d, want 1", got)
	}
	if got := svc.Status().EvidenceSaved; got != 1 {
		t.Fatalf("EvidenceSaved = %d, want 1", got)
	}
	if got := backlog.Status().BacklogCount; got != 1 {
		t.Fatalf("backlog BacklogCount = %d, want 1 (before drain)", got)
	}

	sender := &fakeLocalEventSender{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Drain metadata -> capture -> mark-complete (no clip was attached).
	for i := 0; i < 5 && backlog.Status().BacklogCount > 0; i++ {
		if !backlog.ProcessOne(ctx, sender, "device-1", "credential-1") {
			break
		}
	}

	if got := backlog.Status().BacklogCount; got != 0 {
		t.Fatalf("backlog BacklogCount after drain = %d, want 0", got)
	}
	if len(sender.calls) == 0 {
		t.Fatal("fake sender received no calls")
	}

	var eventUUID string
	for _, c := range sender.calls {
		if strings.HasPrefix(c, "metadata:") {
			eventUUID = strings.TrimPrefix(c, "metadata:")
			break
		}
	}
	if eventUUID == "" {
		t.Fatalf("no metadata call observed among %v", sender.calls)
	}

	evt, err := store.Get(eventUUID)
	if err != nil {
		t.Fatalf("store.Get(%s): %v", eventUUID, err)
	}
	if evt.SyncStatus != fulledge.SyncStatusSynced {
		t.Fatalf("event SyncStatus = %q, want %q", evt.SyncStatus, fulledge.SyncStatusSynced)
	}
	if evt.CorrelationID != "cam-1-42" {
		t.Fatalf("event CorrelationID = %q, want %q", evt.CorrelationID, "cam-1-42")
	}

	// M2: the frame's own timestamp must survive Frame -> YOLO result ->
	// LocalEvent -> transport.LocalEvent unchanged (RFC3339 at the wire
	// boundary only), never replaced by time.Now().
	if len(sender.events) == 0 {
		t.Fatal("fake sender captured no transport.LocalEvent")
	}
	wireEvent := sender.events[0]
	if wireEvent.Timestamp != frameTimestamp.UTC().Format(rfc3339Milli) {
		t.Errorf("wire Timestamp = %q, want frame timestamp %q", wireEvent.Timestamp, frameTimestamp.UTC().Format(rfc3339Milli))
	}
	// M3: Edge never sends org/tenant/site/camera IDs — only what
	// transport.LocalEvent declares.
	if wireEvent.CandidateKey != "cam-1" {
		t.Errorf("wire CandidateKey = %q, want cam-1", wireEvent.CandidateKey)
	}
	// M4: the specific label ("car") must survive to the wire Class field,
	// not the generic Type ("vehicle").
	if wireEvent.Class != "car" {
		t.Errorf("wire Class = %q, want car (specific label, not generic type)", wireEvent.Class)
	}
	// M11: correlation ID on wire
	if wireEvent.CorrelationID != "cam-1-42" {
		t.Fatalf("wire LocalEvent CorrelationID = %q, want %q", wireEvent.CorrelationID, "cam-1-42")
	}
}

type mockHistoryProvider struct {
	frames []processing.Frame
}

func (m *mockHistoryProvider) FrameHistory(candidateKey string) processing.FrameHistory {
	return m
}

func (m *mockHistoryProvider) Snapshot() []processing.Frame {
	return m.frames
}

func testFFmpegPath(t *testing.T, dir string) string {
	t.Helper()
	if p, err := exec.LookPath("ffmpeg"); err == nil {
		return p
	}
	fake := filepath.Join(dir, "fake_ffmpeg.sh")
	script := "#!/bin/sh\ncat > /dev/null\nfor arg; do true; done\necho 'fake-mp4-data' > \"$arg\"\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return fake
}

func TestFullEdgeWiring_EventWithClipEndToEnd(t *testing.T) {
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
		EdgeID:      "edge-test",
		DataDir:     dataDir,
		ModelName:   "yolo11s-pose.pt+yolo11n.pt",
		DeviceMode:  fulledge.DeviceCPU,
		Limits:      fulledge.LimitsConfig{MaxConcurrentInference: 1, QueueDepth: 8},
		JPEGQuality: 85,
	}, store, evidenceMgr, hw, limits, nil, logger)

	backlogDir := t.TempDir()
	backlog, err := edgebacklog.Open(edgebacklog.Config{Dir: backlogDir, MaxOperations: 10, MaxBytes: 10 << 20})
	if err != nil {
		t.Fatalf("edgebacklog.Open: %v", err)
	}
	backlog.SetSyncCallbacks(
		func(eventUUID string) { _ = store.MarkSynced(eventUUID) },
		func(eventUUID, reason string) { _ = store.MarkQuarantined(eventUUID, reason) },
	)

	ffmpeg := testFFmpegPath(t, dataDir)
	clipper, err := evidence.NewClipper(evidence.ClipConfig{
		DataDir:    dataDir,
		FFmpegPath: ffmpeg,
		MaxFrames:  10,
		PreEvent:   2 * time.Second,
		PostEvent:  0,
		FrameRate:  5,
	})
	if err != nil {
		t.Fatalf("NewClipper: %v", err)
	}

	now := time.Now().UTC()
	frameData := make([]byte, 64*64*3/2)
	for i := range frameData {
		frameData[i] = 128
	}
	history := &mockHistoryProvider{
		frames: []processing.Frame{
			{Seq: 1, Timestamp: now.Add(-time.Second), OutputWidth: 64, OutputHeight: 64, Data: frameData},
			{Seq: 2, Timestamp: now, OutputWidth: 64, OutputHeight: 64, Data: frameData},
		},
	}

	consumer := newFullEdgeEventConsumer(svc, backlog, clipper, history, dataDir, logger)

	result := vision.InferenceResult{
		CandidateKey: "cam-clip",
		FrameSeq:     77,
		Timestamp:    now,
		InferenceMS:  15.0,
		Device:       "cpu",
		Detections: []vision.Detection{
			{ClassID: vision.ClassIDPerson, Label: "person", Type: vision.DetectionTypePerson, Confidence: 0.94, BBox: [4]float64{5, 10, 50, 100}},
		},
	}
	fakeJPEG := []byte("fake-jpeg-bytes")
	consumer.ConsumeInference(result, fakeJPEG)

	if got := backlog.Status().BacklogCount; got != 1 {
		t.Fatalf("backlog BacklogCount = %d, want 1", got)
	}

	sender := &fakeLocalEventSender{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Drain: metadata -> capture -> clip -> complete (3 steps for a submission with both capture and clip).
	for i := 0; i < 5 && backlog.Status().BacklogCount > 0; i++ {
		if !backlog.ProcessOne(ctx, sender, "device-1", "credential-1") {
			break
		}
	}

	if got := backlog.Status().BacklogCount; got != 0 {
		t.Fatalf("backlog BacklogCount after drain = %d, want 0", got)
	}

	// Verify all 3 stages executed in order
	if len(sender.calls) != 3 {
		t.Fatalf("expected 3 sender calls (metadata, capture, clip), got %v", sender.calls)
	}
	if !strings.HasPrefix(sender.calls[0], "metadata:") {
		t.Errorf("call 0 want metadata, got %s", sender.calls[0])
	}
	if !strings.HasPrefix(sender.calls[1], "capture:") {
		t.Errorf("call 1 want capture, got %s", sender.calls[1])
	}
	if !strings.HasPrefix(sender.calls[2], "clip:") {
		t.Errorf("call 2 want clip, got %s", sender.calls[2])
	}

	eventUUID := strings.TrimPrefix(sender.calls[0], "metadata:")

	// Verify clip file was written to GEOCAM_DATA_DIR/evidence/clips/<event_uuid>.mp4
	clipFile := filepath.Join(dataDir, "evidence", "clips", eventUUID+".mp4")
	info, err := os.Stat(clipFile)
	if err != nil || info.Size() == 0 {
		t.Fatalf("clip file %s stat error: %v", clipFile, err)
	}

	// Verify store state synced
	evt, err := store.Get(eventUUID)
	if err != nil {
		t.Fatalf("store.Get(%s): %v", eventUUID, err)
	}
	if evt.SyncStatus != fulledge.SyncStatusSynced {
		t.Fatalf("event SyncStatus = %q, want %q", evt.SyncStatus, fulledge.SyncStatusSynced)
	}

	// Verify correlation ID on wire
	if len(sender.events) == 0 || sender.events[0].CorrelationID != "cam-clip-77" {
		t.Fatalf("wire CorrelationID = %q, want %q", sender.events[0].CorrelationID, "cam-clip-77")
	}
}

func TestFullEdgeWiring_CorrelationIDSurvivesBacklogRestart(t *testing.T) {
	dataDir := t.TempDir()
	backlogDir := t.TempDir()
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
		EdgeID:      "edge-test",
		DataDir:     dataDir,
		ModelName:   "yolo11s-pose.pt+yolo11n.pt",
		DeviceMode:  fulledge.DeviceCPU,
		Limits:      fulledge.LimitsConfig{MaxConcurrentInference: 1, QueueDepth: 8},
		JPEGQuality: 85,
	}, store, evidenceMgr, hw, limits, nil, logger)

	backlog1, err := edgebacklog.Open(edgebacklog.Config{Dir: backlogDir, MaxOperations: 10, MaxBytes: 10 << 20})
	if err != nil {
		t.Fatalf("edgebacklog.Open: %v", err)
	}

	consumer := newFullEdgeEventConsumer(svc, backlog1, nil, nil, dataDir, logger)
	now := time.Now().UTC()
	consumer.ConsumeInference(vision.InferenceResult{
		CandidateKey: "cam-restart",
		FrameSeq:     99,
		Timestamp:    now,
		InferenceMS:  10.0,
		Device:       "cpu",
		Detections: []vision.Detection{
			{ClassID: vision.ClassIDPerson, Label: "person", Type: vision.DetectionTypePerson, Confidence: 0.90, BBox: [4]float64{0, 0, 10, 10}},
		},
	}, []byte("jpeg-bytes"))

	// Simulate restart: re-open backlog from same directory
	backlog2, err := edgebacklog.Open(edgebacklog.Config{Dir: backlogDir, MaxOperations: 10, MaxBytes: 10 << 20})
	if err != nil {
		t.Fatalf("re-open edgebacklog: %v", err)
	}

	sender := &fakeLocalEventSender{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if !backlog2.ProcessOne(ctx, sender, "dev", "cred") {
		t.Fatal("ProcessOne failed after restart")
	}

	if len(sender.events) == 0 {
		t.Fatal("no events sent")
	}
	if got := sender.events[0].CorrelationID; got != "cam-restart-99" {
		t.Fatalf("CorrelationID after restart = %q, want %q", got, "cam-restart-99")
	}
}
