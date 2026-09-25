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

func TestLoadRejectsSaaSURLCredentialsAndSensitiveQuery(t *testing.T) {
	for name, rawURL := range map[string]string{
		"userinfo":            "https://dummy-user:dummy-pass@example.test/",
		"token query":         "https://example.test/?token=dummy-token",
		"API key query":       "https://example.test/?api-key=dummy-key",
		"access token query":  "https://example.test/?access_token=dummy-token",
		"password query":      "https://example.test/?password=dummy-password",
		"client secret query": "https://example.test/?client_secret=dummy-secret",
		"signed query":        "https://example.test/?X-Amz-Signature=dummy-signature",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "edge.env")
			if err := os.WriteFile(path, nil, 0o600); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}
			t.Setenv(ConfigFileEnv, path)
			t.Setenv("GEOCAM_SAAS_URL", rawURL)

			_, err := Load()
			if err == nil {
				t.Fatal("Load() accepted a SaaS URL containing credentials")
			}
			for _, secret := range []string{"dummy-user", "dummy-pass", "dummy-token", "dummy-key", "dummy-password", "dummy-secret", "dummy-signature"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("configuration error leaked URL secret %q: %v", secret, err)
				}
			}
		})
	}
}

func TestLoadAcceptsNormalSaaSBaseURL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "edge.env")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	t.Setenv(ConfigFileEnv, path)
	t.Setenv("GEOCAM_SAAS_URL", "https://example.test")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() rejected normal SaaS URL: %v", err)
	}
	if cfg.SaaSURL != "https://example.test" {
		t.Fatalf("SaaSURL=%q, want https://example.test", cfg.SaaSURL)
	}
}

func TestSanitizeSaaSURLRedactsCredentialsAndSensitiveQuery(t *testing.T) {
	const raw = "https://dummy-user:dummy-pass@example.test/api?token=dummy-token&mode=cloud"
	got := SanitizeSaaSURL(raw)
	for _, secret := range []string{"dummy-user", "dummy-pass", "dummy-token"} {
		if strings.Contains(got, secret) {
			t.Errorf("SanitizeSaaSURL leaked %q in %q", secret, got)
		}
	}
	if !strings.Contains(got, "example.test") || !strings.Contains(got, "mode=cloud") || !strings.Contains(got, "token=%5Bredacted%5D") {
		t.Errorf("SanitizeSaaSURL lost safe fields or did not redact token: %q", got)
	}
}
