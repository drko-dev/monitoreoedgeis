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
}

func TestLoadOverrides(t *testing.T) {
	t.Setenv("GEOCAM_EDGE_ID", "edge-001")
	t.Setenv("GEOCAM_PROCESSING_MODE", "EDGE") // case-insensitive
	t.Setenv("GEOCAM_LOG_LEVEL", "debug")
	t.Setenv("GEOCAM_SAAS_URL", "https://saas.example.test")
	t.Setenv("GEOCAM_HEARTBEAT_INTERVAL", "90s")
	t.Setenv("GEOCAM_DATA_DIR", "/tmp/geocam")
	t.Setenv("GEOCAM_HEALTH_ADDR", "127.0.0.1:9091")

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
}

func TestLoadRejectsInvalidValues(t *testing.T) {
	tests := []struct{ key, value string }{
		{"GEOCAM_PROCESSING_MODE", "gpu"},
		{"GEOCAM_LOG_LEVEL", "verbose"},
		{"GEOCAM_HEARTBEAT_INTERVAL", "soon"},
		{"GEOCAM_HEARTBEAT_INTERVAL", "-5s"},
		{"GEOCAM_HEALTH_ADDR", "not-a-valid-addr"},
		{"GEOCAM_SAAS_TIMEOUT", "soon"},
		{"GEOCAM_SAAS_TIMEOUT", "-5s"},
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
