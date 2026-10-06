package agent

import (
	"context"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/cloudsink"
	"github.com/drko-dev/monitoreoedgeis/internal/config"
	"github.com/drko-dev/monitoreoedgeis/internal/credentials"
	"github.com/drko-dev/monitoreoedgeis/internal/health"
	"github.com/drko-dev/monitoreoedgeis/internal/logging"
	"github.com/drko-dev/monitoreoedgeis/internal/processing"
	"github.com/drko-dev/monitoreoedgeis/internal/transport"
)

// cloudBufferDirName is the subdirectory of GEOCAM_DATA_DIR holding
// Milestone I6's offline spool.
const cloudBufferDirName = "cloud-buffer"

// edgeVideoSinkName is the router name of the Full Edge live-view sink.
const edgeVideoSinkName = "edge-video"

// newCloudSink builds the Milestone I video-frame-upload sink, or returns nil
// when this Edge has nothing to push frames to or isn't configured for cloud
// processing.
//
// Gated on cfg.ProcessingMode being ModeCloud or ModeHybrid (not a separate
// env var): this reuses the mode the Edge already reports in every
// heartbeat, rather than adding a second knob that could disagree with it.
// A camera the SaaS has linked to this device (edge_device_cameras) only
// receives Edge-push frames while this Edge itself is configured for cloud
// or hybrid processing — never both RTSP-pull (SaaS-side, legacy) and
// Edge-push for the same camera.
//
// In ModeHybrid, CloudSink stays exactly what it was in ModeCloud: Cloud
// remains the sole inference engine, and this sink has no idea frames were
// pre-filtered. The filtering itself (Milestone J's local motion
// evaluator) lives entirely upstream, in
// internal/processing.cameraPipeline.readLoop, ahead of router.Dispatch —
// never here.
// buildCloudSink builds the Milestone I video-frame-upload sink regardless of
// startup ProcessingMode, returning nil when this Edge has nothing to push frames to.
func buildCloudSink(cfg *config.Config, creds credentials.Credentials, reporter *health.Reporter, log *slog.Logger) processing.Sink {
	if cfg.SaaSURL == "" {
		log.Info("cloud video sink disabled: no GEOCAM_SAAS_URL configured")
		return nil
	}
	if !creds.IsEnrolled() || creds.Credential == "" || creds.DeviceID == "" {
		log.Info("cloud video sink disabled: edge is not enrolled",
			slog.String("credential_status", creds.Status.String()))
		return nil
	}

	client, err := transport.New(cfg.SaaSURL, cfg.AllowInsecureHTTP, cfg.SaaSTimeout, Version)
	if err != nil {
		log.Warn("cloud video sink disabled: transport client error", slog.Any("error", err))
		return nil
	}

	var opts []cloudsink.Option
	if cfg.CloudBufferMaxBytes > 0 && cfg.CloudBufferMaxFrames > 0 {
		opts = append(opts, cloudsink.WithBuffer(
			filepath.Join(cfg.DataDir, cloudBufferDirName),
			cfg.CloudBufferMaxBytes, cfg.CloudBufferMaxFrames, cfg.CloudBufferMaxAge))
	} else {
		log.Info("cloud offline buffer disabled: GEOCAM_CLOUD_BUFFER_MAX_BYTES/GEOCAM_CLOUD_BUFFER_MAX_FRAMES not set")
	}

	sinkCfg := cloudsink.Config{
		JPEGQuality:    cfg.CloudJPEGQuality,
		MaxBytesPerSec: cfg.CloudMaxBytesPerSec,
		BurstBytes:     cfg.CloudBurstBytes,
		MaxFPS:         cfg.CloudMaxFPS,
	}
	return cloudsink.New(client, creds.DeviceID, creds.Credential, sinkCfg, logging.Component(log, "cloud-sink"), reporter, opts...)
}

func newCloudSink(cfg *config.Config, creds credentials.Credentials, reporter *health.Reporter, log *slog.Logger) processing.Sink {
	if cfg.ProcessingMode != config.ModeCloud && cfg.ProcessingMode != config.ModeHybrid {
		return nil
	}
	return buildCloudSink(cfg, creds, reporter, log)
}

// videoFrameSender adapts transport.Client to cloudsink.FrameSender, routing
// every frame to the display-only VideoFramesPath. It deliberately does not
// implement cloudsink.MetadataFrameSender, so no hybrid candidate metadata
// can ever reach the SaaS through it.
type videoFrameSender struct{ client *transport.Client }

func (s videoFrameSender) PostFrame(ctx context.Context, deviceID, credential, candidateKey string, seq uint64, capturedAt time.Time, jpeg []byte) error {
	return s.client.PostVideoFrame(ctx, deviceID, credential, candidateKey, seq, capturedAt, jpeg)
}

// edgeVideoHealth reports the video sink's counters under edge_video, never
// under cloud: video transport is not Cloud inference.
type edgeVideoHealth struct{ reporter *health.Reporter }

func (h edgeVideoHealth) SetCloudStatus(s cloudsink.Status) { h.reporter.SetEdgeVideoStatus(s) }

// buildEdgeVideoSink builds the Full Edge live-view sink: it reuses
// CloudSink's JPEG encoding and I7 rate limits, but uploads to the
// display-only VideoFramesPath. No offline buffer: a stale live frame is
// worthless. Returns nil when this Edge has nothing to push frames to.
func buildEdgeVideoSink(cfg *config.Config, creds credentials.Credentials, reporter *health.Reporter, log *slog.Logger) processing.Sink {
	if cfg.SaaSURL == "" || !creds.IsEnrolled() || creds.Credential == "" || creds.DeviceID == "" {
		return nil
	}
	client, err := transport.New(cfg.SaaSURL, cfg.AllowInsecureHTTP, cfg.SaaSTimeout, Version)
	if err != nil {
		log.Warn("edge video sink disabled: transport client error", slog.Any("error", err))
		return nil
	}
	sinkCfg := cloudsink.Config{
		JPEGQuality:    cfg.CloudJPEGQuality,
		MaxBytesPerSec: cfg.CloudMaxBytesPerSec,
		BurstBytes:     cfg.CloudBurstBytes,
		MaxFPS:         cfg.CloudMaxFPS,
	}
	return cloudsink.New(videoFrameSender{client}, creds.DeviceID, creds.Credential, sinkCfg, logging.Component(log, "edge-video-sink"), edgeVideoHealth{reporter}, cloudsink.WithName(edgeVideoSinkName))
}

// newEdgeVideoSink returns the live-view sink only in ModeEdge; in cloud and
// hybrid the CloudSink already carries the video.
func newEdgeVideoSink(cfg *config.Config, creds credentials.Credentials, reporter *health.Reporter, log *slog.Logger) processing.Sink {
	if cfg.ProcessingMode != config.ModeEdge {
		return nil
	}
	return buildEdgeVideoSink(cfg, creds, reporter, log)
}
