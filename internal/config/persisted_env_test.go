package config

import (
	"errors"
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

func TestWritePersistentValuesCreatesFileAndRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "edge.env")
	t.Setenv(ConfigFileEnv, path)

	if err := WritePersistentValues(map[string]string{
		"GEOCAM_PROCESSING_MODE":        "edge",
		"GEOCAM_VIDEO_PIPELINE_ENABLED": "true",
	}); err != nil {
		t.Fatalf("WritePersistentValues: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat written file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("file mode = %v, want 0600", info.Mode().Perm())
	}

	mode, ok, err := PersistentFileValue("GEOCAM_PROCESSING_MODE")
	if err != nil || !ok || mode != "edge" {
		t.Fatalf("PersistentFileValue(mode) = %q, %v, %v", mode, ok, err)
	}
	pipeline, ok, err := PersistentFileValue("GEOCAM_VIDEO_PIPELINE_ENABLED")
	if err != nil || !ok || pipeline != "true" {
		t.Fatalf("PersistentFileValue(pipeline) = %q, %v, %v", pipeline, ok, err)
	}
}

func TestWritePersistentValuesMergesUnrelatedKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "edge.env")
	if err := os.WriteFile(path, []byte("GEOCAM_SAAS_URL=https://example.test\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	t.Setenv(ConfigFileEnv, path)

	if err := WritePersistentValues(map[string]string{"GEOCAM_PROCESSING_MODE": "hybrid"}); err != nil {
		t.Fatalf("WritePersistentValues: %v", err)
	}

	saasURL, ok, err := PersistentFileValue("GEOCAM_SAAS_URL")
	if err != nil || !ok || saasURL != "https://example.test" {
		t.Fatalf("unrelated key not preserved: %q, %v, %v", saasURL, ok, err)
	}
	mode, ok, err := PersistentFileValue("GEOCAM_PROCESSING_MODE")
	if err != nil || !ok || mode != "hybrid" {
		t.Fatalf("PersistentFileValue(mode) = %q, %v, %v", mode, ok, err)
	}
}

func TestWritePersistentValuesRejectsUnknownKeyWithoutWriting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "edge.env")
	t.Setenv(ConfigFileEnv, path)

	err := WritePersistentValues(map[string]string{"GEOCAM_DEVICE_PASSWORD": "x"})
	if err == nil {
		t.Fatal("WritePersistentValues accepted a disallowed key")
	}
	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatal("WritePersistentValues created a file despite rejecting the update")
	}
}

func TestPersistentFileRawAndRestoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "edge.env")
	t.Setenv(ConfigFileEnv, path)

	snapshot, existed, err := PersistentFileRaw()
	if err != nil || existed {
		t.Fatalf("PersistentFileRaw before creation = %v, %v, %v", snapshot, existed, err)
	}

	if err := WritePersistentValues(map[string]string{"GEOCAM_PROCESSING_MODE": "edge"}); err != nil {
		t.Fatalf("WritePersistentValues: %v", err)
	}

	if err := RestorePersistentFileRaw(snapshot, existed); err != nil {
		t.Fatalf("RestorePersistentFileRaw: %v", err)
	}
	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatal("RestorePersistentFileRaw did not remove a file that did not exist before")
	}

	if err := WritePersistentValues(map[string]string{"GEOCAM_PROCESSING_MODE": "cloud"}); err != nil {
		t.Fatalf("WritePersistentValues (first): %v", err)
	}
	snapshot2, existed2, err := PersistentFileRaw()
	if err != nil || !existed2 {
		t.Fatalf("PersistentFileRaw after first write = %v, %v, %v", snapshot2, existed2, err)
	}
	if err := WritePersistentValues(map[string]string{"GEOCAM_PROCESSING_MODE": "edge"}); err != nil {
		t.Fatalf("WritePersistentValues (second): %v", err)
	}
	if err := RestorePersistentFileRaw(snapshot2, existed2); err != nil {
		t.Fatalf("RestorePersistentFileRaw: %v", err)
	}
	mode, ok, err := PersistentFileValue("GEOCAM_PROCESSING_MODE")
	if err != nil || !ok || mode != "cloud" {
		t.Fatalf("restored mode = %q, %v, %v, want cloud", mode, ok, err)
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
