package config

import (
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	// t.Setenv with empty values isolates this test from the ambient env.
	for _, k := range []string{
		"GEOCAM_EDGE_ID", "GEOCAM_PROCESSING_MODE", "GEOCAM_LOG_LEVEL",
		"GEOCAM_SAAS_URL", "GEOCAM_HEARTBEAT_INTERVAL", "GEOCAM_DATA_DIR",
		"GEOCAM_HEALTH_ADDR", "GEOCAM_ALLOW_INSECURE_HTTP", "GEOCAM_SAAS_TIMEOUT",
		"GEOCAM_DISCOVERY_ENABLED", "GEOCAM_DISCOVERY_INTERVAL", "GEOCAM_DISCOVERY_TIMEOUT",
		"GEOCAM_DISCOVERY_INTERFACES",
	} {
		t.Setenv(k, "")
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.ProcessingMode != ModeCloud {
		t.Errorf("ProcessingMode = %q, want %q", cfg.ProcessingMode, ModeCloud)
	}
	if cfg.LogLevel != DefaultLogLevel {
		t.Errorf("LogLevel = %q, want %q", cfg.LogLevel, DefaultLogLevel)
	}
	if cfg.HeartbeatInterval != DefaultHeartbeatInterval {
		t.Errorf("HeartbeatInterval = %v, want %v", cfg.HeartbeatInterval, DefaultHeartbeatInterval)
	}
	if cfg.DataDir != DefaultDataDir {
		t.Errorf("DataDir = %q, want %q", cfg.DataDir, DefaultDataDir)
	}
	if cfg.EdgeID != "" {
		t.Errorf("EdgeID = %q, want empty", cfg.EdgeID)
	}
	if cfg.HealthAddr != DefaultHealthAddr {
		t.Errorf("HealthAddr = %q, want %q", cfg.HealthAddr, DefaultHealthAddr)
	}
	if cfg.AllowInsecureHTTP {
		t.Error("AllowInsecureHTTP = true, want false by default")
	}
	if cfg.SaaSTimeout != DefaultSaaSTimeout {
		t.Errorf("SaaSTimeout = %v, want %v", cfg.SaaSTimeout, DefaultSaaSTimeout)
	}
	if !cfg.DiscoveryEnabled {
		t.Errorf("DiscoveryEnabled = false, want true by default")
	}
	if cfg.DiscoveryInterval != DefaultDiscoveryInterval {
		t.Errorf("DiscoveryInterval = %v, want %v", cfg.DiscoveryInterval, DefaultDiscoveryInterval)
	}
	if cfg.DiscoveryTimeout != DefaultDiscoveryTimeout {
		t.Errorf("DiscoveryTimeout = %v, want %v", cfg.DiscoveryTimeout, DefaultDiscoveryTimeout)
	}
	if len(cfg.DiscoveryInterfaces) != 0 {
		t.Errorf("DiscoveryInterfaces = %v, want empty", cfg.DiscoveryInterfaces)
	}
}

func TestLoadOverrides(t *testing.T) {
	t.Setenv("GEOCAM_EDGE_ID", "edge-001")
	t.Setenv("GEOCAM_PROCESSING_MODE", "EDGE") // case-insensitive
	t.Setenv("GEOCAM_LOG_LEVEL", "debug")
	t.Setenv("GEOCAM_SAAS_URL", "https://saas.example.test")
	t.Setenv("GEOCAM_HEARTBEAT_INTERVAL", "90s")
	t.Setenv("GEOCAM_DATA_DIR", "/tmp/geocam")
	t.Setenv("GEOCAM_HEALTH_ADDR", "127.0.0.1:9091")
	t.Setenv("GEOCAM_DISCOVERY_ENABLED", "false")
	t.Setenv("GEOCAM_DISCOVERY_INTERVAL", "10m")
	t.Setenv("GEOCAM_DISCOVERY_TIMEOUT", "6s")
	t.Setenv("GEOCAM_DISCOVERY_INTERFACES", "eth0, eth1")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.EdgeID != "edge-001" {
		t.Errorf("EdgeID = %q", cfg.EdgeID)
	}
	if cfg.ProcessingMode != ModeEdge {
		t.Errorf("ProcessingMode = %q, want %q", cfg.ProcessingMode, ModeEdge)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("LogLevel = %q", cfg.LogLevel)
	}
	if cfg.HeartbeatInterval != 90*time.Second {
		t.Errorf("HeartbeatInterval = %v", cfg.HeartbeatInterval)
	}
	if cfg.DataDir != "/tmp/geocam" {
		t.Errorf("DataDir = %q", cfg.DataDir)
	}
	if cfg.HealthAddr != "127.0.0.1:9091" {
		t.Errorf("HealthAddr = %q", cfg.HealthAddr)
	}
	if cfg.DiscoveryEnabled {
		t.Errorf("DiscoveryEnabled = true, want false")
	}
	if cfg.DiscoveryInterval != 10*time.Minute {
		t.Errorf("DiscoveryInterval = %v, want 10m", cfg.DiscoveryInterval)
	}
	if cfg.DiscoveryTimeout != 6*time.Second {
		t.Errorf("DiscoveryTimeout = %v, want 6s", cfg.DiscoveryTimeout)
	}
	if len(cfg.DiscoveryInterfaces) != 2 || cfg.DiscoveryInterfaces[0] != "eth0" || cfg.DiscoveryInterfaces[1] != "eth1" {
		t.Errorf("DiscoveryInterfaces = %v, want [eth0, eth1]", cfg.DiscoveryInterfaces)
	}
}

func TestLoadRejectsInvalidValues(t *testing.T) {
	tests := []struct{ key, value string }{
		{"GEOCAM_PROCESSING_MODE", "gpu"},
		{"GEOCAM_LOG_LEVEL", "verbose"},
		{"GEOCAM_HEARTBEAT_INTERVAL", "soon"},
		{"GEOCAM_HEARTBEAT_INTERVAL", "-5s"},
		{"GEOCAM_HEARTBEAT_INTERVAL", "0s"},
		// Out of the documented 5s..5m band: too chatty for the SaaS below,
		// too slow to distinguish from an outage above.
		{"GEOCAM_HEARTBEAT_INTERVAL", "4s"},
		{"GEOCAM_HEARTBEAT_INTERVAL", "5m1s"},
		{"GEOCAM_HEALTH_ADDR", "not-a-valid-addr"},
		{"GEOCAM_SAAS_TIMEOUT", "soon"},
		{"GEOCAM_SAAS_TIMEOUT", "-5s"},
		{"GEOCAM_DISCOVERY_INTERVAL", "invalid"},
		{"GEOCAM_DISCOVERY_INTERVAL", "30s"}, // Below MinDiscoveryInterval 1m
		{"GEOCAM_DISCOVERY_INTERVAL", "25h"}, // Above MaxDiscoveryInterval 24h
		{"GEOCAM_DISCOVERY_TIMEOUT", "invalid"},
		{"GEOCAM_DISCOVERY_TIMEOUT", "500ms"}, // Below MinDiscoveryTimeout 1s
		{"GEOCAM_DISCOVERY_TIMEOUT", "31s"},   // Above MaxDiscoveryTimeout 30s
	}
	for _, tt := range tests {
		t.Run(tt.key+"="+tt.value, func(t *testing.T) {
			t.Setenv(tt.key, tt.value)
			if _, err := Load(); err == nil {
				t.Fatalf("Load() succeeded with %s=%q, want error", tt.key, tt.value)
			}
		})
	}
}

// The bounds are inclusive: an operator who sets exactly the documented
// minimum or maximum must not be rejected by an off-by-one comparison.
func TestLoadAcceptsTheHeartbeatIntervalBounds(t *testing.T) {
	for _, want := range []time.Duration{MinHeartbeatInterval, MaxHeartbeatInterval} {
		t.Run(want.String(), func(t *testing.T) {
			t.Setenv("GEOCAM_HEARTBEAT_INTERVAL", want.String())
			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load() error = %v, want the boundary value accepted", err)
			}
			if cfg.HeartbeatInterval != want {
				t.Errorf("HeartbeatInterval = %v, want %v", cfg.HeartbeatInterval, want)
			}
		})
	}
}

func TestLoadRejectsInsecureHTTPByDefault(t *testing.T) {
	t.Setenv("GEOCAM_SAAS_URL", "http://saas.example.test")
	t.Setenv("GEOCAM_ALLOW_INSECURE_HTTP", "")
	if _, err := Load(); err == nil {
		t.Fatal("Load() succeeded with http:// GEOCAM_SAAS_URL and no override, want error")
	}
}

func TestLoadAllowsInsecureHTTPWhenExplicit(t *testing.T) {
	t.Setenv("GEOCAM_SAAS_URL", "http://saas.example.test")
	t.Setenv("GEOCAM_ALLOW_INSECURE_HTTP", "true")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !cfg.AllowInsecureHTTP {
		t.Error("AllowInsecureHTTP = false, want true")
	}
	if cfg.SaaSURL != "http://saas.example.test" {
		t.Errorf("SaaSURL = %q", cfg.SaaSURL)
	}
}

func TestLoadAllowsHTTPSWithoutOverride(t *testing.T) {
	t.Setenv("GEOCAM_SAAS_URL", "https://saas.example.test")
	t.Setenv("GEOCAM_ALLOW_INSECURE_HTTP", "")
	if _, err := Load(); err != nil {
		t.Fatalf("Load() error = %v, want nil for https:// URL", err)
	}
}

func TestParseProcessingMode(t *testing.T) {
	tests := []struct {
		in      string
		want    ProcessingMode
		wantErr bool
	}{
		{in: "cloud", want: ModeCloud},
		{in: "hybrid", want: ModeHybrid},
		{in: "edge", want: ModeEdge},
		{in: "invalid", wantErr: true},
		{in: "", wantErr: true},
		{in: "Cloud", wantErr: true}, // ParseProcessingMode itself is exact
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := ParseProcessingMode(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseProcessingMode(%q) = %q, want error", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseProcessingMode(%q) error = %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("ParseProcessingMode(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestLoadVideoPipelineDefaults(t *testing.T) {
	for _, k := range []string{
		"GEOCAM_VIDEO_PIPELINE_ENABLED", "GEOCAM_VIDEO_TARGET_FPS",
		"GEOCAM_VIDEO_OUTPUT_WIDTH", "GEOCAM_VIDEO_OUTPUT_HEIGHT",
		"GEOCAM_VIDEO_RINGBUFFER_SIZE", "GEOCAM_VIDEO_QUEUE_DEPTH",
		"GEOCAM_VIDEO_DECODE_QUEUE_DEPTH", "GEOCAM_VIDEO_MAX_CONCURRENT_PIPELINES",
		"GEOCAM_VIDEO_FFMPEG_PATH", "GEOCAM_VIDEO_DECODE_TIMEOUT",
	} {
		t.Setenv(k, "")
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.VideoPipelineEnabled {
		t.Error("VideoPipelineEnabled = true, want false by default (opt-in)")
	}
	if cfg.VideoTargetFPS != DefaultVideoTargetFPS {
		t.Errorf("VideoTargetFPS = %v, want %v", cfg.VideoTargetFPS, DefaultVideoTargetFPS)
	}
	if cfg.VideoOutputWidth != DefaultVideoOutputWidth || cfg.VideoOutputHeight != DefaultVideoOutputHeight {
		t.Errorf("output dims = %dx%d, want %dx%d", cfg.VideoOutputWidth, cfg.VideoOutputHeight,
			DefaultVideoOutputWidth, DefaultVideoOutputHeight)
	}
	if cfg.VideoRingBufferSize != DefaultVideoRingBufferSize {
		t.Errorf("VideoRingBufferSize = %d, want %d", cfg.VideoRingBufferSize, DefaultVideoRingBufferSize)
	}
	if cfg.VideoQueueDepth != DefaultVideoQueueDepth {
		t.Errorf("VideoQueueDepth = %d, want %d", cfg.VideoQueueDepth, DefaultVideoQueueDepth)
	}
	if cfg.VideoDecodeQueueDepth != DefaultVideoDecodeQueueDepth {
		t.Errorf("VideoDecodeQueueDepth = %d, want %d", cfg.VideoDecodeQueueDepth, DefaultVideoDecodeQueueDepth)
	}
	if cfg.VideoMaxConcurrentPipelines != DefaultVideoMaxConcurrentPipelines {
		t.Errorf("VideoMaxConcurrentPipelines = %d, want %d", cfg.VideoMaxConcurrentPipelines, DefaultVideoMaxConcurrentPipelines)
	}
	if cfg.VideoFFmpegPath != DefaultVideoFFmpegPath {
		t.Errorf("VideoFFmpegPath = %q, want %q", cfg.VideoFFmpegPath, DefaultVideoFFmpegPath)
	}
	if cfg.VideoDecodeTimeout != DefaultVideoDecodeTimeout {
		t.Errorf("VideoDecodeTimeout = %v, want %v", cfg.VideoDecodeTimeout, DefaultVideoDecodeTimeout)
	}
}

func TestLoadVideoPipelineOverrides(t *testing.T) {
	t.Setenv("GEOCAM_VIDEO_PIPELINE_ENABLED", "true")
	t.Setenv("GEOCAM_VIDEO_TARGET_FPS", "2")
	t.Setenv("GEOCAM_VIDEO_OUTPUT_WIDTH", "320")
	t.Setenv("GEOCAM_VIDEO_OUTPUT_HEIGHT", "180")
	t.Setenv("GEOCAM_VIDEO_RINGBUFFER_SIZE", "10")
	t.Setenv("GEOCAM_VIDEO_QUEUE_DEPTH", "128")
	t.Setenv("GEOCAM_VIDEO_DECODE_QUEUE_DEPTH", "2")
	t.Setenv("GEOCAM_VIDEO_MAX_CONCURRENT_PIPELINES", "2")
	t.Setenv("GEOCAM_VIDEO_FFMPEG_PATH", "/usr/local/bin/ffmpeg")
	t.Setenv("GEOCAM_VIDEO_DECODE_TIMEOUT", "5s")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !cfg.VideoPipelineEnabled {
		t.Error("VideoPipelineEnabled = false, want true")
	}
	if cfg.VideoTargetFPS != 2 {
		t.Errorf("VideoTargetFPS = %v, want 2", cfg.VideoTargetFPS)
	}
	if cfg.VideoOutputWidth != 320 || cfg.VideoOutputHeight != 180 {
		t.Errorf("output dims = %dx%d, want 320x180", cfg.VideoOutputWidth, cfg.VideoOutputHeight)
	}
	if cfg.VideoRingBufferSize != 10 {
		t.Errorf("VideoRingBufferSize = %d, want 10", cfg.VideoRingBufferSize)
	}
	if cfg.VideoQueueDepth != 128 {
		t.Errorf("VideoQueueDepth = %d, want 128", cfg.VideoQueueDepth)
	}
	if cfg.VideoDecodeQueueDepth != 2 {
		t.Errorf("VideoDecodeQueueDepth = %d, want 2", cfg.VideoDecodeQueueDepth)
	}
	if cfg.VideoMaxConcurrentPipelines != 2 {
		t.Errorf("VideoMaxConcurrentPipelines = %d, want 2", cfg.VideoMaxConcurrentPipelines)
	}
	if cfg.VideoFFmpegPath != "/usr/local/bin/ffmpeg" {
		t.Errorf("VideoFFmpegPath = %q, want /usr/local/bin/ffmpeg", cfg.VideoFFmpegPath)
	}
	if cfg.VideoDecodeTimeout != 5*time.Second {
		t.Errorf("VideoDecodeTimeout = %v, want 5s", cfg.VideoDecodeTimeout)
	}
}

func TestLoadVideoOutputDimensionsValidation(t *testing.T) {
	cases := []struct {
		name    string
		width   string
		height  string
		wantErr bool
	}{
		{"both unset (defaults, valid)", "", "", false},
		{"both zero disables resize", "0", "0", false},
		{"both even positive", "640", "360", false},
		{"width set, height unset matches valid default", "640", "", false},
		{"width positive, height explicitly zero", "640", "0", true},
		{"height positive, width explicitly zero", "0", "360", true},
		{"odd width", "641", "360", true},
		{"odd height", "640", "361", true},
		{"exceeds max width", "1921", "1080", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("GEOCAM_VIDEO_OUTPUT_WIDTH", tc.width)
			t.Setenv("GEOCAM_VIDEO_OUTPUT_HEIGHT", tc.height)
			_, err := Load()
			if (err != nil) != tc.wantErr {
				t.Fatalf("Load() error = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}

func TestLoadVideoTargetFPSOutOfRange(t *testing.T) {
	t.Setenv("GEOCAM_VIDEO_TARGET_FPS", "0")
	if _, err := Load(); err == nil {
		t.Fatal("expected error for GEOCAM_VIDEO_TARGET_FPS=0 (below MinVideoTargetFPS)")
	}
	t.Setenv("GEOCAM_VIDEO_TARGET_FPS", "31")
	if _, err := Load(); err == nil {
		t.Fatal("expected error for GEOCAM_VIDEO_TARGET_FPS=31 (above MaxVideoTargetFPS)")
	}
}

func TestLoadVideoMaxConcurrentPipelinesOutOfRange(t *testing.T) {
	t.Setenv("GEOCAM_VIDEO_MAX_CONCURRENT_PIPELINES", "0")
	if _, err := Load(); err == nil {
		t.Fatal("expected error for GEOCAM_VIDEO_MAX_CONCURRENT_PIPELINES=0")
	}
	t.Setenv("GEOCAM_VIDEO_MAX_CONCURRENT_PIPELINES", "17")
	if _, err := Load(); err == nil {
		t.Fatal("expected error for GEOCAM_VIDEO_MAX_CONCURRENT_PIPELINES=17")
	}
}
