//go:build windows

package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

func IsWindowsService() (bool, error) { return svc.IsWindowsService() }

func runIfWindowsService(run func(context.Context) error) (bool, error) {
	isService, err := svc.IsWindowsService()
	if err != nil || !isService {
		return false, err
	}
	return true, svc.Run(Name, windowsHandler{run: run})
}

type windowsHandler struct {
	run func(context.Context) error
}

func (h windowsHandler) Execute(_ []string, requests <-chan svc.ChangeRequest, statuses chan<- svc.Status) (bool, uint32) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan error, 1)
	go func() { finished <- h.run(ctx) }()
	statuses <- svc.Status{State: svc.StartPending, WaitHint: uint32((30 * time.Second).Milliseconds())}
	statuses <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for {
		select {
		case change, ok := <-requests:
			if !ok {
				cancel()
				return false, windowsExitCode(<-finished)
			}
			switch change.Cmd {
			case svc.Interrogate:
				statuses <- change.CurrentStatus
			case svc.Stop, svc.Shutdown:
				statuses <- svc.Status{State: svc.StopPending, WaitHint: uint32((30 * time.Second).Milliseconds())}
				cancel()
				err := <-finished
				code := windowsExitCode(err)
				statuses <- svc.Status{State: svc.Stopped, Win32ExitCode: code}
				return false, code
			}
		case err := <-finished:
			code := windowsExitCode(err)
			statuses <- svc.Status{State: svc.Stopped, Win32ExitCode: code}
			return false, code
		}
	}
}

func windowsExitCode(err error) uint32 {
	if err == nil || errors.Is(err, context.Canceled) {
		return 0
	}
	return 1
}

func serviceCommand(action string, options Options) error {
	switch action {
	case "install":
		return installWindowsService(options)
	case "start":
		return withWindowsService(func(service *mgr.Service) error { return service.Start() })
	case "stop":
		return stopWindowsService()
	case "restart":
		if err := stopWindowsService(); err != nil {
			return err
		}
		return withWindowsService(func(service *mgr.Service) error { return service.Start() })
	case "status":
		return withWindowsService(func(service *mgr.Service) error {
			status, err := service.Query()
			if err != nil {
				return err
			}
			fmt.Printf("service: %s\nstate: %s\npid: %d\n", Name, windowsServiceState(status.State), status.ProcessId)
			return nil
		})
	case "logs":
		if options.DataDir == "" {
			return errors.New("GEOCAM_DATA_DIR is not configured; run `geocam-edge config`")
		}
		return printWindowsLogTail(filepath.Join(options.DataDir, "logs", "geocam-edge.log"), 100)
	case "uninstall":
		return uninstallWindowsService()
	default:
		return errors.New("unsupported Windows service action")
	}
}

func installWindowsService(options Options) error {
	if options.Executable == "" || !filepath.IsAbs(options.Executable) || options.DataDir == "" || options.ConfigFile == "" {
		return errors.New("service install requires absolute executable, data-dir and config-file paths")
	}
	if err := os.MkdirAll(filepath.Join(options.DataDir, "logs"), 0o700); err != nil {
		return fmt.Errorf("create service log directory: %w", err)
	}
	manager, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect Windows Service Control Manager: %w", err)
	}
	defer manager.Disconnect()
	if existing, err := manager.OpenService(Name); err == nil {
		_ = existing.Close()
		return fmt.Errorf("Windows service %s is already installed", Name)
	}
	service, err := manager.CreateService(Name, options.Executable, mgr.Config{
		StartType:    mgr.StartAutomatic,
		ErrorControl: mgr.ErrorNormal,
		DisplayName:  "GEO CAM Edge",
		Description:  "GEO CAM Edge camera gateway and processing agent",
	}, "run")
	if err != nil {
		return fmt.Errorf("create Windows service: %w", err)
	}
	defer service.Close()
	if err := service.SetRecoveryActions([]mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 2 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 10 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
		{Type: mgr.NoAction, Delay: 0},
	}, 300); err != nil {
		_ = service.Delete()
		return fmt.Errorf("configure bounded Windows service recovery: %w", err)
	}
	if err := service.SetRecoveryActionsOnNonCrashFailures(true); err != nil {
		_ = service.Delete()
		return fmt.Errorf("enable recovery for non-crash service failures: %w", err)
	}
	if err := setWindowsServiceEnvironment(options); err != nil {
		_ = service.Delete()
		return fmt.Errorf("configure Windows service environment: %w", err)
	}
	fmt.Printf("Windows service %s installed as automatic; start with `geocam-edge service start`. Data and config are preserved on uninstall.\n", Name)
	return nil
}

func setWindowsServiceEnvironment(options Options) error {
	key, _, err := registry.CreateKey(registry.LOCAL_MACHINE,
		`SYSTEM\CurrentControlSet\Services\`+Name, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()
	values := []string{
		"GEOCAM_CONFIG_FILE=" + options.ConfigFile,
		"GEOCAM_DATA_DIR=" + options.DataDir,
		"GEOCAM_SERVICE_MODE=true",
		"GEOCAM_LOG_FILE=" + filepath.Join(options.DataDir, "logs", "geocam-edge.log"),
	}
	return key.SetStringsValue("Environment", values)
}

func withWindowsService(fn func(*mgr.Service) error) error {
	manager, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect Windows Service Control Manager: %w", err)
	}
	defer manager.Disconnect()
	service, err := manager.OpenService(Name)
	if err != nil {
		return fmt.Errorf("open Windows service %s: %w", Name, err)
	}
	defer service.Close()
	return fn(service)
}

func stopWindowsService() error {
	return withWindowsService(func(service *mgr.Service) error {
		status, err := service.Query()
		if err != nil {
			return err
		}
		if status.State == svc.Stopped {
			return nil
		}
		if _, err := service.Control(svc.Stop); err != nil {
			return fmt.Errorf("send service stop: %w", err)
		}
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			status, err = service.Query()
			if err != nil {
				return err
			}
			if status.State == svc.Stopped {
				return nil
			}
			time.Sleep(200 * time.Millisecond)
		}
		return errors.New("Windows Edge service did not stop within 30 seconds")
	})
}

func uninstallWindowsService() error {
	manager, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect Windows Service Control Manager: %w", err)
	}
	defer manager.Disconnect()
	service, err := manager.OpenService(Name)
	if err != nil {
		if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
			return nil
		}
		return fmt.Errorf("open Windows service %s: %w", Name, err)
	}
	defer service.Close()
	status, err := service.Query()
	if err != nil {
		return fmt.Errorf("query Windows service before uninstall: %w", err)
	}
	if status.State != svc.Stopped {
		if _, err := service.Control(svc.Stop); err != nil && !errors.Is(err, windows.ERROR_SERVICE_NOT_ACTIVE) {
			return fmt.Errorf("stop Windows service before uninstall: %w", err)
		}
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			status, err = service.Query()
			if err != nil {
				return fmt.Errorf("wait for Windows service stop: %w", err)
			}
			if status.State == svc.Stopped {
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		if status.State != svc.Stopped {
			return errors.New("Windows Edge service did not stop within 30 seconds")
		}
	}
	if err := service.Delete(); err != nil {
		return fmt.Errorf("remove Windows service registration: %w", err)
	}
	fmt.Println("Windows service removed; Edge data, configuration, identity, credentials and logs were preserved.")
	return nil
}

func windowsServiceState(state svc.State) string {
	switch state {
	case svc.Stopped:
		return "stopped"
	case svc.StartPending:
		return "start_pending"
	case svc.StopPending:
		return "stop_pending"
	case svc.Running:
		return "running"
	case svc.Paused:
		return "paused"
	default:
		return fmt.Sprintf("state_%d", state)
	}
}

func printWindowsLogTail(path string, count int) error {
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
