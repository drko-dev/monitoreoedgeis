package health

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/config"
	"github.com/drko-dev/monitoreoedgeis/internal/discovery"
	"github.com/drko-dev/monitoreoedgeis/internal/edgebacklog"
	"github.com/drko-dev/monitoreoedgeis/internal/fulledge"
	"github.com/drko-dev/monitoreoedgeis/internal/identity"
	"github.com/drko-dev/monitoreoedgeis/internal/platform"
	"github.com/drko-dev/monitoreoedgeis/internal/processing"
	"github.com/drko-dev/monitoreoedgeis/internal/rtsp"
)

func TestReporterStartsInStarting(t *testing.T) {
	if got := newTestReporter().State(); got != StateStarting {
		t.Errorf("State() = %q, want %q", got, StateStarting)
	}
}

func TestReporterTransitions(t *testing.T) {
	r := newTestReporter()
	for _, want := range []State{StateReady, StateDegraded, StateStopping} {
		r.Set(want)
		if got := r.State(); got != want {
			t.Errorf("State() = %q, want %q", got, want)
		}
	}
}

func TestSnapshot(t *testing.T) {
	cfg := &config.Config{ProcessingMode: config.ModeHybrid, LogLevel: "info"}
	r := New("0.1.0", cfg, identity.Identity{EdgeID: "edge-42", Status: identity.StatusEnrolled},
		platform.Info{Hostname: "test-host", OS: "linux", GOARCH: "arm64"})
	r.Set(StateReady)

	got := r.Snapshot()
	if got.Status != StateReady {
		t.Errorf("Status = %q, want %q", got.Status, StateReady)
	}
	if got.Version != "0.1.0" {
		t.Errorf("Version = %q, want %q", got.Version, "0.1.0")
	}
	if got.EdgeID != "edge-42" {
		t.Errorf("EdgeID = %q, want %q", got.EdgeID, "edge-42")
	}
	if got.EnrollmentStatus != string(identity.StatusEnrolled) {
		t.Errorf("EnrollmentStatus = %q, want %q", got.EnrollmentStatus, identity.StatusEnrolled)
	}
	if got.Hostname != "test-host" || got.OS != "linux" || got.Architecture != "arm64" {
		t.Errorf("platform fields = %+v", got)
	}
	if got.ProcessingMode != string(config.ModeHybrid) {
		t.Errorf("ProcessingMode = %q, want %q", got.ProcessingMode, config.ModeHybrid)
	}
	if got.UptimeSeconds < 0 {
		t.Error("UptimeSeconds is negative")
	}
	if got.Modules == nil {
		t.Error("Modules is nil, want an (empty) map")
	}
}

func TestSnapshotInsecureHTTPAllowed(t *testing.T) {
	secure := New("0.1.0", &config.Config{}, identity.Identity{}, platform.Info{}).Snapshot()
	if secure.InsecureHTTPAllowed {
		t.Error("InsecureHTTPAllowed = true, want false when AllowInsecureHTTP is unset")
	}

	insecureCfg := &config.Config{AllowInsecureHTTP: true}
	insecure := New("0.1.0", insecureCfg, identity.Identity{}, platform.Info{}).Snapshot()
	if !insecure.InsecureHTTPAllowed {
		t.Error("InsecureHTTPAllowed = false, want true when AllowInsecureHTTP is set")
	}
}

func TestSnapshotUnenrolled(t *testing.T) {
	cfg := &config.Config{ProcessingMode: config.ModeCloud}
	r := New("0.1.0", cfg, identity.Identity{}, platform.Info{})

	snap := r.Snapshot()
	if snap.EnrollmentStatus != "" {
		t.Errorf("EnrollmentStatus = %q, want empty", snap.EnrollmentStatus)
	}
	if snap.EdgeID != "" {
		t.Errorf("EdgeID = %q, want empty", snap.EdgeID)
	}
}

func TestReporterCredentialStatus(t *testing.T) {
	r := newTestReporter()
	r.SetCredentialStatus("ENROLLED")

	if got := r.Snapshot().CredentialStatus; got != "ENROLLED" {
		t.Errorf("CredentialStatus = %q, want %q", got, "ENROLLED")
	}
}

func TestReporterModuleStates(t *testing.T) {
	r := newTestReporter()
	r.SetModuleState("health-http", "starting")
	r.SetModuleState("health-http", "running")
	r.SetModuleState("other", "stopped")

	snap := r.Snapshot()
	if snap.Modules["health-http"] != "running" {
		t.Errorf("Modules[health-http] = %q, want %q", snap.Modules["health-http"], "running")
	}
	if snap.Modules["other"] != "stopped" {
		t.Errorf("Modules[other] = %q, want %q", snap.Modules["other"], "stopped")
	}
}

func TestReporterDiscoveryStatus(t *testing.T) {
	r := newTestReporter()
	if r.Snapshot().Discovery != nil {
		t.Errorf("Discovery should be nil initially")
	}

	r.SetDiscoveryStatus(discovery.ModuleStatus{
		State:       "idle",
		DeviceCount: 3,
	})

	snap := r.Snapshot()
	if snap.Discovery == nil {
		t.Fatalf("expected non-nil Discovery in snapshot")
	}
	if snap.Discovery.State != "idle" || snap.Discovery.DeviceCount != 3 {
		t.Errorf("unexpected Discovery snapshot: %+v", snap.Discovery)
	}
}

func TestReporterFullEdgeStatus(t *testing.T) {
	r := newTestReporter()
	if r.Snapshot().FullEdge != nil {
		t.Errorf("FullEdge should be nil initially")
	}

	r.SetFullEdgeStatus(fulledge.Status{
		LocalDetections:        5,
		LocalEventsCreated:     3,
		EvidenceSaved:          3,
		EvidenceFailures:       0,
		LocalEventBacklog:      3,
		CurrentInferenceDevice: "cpu",
		Hardware: fulledge.HardwareStatus{
			ConfiguredDevice: "auto",
			CurrentDevice:    "cpu",
			CUDAAvailable:    false,
			NPU: fulledge.NPUCapability{
				AdapterReady:  true,
				BackendActive: false,
				Status:        fulledge.NPUStatusMessage,
			},
		},
	})

	snap := r.Snapshot()
	if snap.FullEdge == nil {
		t.Fatalf("expected non-nil FullEdge in snapshot")
	}
	if snap.FullEdge.LocalDetections != 5 || snap.FullEdge.LocalEventsCreated != 3 {
		t.Errorf("unexpected FullEdge counts: %+v", snap.FullEdge)
	}
	if snap.FullEdge.Hardware.NPU.Status != fulledge.NPUStatusMessage {
		t.Errorf("unexpected NPU status: %s", snap.FullEdge.Hardware.NPU.Status)
	}
}

// TestReporterVideoPipelineStatus is Hito N's targeted test for item 4
// (status exposes the expected metrics): the real, reused frames/FPS/latency
// counters from internal/processing (built in earlier hitos) must reach the
// /status snapshot unchanged, with no invented precision.
func TestReporterVideoPipelineStatus(t *testing.T) {
	r := newTestReporter()
	if r.Snapshot().VideoPipeline != nil {
		t.Errorf("VideoPipeline should be nil initially")
	}

	r.SetVideoPipeline(processing.VideoPipelineSummary{
		CameraCount: 1,
		Cameras: []processing.PipelineStatus{
			{
				CandidateKey:    "cam-1",
				State:           "running",
				InputFPS:        15.0,
				DecodedFPS:      14.8,
				OutputFPS:       5.0,
				FramesReceived:  100,
				FramesDecoded:   98,
				FramesSampled:   33,
				FramesDropped:   2,
				DecodeLatencyMs: 12.5,
			},
		},
	})

	snap := r.Snapshot()
	if snap.VideoPipeline == nil {
		t.Fatalf("expected non-nil VideoPipeline in snapshot")
	}
	if snap.VideoPipeline.CameraCount != 1 || len(snap.VideoPipeline.Cameras) != 1 {
		t.Fatalf("unexpected VideoPipeline shape: %+v", snap.VideoPipeline)
	}
	cam := snap.VideoPipeline.Cameras[0]
	if cam.CandidateKey != "cam-1" || cam.FramesDecoded != 98 || cam.FramesDropped != 2 {
		t.Errorf("unexpected per-camera counters: %+v", cam)
	}
	if cam.DecodeLatencyMs != 12.5 {
		t.Errorf("DecodeLatencyMs = %v, want 12.5 (real measurement, not invented)", cam.DecodeLatencyMs)
	}
}

func TestSnapshot_ResourcesNoFalseZeros(t *testing.T) {
	r := newTestReporter()
	// Case 1: Unprimed CPU and macOS-like memory (total known, used not derivable)
	r.SetPlatformSample(platform.Sample{
		CPUPercent:         nil, // Unprimed / unmeasurable
		MemTotalBytes:      16 * 1024 * 1024 * 1024,
		MemUsedBytes:       0, // not derivable
		MemAvailableBytes:  0,
		DiskTotalBytes:     100 * 1024 * 1024 * 1024,
		DiskUsedBytes:      40 * 1024 * 1024 * 1024,
		DiskAvailableBytes: 60 * 1024 * 1024 * 1024,
		DiskAvailableKnown: true,
		DiskDataDir:        "/var/lib/geocam-edge",
		TemperatureC:       nil, // no thermal sensor
	})

	snap := r.Snapshot()
	if snap.Resources == nil {
		t.Fatalf("expected non-nil Resources in Snapshot")
	}
	if snap.Resources.CPU != nil && snap.Resources.CPU.Percent != nil {
		t.Errorf("expected CPU to be nil or have nil Percent when unprimed, got %v", *snap.Resources.CPU.Percent)
	}
	if snap.Resources.Memory == nil || snap.Resources.Memory.TotalBytes != 16*1024*1024*1024 {
		t.Errorf("unexpected Memory TotalBytes: %+v", snap.Resources.Memory)
	}
	if snap.Resources.Memory.UsedBytes != nil {
		t.Errorf("expected Memory UsedBytes to be nil when not derivable, got %d (false zero)", *snap.Resources.Memory.UsedBytes)
	}
	if snap.Resources.Thermal != nil {
		t.Errorf("expected Thermal to be nil when no sensor exists, got %+v", snap.Resources.Thermal)
	}
	if snap.Resources.Disk == nil || snap.Resources.Disk.AvailableBytes != 60*1024*1024*1024 {
		t.Errorf("unexpected Disk status: %+v", snap.Resources.Disk)
	}
	if snap.Resources.Disk.DataDir != "/var/lib/geocam-edge" {
		t.Errorf("unexpected Disk data dir: %s", snap.Resources.Disk.DataDir)
	}
}

// TestSnapshot_DiskAvailableZeroIsReal is Blocker 1's required test: a
// filesystem that is genuinely full for an unprivileged writer (Bavail==0,
// but statfs succeeded) must report available_bytes=0 — never fall back to
// total-used, which can include root-reserved blocks and would mask a real
// out-of-space condition.
func TestSnapshot_DiskAvailableZeroIsReal(t *testing.T) {
	r := newTestReporter()
	r.SetPlatformSample(platform.Sample{
		DiskTotalBytes:     100,
		DiskUsedBytes:      90,
		DiskAvailableBytes: 0,
		DiskAvailableKnown: true,
		DiskDataDir:        "/var/lib/geocam-edge",
	})

	snap := r.Snapshot()
	if snap.Resources == nil || snap.Resources.Disk == nil {
		t.Fatalf("expected non-nil Resources.Disk in Snapshot")
	}
	if got := snap.Resources.Disk.AvailableBytes; got != 0 {
		t.Errorf("AvailableBytes = %d, want 0 (real, known available -- not the total-used fallback of 10)", got)
	}
}

func TestSnapshot_QueuesDistinctAndCoherent(t *testing.T) {
	r := newTestReporter()
	now := time.Now().UTC()

	// 1. Router queues
	r.SetVideoPipeline(processing.VideoPipelineSummary{
		CameraCount: 1,
		RouterQueues: []processing.RouterQueueStats{
			{SinkName: "debug", Depth: 2, Capacity: 16, Drops: 0},
			{SinkName: "cloud", Depth: 4, Capacity: 32, Drops: 1},
		},
		CloudBuffer: &processing.CloudBufferStats{
			BufferedFrames: 5,
			Capacity:       100,
			DroppedFull:    2,
			OldestPending:  &now,
		},
	})

	// 2. EdgeBacklog
	r.SetLocalEventBacklogStatus(edgebacklog.Status{
		BacklogCount:  3,
		Capacity:      500,
		Drops:         0,
		OldestPending: &now,
		Degraded:      false,
		Quarantined:   1,
	})

	// 3. FullEdge & Vision
	r.SetFullEdgeStatus(fulledge.Status{
		Limits: fulledge.LimitsStatus{
			InFlightInference:      1,
			MaxConcurrentInference: 2,
			QueueDepth:             8,
			QueueDropped:           3,
			MemoryPressure:         false,
		},
	})

	snap := r.Snapshot()
	if snap.Queues == nil {
		t.Fatalf("expected non-nil Queues in Snapshot")
	}

	// Verify each component is distinctly populated
	if len(snap.Queues.Router) != 2 {
		t.Fatalf("expected 2 router queues, got %d", len(snap.Queues.Router))
	}
	if snap.Queues.Router[0].Name != "debug" || snap.Queues.Router[0].Depth != 2 || snap.Queues.Router[0].Capacity != 16 {
		t.Errorf("unexpected router queue 0: %+v", snap.Queues.Router[0])
	}
	if snap.Queues.Router[1].Name != "cloud" || snap.Queues.Router[1].Depth != 4 || snap.Queues.Router[1].Drops != 1 {
		t.Errorf("unexpected router queue 1: %+v", snap.Queues.Router[1])
	}

	if snap.Queues.CloudBuffer == nil || snap.Queues.CloudBuffer.Depth != 5 || snap.Queues.CloudBuffer.Capacity != 100 {
		t.Errorf("unexpected CloudBuffer queue: %+v", snap.Queues.CloudBuffer)
	}

	if snap.Queues.EdgeBacklog == nil || snap.Queues.EdgeBacklog.Depth != 3 || snap.Queues.EdgeBacklog.Capacity != 500 {
		t.Errorf("unexpected EdgeBacklog queue: %+v", snap.Queues.EdgeBacklog)
	}

	// Capacity must be the bound that actually applies to Depth. Depth is
	// InFlightInference, whose real bound is the MaxConcurrentInference
	// admission semaphore — NOT Limits.QueueDepth (8 here), which no code path
	// enforces. Reporting QueueDepth claimed a vision queue bound that does not
	// exist: the real frame-level queue for this sink is its
	// processing.Router channel, reported under router[] with its own capacity
	// and drop counter.
	if snap.Queues.Vision == nil || snap.Queues.Vision.Depth != 1 || snap.Queues.Vision.Capacity != 2 || snap.Queues.Vision.Drops != 3 {
		t.Errorf("unexpected Vision queue: %+v", snap.Queues.Vision)
	}
	if snap.Queues.Vision.Capacity == 8 {
		t.Error("vision queue Capacity is still the unenforced Limits.QueueDepth; it must report the real admission bound")
	}
}

func TestSnapshot_BackwardCompatibility(t *testing.T) {
	r := newTestReporter()
	r.Set(StateReady)
	r.SetModuleState("health-http", "running")

	snap := r.Snapshot()
	data, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("failed to marshal snapshot: %v", err)
	}

	jsonStr := string(data)
	requiredKeys := []string{
		`"status":"READY"`,
		`"version":`,
		`"edge_id":`,
		`"modules":`,
		`"resources":`,
	}
	for _, key := range requiredKeys {
		if !strings.Contains(jsonStr, key) {
			t.Errorf("serialized snapshot missing required key: %s", key)
		}
	}
}

func TestCameraFailureDoesNotDegradeAgent(t *testing.T) {
	r := newTestReporter()
	r.Set(StateReady)

	// One camera fails / degrades
	r.SetCameras([]rtsp.CameraStreamStatus{
		{
			CandidateKey:  "cam-offline",
			Status:        rtsp.StateDegraded,
			LastErrorSafe: "connection refused",
		},
	})

	// Overall agent status MUST remain READY
	if state := r.State(); state != StateReady {
		t.Errorf("camera failure degraded the agent! state = %s, want READY", state)
	}
	if snap := r.Snapshot(); snap.Status != StateReady {
		t.Errorf("snapshot status = %s, want READY", snap.Status)
	}
}
