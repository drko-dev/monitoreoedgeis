// Package config loads GEO CAM Edge configuration from environment variables.
package config

import (
	"fmt"
	"net"
	"os"
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
	DataDir           string
	HealthAddr        string
	// AllowInsecureHTTP permits SaaSURL to use http:// instead of https://.
	// It never weakens TLS verification for an https:// URL — see
	// internal/transport. Development only; defaults to false.
	AllowInsecureHTTP bool
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
	DefaultDataDir       = "/var/lib/geocam-edge"
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
	// Cloud bandwidth control defaults and bounds (Milestone I7).
	DefaultCloudJPEGQuality    = 85
	MinCloudJPEGQuality        = 1
	MaxCloudJPEGQuality        = 100
	DefaultCloudMaxBytesPerSec = 0
	DefaultCloudBurstBytes     = 0
	DefaultCloudMaxFPS         = 0.0
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
		CloudJPEGQuality:            DefaultCloudJPEGQuality,
		CloudMaxBytesPerSec:         DefaultCloudMaxBytesPerSec,
		CloudBurstBytes:             DefaultCloudBurstBytes,
		CloudMaxFPS:                 DefaultCloudMaxFPS,
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

	// Fail-fast: reject an insecure http:// SaaS URL here, before any
	// request is ever attempted, unless explicitly allowed for development.
	if cfg.SaaSURL != "" && strings.HasPrefix(strings.ToLower(cfg.SaaSURL), "http://") && !cfg.AllowInsecureHTTP {
		return nil, fmt.Errorf("insecure GEOCAM_SAAS_URL %q: http:// is disabled by default; "+
			"set GEOCAM_ALLOW_INSECURE_HTTP=true to allow it in development", cfg.SaaSURL)
	}

	return cfg, nil
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
