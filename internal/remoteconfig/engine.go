package remoteconfig

import (
	"context"
	"fmt"
)

// Error codes reported alongside a terminal ApplyStatus.
const (
	ErrCodeStaleVersion     = "STALE_VERSION"
	ErrCodeVersionConflict  = "VERSION_CONFLICT"
	ErrCodePreviouslyFailed = "PREVIOUSLY_FAILED"
	ErrCodeValidationFailed = "VALIDATION_FAILED"
	ErrCodeApplyFailed      = "APPLY_FAILED"
)

// Engine sequences the O5-O8 apply lifecycle (validate -> stage -> apply ->
// verify -> publish/rollback) around a caller-supplied RuntimeAdapter, on
// top of a durable Store. It never polls the network itself -- see Module
// for the outbound poll loop that feeds it.
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
// rolls back to the previous known-good config and records the interrupted
// version as failed. Must be called once at startup before any
// ReceiveDesired call. A restart never retries the interrupted version
// automatically -- a later ReceiveDesired with the identical version and
// content is treated as "previously failed", not reattempted.
func (e *Engine) Recover(ctx context.Context) error {
	st := e.store.Get()
	if st.Staging == nil {
		return nil
	}

	interrupted := *st.Staging
	previous := previousKnownGood(st)

	rollbackErr := e.adapter.RollbackRuntimeConfig(ctx, previous)
	_, err := e.store.Update(func(s State) State {
		s.Staging = nil
		s.LastFailedVersion = interrupted.Version
		s.LastFailedConfig = &interrupted
		s.LastApplyAt = nowRFC3339()
		s.RollbackCount++
		if rollbackErr != nil {
			s.LastApplyStatus = ApplyStatusFailed
			s.LastErrorSafe = sanitizeApplyError(fmt.Errorf("recovery: interrupted apply, rollback failed: %w", rollbackErr))
		} else {
			s.LastApplyStatus = ApplyStatusRolledBack
			s.LastErrorSafe = "recovery: apply interrupted by restart, rolled back to previous known-good"
		}
		return s
	})
	return err
}

// ReceiveDesired processes one desired config polled from SaaS, running it
// through the full O1-O8 lifecycle, and returns the terminal ApplyStatus
// plus an error code to report back to SaaS (empty on success).
func (e *Engine) ReceiveDesired(ctx context.Context, desired Config) (ApplyStatus, string, error) {
	st := e.store.Get()

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

	// 4. apply via the runtime adapter.
	applyErr := e.adapter.ApplyRuntimeConfig(ctx, desired)

	// 5. verify: the adapter's own error return is the only health signal
	// this core engine has -- a richer probe belongs to IA2's adapter.
	if applyErr != nil {
		previous := previousKnownGood(staged)
		rollbackErr := e.adapter.RollbackRuntimeConfig(ctx, previous)

		finalStatus := ApplyStatusRolledBack
		errMsg := sanitizeApplyError(applyErr)
		if rollbackErr != nil {
			finalStatus = ApplyStatusFailed
			errMsg = sanitizeApplyError(fmt.Errorf("apply failed (%v); rollback also failed: %w", applyErr, rollbackErr))
		}

		if _, saveErr := e.store.Update(func(s State) State {
			s.Staging = nil
			s.LastFailedVersion = desired.Version
			cfg := desired
			s.LastFailedConfig = &cfg
			s.LastApplyStatus = finalStatus
			s.LastApplyAt = nowRFC3339()
			s.LastErrorSafe = errMsg
			s.RollbackCount++
			return s
		}); saveErr != nil {
			return "", "", saveErr
		}
		return finalStatus, ErrCodeApplyFailed, nil
	}

	// 6. publish current. 7. mark applied.
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
		return "", "", saveErr
	}
	return ApplyStatusApplied, "", nil
}

// previousKnownGood returns the config the runtime should be rolled back
// to: the recorded previous-known-good if there is one, else the
// currently-applied config, else the zero Config (nothing was ever
// applied).
func previousKnownGood(st State) Config {
	if st.PreviousKnownGoodConfig != nil {
		return *st.PreviousKnownGoodConfig
	}
	if st.AppliedConfig != nil {
		return *st.AppliedConfig
	}
	return Config{}
}
