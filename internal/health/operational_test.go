package health

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/config"
	"github.com/drko-dev/monitoreoedgeis/internal/identity"
	"github.com/drko-dev/monitoreoedgeis/internal/platform"
	"github.com/drko-dev/monitoreoedgeis/internal/processing"
	"github.com/drko-dev/monitoreoedgeis/internal/rtsp"
)

func TestOperationalReadinessDetectsCameraAndPipelineGaps(t *testing.T) {
	cfg := &config.Config{ProcessingMode: config.ModeCloud, VideoPipelineEnabled: true}
	tests := []struct {
		name     string
		cfg      *config.Config
		targets  *CameraTargetsStatus
		pipeline *processing.VideoPipelineSummary
		cameras  []rtsp.CameraStreamStatus
		want     string
		contains string
	}{
		{
			name:     "reconciliation not run",
			want:     "WAITING",
			contains: "camera_target_reconciliation_pending",
		},
		{
			name: "discovered authenticated camera has no credential",
			targets: &CameraTargetsStatus{
				State: "reconciled", DiscoveredDeviceCount: 1, SkippedByReason: map[string]int{"auth_required_no_credential": 1},
			},
			want:     "DEGRADED",
			contains: "camera_credentials_unavailable",
		},
		{
			name: "expected target has no pipeline",
			targets: &CameraTargetsStatus{
				State: "reconciled", DiscoveredDeviceCount: 1, ExpectedCameraCount: 1,
			},
			want:     "DEGRADED",
			contains: "expected_camera_pipeline_missing",
		},
		{
			name:    "camera offline does not change process health but degrades operation",
			targets: &CameraTargetsStatus{State: "reconciled", DiscoveredDeviceCount: 1, ExpectedCameraCount: 1},
			pipeline: &processing.VideoPipelineSummary{
				CameraCount: 1, Cameras: []processing.PipelineStatus{{State: "running"}},
			},
			cameras:  []rtsp.CameraStreamStatus{{Status: rtsp.StateOffline}},
			want:     "DEGRADED",
			contains: "camera_stream_unavailable",
		},
		{
			name:     "pipeline disabled is explicit not configured profile",
			cfg:      &config.Config{ProcessingMode: config.ModeCloud, VideoPipelineEnabled: false},
			want:     "NOT_CONFIGURED",
			contains: "video_pipeline_disabled",
		},
		{
			name:     "expected camera online and processing",
			targets:  &CameraTargetsStatus{State: "reconciled", DiscoveredDeviceCount: 1, ExpectedCameraCount: 1, LastReconciledAt: time.Now()},
			pipeline: &processing.VideoPipelineSummary{CameraCount: 1, Cameras: []processing.PipelineStatus{{State: "running", FramesDecoded: 12, FramesSampled: 6}}},
			cameras:  []rtsp.CameraStreamStatus{{Status: rtsp.StateOnline}},
			want:     "READY",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfgForTest := tt.cfg
			if cfgForTest == nil {
				cfgForTest = cfg
			}
			got := calculateOperationalReadiness(
				cfgForTest,
				cfgForTest.Profile(),
				tt.pipeline,
				tt.cameras,
				tt.targets,
			)
			if got.State != tt.want {
				t.Fatalf("state=%q, want %q (reasons=%v)", got.State, tt.want, got.Reasons)
			}
			if tt.contains != "" && !containsString(got.Reasons, tt.contains) {
				t.Errorf("reasons=%v, want %q", got.Reasons, tt.contains)
			}
		})
	}
}

func TestOperationalEndpointDegradesWithoutRestartingProcessHealth(t *testing.T) {
	cfg := &config.Config{ProcessingMode: config.ModeCloud, VideoPipelineEnabled: true}
	reporter := New("test", cfg, identity.Identity{EdgeID: "edge-test"}, platform.Info{})
	reporter.Set(StateReady)
	reporter.SetCameraTargets(CameraTargetsStatus{
		State: "reconciled", DiscoveredDeviceCount: 1,
		SkippedByReason: map[string]int{"auth_required_no_credential": 1},
	})
	handler := Handler(reporter)

	for path, wantCode := range map[string]int{
		"/healthz":      http.StatusOK,
		"/readyz":       http.StatusOK,
		"/operationalz": http.StatusServiceUnavailable,
	} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != wantCode {
			t.Errorf("GET %s = %d, want %d", path, recorder.Code, wantCode)
		}
	}

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/status", nil))
	var snapshot Snapshot
	if err := json.Unmarshal(recorder.Body.Bytes(), &snapshot); err != nil {
		t.Fatalf("decode /status: %v", err)
	}
	if snapshot.Status != StateReady || snapshot.Operational.State != "DEGRADED" {
		t.Fatalf("process/operational state = %q/%q, want READY/DEGRADED", snapshot.Status, snapshot.Operational.State)
	}
	if !containsString(snapshot.Operational.Reasons, "camera_credentials_unavailable") {
		t.Fatalf("operational reasons=%v", snapshot.Operational.Reasons)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
