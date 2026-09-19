# GEO CAM Edge — Security Event Logging / Audit (Hito S, S11)

Scope: which security-sensitive events the Edge logs today, what they
contain, what they never contain, and the honest distinction between
operational logging and a durable, tamper-evident audit trail. Reuses
`log/slog` exclusively — **no second logging stack was created.**

```
EDGE SECURITY EVENT LOGGING:      PARTIAL
DURABLE / TAMPER-EVIDENT AUDIT:   NOT IMPLEMENTED
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
