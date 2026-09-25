//go:build darwin

package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const macOSLabel = "io.geocam.edge"

var launchctlRun = func(args ...string) ([]byte, error) {
	return exec.Command("launchctl", args...).CombinedOutput()
}

func runIfWindowsService(func(context.Context) error) (bool, error) { return false, nil }

func serviceCommand(action string, options Options) error {
	plistPath, domain, err := macOSServicePaths()
	if err != nil {
		return err
	}
	target := domain + "/" + macOSLabel
	switch action {
	case "install":
		return installMacOSService(plistPath, domain, target, options)
	case "start":
		if !macOSJobLoaded(target) {
			if _, err := os.Stat(plistPath); err != nil {
				return errors.New("LaunchAgent is not installed; configure persistent settings then run `geocam-edge service install`")
			}
			if err := launchctl("bootstrap", domain, plistPath); err != nil {
				return err
			}
		}
		return launchctl("kickstart", target)
	case "stop":
		if !macOSJobLoaded(target) {
			return nil
		}
		return launchctl("bootout", target)
	case "restart":
		if macOSJobLoaded(target) {
			if err := launchctl("bootout", target); err != nil {
				return err
			}
		}
		if err := launchctl("bootstrap", domain, plistPath); err != nil {
			return err
		}
		return launchctl("kickstart", target)
	case "status":
		cmd := exec.Command("launchctl", "print", target)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("LaunchAgent status unavailable (%s)", commandOutputSummary(out))
		}
		fmt.Print(string(out))
		return nil
	case "logs":
		if options.DataDir == "" {
			return errors.New("GEOCAM_DATA_DIR is not configured; see `geocam-edge config`")
		}
		return printLogTail(filepath.Join(options.DataDir, "logs", "launchd.out.log"), 100)
	case "uninstall":
		if macOSJobLoaded(target) {
			if err := launchctl("bootout", target); err != nil {
				return err
			}
		}
		if err := os.Remove(plistPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove LaunchAgent definition: %w", err)
		}
		fmt.Println("LaunchAgent removed; configuration, logs and Edge data were preserved.")
		return nil
	default:
		return errors.New("unsupported macOS service action")
	}
}

func installMacOSService(plistPath, domain, target string, options Options) error {
	if err := validateMacOSOptions(options); err != nil {
		return err
	}
	loaded := macOSJobLoaded(target)
	if loaded {
		if err := launchctl("bootout", target); err != nil {
			return fmt.Errorf("unload existing LaunchAgent before reinstall: %w", err)
		}
	}
	if err := writeMacOSPlist(plistPath, options); err != nil {
		return err
	}
	if loaded {
		if err := launchctl("bootstrap", domain, plistPath); err != nil {
			return fmt.Errorf("load updated LaunchAgent: %w", err)
		}
	}
	fmt.Printf("LaunchAgent installed for current user login; start with `geocam-edge service start`.\n")
	fmt.Printf("plist: %s\nlogs: %s\n", plistPath, filepath.Join(options.DataDir, "logs"))
	return nil
}

func validateMacOSOptions(options Options) error {
	if options.Executable == "" || !filepath.IsAbs(options.Executable) {
		return errors.New("service install requires an absolute Edge executable path")
	}
	if options.DataDir == "" || !filepath.IsAbs(options.DataDir) {
		return errors.New("service install requires an explicitly configured absolute GEOCAM_DATA_DIR; it never chooses a device identity directory")
	}
	info, err := os.Stat(options.DataDir)
	if err != nil || !info.IsDir() {
		return errors.New("service install requires the existing canonical GEOCAM_DATA_DIR")
	}
	if options.ConfigFile == "" || !filepath.IsAbs(options.ConfigFile) {
		return errors.New("service install requires an absolute persistent config file path")
	}
	if _, err := os.Stat(options.ConfigFile); err != nil {
		return errors.New("persistent config file is missing; run `geocam-edge config` and configure the existing device identity first")
	}
	if _, err := os.Stat(filepath.Join(options.DataDir, "identity.json")); err != nil {
		return errors.New("service install requires the existing identity.json; it will not create or replace device identity")
	}
	if _, err := os.Stat(filepath.Join(options.DataDir, "credentials.json")); err != nil {
		return errors.New("service install requires existing credentials.json; use the supported SaaS enrollment flow first")
	}
	return nil
}

func macOSServicePaths() (plistPath, domain string, err error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", fmt.Errorf("resolve current user home: %w", err)
	}
	return filepath.Join(home, "Library", "LaunchAgents", macOSLabel+".plist"),
		fmt.Sprintf("gui/%d", os.Getuid()), nil
}

func macOSJobLoaded(target string) bool {
	_, err := launchctlRun("print", target)
	return err == nil
}

func launchctl(args ...string) error {
	out, err := launchctlRun(args...)
	if err != nil {
		return fmt.Errorf("launchctl %s failed (%s)", strings.Join(args, " "), commandOutputSummary(out))
	}
	return nil
}

func writeMacOSPlist(path string, options Options) error {
	logDir := filepath.Join(options.DataDir, "logs")
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		return fmt.Errorf("create service log directory: %w", err)
	}
	if err := os.Chmod(logDir, 0o700); err != nil {
		return fmt.Errorf("secure service log directory: %w", err)
	}
	content := renderMacOSPlist(options, logDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create LaunchAgents directory: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, content, 0o600); err != nil {
		return fmt.Errorf("write LaunchAgent plist: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("install LaunchAgent plist: %w", err)
	}
	return nil
}

func renderMacOSPlist(options Options, logDir string) []byte {
	return []byte(fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>Label</key><string>%s</string>
<key>ProgramArguments</key><array><string>%s</string><string>service</string><string>supervise</string></array>
<key>WorkingDirectory</key><string>%s</string>
<key>RunAtLoad</key><true/>
<key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>
<key>ThrottleInterval</key><integer>30</integer>
<key>ProcessType</key><string>Background</string>
<key>EnvironmentVariables</key><dict>
<key>GEOCAM_CONFIG_FILE</key><string>%s</string>
<key>GEOCAM_DATA_DIR</key><string>%s</string>
<key>GEOCAM_SERVICE_MODE</key><string>true</string>
<key>GEOCAM_LOG_FILE</key><string>%s</string>
</dict>
<key>StandardOutPath</key><string>%s</string>
<key>StandardErrorPath</key><string>%s</string>
</dict></plist>
	`, plistEscape(macOSLabel), plistEscape(options.Executable), plistEscape(options.DataDir),
		plistEscape(options.ConfigFile), plistEscape(options.DataDir), plistEscape(filepath.Join(logDir, "geocam-edge.log")),
		plistEscape(filepath.Join(logDir, "launchd.out.log")), plistEscape(filepath.Join(logDir, "launchd.err.log"))))
}

func plistEscape(value string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\"", "&quot;", "'", "&apos;").Replace(value)
}

func commandOutputSummary(out []byte) string {
	text := strings.TrimSpace(string(out))
	if text == "" {
		return "no detail"
	}
	if len(text) > 300 {
		text = text[:300]
	}
	return text
}

func printLogTail(path string, count int) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read Edge service log: %w", err)
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	start := len(lines) - count
	if start < 0 {
		start = 0
	}
	for _, line := range lines[start:] {
		fmt.Println(line)
	}
	return nil
}
