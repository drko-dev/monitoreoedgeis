package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func unsetConfigEnv(t *testing.T, key string) {
	t.Helper()
	previous, wasSet := os.LookupEnv(key)
	if err := os.Unsetenv(key); err != nil {
		t.Fatalf("Unsetenv(%s): %v", key, err)
	}
	t.Cleanup(func() {
		if wasSet {
			_ = os.Setenv(key, previous)
		} else {
			_ = os.Unsetenv(key)
		}
	})
}

func TestLoadPersistentConfigThenEnvironmentOverrides(t *testing.T) {
	path := filepath.Join(t.TempDir(), "edge.env")
	contents := strings.Join([]string{
		"GEOCAM_DATA_DIR=/persisted/edge-data",
		"GEOCAM_SAAS_URL=https://cloud.example.test",
		"GEOCAM_PROCESSING_MODE=cloud",
		"GEOCAM_VIDEO_PIPELINE_ENABLED=true",
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	t.Setenv(ConfigFileEnv, path)
	for _, key := range []string{"GEOCAM_DATA_DIR", "GEOCAM_SAAS_URL", "GEOCAM_PROCESSING_MODE", "GEOCAM_VIDEO_PIPELINE_ENABLED"} {
		unsetConfigEnv(t, key)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() from persistent config: %v", err)
	}
	if cfg.DataDir != "/persisted/edge-data" || cfg.SaaSURL != "https://cloud.example.test" {
		t.Fatalf("persistent paths not loaded: data_dir=%q saas_url=%q", cfg.DataDir, cfg.SaaSURL)
	}
	if cfg.ProcessingMode != ModeCloud || !cfg.VideoPipelineEnabled {
		t.Fatalf("persistent runtime settings not loaded: mode=%q pipeline=%v", cfg.ProcessingMode, cfg.VideoPipelineEnabled)
	}
	if cfg.ConfigFilePath != path {
		t.Fatalf("ConfigFilePath=%q, want %q", cfg.ConfigFilePath, path)
	}
	if _, ok := os.LookupEnv("GEOCAM_DATA_DIR"); ok {
		t.Fatal("persistent config values leaked into the process environment after Load")
	}

	t.Setenv("GEOCAM_PROCESSING_MODE", "hybrid")
	cfg, err = Load()
	if err != nil {
		t.Fatalf("Load() with environment override: %v", err)
	}
	if cfg.ProcessingMode != ModeHybrid {
		t.Fatalf("environment override mode=%q, want %q", cfg.ProcessingMode, ModeHybrid)
	}
}

func TestPersistentConfigRejectsSecretAndUnknownSettings(t *testing.T) {
	for _, key := range []string{"GEOCAM_ENROLLMENT_TOKEN", "GEOCAM_DEVICE_PASSWORD", "OTHER_SETTING"} {
		t.Run(key, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "edge.env")
			if err := os.WriteFile(path, []byte(key+"=sensitive-value\n"), 0o600); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}
			t.Setenv(ConfigFileEnv, path)
			_, err := Load()
			if err == nil {
				t.Fatal("Load() accepted unsupported/secret config key")
			}
			if strings.Contains(err.Error(), "sensitive-value") {
				t.Fatal("config parse error leaked the setting value")
			}
		})
	}
}

func TestPersistentConfigRejectsMalformedAndDuplicateSettings(t *testing.T) {
	for name, contents := range map[string]string{
		"malformed": "GEOCAM_DATA_DIR\n",
		"duplicate": "GEOCAM_DATA_DIR=/first\nGEOCAM_DATA_DIR=/second\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "edge.env")
			if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}
			t.Setenv(ConfigFileEnv, path)
			_, err := Load()
			if err == nil {
				t.Fatal("Load() accepted malformed config")
			}
		})
	}
}
