// Package remoteconfig is the Edge-side engine for SaaS-assigned remote
// configuration (Hito O): versioning, validation, atomic staged apply,
// rollback to the previous known-good config, and restart recovery.
//
// This package owns the lifecycle only. It never interprets the config
// payload itself -- FPS, resolution, ROI, model selection, processing mode
// and any other runtime knob belong to a RuntimeAdapter implementation
// supplied by the caller (IA2). Payload here is an opaque json.RawMessage.
//
// Retrieval reuses the same outbound-only, Edge-initiated poll model as
// internal/control (Hito L): no inbound port, no WebSocket, no second
// control channel. Persistence reuses the temp-file-then-rename atomic
// write pattern already used by internal/control's ledger and
// internal/cameracreds.
package remoteconfig

import (
	"encoding/json"
)

// Config is the versioned desired-configuration document. Version must be
// a monotonically comparable integer assigned by SaaS; Payload is opaque
// and validated/applied only by the RuntimeAdapter.
type Config struct {
	Version int64           `json:"version"`
	Payload json.RawMessage `json:"payload"`
}

// Equal reports whether two configs carry the same version and
// byte-for-byte payload. Used to detect the idempotent case (same
// version, same content) versus a same-version conflict (same version,
// divergent content).
func (c Config) Equal(other Config) bool {
	if c.Version != other.Version {
		return false
	}
	return string(canonicalize(c.Payload)) == string(canonicalize(other.Payload))
}

// canonicalize re-marshals a JSON payload through json.Unmarshal/Marshal so
// that semantically identical documents with different whitespace or key
// order compare equal. Malformed input is returned unchanged: Equal then
// falls back to plain byte comparison, which never spuriously reports a
// match for invalid JSON.
func canonicalize(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return raw
	}
	out, err := json.Marshal(v)
	if err != nil {
		return raw
	}
	return out
}

// ApplyStatus is the lifecycle status of the last apply attempt.
type ApplyStatus string

const (
	ApplyStatusPending    ApplyStatus = "pending"
	ApplyStatusApplying   ApplyStatus = "applying"
	ApplyStatusApplied    ApplyStatus = "applied"
	ApplyStatusFailed     ApplyStatus = "failed"
	ApplyStatusRolledBack ApplyStatus = "rolled_back"
)
