package transport

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestTrafficMeterConcurrentSnapshotAndReset(t *testing.T) {
	m := NewTrafficMeter()
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.Record(TrafficControl, 10, 5, 1)
		}()
	}
	wg.Wait()

	got := m.Snapshot()[TrafficControl]
	if got.BytesSent != 1000 || got.BytesReceived != 500 || got.Requests != 100 {
		t.Fatalf("unexpected counters: %+v", got)
	}

	m.Reset()
	if len(m.Snapshot()) != 0 {
		t.Fatal("Reset did not clear counters")
	}
}

func TestTrafficPathClassification(t *testing.T) {
	cases := map[string]TrafficCategory{
		HeartbeatPath:              TrafficHeartbeat,
		DiscoveryNextPath:          TrafficDiscovery,
		FramesPath:                 TrafficFrames,
		LocalEventsPath:            TrafficEvents,
		OTANextPath:                TrafficOTA,
		ControlNextPath:            TrafficControl,
		"/api/v1/edge/other":       TrafficControl,
		"/api/v1/discovery/report": TrafficDiscovery,
	}
	for path, want := range cases {
		if got := classifyTrafficPath(path); got != want {
			t.Errorf("classifyTrafficPath(%q)=%q want %q", path, got, want)
		}
	}
}

func TestClientTrafficMeterCountsApplicationPayloadOnly(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPut:
			w.WriteHeader(http.StatusOK)
		case strings.Contains(r.URL.Path, "frame"):
			w.WriteHeader(http.StatusAccepted)
		case strings.Contains(r.URL.Path, "event"):
			w.WriteHeader(http.StatusCreated)
		default:
			w.WriteHeader(http.StatusOK)
		}
		_, _ = w.Write([]byte("{}"))
	}))
	defer server.Close()

	client, err := New(server.URL, true, time.Second, "test")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	meter := NewTrafficMeter()
	client.SetTrafficMeter(meter)

	ctx := context.Background()
	if err := client.Heartbeat(ctx, "device-id", "super-secret", HeartbeatRequest{EdgeID: "edge-1"}); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}

	frame := []byte("jpeg-payload")
	if err := client.PostFrame(ctx, "device-id", "super-secret", "cam-1", 1, time.Now(), frame); err != nil {
		t.Fatalf("PostFrame: %v", err)
	}

	event := LocalEvent{
		EventUUID:    "event-1",
		CandidateKey: "cam-1",
		Class:        "person",
		Confidence:   0.9,
		Timestamp:    time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := client.PostLocalEvent(ctx, "device-id", "super-secret", event); err != nil {
		t.Fatalf("PostLocalEvent: %v", err)
	}

	evidence := []byte("evidence-payload")
	if err := client.PutLocalEventEvidence(ctx, "device-id", "super-secret", "event-1", "cam-1", "capture", evidence, "abc123", int64(len(evidence))); err != nil {
		t.Fatalf("PutLocalEventEvidence: %v", err)
	}

	snap := meter.Snapshot()
	if got := snap[TrafficHeartbeat]; got.Requests != 1 || got.BytesSent == 0 {
		t.Fatalf("heartbeat counters: %+v", got)
	}
	if got := snap[TrafficFrames]; got.Requests != 1 || got.BytesSent != uint64(len(frame)) {
		t.Fatalf("frame counters: %+v", got)
	}
	if got := snap[TrafficEvents]; got.Requests != 1 || got.BytesSent == 0 {
		t.Fatalf("event counters: %+v", got)
	}
	if got := snap[TrafficEvidence]; got.Requests != 1 || got.BytesSent != uint64(len(evidence)) {
		t.Fatalf("evidence counters: %+v", got)
	}

	encoded, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if strings.Contains(string(encoded), "super-secret") ||
		strings.Contains(string(encoded), "device-id") ||
		strings.Contains(string(encoded), "jpeg-payload") ||
		strings.Contains(string(encoded), "evidence-payload") {
		t.Fatalf("traffic snapshot leaked request data: %s", encoded)
	}
}
