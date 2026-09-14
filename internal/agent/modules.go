package agent

import (
	"context"
	"errors"
	"fmt"
)

// Module is a long-lived agent subsystem with an ordered startup and
// shutdown. The only real module in this milestone is the local health HTTP
// server (see health_module.go); discovery/transport/video will implement
// this interface later.
type Module interface {
	Name() string
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
}

// moduleManager starts modules in order and stops them in reverse order. A
// failed Start aborts startup and unwinds whatever already started, so
// nothing is left orphaned.
type moduleManager struct {
	modules []Module
	started []Module
	report  func(name, state string)
}

// newModuleManager builds a manager for modules, started in the given
// order. report, if non-nil, is called on every module state transition
// (e.g. to mirror it into the health snapshot).
func newModuleManager(report func(name, state string), modules ...Module) *moduleManager {
	return &moduleManager{modules: modules, report: report}
}

func (m *moduleManager) setState(name, state string) {
	if m.report != nil {
		m.report(name, state)
	}
}

// Start starts every module in order. On the first failure it stops
// whatever already started (reverse order) and returns the error.
func (m *moduleManager) Start(ctx context.Context) error {
	for _, mod := range m.modules {
		if err := mod.Start(ctx); err != nil {
			m.setState(mod.Name(), "failed")
			_ = m.Stop(ctx)
			return fmt.Errorf("module %s: start: %w", mod.Name(), err)
		}
		m.started = append(m.started, mod)
		m.setState(mod.Name(), "running")
	}
	return nil
}

// Stop stops every started module in reverse startup order, propagating ctx
// cancellation and collecting every error rather than stopping at the first.
func (m *moduleManager) Stop(ctx context.Context) error {
	var errs []error
	for i := len(m.started) - 1; i >= 0; i-- {
		mod := m.started[i]
		if err := mod.Stop(ctx); err != nil {
			m.setState(mod.Name(), "failed")
			errs = append(errs, fmt.Errorf("module %s: stop: %w", mod.Name(), err))
			continue
		}
		m.setState(mod.Name(), "stopped")
	}
	m.started = nil
	return errors.Join(errs...)
}
