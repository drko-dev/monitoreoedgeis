// Package auditjournal implements a durable, local, hash-chained,
// append-only security audit journal for the Edge agent.
//
// Threat model (see docs/security/threat-model.md for the full statement):
// this package provides LOCAL TAMPER EVIDENCE — it can detect that the
// on-disk journal was modified, truncated, reordered, or had records deleted
// relative to the chain this process itself wrote. It does NOT provide:
//
//   - protection against an attacker with full root/host compromise, who
//     could rewrite history and recompute a self-consistent chain from
//     scratch (a hash chain proves internal consistency, not authorship —
//     that requires a signature or an external anchor, neither of which
//     this package implements);
//   - remote immutable retention (nothing is shipped off-device);
//   - an external cryptographic anchor (no TPM/HSM/transparency log).
//
// None of those properties are claimed by this package. They are explicitly
// out of scope for this milestone (S11A) — see docs/security/audit.md.
package auditjournal

// SchemaVersion is the current audit record schema. Bump it whenever the
// canonical field set changes, so a verifier reading an old journal can
// tell "old schema" apart from "corrupt".
const SchemaVersion = 1

// EventType enumerates the security-sensitive events this journal records.
// Only events the runtime can honestly distinguish today are defined here.
type EventType string

const (
	EventEnrollmentSuccess         EventType = "ENROLLMENT_SUCCESS"
	EventEnrollmentFailure         EventType = "ENROLLMENT_FAILURE"
	EventCredentialRotationSuccess EventType = "CREDENTIAL_ROTATION_SUCCESS"
	EventCredentialRotationFailure EventType = "CREDENTIAL_ROTATION_FAILURE"
	EventFactoryResetRequested     EventType = "FACTORY_RESET_REQUESTED"
	EventFactoryResetCompleted     EventType = "FACTORY_RESET_COMPLETED"
	EventFactoryResetFailed        EventType = "FACTORY_RESET_FAILED"
	EventControlCommandReceived    EventType = "CONTROL_COMMAND_RECEIVED"
	EventControlCommandExecuted    EventType = "CONTROL_COMMAND_EXECUTED"
	EventControlCommandFailed      EventType = "CONTROL_COMMAND_FAILED"
	EventRemoteConfigApply         EventType = "REMOTE_CONFIG_APPLY"
	EventRemoteConfigRollback      EventType = "REMOTE_CONFIG_ROLLBACK"
	EventRemoteConfigFailure       EventType = "REMOTE_CONFIG_FAILURE"
	EventAuthRejected              EventType = "AUTH_REJECTED"
	EventDeviceRevoked             EventType = "DEVICE_REVOKED"
)

// Result is the terminal outcome of the event being recorded.
type Result string

const (
	ResultSuccess Result = "success"
	ResultFailure Result = "failure"
)

// Record is one canonical audit journal entry. Field order is fixed by
// struct declaration order — encoding/json marshals struct fields
// deterministically (unlike map keys), which is what makes canonicalization
// deterministic without a custom encoder. Every field is a bounded scalar;
// there is deliberately no arbitrary map/payload field, so a caller cannot
// smuggle an unbounded or secret-shaped blob into the journal.
type Record struct {
	SchemaVersion int       `json:"schema_version"`
	Sequence      uint64    `json:"sequence"`
	Timestamp     string    `json:"timestamp"` // RFC3339Nano, UTC — set by the journal, never by the caller
	EventType     EventType `json:"event_type"`
	Result        Result    `json:"result"`
	EdgeID        string    `json:"edge_id,omitempty"`
	DeviceID      string    `json:"device_id,omitempty"`
	CommandID     string    `json:"command_id,omitempty"`
	RotationID    string    `json:"rotation_id,omitempty"`
	ConfigVersion string    `json:"config_version,omitempty"`
	SafeReason    string    `json:"safe_reason,omitempty"`
	PrevHash      string    `json:"prev_hash"`
	RecordHash    string    `json:"record_hash"`
}
