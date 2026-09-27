# GEO CAM Edge — Security Event Logging / Audit (Hito S, S11, S11A)

Scope: which security-sensitive events the Edge logs today, what they
contain, what they never contain, and the honest distinction between
operational logging and a durable, tamper-evident audit trail. Reuses
`log/slog` exclusively for operational logging — **no second logging stack
was created** for that. S11A adds a *separate*, purpose-built durable audit
journal (`internal/auditjournal`) for security-sensitive events specifically
— see "S11A — durable local audit journal" below.

```
EDGE SECURITY EVENT LOGGING:          PARTIAL (slog, Hito S11)
LOCAL DURABLE HASH-CHAINED AUDIT:     IMPLEMENTED (Hito S11A)
LOCAL TAMPER EVIDENCE:                IMPLEMENTED (Hito S11A)
REMOTE IMMUTABLE RETENTION:           NOT IMPLEMENTED
EXTERNAL CRYPTOGRAPHIC ANCHOR:        NOT IMPLEMENTED (no TPM/HSM/transparency log)
ROOT COMPROMISE PROTECTION:           NOT CLAIMED
```

## Events covered, and by what

| Event | Logged? | Where |
|---|---|---|
| Enrollment success/failure | **Yes** (added this hito) | `cmd/geocam-edge/main.go` (`runEnrollCmd`) |
| Credential rotation success/failure | **Yes** (added this hito) | `cmd/geocam-edge/main.go` (`runCredentialRotateCmd`) |
| Factory reset | **Yes** (added this hito) | `cmd/geocam-edge/main.go` (`runFactoryResetCmd`) |
| Control command execution | **Yes** (added this hito) | `internal/control/module.go` |
| Config apply/rollback (remote config) | **Yes** (pre-existing, Hito N) | `internal/remoteconfig/adapter.go`, `module.go` |
| Revocation / auth failure (401/403 on heartbeat) | **Yes** (pre-existing, Hito D) | `internal/heartbeat/heartbeat.go` — `slog.Error("heartbeat rejected: credential revoked or device disabled...")` |

Before this hito, enrollment/credential-rotation/factory-reset/control-
command execution were **not** logged via structured `slog` at all — only
human-readable text to stdout/stderr from the CLI, which journald still
captures as unstructured lines with no explicit `action`/`result` fields.
This hito closes that specific gap by adding `slog` calls at each of
those four sites, reusing the exact same `log/slog` the rest of the
codebase already uses (`internal/heartbeat`, `internal/remoteconfig`) —
no new logging library, no new log format, no new sink.

## What every added log line contains, and never contains

Every new log line carries: a fixed event description, a timestamp
(added automatically by the `slog.Handler`, never manually formatted),
and non-secret identifiers — `edge_id`, `device_id`, `tenant_id`,
`site_id`, `rotation_id`, `command_id`, `command_type`, `data_dir` (a
path), `new_credential_version` (an integer), and a `status`/`error_code`
or sanitized error string.

**Never logged, anywhere in this repo, verified by inspection**: the raw
enrollment token, the device credential (old or new, at enroll or at
rotate), the RTSP camera password, the camera credential
(`internal/cameracreds` encrypts it before disk and it is never logged in
plaintext), or an `Authorization` header value. `saasErrorMessage()` — the
existing, already-tested error-formatting helper reused verbatim by the
new log calls — was not modified in this hito.

Covered by `internal/control/audit_log_test.go`
(`TestExecuteCommandLogsOutcomeWithoutLeakingCredential`), which asserts
the log line for a dispatched command never contains the device
credential passed to the control module.

## Operational logging vs. durable audit — the real distinction

**What exists today is operational logging**: `slog` output flows to
stdout/stderr, which systemd/journald captures. This is useful for
debugging, alerting, and forensic review of *what the process did*, but
it has none of the properties a durable security audit trail needs:

- **Not append-only / tamper-evident.** journald's own rotation and
  retention are host-configured, not cryptographically chained; nothing
  in this repo detects or prevents a log line from being deleted or
  edited after the fact.
- **Not centrally retained by this repo's own mechanism.** Whether these
  log lines survive a reboot, a factory reset, or a disk failure depends
  entirely on the host's journald/syslog configuration — outside this
  repo's control.
- **Not distinct from ordinary operational noise.** These security
  events are `slog.Info`/`slog.Error` calls exactly like any other log
  line in the process; there is no separate, higher-integrity channel or
  format that marks them as security-relevant to a downstream collector.

None of that is being built in this hito. A `DURABLE / TAMPER-EVIDENT
AUDIT` implementation (append-only storage, integrity hashing/chaining,
retention independent of the local host, and a channel a compromised host
cannot silently disable) is real, future work — not claimed as done here.

### The control-plane command ledger is not an audit log either

`internal/control.Ledger` persists command execution outcomes to a local
JSON file under `GEOCAM_DATA_DIR`, bounded to the most recent 100 entries
(oldest evicted). Its purpose is **idempotency/retry-safety** — answering
"did I already execute this command" across restarts and retries — not
security audit. It has no tamper-evidence (a local file, editable like any
other), no unbounded retention, and is not a substitute for the durable
audit ledger described above. This distinction matters: without stating
it explicitly, the ledger's existence could be mistaken for an audit trail
it was never designed to be.

## Edge ↔ SaaS relationship (not duplicated here)

The SaaS (`monitoreoia`) already has its own `log_audit` system. This
hito does not duplicate it, extend it, or modify it — `monitoreoia` is a
separate repository with its own lifecycle (see `AGENTS.md`). The
relationship, stated for completeness: SaaS-side actions the SaaS itself
performs (enrollment-token issuance/reissue/revocation, RBAC changes,
onboarding state transitions) are that system's own audit concern, not
this repo's. Edge-side events documented above are local to the Edge
process and are not currently forwarded into the SaaS's `log_audit` —
no such forwarding exists in this repo, and none was added.

## Factory reset and this hito's new state

This hito adds no new local security-state file (only `slog` calls, which
produce no new on-disk file of their own — output goes to
stdout/stderr/journald, not to a file under `GEOCAM_DATA_DIR`). Factory
reset's existing allowlist (`internal/factoryreset`) is therefore
unchanged — there is nothing new for it to evaluate deleting.

## S11A — durable local audit journal

`internal/auditjournal` adds a durable, local, hash-chained, append-only
security audit journal, separate from `slog` and from
`internal/control.Ledger` (see above — the ledger is idempotency/retry-safety,
never audit).

```
LOCAL DURABLE HASH-CHAINED AUDIT:   IMPLEMENTED
LOCAL TAMPER EVIDENCE:              IMPLEMENTED
REMOTE IMMUTABLE RETENTION:         NOT IMPLEMENTED
EXTERNAL CRYPTOGRAPHIC ANCHOR:      NOT IMPLEMENTED
ROOT COMPROMISE PROTECTION:         NOT CLAIMED
```

### Threat model (honest statement)

This journal provides **local tamper evidence**: it can detect that the
on-disk journal was modified, truncated, reordered, or had a record deleted
relative to the chain this process itself wrote. It does **not** protect
against an attacker with full root/host compromise, who could rewrite
history and recompute a self-consistent chain from scratch — a hash chain
proves internal consistency, not authorship. There is no remote immutable
retention, no external cryptographic anchor (TPM/HSM/transparency log), and
no signature. See `docs/security/threat-model.md`.

### Record contract

One JSON line per record, canonical field order (Go struct marshaling,
never a map), UTC RFC3339Nano timestamp, bounded scalar fields only —
`schema_version`, `sequence`, `timestamp`, `event_type`, `result`, `edge_id`,
`device_id`, `command_id`, `rotation_id`, `config_version`, `safe_reason`,
`prev_hash`, `record_hash`. No arbitrary map, no raw payload. `safe_reason`
is redacted (password/secret/token/auth/bearer/credential/rtsp-shaped
fragments) and truncated before it is ever hashed or written — callers
cannot opt out.

`record_hash = SHA256(prev_hash || canonical_json(record without record_hash))`.
The first record's `prev_hash` is the explicit genesis value `genesis:v1`,
never ambiguous with a real 64-hex-char digest.

### Durability and concurrency

Directory `0700`, file `0600`, single append-only file
(`$GEOCAM_DATA_DIR/audit/security-audit.jsonl`), each `Append` does a
`Write` + `Sync` (fsync) before returning success. A single mutex serializes
every append end to end (sequence assignment, hashing, write, fsync) —
security audit events are low-volume by design, so a global lock is the
deliberately simple, obviously-correct choice (see `BenchmarkAppend` for
measured cost at 100/1,000/10,000 appends).

### Restart and corruption

On open, the journal replays existing records to recover the last valid
sequence/hash so a new append continues the chain rather than restarting
it. A verifier distinguishes:

- **PASS** — full valid chain.
- **EMPTY** — journal does not exist yet, or has zero records.
- **CORRUPT** — a modified/deleted/reordered record, bad `prev_hash`,
  bad `record_hash`, duplicate/skipped sequence, or malformed JSON not at
  the very end of the file. `Open` refuses to start appending onto a
  corrupt chain.
- **TRUNCATED_LAST_WRITE** — every record up to the last one is valid, and
  only the final line is incomplete — consistent with a crash mid-append,
  not tampering. The valid prefix is recovered and appending continues;
  reported as `AUDIT_DEGRADED`, not `AUDIT_CORRUPT`.

Health states (`AUDIT_HEALTHY` / `AUDIT_DEGRADED` / `AUDIT_CORRUPT` /
`AUDIT_WRITE_FAILED`) are safe to expose as-is.

### CLI

```
geocam-edge audit verify   # walks the full chain, exit code non-zero on CORRUPT
geocam-edge audit status   # cheap in-memory snapshot, no full re-scan
```

Both are read-only. There is no `audit edit`/`delete`/`reset` — append-only
means exactly that.

### Events integrated in this hito

- `ENROLLMENT_SUCCESS`/`ENROLLMENT_FAILURE`,
  `CREDENTIAL_ROTATION_SUCCESS`/`CREDENTIAL_ROTATION_FAILURE`,
  `FACTORY_RESET_REQUESTED`/`FACTORY_RESET_COMPLETED`/`FACTORY_RESET_FAILED`
  — wired into `cmd/geocam-edge/main.go` (enroll, credential rotate,
  factory-reset).
- `CONTROL_COMMAND_RECEIVED`, `CONTROL_COMMAND_EXECUTED`, `CONTROL_COMMAND_FAILED`
  — wired into `internal/control/module.go` upon command claim, success, and
  failure/invalidation. Replay lookups do not re-emit executed events.
- `REMOTE_CONFIG_APPLY`, `REMOTE_CONFIG_ROLLBACK`, `REMOTE_CONFIG_FAILURE`
  — wired into `internal/remoteconfig/engine.go` upon actual completed apply,
  crash/apply recovery rollback, and configuration/validation failures. Routine
  idempotent polls do not re-emit apply events.
- `AUTH_REJECTED`
  — wired into `internal/heartbeat/heartbeat.go` upon receiving 401/403.
  Deduplicated across streaks so rejected heartbeat loops emit exactly once
  per unauthorized episode.
- `DEVICE_REVOKED`
  — **NOT_DISTINGUISHABLE**: `transport.ErrUnauthorized` covers all 401/403
  responses; the Edge runtime cannot distinguish revocation from general
  credential rejection. The audit trail records `AUTH_REJECTED` honestly
  without inventing unprovable revocation distinctions.

A failure to open or write to the audit journal never blocks main runtime
operations; failures are logged via `slog` and surfaced by `audit status`.

### Factory reset

`internal/factoryreset.StatePaths` does not include `audit/` — a factory
reset never deletes the audit journal. No code change to `reset.go` was
needed for that property to hold.

### Performance

Security audit events are low-volume. `BenchmarkAppend` measures real
append cost (write + fsync) at 100/1,000/10,000 records — see the package
for current numbers on this hardware; no full-journal scan happens on the
append hot path (only on `audit verify`/on `Open`/on recovery).
