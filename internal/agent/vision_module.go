package agent

import (
	"context"
	"log/slog"

	"github.com/drko-dev/monitoreoedgeis/internal/config"
	"github.com/drko-dev/monitoreoedgeis/internal/health"
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
// asks for, rather than crashing agent startup.
func newVisionSink(cfg *config.Config, reporter *health.Reporter, log *slog.Logger) (*vision.Sink, Module) {
	if cfg.ProcessingMode != config.ModeEdge {
		return nil, nil
	}
	models := vision.NewModelManager(cfg.EdgeYOLOModelsDir, cfg.EdgeYOLOPersonModel, cfg.EdgeYOLOVehicleModel)
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

	sink := vision.NewSink(worker, models, reporter, log)
	return sink, &visionModule{worker: worker}
}
