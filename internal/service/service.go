// Package service exposes a small, OS-specific lifecycle surface for the
// standalone Edge process. It never owns or mutates the Edge data directory.
package service

import (
	"context"
	"errors"
	"os"
	"strings"
)

const Name = "geocam-edge"

var ErrUnsupported = errors.New("service management is not supported on this operating system")

type Options struct {
	Executable string
	DataDir    string
	ConfigFile string
}

func ProcessModeEnabled() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv("GEOCAM_SERVICE_MODE")), "true") || os.Getenv("GEOCAM_SERVICE_MODE") == "1"
}

// RunIfWindowsService dispatches run to the SCM when this process was started
// by Windows Service Control Manager. It returns handled=false on other
// platforms and on a normal interactive Windows console invocation.
func RunIfWindowsService(run func(context.Context) error) (handled bool, err error) {
	return runIfWindowsService(run)
}

func Command(action string, options Options) error {
	action = strings.ToLower(strings.TrimSpace(action))
	switch action {
	case "install", "start", "stop", "restart", "status", "uninstall", "logs":
		return serviceCommand(action, options)
	default:
		return errors.New("usage: geocam-edge service install|start|stop|restart|status|logs|uninstall")
	}
}
