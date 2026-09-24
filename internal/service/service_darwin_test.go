//go:build darwin

package service

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLaunchAgentPlistIsValidPersistentAndSecretFree(t *testing.T) {
	content := renderMacOSPlist(Options{
		Executable: "/Applications/Geo & Cam Edge/bin/geocam-edge",
		DataDir:    "/Users/operator/Library/Application Support/Geo Cam Edge/data",
		ConfigFile: "/Users/operator/Library/Application Support/geocam-edge/edge.env",
	}, "/Users/operator/Library/Application Support/Geo Cam Edge/data/logs")
	decoder := xml.NewDecoder(bytes.NewReader(content))
	for {
		if _, err := decoder.Token(); err != nil {
			if err == io.EOF {
				break
			}
			t.Fatalf("LaunchAgent plist is not well-formed XML: %v", err)
		}
	}
	text := string(content)
	for _, required := range []string{
		"<key>RunAtLoad</key><true/>",
		"<key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>",
		"<key>ThrottleInterval</key><integer>30</integer>",
		"<key>WorkingDirectory</key>",
		"<string>service</string><string>supervise</string>",
		"GEOCAM_CONFIG_FILE",
		"GEOCAM_DATA_DIR",
		"GEOCAM_SERVICE_MODE",
		"launchd.out.log",
		"&amp;",
	} {
		if !strings.Contains(text, required) {
			t.Errorf("LaunchAgent plist missing %q:\n%s", required, text)
		}
	}
	for _, forbidden := range []string{"ENROLLMENT_TOKEN", "DEVICE_KEY", "PASSWORD=", "credentials.json"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("LaunchAgent plist contains secret-bearing setting %q", forbidden)
		}
	}
}

func TestLaunchAgentRequiresExplicitExistingDataAndConfigPaths(t *testing.T) {
	tests := []struct {
		name    string
		options Options
		want    string
	}{
		{name: "empty options", want: "absolute Edge executable"},
		{name: "no data dir", options: Options{Executable: "/usr/local/bin/geocam-edge"}, want: "explicitly configured absolute GEOCAM_DATA_DIR"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateMacOSOptions(tt.options)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("validateMacOSOptions() error=%v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestInstallReloadsAnAlreadyLoadedLaunchAgent(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "new-edge-data")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatalf("MkdirAll(dataDir): %v", err)
	}
	configPath := filepath.Join(root, "edge.env")
	for _, path := range []string{
		configPath,
		filepath.Join(dataDir, "identity.json"),
		filepath.Join(dataDir, "credentials.json"),
	} {
		if err := os.WriteFile(path, []byte("test"), 0o600); err != nil {
			t.Fatalf("WriteFile(%s): %v", path, err)
		}
	}
	plistPath := filepath.Join(root, "io.geocam.edge.plist")
	if err := os.WriteFile(plistPath, []byte("old plist"), 0o600); err != nil {
		t.Fatalf("seed old plist: %v", err)
	}
	options := Options{
		Executable: "/Applications/Geo Cam Edge/geocam-edge",
		DataDir:    dataDir,
		ConfigFile: configPath,
	}

	originalRunner := launchctlRun
	t.Cleanup(func() { launchctlRun = originalRunner })
	var calls []string
	launchctlRun = func(args ...string) ([]byte, error) {
		calls = append(calls, strings.Join(args, " "))
		return nil, nil // launchctl print reports that the job is already loaded.
	}

	if err := installMacOSService(plistPath, "gui/501", "gui/501/"+macOSLabel, options); err != nil {
		t.Fatalf("installMacOSService: %v", err)
	}
	if want := []string{"print gui/501/" + macOSLabel, "bootout gui/501/" + macOSLabel, "bootstrap gui/501 " + plistPath}; strings.Join(calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("launchctl sequence=%v, want %v", calls, want)
	}
	updated, err := os.ReadFile(plistPath)
	if err != nil {
		t.Fatalf("ReadFile(updated plist): %v", err)
	}
	if !strings.Contains(string(updated), plistEscape(dataDir)) {
		t.Fatalf("updated plist does not contain the new data directory:\n%s", updated)
	}
}

func TestInstallDoesNotBootstrapAnUnloadedLaunchAgent(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "edge-data")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatalf("MkdirAll(dataDir): %v", err)
	}
	configPath := filepath.Join(root, "edge.env")
	for _, path := range []string{configPath, filepath.Join(dataDir, "identity.json"), filepath.Join(dataDir, "credentials.json")} {
		if err := os.WriteFile(path, []byte("test"), 0o600); err != nil {
			t.Fatalf("WriteFile(%s): %v", path, err)
		}
	}
	var calls []string
	originalRunner := launchctlRun
	t.Cleanup(func() { launchctlRun = originalRunner })
	launchctlRun = func(args ...string) ([]byte, error) {
		calls = append(calls, strings.Join(args, " "))
		return nil, errors.New("not loaded")
	}

	plistPath := filepath.Join(root, "io.geocam.edge.plist")
	if err := installMacOSService(plistPath, "gui/501", "gui/501/"+macOSLabel, Options{
		Executable: "/usr/local/bin/geocam-edge", DataDir: dataDir, ConfigFile: configPath,
	}); err != nil {
		t.Fatalf("installMacOSService: %v", err)
	}
	if len(calls) != 1 || calls[0] != "print gui/501/"+macOSLabel {
		t.Fatalf("launchctl sequence=%v, want only the loaded-state query", calls)
	}
}
