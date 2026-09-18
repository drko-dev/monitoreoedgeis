# Hito O — Remote Config Core (O1-O9)

Edge-side engine for SaaS-assigned remote configuration: versioning,
validation, atomic staged apply, rollback to the previous known-good
config, and restart recovery. Package: `internal/remoteconfig`.

This slice (IA1) owns the lifecycle only. It never interprets the config
payload itself — FPS, resolution, ROI, model selection, processing mode
and any other runtime knob are IA2's responsibility, connected through
`remoteconfig.RuntimeAdapter`. Today the agent wires a
`remoteconfig.NoopRuntimeAdapter{}` (validates OK, applies nothing) so the
whole engine — sync, version, persist, apply lifecycle, rollback, status —
runs and is testable before IA2 lands.

**`internal/agent/remoteconfig_module.go`'s `NoopRuntimeAdapter{}` is a
temporary seam only.** This PR must not be merged individually to `main`
with it as the production runtime — `integration/hito-o-final` (IA2) must
replace it with the real `RuntimeAdapter` before that integration branch
merges.

## Architecture reused, not duplicated (no second poller)

`remoteconfig.Module` owns **no poll loop of its own**. It exposes
`Recover(ctx)`, `SyncOnce(ctx)`, and `Status()` — a sync component, not an
independent `Start`/`Stop` agent module. It is driven entirely by
`internal/control`'s existing outbound poll cadence (Hito L):

```
SaaS: desired config changes
  → SaaS queues an allowlisted "reload_config" control command
  → Edge control.Module claims it on its own existing poll loop
  → controlExecutor.ReloadConfig(ctx)          (internal/agent/control_module.go)
  → remoteConfigModule.SyncOnce(ctx)           (internal/remoteconfig/module.go)
      → GET  /api/v1/edge/remote-config/next
      → Engine.ReceiveDesired(...)
      → POST /api/v1/edge/remote-config/ack
  → control command reports succeeded/failed ("RELOAD_CONFIG_FAILED") back to SaaS
```

The two-endpoint split is deliberate and unchanged: `GET .../remote-config/next`
/ `POST .../remote-config/ack` carry the versioned config **document**,
while `reload_config` is the allowlisted, empty-payload **trigger** riding
the control channel — the same separation `internal/control.ControlCommand`
already enforces everywhere else (no arbitrary payload, no second command
queue). `restart_video_pipeline` remains `UNSUPPORTED`, unaffected.

**Dependency on IA3/SaaS for final integration**: SaaS must queue
`reload_config` whenever a device's desired remote config changes (new
version assigned, or a previously-failed version corrected). Without that,
Edge has no trigger to sync — there is intentionally no independent timer
polling `/remote-config/next` on its own.

Persistence reuses the temp-file-then-rename atomic-write pattern already
used by `internal/control.Ledger` and `internal/cameracreds`: a crash
mid-write never leaves a partial file, and (as of this fix) `Store.Update`
persists the candidate state *before* committing it to memory — a failed
write leaves the in-memory state completely unchanged.

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
| `staging` is non-nil (unresolved prior attempt) | rejected, `STAGING_UNRESOLVED`; fail closed, no apply attempted at all |
| `version < applied_version` | rejected, `STALE_VERSION`; current untouched |
| `version == applied_version`, same content | idempotent, reports `applied` again, no re-apply |
| `version == applied_version`, different content | `VERSION_CONFLICT`, fail-closed |
| `version == last_failed_version`, same content | `PREVIOUSLY_FAILED`, not reattempted (no infinite retry loop) |
| `version == last_failed_version`, different content | `VERSION_CONFLICT`, fail-closed (a version number is never reused for different content) |
| `version > applied_version`, new | proceeds to validate → apply |

## Persistence (`GEOCAM_DATA_DIR/remote_config_state.json`)

`remoteconfig.State`, atomically written via `internal/remoteconfig.Store`:

- `applied_version` / `applied_config` — the current, live config.
- `previous_known_good_version` / `previous_known_good_config` — an older
  historical snapshot, used only as a fallback (see rollback target below).
- `staging` — set only while an apply is in flight; a non-nil `staging` on
  disk after a restart means the process crashed mid-apply, **and blocks
  every new apply attempt** until `Recover` resolves it.
- `last_failed_version` / `last_failed_config` — the last version that
  failed validation or apply, used to detect and stop retry loops.
- `last_apply_status` (`pending` / `applying` / `applied` / `failed` / `rolled_back`),
  `last_apply_at`, `last_error_safe`, `rollback_count`, `received_at`.

`Store.Update`/`Store.Save` are transactional: they build the candidate
state, persist it durably first, and only then commit it to the in-memory
copy. A write failure leaves memory exactly as it was — the engine can
never believe a version is applied without a durable commit backing it.

Never persisted here: passwords, Bearer tokens, device credentials, RTSP
credentials (those live in their own storage —
`internal/credentials`, `internal/cameracreds`). A remote-config `Payload`
must never carry them in the first place; this store does not attempt to
scrub an opaque payload it does not understand.

## Rollback target: always the current applied config

The rollback target for any failed apply is **the config that was actually
live immediately before the failed attempt began** — `AppliedConfig` —
never an older `PreviousKnownGoodConfig` snapshot. Example: v1 applied,
then v2 applied, then v3 fails — rollback must go to v2, not v1.
`PreviousKnownGoodConfig` is only a fallback for the case where nothing is
currently applied (e.g. the very first apply ever fails).

## Apply lifecycle (O6) — fail-closed at every step

1. Reject outright if `staging` is already non-nil (unresolved prior
   attempt) — see the version-model table above.
2. `RuntimeAdapter.ValidateRuntimeConfig` — on error, state and current
   config are untouched; only `last_failed_*` records the rejection.
3. Write `staging` atomically — the restart-safe marker that an apply is
   about to begin. Captures the rollback target (current `AppliedConfig`)
   at this point.
4. `RuntimeAdapter.ApplyRuntimeConfig`.
5. Verify: today the adapter's own error return is the only health signal
   this core engine has; a richer live-health probe is IA2's to add inside
   its adapter.
6. On success: **persist** the promotion (old `applied_config` →
   `previous_known_good_config`, new `applied_config`/`applied_version`,
   clear `staging`, mark `applied`) *before* reporting success.
   - If that persistence itself fails, the runtime already changed but the
     outcome could not be made durable: the engine calls
     `RollbackRuntimeConfig` back to what was live before, returns a
     non-nil error, and leaves `staging` exactly as it was (still
     unresolved) — the caller must not ACK `applied`.
7. On apply failure: call `RollbackRuntimeConfig` with the current applied
   config.
   - Rollback succeeds → clear `staging`, mark `rolled_back`, increment
     `rollback_count`.
   - **Rollback itself fails → `staging` is deliberately left in place**
     (never cleared), the engine returns a non-nil error, and every
     subsequent `ReceiveDesired` call fails closed with
     `STAGING_UNRESOLVED` until a restart's `Recover` retries the
     rollback.

A config is never left partially applied, and the engine never silently
forgets that the runtime's real state is uncertain.

## Restart / recovery (O8)

`Engine.Recover`, called once before syncing starts: if the persisted
`staging` is non-nil, the process crashed (or a prior rollback failed)
between step 3 and a terminal outcome. Recovery calls
`RollbackRuntimeConfig` against the current applied config (same priority
rule as above — never an older snapshot), clears `staging` only on
success, and records the interrupted version as `last_failed_version` — so
a later sync offering the identical interrupted version+content is treated
as `PREVIOUSLY_FAILED` (no automatic retry), while a corrected config under
a new version number proceeds normally once `staging` is clear. If the
recovery rollback itself fails, `staging` stays unresolved and `Recover`
returns a non-nil error — the next restart tries again.

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
sync wiring, control-channel integration, transport, persistence, or
`/status` wiring should be needed.
