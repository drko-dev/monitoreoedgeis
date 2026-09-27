package main

import (
	"context"

	"github.com/drko-dev/monitoreoedgeis/internal/installer"
)

// App is the desktop application bridge. It exposes strictly typed, audited methods
// to the Wails frontend. It contains NO arbitrary command execution, NO raw filesystem
// bindings, and NO secret access.
type App struct {
	ctx       context.Context
	installer *installer.Service
}

// NewApp creates a new desktop App instance backed by the Go installer facade.
func NewApp() *App {
	return &App{
		installer: installer.NewService("", ""),
	}
}

// startup is called by Wails when the application starts.
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
}

// GetSystemReport returns sanitized, non-secret host environment information.
func (a *App) GetSystemReport() (*installer.SystemReport, error) {
	return a.installer.GetSystemReport(a.ctx)
}

// GetInstallerState returns the derived state of the Edge installer state machine.
func (a *App) GetInstallerState() (*installer.InstallerState, error) {
	return a.installer.GetInstallerState(a.ctx)
}
