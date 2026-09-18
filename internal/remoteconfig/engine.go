package remoteconfig

import (
	"context"
	"fmt"
)

// Error codes reported alongside a terminal ApplyStatus.
const (
	ErrCodeStaleVersion      = "STALE_VERSION"
	ErrCodeVersionConflict   = "VERSION_CONFLICT"
	ErrCodePreviouslyFailed  = "PREVIOUSLY_FAILED"
	ErrCodeValidationFailed  = "VALIDATION_FAILED"
	ErrCodeApplyFailed       = "APPLY_FAILED"
	ErrCodeStagingUnresolved = "STAGING_UNRESOLVED"
)

// Engine sequences the O5-O8 apply lifecycle (validate -> stage -> apply ->
// verify -> publish/rollback) around a caller-supplied RuntimeAdapter, on
// top of a durable Store. It never polls the network itself -- see Module
// for the outbound sync it's driven by.
type Engine struct {
	store   *Store
	adapter RuntimeAdapter
}

// NewEngine builds an Engine over store using adapter for the actual
// runtime knobs.
func NewEngine(store *Store, adapter RuntimeAdapter) *Engine {
	if adapter == nil {
		adapter = NoopRuntimeAdapter{}
	}
	return &Engine{store: store, adapter: adapter}
}

// Recover resolves any config left mid-apply by a crash: if the persisted
// state shows a Staging config with no terminal outcome, it defensively
// rolls back to the config that was applied immediately before that
// attempt began. Must be called once at startup before any ReceiveDesired
// call.
//
// If the rollback itself fails, Staging is deliberately left in place --
// it is the durable marker that the runtime's actual state is unknown --
// and Recover returns a non-nil error. A later restart will retry the
// same rollback; ReceiveDesired refuses all new applies while Staging is
// unresolved (fail closed).
func (e *Engine) Recover(ctx context.Context) error {
	st := e.store.Get()
	if st.Staging == nil {
		return nil
	}

	interrupted := *st.Staging
	previous := previousKnownGood(st)
	rollbackErr := e.adapter.RollbackRuntimeConfig(ctx, previous)

	if rollbackErr != nil {
		_, saveErr := e.store.Update(func(s State) State {
			// Staging is intentionally left untouched: it is still the
			// only durable record that this version's runtime state is
			// unresolved.
			s.LastFailedVersion = interrupted.Version
			s.LastFailedConfig = &interrupted
			s.LastApplyStatus = ApplyStatusFailed
			s.LastApplyAt = nowRFC3339()
			s.LastErrorSafe = sanitizeApplyError(fmt.Errorf("recovery: interrupted apply, rollback failed: %w", rollbackErr))
			s.RollbackCount++
			return s
		})
		if saveErr != nil {
			return saveErr
		}
		return fmt.Errorf("remoteconfig: recovery rollback failed, staging left unresolved: %w", rollbackErr)
	}

	_, err := e.store.Update(func(s State) State {
		s.Staging = nil
		s.LastFailedVersion = interrupted.Version
		s.LastFailedConfig = &interrupted
		s.LastApplyStatus = ApplyStatusRolledBack
		s.LastApplyAt = nowRFC3339()
		s.LastErrorSafe = "recovery: apply interrupted by restart, rolled back to previous known-good"
		s.RollbackCount++
		return s
	})
	return err
}

// ReceiveDesired processes one desired config, running it through the full
// O1-O8 lifecycle, and returns the terminal ApplyStatus plus an error code
// to report back to SaaS (empty on success). A non-nil error means the
// outcome could not be durably recorded -- callers must not ACK any
// status in that case.
func (e *Engine) ReceiveDesired(ctx context.Context, desired Config) (ApplyStatus, string, error) {
	st := e.store.Get()

	if st.Staging != nil {
		// A previous apply/rollback attempt never reached a terminal,
		// durable outcome (crash, or a rollback that itself failed).
		// Fail closed: refuse every new apply until Recover resolves it
		// (normally at the next process start).
		return ApplyStatusFailed, ErrCodeStagingUnresolved, nil
	}

	switch {
	case desired.Version < st.AppliedVersion:
		// Never apply a version older than what's already applied; current
		// stays intact, nothing persisted.
		return ApplyStatusFailed, ErrCodeStaleVersion, nil

	case desired.Version == st.AppliedVersion:
		if st.AppliedConfig != nil && desired.Equal(*st.AppliedConfig) {
			return ApplyStatusApplied, "", nil // idempotent: already applied
		}
		return ApplyStatusFailed, ErrCodeVersionConflict, nil // same version, divergent content

	case st.LastFailedVersion != 0 && desired.Version == st.LastFailedVersion:
		if st.LastFailedConfig != nil && desired.Equal(*st.LastFailedConfig) {
			// Identical version+content that already failed: do not
			// reattempt apply on every poll.
			return ApplyStatusFailed, ErrCodePreviouslyFailed, nil
		}
		return ApplyStatusFailed, ErrCodeVersionConflict, nil // reusing a failed version number with different content
	}

	// New version. 1. received (implicit, we have it). 2. validate.
	if err := e.adapter.ValidateRuntimeConfig(ctx, desired); err != nil {
		if _, saveErr := e.store.Update(func(s State) State {
			s.LastFailedVersion = desired.Version
			cfg := desired
			s.LastFailedConfig = &cfg
			s.LastApplyStatus = ApplyStatusFailed
			s.LastApplyAt = nowRFC3339()
			s.LastErrorSafe = sanitizeApplyError(err)
			s.ReceivedAt = nowRFC3339()
			return s
		}); saveErr != nil {
			return "", "", saveErr
		}
		return ApplyStatusFailed, ErrCodeValidationFailed, nil
	}

	// 3. write staging atomically -- restart-safe marker before any apply
	// side effect begins.
	staged, err := e.store.Update(func(s State) State {
		cfg := desired
		s.Staging = &cfg
		s.LastApplyStatus = ApplyStatusApplying
		s.ReceivedAt = nowRFC3339()
		return s
	})
	if err != nil {
		return "", "", err
	}
	// The config that was actually live immediately before this attempt --
	// the correct rollback target for everything below (Blocker 2: the
	// current applied config takes priority over any older snapshot).
	rollbackTarget := previousKnownGood(staged)

	// 4. apply via the runtime adapter.
	applyErr := e.adapter.ApplyRuntimeConfig(ctx, desired)

	// 5. verify: the adapter's own error return is the only health signal
	// this core engine has -- a richer probe belongs to IA2's adapter.
	if applyErr != nil {
		rollbackErr := e.adapter.RollbackRuntimeConfig(ctx, rollbackTarget)
		if rollbackErr != nil {
			// Fail closed: leave Staging in place as the unresolved
			// marker (nothing here clears it) and report a hard error --
			// the caller must not ACK any status for this version.
			errMsg := sanitizeApplyError(fmt.Errorf("apply failed (%v); rollback also failed: %w", applyErr, rollbackErr))
			if _, saveErr := e.store.Update(func(s State) State {
				s.LastFailedVersion = desired.Version
				cfg := desired
				s.LastFailedConfig = &cfg
				s.LastApplyStatus = ApplyStatusFailed
				s.LastApplyAt = nowRFC3339()
				s.LastErrorSafe = errMsg
				s.RollbackCount++
				return s
			}); saveErr != nil {
				return "", "", saveErr
			}
			return "", "", fmt.Errorf("remoteconfig: %s", errMsg)
		}

		if _, saveErr := e.store.Update(func(s State) State {
			s.Staging = nil
			s.LastFailedVersion = desired.Version
			cfg := desired
			s.LastFailedConfig = &cfg
			s.LastApplyStatus = ApplyStatusRolledBack
			s.LastApplyAt = nowRFC3339()
			s.LastErrorSafe = sanitizeApplyError(applyErr)
			s.RollbackCount++
			return s
		}); saveErr != nil {
			return "", "", saveErr
		}
		return ApplyStatusRolledBack, ErrCodeApplyFailed, nil
	}

	// 6. publish current. 7. mark applied -- persisted first; only a
	// durable commit may report "applied" back to SaaS.
	if _, saveErr := e.store.Update(func(s State) State {
		if s.AppliedConfig != nil {
			s.PreviousKnownGoodVersion = s.AppliedVersion
			s.PreviousKnownGoodConfig = s.AppliedConfig
		}
		cfg := desired
		s.AppliedVersion = desired.Version
		s.AppliedConfig = &cfg
		s.Staging = nil
		s.LastApplyStatus = ApplyStatusApplied
		s.LastApplyAt = nowRFC3339()
		s.LastErrorSafe = ""
		return s
	}); saveErr != nil {
		// The runtime already applied `desired`, but that outcome could
		// not be durably recorded. Never leave a new runtime state active
		// without a durable commit backing it: roll back the runtime to
		// what was live before, fail closed, and leave Staging exactly as
		// Store.Update left it on failure (unchanged, still unresolved) so
		// a restart's Recover can act on it.
		if rollbackErr := e.adapter.RollbackRuntimeConfig(ctx, rollbackTarget); rollbackErr != nil {
			return "", "", fmt.Errorf("remoteconfig: publish failed (%v) and rollback also failed (%v); staging left unresolved", saveErr, rollbackErr)
		}
		return "", "", fmt.Errorf("remoteconfig: publish failed after a successful apply, rolled back runtime: %w", saveErr)
	}
	return ApplyStatusApplied, "", nil
}

// previousKnownGood returns the config the runtime should be rolled back
// to: the config that is (or was about to be replaced as) currently
// applied takes priority, since that is what is actually live right now.
// PreviousKnownGoodConfig is only a fallback for when nothing is currently
// applied (e.g. the very first apply ever fails). The zero Config means
// nothing was ever applied.
func previousKnownGood(st State) Config {
	if st.AppliedConfig != nil {
		return *st.AppliedConfig
	}
	if st.PreviousKnownGoodConfig != nil {
		return *st.PreviousKnownGoodConfig
	}
	return Config{}
}
