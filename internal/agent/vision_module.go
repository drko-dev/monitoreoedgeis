package agent

import (
	"context"
	"log/slog"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/config"
	"github.com/drko-dev/monitoreoedgeis/internal/health"
	"github.com/drko-dev/monitoreoedgeis/internal/inference"
	"github.com/drko-dev/monitoreoedgeis/internal/processing"
	"github.com/drko-dev/monitoreoedgeis/internal/remoteconfig"
	"github.com/drko-dev/monitoreoedgeis/internal/vision"
)

// visionModule adapts vision.Worker's lifecycle to the agent's Module
// interface, so its start/stop is ordered by moduleManager exactly like
// every other subsystem instead of a bespoke goroutine kicked off from New.
type visionModule struct {
	worker *vision.Worker
}

func (m *visionModule) Name() string                    { return "edge-vision" }
func (m *visionModule) Start(ctx context.Context) error { return m.worker.Start(ctx) }
func (m *visionModule) Stop(ctx context.Context) error  { return m.worker.Stop(ctx) }

// newVisionSink builds Milestone K's local-inference routing destination
// and its Module wrapper, or returns a nil sink when this Edge isn't
// configured for edge processing mode.
//
// Gated on cfg.ProcessingMode == ModeEdge — the same "derive from the
// existing mode, don't add a second knob" rule newCloudSink already
// follows (see cloudsink_module.go) — so cloud/hybrid Edges build no
// vision.Worker at all, not even an idle one.
//
// A missing GEOCAM_EDGE_YOLO_WORKER_CMD or missing model weights is never
// fatal here: the worker reports StateNotConfigured/StateModelMissing under
// /status and every frame Route sees is counted as dropped-not-ready,
// exactly the explicit "NOT_READY / model_missing" contract Milestone K3
// buildVisionSink builds Milestone K's local-inference routing destination
// and its Module wrapper using the provided ModelManager, without gating on cfg.ProcessingMode.
func buildVisionSink(cfg *config.Config, reporter *health.Reporter, consumer vision.EventConsumer, models *vision.ModelManager, log *slog.Logger) (*vision.Sink, Module) {
	if models == nil {
		models = vision.NewModelManager(cfg.EdgeYOLOModelsDir, cfg.EdgeYOLOPersonModel, cfg.EdgeYOLOVehicleModel)
	}
	if cfg.EdgeYOLOWorkerCmd == "" {
		log.Warn("edge vision worker disabled: GEOCAM_EDGE_YOLO_WORKER_CMD is not configured")
	}
	worker := vision.NewWorker(vision.Config{
		WorkerCmd:         cfg.EdgeYOLOWorkerCmd,
		WorkerArgs:        cfg.EdgeYOLOWorkerArgs,
		ModelsDir:         cfg.EdgeYOLOModelsDir,
		PersonModel:       cfg.EdgeYOLOPersonModel,
		VehicleModel:      cfg.EdgeYOLOVehicleModel,
		PersonConfidence:  cfg.EdgeYOLOPersonConfidence,
		VehicleConfidence: cfg.EdgeYOLOVehicleConfidence,
		NMSIoU:            cfg.EdgeYOLONMSIoU,
		Device:            cfg.EdgeYOLODevice,
		ImgSize:           cfg.EdgeYOLOImgSize,
		SocketPath:        cfg.EdgeYOLOSocketPath,
		StartTimeout:      cfg.EdgeYOLOStartTimeout,
		InferTimeout:      cfg.EdgeYOLOInferTimeout,
	}, models, log)

	sink := vision.NewSink(worker, models, reporter, consumer, log)
	return sink, &visionModule{worker: worker}
}

// newVisionSink builds Milestone K's local-inference routing destination
// and its Module wrapper, or returns a nil sink when this Edge isn't
// configured for edge processing mode.
func newVisionSink(cfg *config.Config, reporter *health.Reporter, consumer vision.EventConsumer, log *slog.Logger, models ...*vision.ModelManager) (*vision.Sink, Module) {
	if cfg.ProcessingMode != config.ModeEdge {
		return nil, nil
	}
	var mm *vision.ModelManager
	if len(models) > 0 {
		mm = models[0]
	}
	return buildVisionSink(cfg, reporter, consumer, mm, log)
}

// clipPreEvent is how much history before an event a clip covers;
// clipHistoryMargin keeps the ring a little longer than that so the window's
// oldest frame is never the one being overwritten.
const (
	clipPreEvent      = 3 * time.Second
	clipHistoryMargin = time.Second
)

// inferenceMaxFrameAge drops a frame whose turn at the shared YOLO worker
// comes later than this after it was decoded: an old detection is worse
// than a skipped one. With one pending frame per camera it only triggers
// when the round-robin round itself exceeds it (worker overloaded).
const inferenceMaxFrameAge = 2 * time.Second

// freshInference puts vs behind a processing.FreshSink: latest frame per
// camera, served round-robin, so one fast camera never queues ahead of the
// others at the single YOLO worker (and the Router FIFO never fills).
func (a *Agent) freshInference(vs *vision.Sink, reporter *health.Reporter) processing.Sink {
	configured := func(key string) (float64, bool) {
		if a.videoManager == nil {
			return 0, false
		}
		return managerFPSController{a.videoManager}.CurrentTargetFPS(key)
	}
	fs := processing.NewFreshSink(vs, inferenceMaxFrameAge, configured, reporter.SetEdgeInferenceStatus)
	a.freshSink.Store(fs)
	return fs
}

// motionObservingConsumer tells each camera's passive MotionGate when local
// YOLO detected something (activation vs. detection correlation), then
// forwards the result unchanged. It never filters, delays or alters events.
type motionObservingConsumer struct {
	inner vision.EventConsumer
	gate  func(candidateKey string) *processing.MotionGate
}

func (c motionObservingConsumer) ConsumeInference(result vision.InferenceResult, jpeg []byte) {
	if g := c.gate(result.CandidateKey); g != nil {
		g.ObserveDetection(result.Timestamp)
	}
	c.inner.ConsumeInference(result, jpeg)
}

func (a *Agent) motionObserved(inner vision.EventConsumer) vision.EventConsumer {
	if !a.cfg.EdgeMotionGateEnabled {
		return inner
	}
	return motionObservingConsumer{inner: inner, gate: func(key string) *processing.MotionGate {
		if a.videoManager == nil {
			return nil
		}
		return a.videoManager.MotionGate(key)
	}}
}

// inferenceVideo adapts processing.Manager to inference.Video.
type inferenceVideo struct{ m *processing.Manager }

func (v inferenceVideo) CandidateKeys() []string { return v.m.CandidateKeys() }
func (v inferenceVideo) CurrentTargetFPS(key string) (float64, bool) {
	return managerFPSController{v.m}.CurrentTargetFPS(key)
}
func (v inferenceVideo) SetTargetFPS(key string, fps float64) error {
	return v.m.SetTargetFPS(key, fps)
}
func (v inferenceVideo) MotionState(key string) string {
	if g := v.m.MotionGate(key); g != nil {
		return g.State()
	}
	return ""
}
func (v inferenceVideo) SetMotionSensitivity(key, s string) {
	if g := v.m.MotionGate(key); g != nil {
		g.SetSensitivity(s)
	}
}

func (a *Agent) newInferenceManager(videoMgr *processing.Manager, reporter *health.Reporter) *inference.Manager {
	return inference.NewManager(inferenceVideo{videoMgr}, inference.Options{
		GlobalTargetFPS: a.cfg.VideoTargetFPS,
		Default:         inference.CameraConfig{Mode: a.cfg.EdgeInferenceMode, MotionSensitivity: a.cfg.EdgeMotionSensitivity},
		Reserve:         a.cfg.EdgeInferenceReserve,
		OnStatus:        reporter.SetAdaptiveInferenceStatus,
		// Only while the runtime is in edge mode: after a remote switch to
		// cloud/hybrid the samplers are not ours.
		Active: func() bool {
			if m, ok := a.runtimeApplier.(interface{ CurrentMode() config.ProcessingMode }); ok {
				return m.CurrentMode() == config.ModeEdge
			}
			return a.cfg.ProcessingMode == config.ModeEdge
		},
		Measure: func() map[string]inference.Measured {
			fs := a.freshSink.Load()
			if fs == nil {
				return nil
			}
			out := map[string]inference.Measured{}
			for key, st := range fs.Status() {
				out[key] = inference.Measured{EffectiveFPS: st.EffectiveFPS, LatencyMS: st.AvgLatencyMS, Dropped: st.DroppedStale + st.DroppedSuperseded}
			}
			return out
		},
	})
}

// inferencePolicy is nil (adapter keeps setting samplers) when no Manager.
func (a *Agent) inferencePolicy() remoteconfig.InferencePolicy {
	if a.inferenceMgr == nil {
		return nil
	}
	return a.inferenceMgr
}

// inferenceModule runs the Manager's tick loop for the agent's lifetime.
type inferenceModule struct{ mgr *inference.Manager }

func (inferenceModule) Name() string { return "inference-manager" }
func (m inferenceModule) Start(ctx context.Context) error {
	go m.mgr.Run(ctx)
	return nil
}
func (inferenceModule) Stop(context.Context) error { return nil }

// anprBurstController lets the ANPR burst hint talk to the Manager: bursts
// are demand (SetBurstFPS), never a direct sampler change.
type anprBurstController struct{ mgr *inference.Manager }

func (c anprBurstController) CurrentTargetFPS(key string) (float64, bool) {
	return c.mgr.CurrentTargetFPS(key)
}
func (c anprBurstController) SetTargetFPS(key string, fps float64) error {
	c.mgr.SetBurstFPS(key, fps)
	return nil
}
func (c anprBurstController) SetBurstFPS(key string, fps float64) { c.mgr.SetBurstFPS(key, fps) }
