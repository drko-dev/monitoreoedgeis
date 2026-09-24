//go:build darwin

package service

import (
	"bytes"
	"encoding/xml"
	"io"
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
