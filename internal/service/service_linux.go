//go:build linux

package service

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

func runIfWindowsService(func(context.Context) error) (bool, error) { return false, nil }

func serviceCommand(action string, _ Options) error {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return fmt.Errorf("systemd/systemctl is unavailable: %w", err)
	}
	run := func(args ...string) error {
		cmd := exec.Command("systemctl", args...)
		output, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("systemctl %s failed: %s", strings.Join(args, " "), safeCommandOutput(output))
		}
		return nil
	}
	switch action {
	case "install":
		if err := run("cat", Name+".service"); err != nil {
			return fmt.Errorf("Linux unit is not installed; run the supported appliance installer first: %w", err)
		}
		return run("enable", Name+".service")
	case "start":
		return run("start", Name+".service")
	case "stop":
		return run("stop", Name+".service")
	case "restart":
		return run("restart", Name+".service")
	case "status":
		cmd := exec.Command("systemctl", "--no-pager", "status", Name+".service")
		output, err := cmd.CombinedOutput()
		fmt.Print(string(output))
		return err
	case "logs":
		cmd := exec.Command("journalctl", "-u", Name+".service", "--no-pager", "-n", "100")
		output, err := cmd.CombinedOutput()
		fmt.Print(string(output))
		return err
	case "uninstall":
		cmd := exec.Command("systemctl", "show", "--property=LoadState", "--value", Name+".service")
		output, err := cmd.Output()
		if err != nil {
			return fmt.Errorf("inspect systemd unit before uninstall: %s", safeCommandOutput(output))
		}
		if strings.TrimSpace(string(output)) == "not-found" {
			return nil
		}
		if err := run("disable", "--now", Name+".service"); err != nil {
			return err
		}
		fmt.Println("systemd unit disabled; the appliance package owns the unit file. Edge data, configuration, identity and credentials were preserved.")
		return nil
	default:
		return errors.New("unsupported service action")
	}
}

func safeCommandOutput(output []byte) string {
	text := strings.TrimSpace(string(output))
	if text == "" {
		return "no diagnostic output"
	}
	if len(text) > 500 {
		text = text[:500]
	}
	return text
}
