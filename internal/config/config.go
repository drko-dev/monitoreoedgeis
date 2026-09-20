// Package config loads GEO CAM Edge configuration from environment variables.
package config

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Config is the full runtime configuration of the agent.
type Config struct {
	EdgeID            string
	ProcessingMode    ProcessingMode
	LogLevel          string
	SaaSURL           string
	HeartbeatInterval time.Duration
	// HeartbeatAuthFailureInterval is the slow poll used after the SaaS
	// rejects the Edge credential with a 401/403, and therefore also the
	// worst-case delay before the Edge notices the credential works again and
	// clears its DEGRADED state. Zero means "use the heartbeat module's own
	// default" (heartbeat.AuthFailureInterval, five minutes) rather than
	// duplicating that number here; tests set it explicitly to exercise
	// recovery without waiting for the production cadence.
	HeartbeatAuthFailureInterval time.Duration
	DataDir                      string
	HealthAddr                   string
	// AllowInsecureHTTP permits SaaSURL to use http:// instead of https://.
	// It never weakens TLS verification for an https:// URL — see
	// internal/transport. Development only; defaults to false.
	AllowInsecureHTTP bool
	// OTAPublicKeyFile is a root-managed file path holding the Ed25519
	// public key used to verify OTA release signatures (Hito T4). Empty
	// means OTA verification fails closed — there is no checksum-only
	// fallback. Provisioned separately from the artifact channel itself.
	OTAPublicKeyFile string
	// SaaSTimeout bounds every SaaS HTTP request (enroll, rotate, me).
	SaaSTimeout time.Duration
	// Discovery settings (Milestone E).
	DiscoveryEnabled    bool
	DiscoveryInterval   time.Duration
	DiscoveryTimeout    time.Duration
	DiscoveryInterfaces []string
	// Camera connectivity settings (Milestone G).
	ConnectivityEnabled bool
	StreamRole          string
	StreamTimeout       time.Duration
	// Video pipeline settings (Milestone H). Meaningless without RTSP
	// connectivity, so it is only actually wired up when both
	// ConnectivityEnabled and VideoPipelineEnabled are true.
	VideoPipelineEnabled        bool
	VideoTargetFPS              float64
	VideoOutputWidth            int
	VideoOutputHeight           int
	VideoRingBufferSize         int
	VideoQueueDepth             int
	VideoDecodeQueueDepth       int
	VideoMaxConcurrentPipelines int
	VideoFFmpegPath             string
	VideoDecodeTimeout          time.Duration
	// Hybrid mode local-analysis settings (Milestone J). Meaningless
	// unless ProcessingMode == ModeHybrid.
	HybridMotionThreshold float64
	HybridMinChangedArea  float64
	HybridBlockSize       int
	HybridROIs            []HybridROI
	HybridIdleFPS         float64
	HybridIdleAfter       time.Duration
	// Cloud offline buffer settings (Milestone I6). CloudBufferMaxBytes and
	// CloudBufferMaxFrames have no production default: shipping a number
	// here would be inventing business policy this package has no basis
	// for. Buffering only activates once both are set to a positive value
	// (see internal/cloudsink.WithBuffer); until then CloudSink behaves
	// exactly as it did before I6 (drop on failure). CloudBufferMaxAge is
	// different: 0 is itself a safe, non-business default (no age-based
	// eviction — rely on the byte/frame caps alone).
	CloudBufferMaxBytes  int64
	CloudBufferMaxFrames int
	CloudBufferMaxAge    time.Duration
	// Cloud bandwidth control settings (Milestone I7).
	CloudJPEGQuality    int
	CloudMaxBytesPerSec int64
	CloudBurstBytes     int64
	CloudMaxFPS         float64
	// Local YOLO vision-worker settings (Milestone K1-K4). Meaningless unless
	// ProcessingMode == ModeEdge — the vision sink is only constructed then
	// (see internal/agent/vision_module.go), matching how Hybrid/CloudSink
	// derive their enablement from ProcessingMode rather than a second knob.
	// EdgeYOLOWorkerCmd has no production default: inventing a Python
	// interpreter path here would silently paper over a missing
	// installation. Empty means "not configured" -- the agent reports
	// NOT_READY/worker_not_configured rather than guessing.
	//
	// EdgeYOLODevice is the single source of truth for which device the
	// vision worker is *asked* to use (K1-K4's --device flag). Milestone
	// K5-K8 originally defined a second, disconnected
	// GEOCAM_EDGE_INFERENCE_DEVICE knob for the same concept (Go-side
	// CUDA-capability preselection feeding fulledge.HardwareManager) — that
	// duplicate has been removed.
	//
	// Honest note on the two layers (verified against the code, Hito Z):
	// this raw string is what internal/agent passes to the worker
	// (internal/agent/vision_module.go), i.e. the worker receives the
	// *requested* device, not a Go-resolved one. Resolution is layered:
	// internal/fulledge.HardwareManager resolves it Go-side for *status and
	// fallback accounting only* (CPU/CUDA preselection via /dev/nvidia*,
	// /dev/nvhost-ctrl and nvidia-smi), while the Python worker resolves it
	// authoritatively at load time through torch.cuda.is_available() and
	// reports both the effective device and what was requested back over the
	// health handshake. The worker-confirmed values are the ones to trust:
	// /status .vision.worker.device / .device_requested and each event's
	// device. See internal/vision and deploy/vision-worker/backend.py.
	EdgeYOLOWorkerCmd         string
	EdgeYOLOWorkerArgs        []string
	EdgeYOLOModelsDir         string
	EdgeYOLOPersonModel       string
	EdgeYOLOVehicleModel      string
	EdgeYOLOPersonConfidence  float64
	EdgeYOLOVehicleConfidence float64
	EdgeYOLONMSIoU            float64
	EdgeYOLODevice            string
	EdgeYOLOImgSize           int
	EdgeYOLOSocketPath        string
	EdgeYOLOStartTimeout      time.Duration
	EdgeYOLOInferTimeout      time.Duration
	// Full Edge local event/evidence settings (Milestone K5-K8).
	// EdgeMaxConcurrentInference/EdgeInferenceQueueDepth describe the
	// *real* effective concurrency/queue of the vision sink's Router path
	// (processing.Router runs exactly one worker goroutine per sink through
	// one bounded queue — see internal/processing/router.go) — reconciled
	// with, not a second independent limit alongside, that queue.
	EdgeMaxConcurrentInference int
	EdgeInferenceQueueDepth    int
	EdgeMinFreeDiskBytes       uint64
	EdgeMaxMemoryPercent       float64
	// Local event transport backlog (K10-K12). Unlike the frame buffer this
	// has conservative defaults because events must survive a SaaS outage.
	LocalEventBacklogMaxOperations int
	LocalEventBacklogMaxBytes      int64
	// EdgeMaxClipSizeBytes is a separate, explicit technical ceiling for MP4
	// clip evidence (K9/integration item #8) — deliberately NOT the same
	// knob as MAX_CAPTURE_SIZE_BYTES on the SaaS side (that one sizes a
	// single JPEG). A clip this Edge would build larger than what the SaaS
	// accepts is rejected locally (never attempted/never uploaded to be
	// quarantined pointlessly) — see internal/evidence.Clipper's caller in
	// internal/agent. Zero means "not configured": no invented commercial
	// default here, only a real one wired to match the SaaS's actual limit.
	EdgeMaxClipSizeBytes int64
}

// HybridROI is one normalized (0..1) region of interest parsed from
// GEOCAM_VIDEO_HYBRID_ROI. Defined here (not in internal/processing) to
// keep config free of a dependency on the feature packages it configures,
// consistent with validateVideoOutputDimensions below; agent wiring
// converts it to processing.ROI.
type HybridROI struct {
	XMin, YMin, XMax, YMax float64
}

// Defaults. No secrets, no credentials.
const (
	DefaultProcessingMode    = ModeCloud
	DefaultLogLevel          = "info"
	DefaultHeartbeatInterval = 30 * time.Second
	// MinHeartbeatInterval and MaxHeartbeatInterval bound
	// GEOCAM_HEARTBEAT_INTERVAL. Below the minimum a fleet becomes a load
	// source rather than a liveness signal; above the maximum the SaaS would
	// declare the Edge offline between two healthy beats, because the
	// server-side offline threshold is a multiple of the nominal interval.
	MinHeartbeatInterval = 5 * time.Second
	MaxHeartbeatInterval = 5 * time.Minute
	// MinHeartbeatAuthFailureInterval is deliberately very small: the lower
	// bound only has to stop a nonsensical value (0 is "use the module
	// default"), and an appliance operator may legitimately want the Edge to
	// re-check a re-enabled credential more often than the five-minute
	// default. The upper bound matches the default's order of magnitude -- a
	// revocation is an administrative state, so polling it faster than the
	// heartbeat itself would be pointless, and slower than a few minutes
	// would leave the Edge DEGRADED long after the cause was gone.
	MinHeartbeatAuthFailureInterval = 100 * time.Millisecond
	MaxHeartbeatAuthFailureInterval = 30 * time.Minute
	DefaultDataDir                  = "/var/lib/geocam-edge"
	// DefaultHealthAddr binds the local health HTTP surface to localhost
	// only: it is not meant to be exposed to the LAN.
	DefaultHealthAddr = "127.0.0.1:8091"
	// DefaultSaaSTimeout bounds SaaS HTTP requests.
	DefaultSaaSTimeout = 10 * time.Second
	// Discovery defaults and bounds.
	DefaultDiscoveryEnabled  = true
	DefaultDiscoveryInterval = 5 * time.Minute
	MinDiscoveryInterval     = 1 * time.Minute
	MaxDiscoveryInterval     = 24 * time.Hour
	DefaultDiscoveryTimeout  = 4 * time.Second
	MinDiscoveryTimeout      = 1 * time.Second
	MaxDiscoveryTimeout      = 30 * time.Second
	// Connectivity defaults and bounds (Milestone G).
	DefaultConnectivityEnabled = true
	DefaultStreamRole          = "sub"
	DefaultStreamTimeout       = 5 * time.Second
	MinStreamTimeout           = 1 * time.Second
	MaxStreamTimeout           = 60 * time.Second
	// Video pipeline defaults and bounds (Milestone H).
	DefaultVideoPipelineEnabled        = false
	DefaultVideoTargetFPS              = 5.0
	MinVideoTargetFPS                  = 0.1
	MaxVideoTargetFPS                  = 30.0
	DefaultVideoOutputWidth            = 640
	DefaultVideoOutputHeight           = 360
	MaxVideoOutputWidth                = 1920
	MaxVideoOutputHeight               = 1080
	DefaultVideoRingBufferSize         = 30
	MinVideoRingBufferSize             = 1
	MaxVideoRingBufferSize             = 300
	DefaultVideoQueueDepth             = 64
	MinVideoQueueDepth                 = 4
	MaxVideoQueueDepth                 = 512
	DefaultVideoDecodeQueueDepth       = 4
	MinVideoDecodeQueueDepth           = 1
	MaxVideoDecodeQueueDepth           = 16
	DefaultVideoMaxConcurrentPipelines = 4
	MinVideoMaxConcurrentPipelines     = 1
	MaxVideoMaxConcurrentPipelines     = 16
	DefaultVideoFFmpegPath             = "ffmpeg"
	DefaultVideoDecodeTimeout          = 10 * time.Second
	MinVideoDecodeTimeout              = 1 * time.Second
	MaxVideoDecodeTimeout              = 60 * time.Second
	// Hybrid mode defaults and bounds (Milestone J). All meaningless
	// unless GEOCAM_PROCESSING_MODE=hybrid.
	//
	// DefaultHybridMotionThreshold (luma units, 0..255 scale): chosen well
	// above typical H.264 compression/sensor noise on a static scene
	// (empirically a few luma units) but well below a real scene change,
	// so a static camera does not flap into "motion" from encoding noise
	// alone.
	DefaultHybridMotionThreshold = 8.0
	MinHybridMotionThreshold     = 0.0
	MaxHybridMotionThreshold     = 255.0
	// DefaultHybridMinChangedArea (fraction of evaluated blocks, 0..1):
	// low enough to catch a person partially entering frame, high enough
	// that a handful of noisy blocks alone never triggers a candidate.
	DefaultHybridMinChangedArea = 0.05
	MinHybridMinChangedArea     = 0.0
	MaxHybridMinChangedArea     = 1.0
	// DefaultHybridBlockSize (pixels): small enough to localize motion
	// reasonably, large enough to average out single-pixel noise and keep
	// the per-frame cost trivial.
	DefaultHybridBlockSize = 16
	MinHybridBlockSize     = 4
	MaxHybridBlockSize     = 128
	// DefaultHybridIdleFPS = 0 disables Milestone J5 adaptive sampling
	// entirely: any positive value must be explicitly opted into, so a
	// hybrid pipeline with no GEOCAM_VIDEO_HYBRID_IDLE_FPS set has an
	// identical Sampler to a cloud-mode one.
	DefaultHybridIdleFPS = 0.0
	MinHybridIdleFPS     = 0.0
	// DefaultHybridIdleAfter only takes effect once HybridIdleFPS>0: how
	// long the evaluator must see no motion candidate before dropping to
	// idle FPS. Short enough to save bandwidth quickly, long enough to
	// absorb a brief gap between real motion events without flapping.
	DefaultHybridIdleAfter = 5 * time.Second
	// Cloud bandwidth control defaults and bounds (Milestone I7).
	DefaultCloudJPEGQuality    = 85
	MinCloudJPEGQuality        = 1
	MaxCloudJPEGQuality        = 100
	DefaultCloudMaxBytesPerSec = 0
	DefaultCloudBurstBytes     = 0
	DefaultCloudMaxFPS         = 0.0
	// DefaultLocalEventBacklogMaxOperations/MaxBytes (Milestone K10-K12):
	// technical bounds only, no invented commercial retention policy.
	DefaultLocalEventBacklogMaxOperations = 100
	DefaultLocalEventBacklogMaxBytes      = 512 << 20
	// DefaultEdgeMaxClipSizeBytes (Milestone K9/integration item #8): a
	// technical ceiling distinct from the SaaS's JPEG-sized
	// MAX_CAPTURE_SIZE_BYTES=5MB — clips are naturally larger than a single
	// frame. 20MB comfortably covers a few seconds of H.264 at the
	// resolutions this pipeline already produces (Hito H: max 1920x1080),
	// without being large enough to make a rejected-clip retry loop cheap.
	// Not a business/commercial number — a real, coherent value the Edge
	// enforces locally before ever attempting an upload the SaaS would
	// reject anyway.
	DefaultEdgeMaxClipSizeBytes = 20 << 20
	// Local YOLO vision-worker defaults and bounds (Milestone K).
	// DefaultEdgeYOLOModelsDirName is a subdirectory of GEOCAM_DATA_DIR, kept
	// outside any versioned release tree so an agent update never destroys
	// installed weights (K3).
	DefaultEdgeYOLOModelsDirName     = "models"
	DefaultEdgeYOLOPersonModel       = "yolo11s-pose.pt"
	DefaultEdgeYOLOVehicleModel      = "yolo11n.pt"
	DefaultEdgeYOLOPersonConfidence  = 0.5
	DefaultEdgeYOLOVehicleConfidence = 0.5
	MinEdgeYOLOConfidence            = 0.0
	MaxEdgeYOLOConfidence            = 1.0
	DefaultEdgeYOLONMSIoU            = 0.45
	MinEdgeYOLONMSIoU                = 0.0
	MaxEdgeYOLONMSIoU                = 1.0
	DefaultEdgeYOLODevice            = "cpu"
	DefaultEdgeYOLOImgSize           = 640
	MinEdgeYOLOImgSize               = 128
	MaxEdgeYOLOImgSize               = 1920
	DefaultEdgeYOLOSocketName        = "vision-worker.sock"
	DefaultEdgeYOLOStartTimeout      = 30 * time.Second
	MinEdgeYOLOStartTimeout          = 1 * time.Second
	MaxEdgeYOLOStartTimeout          = 5 * time.Minute
	DefaultEdgeYOLOInferTimeout      = 5 * time.Second
	MinEdgeYOLOInferTimeout          = 100 * time.Millisecond
	MaxEdgeYOLOInferTimeout          = 60 * time.Second

	// Full Edge defaults and bounds (Milestone K5-K8).
	// DefaultEdgeMaxConcurrentInference matches processing.Router's real
	// invariant of exactly one worker goroutine per sink — see the struct
	// field doc comment above.
	DefaultEdgeMaxConcurrentInference = 1
	MinEdgeMaxConcurrentInference     = 1
	MaxEdgeMaxConcurrentInference     = 16
	DefaultEdgeInferenceQueueDepth    = 32
	MinEdgeInferenceQueueDepth        = 1
	MaxEdgeInferenceQueueDepth        = 256
	DefaultEdgeMinFreeDiskBytes       = 104857600 // 100MB
	DefaultEdgeMaxMemoryPercent       = 0.0       // disabled
)

var validLogLevels = []string{"debug", "info", "warn", "error"}

// Load reads configuration from the environment, applying safe defaults.
// An invalid value is a hard startup error.
func Load() (*Config, error) {
	cfg := &Config{
		EdgeID:              strings.TrimSpace(os.Getenv("GEOCAM_EDGE_ID")),
		ProcessingMode:      DefaultProcessingMode,
		LogLevel:            DefaultLogLevel,
		SaaSURL:             strings.TrimSpace(os.Getenv("GEOCAM_SAAS_URL")),
		HeartbeatInterval:   DefaultHeartbeatInterval,
		DataDir:             DefaultDataDir,
		HealthAddr:          DefaultHealthAddr,
		SaaSTimeout:         DefaultSaaSTimeout,
		DiscoveryEnabled:    DefaultDiscoveryEnabled,
		DiscoveryInterval:   DefaultDiscoveryInterval,
		DiscoveryTimeout:    DefaultDiscoveryTimeout,
		ConnectivityEnabled: DefaultConnectivityEnabled,
		StreamRole:          DefaultStreamRole,
		StreamTimeout:       DefaultStreamTimeout,

		VideoPipelineEnabled:        DefaultVideoPipelineEnabled,
		VideoTargetFPS:              DefaultVideoTargetFPS,
		VideoOutputWidth:            DefaultVideoOutputWidth,
		VideoOutputHeight:           DefaultVideoOutputHeight,
		VideoRingBufferSize:         DefaultVideoRingBufferSize,
		VideoQueueDepth:             DefaultVideoQueueDepth,
		VideoDecodeQueueDepth:       DefaultVideoDecodeQueueDepth,
		VideoMaxConcurrentPipelines: DefaultVideoMaxConcurrentPipelines,
		VideoFFmpegPath:             DefaultVideoFFmpegPath,
		VideoDecodeTimeout:          DefaultVideoDecodeTimeout,
		HybridMotionThreshold:       DefaultHybridMotionThreshold,
		HybridMinChangedArea:        DefaultHybridMinChangedArea,
		HybridBlockSize:             DefaultHybridBlockSize,
		HybridIdleFPS:               DefaultHybridIdleFPS,
		HybridIdleAfter:             DefaultHybridIdleAfter,
		CloudJPEGQuality:            DefaultCloudJPEGQuality,
		CloudMaxBytesPerSec:         DefaultCloudMaxBytesPerSec,
		CloudBurstBytes:             DefaultCloudBurstBytes,
		CloudMaxFPS:                 DefaultCloudMaxFPS,

		EdgeYOLOPersonModel:       DefaultEdgeYOLOPersonModel,
		EdgeYOLOVehicleModel:      DefaultEdgeYOLOVehicleModel,
		EdgeYOLOPersonConfidence:  DefaultEdgeYOLOPersonConfidence,
		EdgeYOLOVehicleConfidence: DefaultEdgeYOLOVehicleConfidence,
		EdgeYOLONMSIoU:            DefaultEdgeYOLONMSIoU,
		EdgeYOLODevice:            DefaultEdgeYOLODevice,
		EdgeYOLOImgSize:           DefaultEdgeYOLOImgSize,
		EdgeYOLOStartTimeout:      DefaultEdgeYOLOStartTimeout,
		EdgeYOLOInferTimeout:      DefaultEdgeYOLOInferTimeout,

		EdgeMaxConcurrentInference: DefaultEdgeMaxConcurrentInference,
		EdgeInferenceQueueDepth:    DefaultEdgeInferenceQueueDepth,
		EdgeMinFreeDiskBytes:       DefaultEdgeMinFreeDiskBytes,
		EdgeMaxMemoryPercent:       DefaultEdgeMaxMemoryPercent,

		LocalEventBacklogMaxOperations: DefaultLocalEventBacklogMaxOperations,
		LocalEventBacklogMaxBytes:      DefaultLocalEventBacklogMaxBytes,
		EdgeMaxClipSizeBytes:           DefaultEdgeMaxClipSizeBytes,
	}

	if raw := strings.TrimSpace(os.Getenv("GEOCAM_PROCESSING_MODE")); raw != "" {
		mode, err := ParseProcessingMode(strings.ToLower(raw))
		if err != nil {
			return nil, err
		}
		cfg.ProcessingMode = mode
	}

	if raw := strings.TrimSpace(os.Getenv("GEOCAM_LOG_LEVEL")); raw != "" {
		level := strings.ToLower(raw)
		if !slices.Contains(validLogLevels, level) {
			return nil, fmt.Errorf("invalid log level %q: must be one of %s",
				raw, strings.Join(validLogLevels, ", "))
		}
		cfg.LogLevel = level
	}

	if raw := strings.TrimSpace(os.Getenv("GEOCAM_HEARTBEAT_INTERVAL")); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid heartbeat interval %q: %w", raw, err)
		}
		if d < MinHeartbeatInterval || d > MaxHeartbeatInterval {
			return nil, fmt.Errorf("invalid heartbeat interval %q: must be between %s and %s",
				raw, MinHeartbeatInterval, MaxHeartbeatInterval)
		}
		cfg.HeartbeatInterval = d
	}

	if raw := strings.TrimSpace(os.Getenv("GEOCAM_HEARTBEAT_AUTH_FAILURE_INTERVAL")); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid heartbeat auth-failure interval %q: %w", raw, err)
		}
		if d < MinHeartbeatAuthFailureInterval || d > MaxHeartbeatAuthFailureInterval {
			return nil, fmt.Errorf("invalid heartbeat auth-failure interval %q: must be between %s and %s",
				raw, MinHeartbeatAuthFailureInterval, MaxHeartbeatAuthFailureInterval)
		}
		cfg.HeartbeatAuthFailureInterval = d
	}

	if raw := strings.TrimSpace(os.Getenv("GEOCAM_DATA_DIR")); raw != "" {
		cfg.DataDir = raw
	}

	if raw := strings.TrimSpace(os.Getenv("GEOCAM_HEALTH_ADDR")); raw != "" {
		if _, _, err := net.SplitHostPort(raw); err != nil {
			return nil, fmt.Errorf("invalid health addr %q: %w", raw, err)
		}
		cfg.HealthAddr = raw
	}

	if raw := strings.TrimSpace(os.Getenv("GEOCAM_ALLOW_INSECURE_HTTP")); raw == "true" {
		cfg.AllowInsecureHTTP = true
	}

	if raw := strings.TrimSpace(os.Getenv("GEOCAM_OTA_PUBLIC_KEY_FILE")); raw != "" {
		cfg.OTAPublicKeyFile = raw
	}

	if raw := strings.TrimSpace(os.Getenv("GEOCAM_SAAS_TIMEOUT")); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid SaaS timeout %q: %w", raw, err)
		}
		if d <= 0 {
			return nil, fmt.Errorf("invalid SaaS timeout %q: must be positive", raw)
		}
		cfg.SaaSTimeout = d
	}

	if raw := strings.TrimSpace(os.Getenv("GEOCAM_DISCOVERY_ENABLED")); raw != "" {
		cfg.DiscoveryEnabled = !(raw == "false" || raw == "0" || strings.ToLower(raw) == "f")
	}

	if raw := strings.TrimSpace(os.Getenv("GEOCAM_DISCOVERY_INTERVAL")); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid discovery interval %q: %w", raw, err)
		}
		if d < MinDiscoveryInterval || d > MaxDiscoveryInterval {
			return nil, fmt.Errorf("invalid discovery interval %q: must be between %s and %s",
				raw, MinDiscoveryInterval, MaxDiscoveryInterval)
		}
		cfg.DiscoveryInterval = d
	}

	if raw := strings.TrimSpace(os.Getenv("GEOCAM_DISCOVERY_TIMEOUT")); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid discovery timeout %q: %w", raw, err)
		}
		if d < MinDiscoveryTimeout || d > MaxDiscoveryTimeout {
			return nil, fmt.Errorf("invalid discovery timeout %q: must be between %s and %s",
				raw, MinDiscoveryTimeout, MaxDiscoveryTimeout)
		}
		cfg.DiscoveryTimeout = d
	}

	if raw := strings.TrimSpace(os.Getenv("GEOCAM_DISCOVERY_INTERFACES")); raw != "" {
		parts := strings.Split(raw, ",")
		var ifaces []string
		for _, p := range parts {
			if s := strings.TrimSpace(p); s != "" {
				ifaces = append(ifaces, s)
			}
		}
		cfg.DiscoveryInterfaces = ifaces
	}

	if raw := strings.TrimSpace(os.Getenv("GEOCAM_CONNECTIVITY_ENABLED")); raw != "" {
		cfg.ConnectivityEnabled = strings.ToLower(raw) == "true" || raw == "1"
	}

	if raw := strings.TrimSpace(os.Getenv("GEOCAM_STREAM_ROLE")); raw != "" {
		role := strings.ToLower(raw)
		if role != "sub" && role != "main" {
			return nil, fmt.Errorf("invalid stream role %q: must be 'sub' or 'main'", raw)
		}
		cfg.StreamRole = role
	}

	if raw := strings.TrimSpace(os.Getenv("GEOCAM_STREAM_TIMEOUT")); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid stream timeout %q: %w", raw, err)
		}
		if d < MinStreamTimeout || d > MaxStreamTimeout {
			return nil, fmt.Errorf("invalid stream timeout %q: must be between %s and %s",
				raw, MinStreamTimeout, MaxStreamTimeout)
		}
		cfg.StreamTimeout = d
	}

	if raw := strings.TrimSpace(os.Getenv("GEOCAM_VIDEO_PIPELINE_ENABLED")); raw != "" {
		cfg.VideoPipelineEnabled = strings.ToLower(raw) == "true" || raw == "1"
	}

	if raw := strings.TrimSpace(os.Getenv("GEOCAM_VIDEO_TARGET_FPS")); raw != "" {
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid video target FPS %q: %w", raw, err)
		}
		if v < MinVideoTargetFPS || v > MaxVideoTargetFPS {
			return nil, fmt.Errorf("invalid video target FPS %q: must be between %g and %g",
				raw, MinVideoTargetFPS, MaxVideoTargetFPS)
		}
		cfg.VideoTargetFPS = v
	}

	widthSet, heightSet := false, false
	if raw := strings.TrimSpace(os.Getenv("GEOCAM_VIDEO_OUTPUT_WIDTH")); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid video output width %q: %w", raw, err)
		}
		cfg.VideoOutputWidth = v
		widthSet = true
	}
	if raw := strings.TrimSpace(os.Getenv("GEOCAM_VIDEO_OUTPUT_HEIGHT")); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid video output height %q: %w", raw, err)
		}
		cfg.VideoOutputHeight = v
		heightSet = true
	}
	if widthSet || heightSet {
		if err := validateVideoOutputDimensions(cfg.VideoOutputWidth, cfg.VideoOutputHeight); err != nil {
			return nil, err
		}
	}

	if raw := strings.TrimSpace(os.Getenv("GEOCAM_VIDEO_RINGBUFFER_SIZE")); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid video ring buffer size %q: %w", raw, err)
		}
		if v < MinVideoRingBufferSize || v > MaxVideoRingBufferSize {
			return nil, fmt.Errorf("invalid video ring buffer size %q: must be between %d and %d",
				raw, MinVideoRingBufferSize, MaxVideoRingBufferSize)
		}
		cfg.VideoRingBufferSize = v
	}

	if raw := strings.TrimSpace(os.Getenv("GEOCAM_VIDEO_QUEUE_DEPTH")); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid video queue depth %q: %w", raw, err)
		}
		if v < MinVideoQueueDepth || v > MaxVideoQueueDepth {
			return nil, fmt.Errorf("invalid video queue depth %q: must be between %d and %d",
				raw, MinVideoQueueDepth, MaxVideoQueueDepth)
		}
		cfg.VideoQueueDepth = v
	}

	if raw := strings.TrimSpace(os.Getenv("GEOCAM_VIDEO_DECODE_QUEUE_DEPTH")); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid video decode queue depth %q: %w", raw, err)
		}
		if v < MinVideoDecodeQueueDepth || v > MaxVideoDecodeQueueDepth {
			return nil, fmt.Errorf("invalid video decode queue depth %q: must be between %d and %d",
				raw, MinVideoDecodeQueueDepth, MaxVideoDecodeQueueDepth)
		}
		cfg.VideoDecodeQueueDepth = v
	}

	if raw := strings.TrimSpace(os.Getenv("GEOCAM_VIDEO_MAX_CONCURRENT_PIPELINES")); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid video max concurrent pipelines %q: %w", raw, err)
		}
		if v < MinVideoMaxConcurrentPipelines || v > MaxVideoMaxConcurrentPipelines {
			return nil, fmt.Errorf("invalid video max concurrent pipelines %q: must be between %d and %d",
				raw, MinVideoMaxConcurrentPipelines, MaxVideoMaxConcurrentPipelines)
		}
		cfg.VideoMaxConcurrentPipelines = v
	}

	if raw := strings.TrimSpace(os.Getenv("GEOCAM_VIDEO_FFMPEG_PATH")); raw != "" {
		cfg.VideoFFmpegPath = raw
	}

	if raw := strings.TrimSpace(os.Getenv("GEOCAM_VIDEO_DECODE_TIMEOUT")); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid video decode timeout %q: %w", raw, err)
		}
		if d < MinVideoDecodeTimeout || d > MaxVideoDecodeTimeout {
			return nil, fmt.Errorf("invalid video decode timeout %q: must be between %s and %s",
				raw, MinVideoDecodeTimeout, MaxVideoDecodeTimeout)
		}
		cfg.VideoDecodeTimeout = d
	}

	if raw := strings.TrimSpace(os.Getenv("GEOCAM_VIDEO_HYBRID_MOTION_THRESHOLD")); raw != "" {
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid hybrid motion threshold %q: %w", raw, err)
		}
		if v < MinHybridMotionThreshold || v > MaxHybridMotionThreshold {
			return nil, fmt.Errorf("invalid hybrid motion threshold %q: must be between %g and %g",
				raw, MinHybridMotionThreshold, MaxHybridMotionThreshold)
		}
		cfg.HybridMotionThreshold = v
	}

	if raw := strings.TrimSpace(os.Getenv("GEOCAM_VIDEO_HYBRID_MIN_CHANGED_AREA")); raw != "" {
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid hybrid min changed area %q: %w", raw, err)
		}
		if v < MinHybridMinChangedArea || v > MaxHybridMinChangedArea {
			return nil, fmt.Errorf("invalid hybrid min changed area %q: must be between %g and %g",
				raw, MinHybridMinChangedArea, MaxHybridMinChangedArea)
		}
		cfg.HybridMinChangedArea = v
	}

	if raw := strings.TrimSpace(os.Getenv("GEOCAM_VIDEO_HYBRID_BLOCK_SIZE")); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid hybrid block size %q: %w", raw, err)
		}
		if v < MinHybridBlockSize || v > MaxHybridBlockSize {
			return nil, fmt.Errorf("invalid hybrid block size %q: must be between %d and %d",
				raw, MinHybridBlockSize, MaxHybridBlockSize)
		}
		cfg.HybridBlockSize = v
	}

	if raw := strings.TrimSpace(os.Getenv("GEOCAM_VIDEO_HYBRID_IDLE_FPS")); raw != "" {
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid hybrid idle FPS %q: %w", raw, err)
		}
		if v < MinHybridIdleFPS || v > MaxVideoTargetFPS {
			return nil, fmt.Errorf("invalid hybrid idle FPS %q: must be between %g and %g",
				raw, MinHybridIdleFPS, MaxVideoTargetFPS)
		}
		cfg.HybridIdleFPS = v
	}

	if raw := strings.TrimSpace(os.Getenv("GEOCAM_VIDEO_HYBRID_IDLE_AFTER")); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid hybrid idle after %q: %w", raw, err)
		}
		if d < 0 {
			return nil, fmt.Errorf("invalid hybrid idle after %q: must not be negative", raw)
		}
		cfg.HybridIdleAfter = d
	}

	if raw := strings.TrimSpace(os.Getenv("GEOCAM_VIDEO_HYBRID_ROI")); raw != "" {
		rois, err := parseHybridROIs(raw)
		if err != nil {
			return nil, err
		}
		cfg.HybridROIs = rois
	}

	if raw := strings.TrimSpace(os.Getenv("GEOCAM_CLOUD_BUFFER_MAX_BYTES")); raw != "" {
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid cloud buffer max bytes %q: %w", raw, err)
		}
		if v <= 0 {
			return nil, fmt.Errorf("invalid cloud buffer max bytes %q: must be positive", raw)
		}
		cfg.CloudBufferMaxBytes = v
	}

	if raw := strings.TrimSpace(os.Getenv("GEOCAM_CLOUD_BUFFER_MAX_FRAMES")); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid cloud buffer max frames %q: %w", raw, err)
		}
		if v <= 0 {
			return nil, fmt.Errorf("invalid cloud buffer max frames %q: must be positive", raw)
		}
		cfg.CloudBufferMaxFrames = v
	}

	if raw := strings.TrimSpace(os.Getenv("GEOCAM_CLOUD_BUFFER_MAX_AGE")); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid cloud buffer max age %q: %w", raw, err)
		}
		if d < 0 {
			return nil, fmt.Errorf("invalid cloud buffer max age %q: must not be negative", raw)
		}
		cfg.CloudBufferMaxAge = d
	}

	if raw := strings.TrimSpace(os.Getenv("GEOCAM_CLOUD_JPEG_QUALITY")); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid cloud JPEG quality %q: %w", raw, err)
		}
		if v < MinCloudJPEGQuality || v > MaxCloudJPEGQuality {
			return nil, fmt.Errorf("invalid cloud JPEG quality %q: must be between %d and %d",
				raw, MinCloudJPEGQuality, MaxCloudJPEGQuality)
		}
		cfg.CloudJPEGQuality = v
	}

	if raw := strings.TrimSpace(os.Getenv("GEOCAM_CLOUD_MAX_BYTES_PER_SEC")); raw != "" {
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid cloud max bytes per second %q: %w", raw, err)
		}
		if v < 0 {
			return nil, fmt.Errorf("invalid cloud max bytes per second %q: must be non-negative", raw)
		}
		cfg.CloudMaxBytesPerSec = v
	}

	if raw := strings.TrimSpace(os.Getenv("GEOCAM_CLOUD_BURST_BYTES")); raw != "" {
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid cloud burst bytes %q: %w", raw, err)
		}
		if v < 0 {
			return nil, fmt.Errorf("invalid cloud burst bytes %q: must be non-negative", raw)
		}
		cfg.CloudBurstBytes = v
	}

	if raw := strings.TrimSpace(os.Getenv("GEOCAM_CLOUD_MAX_FPS")); raw != "" {
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid cloud max FPS %q: %w", raw, err)
		}
		if v < 0 {
			return nil, fmt.Errorf("invalid cloud max FPS %q: must be non-negative", raw)
		}
		cfg.CloudMaxFPS = v
	}
	if raw := strings.TrimSpace(os.Getenv("GEOCAM_LOCAL_EVENT_BACKLOG_MAX_OPERATIONS")); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v <= 0 {
			return nil, fmt.Errorf("invalid local event backlog max operations %q: must be positive", raw)
		}
		cfg.LocalEventBacklogMaxOperations = v
	}
	if raw := strings.TrimSpace(os.Getenv("GEOCAM_LOCAL_EVENT_BACKLOG_MAX_BYTES")); raw != "" {
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || v <= 0 {
			return nil, fmt.Errorf("invalid local event backlog max bytes %q: must be positive", raw)
		}
		cfg.LocalEventBacklogMaxBytes = v
	}
	if raw := strings.TrimSpace(os.Getenv("GEOCAM_EDGE_MAX_CLIP_SIZE_BYTES")); raw != "" {
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || v <= 0 {
			return nil, fmt.Errorf("invalid edge max clip size %q: must be positive", raw)
		}
		cfg.EdgeMaxClipSizeBytes = v
	}

	// Local YOLO vision-worker settings (Milestone K). ModelsDir/SocketPath
	// defaults are derived from the final cfg.DataDir (parsed above), not a
	// fixed path, so GEOCAM_DATA_DIR alone still relocates them.
	cfg.EdgeYOLOModelsDir = filepath.Join(cfg.DataDir, DefaultEdgeYOLOModelsDirName)
	cfg.EdgeYOLOSocketPath = filepath.Join(cfg.DataDir, "run", DefaultEdgeYOLOSocketName)

	if raw := strings.TrimSpace(os.Getenv("GEOCAM_EDGE_YOLO_WORKER_CMD")); raw != "" {
		cfg.EdgeYOLOWorkerCmd = raw
	}
	if raw := strings.TrimSpace(os.Getenv("GEOCAM_EDGE_YOLO_WORKER_ARGS")); raw != "" {
		cfg.EdgeYOLOWorkerArgs = strings.Fields(raw)
	}
	if raw := strings.TrimSpace(os.Getenv("GEOCAM_EDGE_YOLO_MODELS_DIR")); raw != "" {
		cfg.EdgeYOLOModelsDir = raw
	}
	if raw := strings.TrimSpace(os.Getenv("GEOCAM_EDGE_YOLO_SOCKET_PATH")); raw != "" {
		cfg.EdgeYOLOSocketPath = raw
	}
	if raw := strings.TrimSpace(os.Getenv("GEOCAM_EDGE_YOLO_DEVICE")); raw != "" {
		cfg.EdgeYOLODevice = raw
	}
	if raw := strings.TrimSpace(os.Getenv("GEOCAM_EDGE_YOLO_IMGSZ")); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid edge YOLO image size %q: %w", raw, err)
		}
		if v < MinEdgeYOLOImgSize || v > MaxEdgeYOLOImgSize {
			return nil, fmt.Errorf("invalid edge YOLO image size %q: must be between %d and %d",
				raw, MinEdgeYOLOImgSize, MaxEdgeYOLOImgSize)
		}
		cfg.EdgeYOLOImgSize = v
	}
	if raw := strings.TrimSpace(os.Getenv("GEOCAM_EDGE_YOLO_PERSON_CONFIDENCE")); raw != "" {
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid edge YOLO person confidence %q: %w", raw, err)
		}
		if v < MinEdgeYOLOConfidence || v > MaxEdgeYOLOConfidence {
			return nil, fmt.Errorf("invalid edge YOLO person confidence %q: must be between %g and %g",
				raw, MinEdgeYOLOConfidence, MaxEdgeYOLOConfidence)
		}
		cfg.EdgeYOLOPersonConfidence = v
	}
	if raw := strings.TrimSpace(os.Getenv("GEOCAM_EDGE_YOLO_VEHICLE_CONFIDENCE")); raw != "" {
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid edge YOLO vehicle confidence %q: %w", raw, err)
		}
		if v < MinEdgeYOLOConfidence || v > MaxEdgeYOLOConfidence {
			return nil, fmt.Errorf("invalid edge YOLO vehicle confidence %q: must be between %g and %g",
				raw, MinEdgeYOLOConfidence, MaxEdgeYOLOConfidence)
		}
		cfg.EdgeYOLOVehicleConfidence = v
	}
	if raw := strings.TrimSpace(os.Getenv("GEOCAM_EDGE_YOLO_NMS_IOU")); raw != "" {
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid edge YOLO NMS IoU %q: %w", raw, err)
		}
		if v < MinEdgeYOLONMSIoU || v > MaxEdgeYOLONMSIoU {
			return nil, fmt.Errorf("invalid edge YOLO NMS IoU %q: must be between %g and %g",
				raw, MinEdgeYOLONMSIoU, MaxEdgeYOLONMSIoU)
		}
		cfg.EdgeYOLONMSIoU = v
	}
	if raw := strings.TrimSpace(os.Getenv("GEOCAM_EDGE_YOLO_START_TIMEOUT")); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid edge YOLO start timeout %q: %w", raw, err)
		}
		if d < MinEdgeYOLOStartTimeout || d > MaxEdgeYOLOStartTimeout {
			return nil, fmt.Errorf("invalid edge YOLO start timeout %q: must be between %s and %s",
				raw, MinEdgeYOLOStartTimeout, MaxEdgeYOLOStartTimeout)
		}
		cfg.EdgeYOLOStartTimeout = d
	}
	if raw := strings.TrimSpace(os.Getenv("GEOCAM_EDGE_YOLO_INFER_TIMEOUT")); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid edge YOLO infer timeout %q: %w", raw, err)
		}
		if d < MinEdgeYOLOInferTimeout || d > MaxEdgeYOLOInferTimeout {
			return nil, fmt.Errorf("invalid edge YOLO infer timeout %q: must be between %s and %s",
				raw, MinEdgeYOLOInferTimeout, MaxEdgeYOLOInferTimeout)
		}
		cfg.EdgeYOLOInferTimeout = d
	}

	if raw := strings.TrimSpace(os.Getenv("GEOCAM_EDGE_MAX_CONCURRENT_INFERENCE")); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid edge max concurrent inference %q: %w", raw, err)
		}
		if v < MinEdgeMaxConcurrentInference || v > MaxEdgeMaxConcurrentInference {
			return nil, fmt.Errorf("edge max concurrent inference %d out of range [%d, %d]",
				v, MinEdgeMaxConcurrentInference, MaxEdgeMaxConcurrentInference)
		}
		cfg.EdgeMaxConcurrentInference = v
	}

	if raw := strings.TrimSpace(os.Getenv("GEOCAM_EDGE_INFERENCE_QUEUE_DEPTH")); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid edge inference queue depth %q: %w", raw, err)
		}
		if v < MinEdgeInferenceQueueDepth || v > MaxEdgeInferenceQueueDepth {
			return nil, fmt.Errorf("edge inference queue depth %d out of range [%d, %d]",
				v, MinEdgeInferenceQueueDepth, MaxEdgeInferenceQueueDepth)
		}
		cfg.EdgeInferenceQueueDepth = v
	}

	if raw := strings.TrimSpace(os.Getenv("GEOCAM_EDGE_MIN_FREE_DISK_BYTES")); raw != "" {
		v, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid edge min free disk bytes %q: %w", raw, err)
		}
		cfg.EdgeMinFreeDiskBytes = v
	}

	if raw := strings.TrimSpace(os.Getenv("GEOCAM_EDGE_MAX_MEMORY_PERCENT")); raw != "" {
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid edge max memory percent %q: %w", raw, err)
		}
		if v < 0 || v > 100 {
			return nil, fmt.Errorf("edge max memory percent %.1f out of range [0, 100]", v)
		}
		cfg.EdgeMaxMemoryPercent = v
	}

	// Fail-fast: reject an insecure http:// SaaS URL here, before any
	// request is ever attempted, unless explicitly allowed for development.
	if cfg.SaaSURL != "" && strings.HasPrefix(strings.ToLower(cfg.SaaSURL), "http://") && !cfg.AllowInsecureHTTP {
		return nil, fmt.Errorf("insecure GEOCAM_SAAS_URL %q: http:// is disabled by default; "+
			"set GEOCAM_ALLOW_INSECURE_HTTP=true to allow it in development", cfg.SaaSURL)
	}

	return cfg, nil
}

// parseHybridROIs parses GEOCAM_VIDEO_HYBRID_ROI: semicolon-separated
// rectangles, each "x_min,y_min,x_max,y_max" in normalized 0..1
// coordinates (independent of GEOCAM_VIDEO_OUTPUT_WIDTH/HEIGHT). A
// malformed or out-of-range rectangle is a hard startup error -- consistent
// with every other GEOCAM_VIDEO_* value in this file -- rather than
// silently ignoring a misconfigured ROI at runtime.
func parseHybridROIs(raw string) ([]HybridROI, error) {
	parts := strings.Split(raw, ";")
	rois := make([]HybridROI, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		coords := strings.Split(p, ",")
		if len(coords) != 4 {
			return nil, fmt.Errorf("invalid hybrid ROI %q: expected x_min,y_min,x_max,y_max", p)
		}
		var v [4]float64
		for i, c := range coords {
			f, err := strconv.ParseFloat(strings.TrimSpace(c), 64)
			if err != nil {
				return nil, fmt.Errorf("invalid hybrid ROI %q: %w", p, err)
			}
			v[i] = f
		}
		roi := HybridROI{XMin: v[0], YMin: v[1], XMax: v[2], YMax: v[3]}
		if roi.XMin < 0 || roi.YMin < 0 || roi.XMax > 1 || roi.YMax > 1 || roi.XMin >= roi.XMax || roi.YMin >= roi.YMax {
			return nil, fmt.Errorf("invalid hybrid ROI %q: coordinates must be within 0..1 with min < max", p)
		}
		rois = append(rois, roi)
	}
	return rois, nil
}

// validateVideoOutputDimensions enforces the only two valid shapes for
// GEOCAM_VIDEO_OUTPUT_WIDTH/HEIGHT: both zero (resize disabled) or both
// positive and even (required for yuv420p's half-resolution chroma
// planes). This duplicates internal/processing.ValidateOutputDimensions'
// rule rather than importing that package from here, keeping config the
// lowest-level package with no dependency on the feature packages it
// configures.
func validateVideoOutputDimensions(width, height int) error {
	if width == 0 && height == 0 {
		return nil
	}
	if width <= 0 || height <= 0 {
		return fmt.Errorf("invalid video output dimensions %dx%d: width and height must both be zero or both be positive", width, height)
	}
	if width > MaxVideoOutputWidth || height > MaxVideoOutputHeight {
		return fmt.Errorf("invalid video output dimensions %dx%d: must not exceed %dx%d",
			width, height, MaxVideoOutputWidth, MaxVideoOutputHeight)
	}
	if width%2 != 0 || height%2 != 0 {
		return fmt.Errorf("invalid video output dimensions %dx%d: must be even (yuv420p)", width, height)
	}
	return nil
}
