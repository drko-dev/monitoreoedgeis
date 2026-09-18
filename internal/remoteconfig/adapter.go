package remoteconfig

import "context"

// RuntimeAdapter is the minimal interface IA2 implements to connect the
// real runtime knobs (FPS, resolution, ROI, models, processing mode, ...)
// to this engine. This package never implements those knobs itself -- it
// only sequences validate -> apply -> rollback around a caller-supplied
// adapter.
type RuntimeAdapter interface {
	// ValidateRuntimeConfig checks that cfg is well-formed and applicable
	// to the current runtime, without changing anything. A non-nil error
	// aborts the apply before any state is touched -- current stays
	// intact.
	ValidateRuntimeConfig(ctx context.Context, cfg Config) error

	// ApplyRuntimeConfig applies cfg to the live runtime. A non-nil error
	// triggers RollbackRuntimeConfig with the previous known-good config.
	ApplyRuntimeConfig(ctx context.Context, cfg Config) error

	// RollbackRuntimeConfig restores the runtime to previous, a
	// config that was applied successfully before (the zero Config if
	// none ever was). It is called after a failed apply, and once more,
	// defensively, on restart if the process crashed mid-apply.
	RollbackRuntimeConfig(ctx context.Context, previous Config) error
}

// NoopRuntimeAdapter is a safe, do-nothing RuntimeAdapter: it accepts any
// well-formed Config and never touches real runtime state. It lets the
// engine (poll, version, persist, apply lifecycle, rollback, status) run
// and be wired into the agent today, before IA2 supplies the real runtime
// knobs -- it implements no knobs of its own.
type NoopRuntimeAdapter struct{}

func (NoopRuntimeAdapter) ValidateRuntimeConfig(context.Context, Config) error { return nil }
func (NoopRuntimeAdapter) ApplyRuntimeConfig(context.Context, Config) error    { return nil }
func (NoopRuntimeAdapter) RollbackRuntimeConfig(context.Context, Config) error { return nil }
