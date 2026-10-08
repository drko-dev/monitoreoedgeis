package agent

import (
	"context"
	"log/slog"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/config"
	"github.com/drko-dev/monitoreoedgeis/internal/health"
	"github.com/drko-dev/monitoreoedgeis/internal/processing"
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
	return processing.NewFreshSink(vs, inferenceMaxFrameAge, configured, reporter.SetEdgeInferenceStatus)
}
