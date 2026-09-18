# Hito O — Remote Config Core (O1-O9)

Edge-side engine for SaaS-assigned remote configuration: versioning,
validation, atomic staged apply, rollback to the previous known-good
config, and restart recovery. Package: `internal/remoteconfig`.

This slice (IA1) owns the lifecycle only. It never interprets the config
payload itself — FPS, resolution, ROI, model selection, processing mode
and any other runtime knob are IA2's responsibility, connected through
`remoteconfig.RuntimeAdapter`. Today the agent wires a
`remoteconfig.NoopRuntimeAdapter{}` (validates OK, applies nothing) so the
whole engine — poll, version, persist, apply lifecycle, rollback, status —
runs and is testable before IA2 lands.

## Architecture reused, not duplicated

Retrieval reuses the exact same outbound-only, Edge-initiated poll model
as `internal/control` (Hito L, see `docs/CONTROL_CHANNEL.md`): the Edge
polls `GET /api/v1/edge/remote-config/next` on its own schedule and ACKs
via `POST /api/v1/edge/remote-config/ack`
(`internal/transport/remoteconfig.go`). No inbound port, no WebSocket, no
second control channel — a second, purpose-built `remoteconfig.Module`
poller alongside `control.Module`, `heartbeat`, and `discovery`, exactly
the multi-module pattern `internal/agent` already uses for every other
Edge-initiated channel.

Persistence reuses the temp-file-then-rename atomic-write pattern already
used by `internal/control.Ledger` and `internal/cameracreds`: a crash
mid-write never leaves a partial file.

## Version model

```go
type Config struct {
    Version int64           // monotonically comparable, assigned by SaaS
    Payload json.RawMessage // opaque; IA2's RuntimeAdapter interprets it
}
```

`Config.Equal` compares by re-marshaled (canonicalized) JSON, not raw
bytes, so semantically identical payloads with different whitespace/key
order still compare equal — this matters because the persisted state is
itself JSON-encoded, and a save/reload round trip can reformat nested
`json.RawMessage` content.

`Engine.ReceiveDesired` resolves every desired config against the
persisted state:

| Desired vs. state | Outcome |
|---|---|
| `version < applied_version` | rejected, `STALE_VERSION`; current untouched |
| `version == applied_version`, same content | idempotent, reports `applied` again, no re-apply |
| `version == applied_version`, different content | `VERSION_CONFLICT`, fail-closed |
| `version == last_failed_version`, same content | `PREVIOUSLY_FAILED`, not reattempted (no infinite retry loop) |
| `version == last_failed_version`, different content | `VERSION_CONFLICT`, fail-closed (a version number is never reused for different content) |
| `version > applied_version`, new | proceeds to validate → apply |

## Persistence (`GEOCAM_DATA_DIR/remote_config_state.json`)

`remoteconfig.State`, atomically written via `internal/remoteconfig.Store`:

- `applied_version` / `applied_config` — the current, live config.
- `previous_known_good_version` / `previous_known_good_config` — rollback target.
- `staging` — set only while an apply is in flight; a non-nil `staging` on
  disk after a restart means the process crashed mid-apply.
- `last_failed_version` / `last_failed_config` — the last version that
  failed validation or apply, used to detect and stop retry loops.
- `last_apply_status` (`pending` / `applying` / `applied` / `failed` / `rolled_back`),
  `last_apply_at`, `last_error_safe`, `rollback_count`, `received_at`.

Never persisted here: passwords, Bearer tokens, device credentials, RTSP
credentials (those live in their own storage —
`internal/credentials`, `internal/cameracreds`). A remote-config `Payload`
must never carry them in the first place; this store does not attempt to
scrub an opaque payload it does not understand.

## Apply lifecycle (O6)

1. Receive desired config (poll result).
2. `RuntimeAdapter.ValidateRuntimeConfig` — on error, state and current
   config are untouched; only `last_failed_*` records the rejection.
3. Write `staging` atomically — the restart-safe marker that an apply is
   about to begin.
4. `RuntimeAdapter.ApplyRuntimeConfig`.
5. Verify: today the adapter's own error return is the only health signal
   this core engine has; a richer live-health probe is IA2's to add inside
   its adapter.
6. On success: promote `applied_config` → `previous_known_good_config`,
   publish the new `applied_config`/`applied_version`, clear `staging`,
   mark `applied`.
7. On failure: call `RuntimeAdapter.RollbackRuntimeConfig` with the
   previous known-good config, clear `staging`, mark `rolled_back` (or
   `failed` if the rollback call itself also errored), increment
   `rollback_count`.

A config is never left partially applied: every path out of step 3 ends in
either `applied`, `failed`, or `rolled_back`, with `staging` cleared.

## Restart / recovery (O8)

`Engine.Recover`, called once before the poll loop starts: if the
persisted `staging` is non-nil, the process crashed between step 3 and a
terminal outcome. Recovery calls `RollbackRuntimeConfig` defensively
against the previous known-good config, clears `staging`, and records the
interrupted version as `last_failed_version` — so a later poll offering
the identical interrupted version+content is treated as `PREVIOUSLY_FAILED`
(no automatic retry), while a corrected config under a new version number
proceeds normally.

## Status (`/status` → `remote_config`)

```json
{
  "remote_config": {
    "desired_version": 3,
    "applied_version": 2,
    "last_apply_status": "rolled_back",
    "last_apply_at": "2026-09-18T19:40:00Z",
    "last_error_safe": "...",
    "rollback_count": 1
  }
}
```

Never the full config document — payload knobs (ROI coordinates, model
paths) may be operationally sensitive and don't belong on `/status`.
`last_error_safe` is redacted (`internal/remoteconfig/sanitize.go`, a
local, self-contained password/secret/token/bearer scrubber — not shared
with `internal/rtsp`'s own sanitizer, to avoid coupling this package to
the RTSP protocol package for a plain string-safety helper) and truncated
to 255 bytes.

## Interface for IA2

```go
type RuntimeAdapter interface {
    ValidateRuntimeConfig(ctx context.Context, cfg Config) error
    ApplyRuntimeConfig(ctx context.Context, cfg Config) error
    RollbackRuntimeConfig(ctx context.Context, previous Config) error
}
```

IA2 implements this against the real FPS/resolution/ROI/model/
processing-mode knobs and replaces `remoteconfig.NoopRuntimeAdapter{}` in
`internal/agent/remoteconfig_module.go`. No other change to the engine,
poller, transport, persistence, or `/status` wiring should be needed.
