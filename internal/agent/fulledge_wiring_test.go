package agent

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/edgebacklog"
	"github.com/drko-dev/monitoreoedgeis/internal/fulledge"
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

	consumer := newFullEdgeEventConsumer(svc, backlog, dataDir, logger)

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
}
