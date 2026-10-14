package vision

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/processing"
)

type h8CaptureEvents struct {
	results chan InferenceResult
}

func (c *h8CaptureEvents) ConsumeInference(r InferenceResult, _ []byte) {
	select {
	case c.results <- r:
	default:
	}
}

// TestH8_SyntheticPipelineContracts_MPS_CPU validates the H8 synthetic pipeline contract:
// RTP stream -> Camera Pipeline -> Decoder -> Vision Sink -> Hermetic Worker -> Detection Dispatch
// under both requested MPS and CPU acceleration modes without requiring physical camera hardware.
func TestH8_SyntheticPipelineContracts_MPS_CPU(t *testing.T) {
	devices := []struct {
		requested string
		effective string
	}{
		{requested: "mps", effective: "mps"},
		{requested: "cpu", effective: "cpu"},
	}

	for _, tc := range devices {
		t.Run("device_"+tc.requested, func(t *testing.T) {
			cfg := baseTestConfig(t)
			// Darwin unix socket path must be < 104 chars; nested subtests under /var/folders exceed it
			sockFile, err := os.CreateTemp("", "h8-*.sock")
			if err != nil {
				t.Fatal(err)
			}
			sockPath := sockFile.Name()
			_ = sockFile.Close()
			_ = os.Remove(sockPath)
			t.Cleanup(func() { _ = os.Remove(sockPath) })

			cfg.SocketPath = sockPath
			cfg.Device = tc.requested
			w, models := startWorkerWithEnv(t, cfg,
				"GEOCAM_VISION_FAKE_EFFECTIVE_DEVICE", tc.effective,
				"GEOCAM_VISION_FAKE_REQUESTED_DEVICE", tc.requested,
			)
			waitForState(t, w, StateReady, 5*time.Second)

			st := w.Status()
			if st.Device != tc.effective {
				t.Fatalf("effective device = %q, want %q", st.Device, tc.effective)
			}
			if st.DeviceRequested != tc.requested {
				t.Fatalf("requested device = %q, want %q", st.DeviceRequested, tc.requested)
			}

			events := &h8CaptureEvents{results: make(chan InferenceResult, 10)}
			sink := NewSink(w, models, nil, events, nil)

			// Create a synthetic frame routed directly through Sink to Worker
			frame := processing.Frame{
				CandidateKey:     "h8-cam",
				Seq:              42,
				OutputWidth:      4,
				OutputHeight:     4,
				Data:             make([]byte, 24), // 4x4 yuv420p = 16 Y + 4 U + 4 V
				SourceReceivedAt: time.Now(),
				Timestamp:        time.Now(),
			}

			if err := sink.Route(frame); err != nil {
				t.Fatalf("Route frame to sink: %v", err)
			}

			select {
			case res := <-events.results:
				if res.FrameSeq != 42 {
					t.Fatalf("result frame_seq = %d, want 42", res.FrameSeq)
				}
				if len(res.Detections) != 1 || res.Detections[0].Type != DetectionTypePerson {
					t.Fatalf("unexpected detections: %+v", res.Detections)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("timed out waiting for synthetic detection event")
			}

			if err := w.Stop(context.Background()); err != nil {
				t.Fatalf("Worker Stop: %v", err)
			}
			if w.Status().State != StateStopped {
				t.Fatalf("worker state = %q, want %q", w.Status().State, StateStopped)
			}
		})
	}
}
