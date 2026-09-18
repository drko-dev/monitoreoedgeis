package agent

import (
	"log/slog"

	"github.com/drko-dev/monitoreoedgeis/internal/config"
	"github.com/drko-dev/monitoreoedgeis/internal/credentials"
	"github.com/drko-dev/monitoreoedgeis/internal/fulledge"
	"github.com/drko-dev/monitoreoedgeis/internal/health"
	"github.com/drko-dev/monitoreoedgeis/internal/identity"
)

// newFullEdgeService builds the Milestone K Full Edge event and evidence subsystem
// when the agent is configured in edge processing mode.
func newFullEdgeService(cfg *config.Config, ident identity.Identity, creds credentials.Credentials, reporter *health.Reporter, log *slog.Logger) *fulledge.Service {
	if cfg.ProcessingMode != config.ModeEdge {
		return nil
	}

	// GEOCAM_EDGE_YOLO_DEVICE is the single device knob (see
	// internal/config's doc comment on EdgeYOLODevice) — HardwareManager
	// only uses it as a Go-side capability *preselection*; the device
	// actually reported everywhere else is what the vision worker itself
	// confirms after loading PyTorch/Ultralytics.
	devMode, err := fulledge.ParseDeviceMode(cfg.EdgeYOLODevice)
	if err != nil {
		log.Warn("invalid edge YOLO device, falling back to auto", slog.String("device", cfg.EdgeYOLODevice), slog.Any("error", err))
		devMode = fulledge.DeviceAuto
	}

	limitsCfg := fulledge.LimitsConfig{
		MaxConcurrentInference: cfg.EdgeMaxConcurrentInference,
		QueueDepth:             cfg.EdgeInferenceQueueDepth,
		MinFreeDiskBytes:       cfg.EdgeMinFreeDiskBytes,
		MaxMemoryPercent:       cfg.EdgeMaxMemoryPercent,
	}
	limitsMgr := fulledge.NewLimitsManager(limitsCfg, nil, nil, log)

	hwMgr := fulledge.NewHardwareManager(devMode, nil, log)

	store, err := fulledge.NewEventStore(cfg.DataDir)
	if err != nil {
		log.Error("full edge: failed to initialize event store", slog.Any("error", err))
		return nil
	}

	evidenceMgr, err := fulledge.NewEvidenceManager(cfg.DataDir, limitsMgr, cfg.CloudJPEGQuality, log)
	if err != nil {
		log.Error("full edge: failed to initialize evidence manager", slog.Any("error", err))
		return nil
	}

	var tenantID, siteID string
	if creds.IsEnrolled() {
		tenantID = creds.TenantID
		siteID = creds.SiteID
	}

	svcCfg := fulledge.ServiceConfig{
		EdgeID:   ident.EdgeID,
		TenantID: tenantID,
		SiteID:   siteID,
		DataDir:  cfg.DataDir,
		// The real production model set (Milestone K1-K4), not an invented
		// single-model placeholder — see internal/vision.
		ModelName:   cfg.EdgeYOLOPersonModel + "+" + cfg.EdgeYOLOVehicleModel,
		DeviceMode:  devMode,
		Limits:      limitsCfg,
		JPEGQuality: cfg.CloudJPEGQuality,
	}

	return fulledge.NewService(svcCfg, store, evidenceMgr, hwMgr, limitsMgr, reporter, log)
}
