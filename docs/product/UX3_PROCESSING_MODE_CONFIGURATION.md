# UX-3 — Processing Mode / Effective Profile Configuration

**Status:** Implemented / Tested Local — **UX3_STATUS = COMPLETE** (re-verified during Final UX Closure: `internal/installer/processing_mode.go` verifies `ExpectedEffectiveProfile` against `ActualEffectiveProfile`/`snap.Profile` read from the real running daemon's status snapshot — selecting "edge" without the pipeline actually enabled never reports Full Edge)
**Branch:** `feature/ux3-processing-mode`
**Base:** `feature/ux2-secure-enrollment` @ `20c91e9`
**Milestone:** UX-3

## Overview

UX-3 lets an operator choose **Cloud**, **Hybrid**, or **Full Edge** in the
Wails installer, validates that choice against real, observed host facts,
persists it atomically, and verifies afterwards that the configuration on
disk actually says what was requested — never claiming success on a mismatch.

It does **not** implement camera onboarding, DVR/NVR, privileged service
install/restart, or any deploy/merge action. Those are explicitly out of
scope (see "Known gaps" and "UX-4 prerequisites" below).

## Product semantics (verified against real code, not assumed)

Per [`docs/product/COMMERCIAL_MODES.md`](COMMERCIAL_MODES.md) §1, the agent
has exactly three processing modes (`internal/config/mode.go`): `cloud`,
`hybrid`, `edge`. **Gateway is not a fourth mode** — it is the commercial
name for `cloud` + the video pipeline enabled, an *effective profile*
(`internal/config/profile.go`, `config.ProfileFor`) derived from two
independent knobs, not something an operator selects directly.

All three UX-3 product modes always request the pipeline enabled, matching
the confirmed mapping:

| UX product mode | `GEOCAM_PROCESSING_MODE` | `GEOCAM_VIDEO_PIPELINE_ENABLED` | Effective profile (`config.Profile`) |
| ---------------- | ------------------------ | -------------------------------- | ------------------------------------- |
| Cloud             | `cloud`                  | `true`                            | `gateway`                              |
| Hybrid            | `hybrid`                 | `true`                            | `hybrid`                               |
| Full Edge         | `edge`                   | `true`                            | `full-edge`                            |

No discrepancy was found between this expected mapping and the real code —
`config.ProfileFor` already implements exactly this table. `gateway-no-media`
(pipeline disabled) is the shipped default for a fresh install and is never
offered as a selectable UX-3 option, since it is a *derived* profile, not a
request.

## Architecture

```
 Wails frontend                          Go backend (internal/installer)
 ┌────────────────────────┐              ┌──────────────────────────────┐
 │ ProcessingModeSelector  │─ mode ──────▶│ ValidateProcessingMode        │
 │  (3 cards, capability)  │              │  (real facts, no invented    │
 │                         │              │   hardware minimums)         │
 │ ProcessingModeReview    │◀─ plan ──────│ PlanProcessingMode            │
 │  (diff + Apply button)  │              │  (pure, never mutates)       │
 │                         │─ apply ─────▶│ ApplyProcessingMode           │
 │  Success/RestartRequired│◀─ result ────│  atomic write → read back    │
 │  /RolledBack/Blocked    │              │  → rollback on mismatch      │
 └────────────────────────┘              └───────────┬──────────────────┘
                                                       │
                                          config.WritePersistentValues /
                                          RestorePersistentFileRaw
                                          (internal/config/persisted_env.go)
                                                       │
                                          $XDG_CONFIG_HOME/geocam-edge/edge.env
                                          (or $GEOCAM_CONFIG_FILE)
                                                       │
                                          picked up by config.Load() the
                                          next time the Edge daemon starts
```

`GetCurrentProcessingMode` and rehydration never trust frontend state: they
re-derive from the live daemon's `/status` (when reachable) or, failing
that, the persisted config file — matching the same
`withPersistentEnvironment(loadFromEnvironment)` path `config.Load()` itself
uses at boot.

## ConfigService contract (`internal/installer/processing_mode.go`)

- `GetProcessingModeOptions(ctx) ([]ProcessingModeOption, error)` — the three
  modes with real capability.
- `GetCurrentProcessingMode(ctx) (*CurrentProcessingMode, error)` — live if
  the daemon answers `/status`, else the persisted file, else defaults.
- `ValidateProcessingMode(ctx, req) (*ProcessingModeOption, error)`.
- `PlanProcessingMode(ctx, req) (*ProcessingModePlan, error)` — **never
  mutates** (`TestPlanProcessingModeDoesNotMutate` pins this with a
  before/after byte-for-byte comparison of the config file).
- `ApplyProcessingMode(ctx, req) (*ProcessingModeApplyResult, error)` — the
  only mutating call in the whole `installer.Service`.

The frontend can only ever send `{ mode: "cloud" | "hybrid" | "full_edge" }`
(`ProcessingModeRequest`). There is no raw env map, file path, shell command,
or arbitrary config document reachable from React — Go alone decides the two
concrete keys that get written.

## Capability checker (real facts only)

Per `docs/product/COMMERCIAL_MODES.md` §4/§7, no RAM/disk minimum is
established anywhere in this repository, and GPU is never a hard
requirement. The checker therefore only classifies on facts it can actually
observe:

- **Cloud / Hybrid:** platform supported (`SystemReport.PlatformSupported`)
  and `ffmpeg` on `PATH` (both are already required for the local decode
  stage every mode with the pipeline enabled runs). `UNAVAILABLE` with a
  concrete blocker if either is missing; otherwise `SUPPORTED`.
- **Full Edge:** the above, plus — because without them the worker cannot
  infer at all, not because of an invented minimum — `GEOCAM_EDGE_YOLO_WORKER_CMD`
  must be configured and resolvable (`exec.LookPath` or a direct `os.Stat`
  for a path), and both the person and vehicle model files must exist under
  the resolved models directory. Missing either is `UNAVAILABLE`, never a
  soft warning, because Full Edge without them cannot run at all — this
  matches `docs/product/COMMERCIAL_MODES.md`'s own description of
  `model_missing` as an explicit failure state, not a degraded one.
- **CUDA requested but not detected** (`GEOCAM_EDGE_YOLO_DEVICE=cuda` and
  `fulledge.DefaultHardwareDetector.DetectCUDA()` false): `SUPPORTED_WITH_WARNINGS`,
  never `UNAVAILABLE` — "no bloquear Full Edge por ausencia de GPU si el
  runtime real soporta CPU" is enforced by a dedicated test
  (`TestFullEdgeWarnsWhenCudaRequestedButUnavailable`).

On a fresh developer machine with no vision worker configured, Full Edge is
correctly `UNAVAILABLE` — this is the honest, expected result, not a bug.

## Atomic persistence and rollback

`internal/config/persisted_env.go` gained:

- `PersistentFileRaw() (data []byte, existed bool, err error)` — an exact
  byte snapshot for rollback, not a reconstruction from parsed key/value
  pairs (which could reformat operator-authored lines).
- `WritePersistentValues(updates map[string]string) error` — validates every
  key against the existing `persistentConfigKeys` allowlist (secrets and
  unknown keys are rejected, and nothing is written), then does a
  read-merge-write and an atomic temp-file-then-rename (mirroring the exact
  pattern already used by `internal/cameracreds`, `internal/identity`, and
  `internal/credentials`): a crash mid-write can never leave a partial file,
  and a write failure never touches the previous file at all
  (`TestApplyWriteFailureLeavesOldConfigIntact`).
- `RestorePersistentFileRaw(data []byte, existed bool) error` — restores the
  exact prior bytes, or removes the file if it did not exist before.

`ApplyProcessingMode` snapshots before writing, writes both
`GEOCAM_PROCESSING_MODE` and `GEOCAM_VIDEO_PIPELINE_ENABLED` in the *same*
write (so a Full Edge request can never leave `processing_mode=edge` paired
with the pipeline still disabled, or vice versa), then **reads the two keys
back from disk** before ever reporting anything. If the read-back disagrees
with what was requested, the previous file is restored automatically and the
result is `ROLLED_BACK`, never a silent partial state. If the restore itself
fails, the result is `ROLLBACK_FAILED` → `BLOCKED`, with no success claim.

## Effective-profile verification — and its one honest limitation

Verification happens in two layers:

1. **Disk-level (always performed):** after writing, `ApplyProcessingMode`
   re-reads `GEOCAM_PROCESSING_MODE`/`GEOCAM_VIDEO_PIPELINE_ENABLED` from the
   file it just wrote and recomputes the effective profile with the exact
   same `config.ProfileFor` the real daemon uses. This catches write
   corruption, encoding bugs, and concurrent-writer races — genuinely,
   without needing a running daemon.
2. **Live-runtime (best effort):** if a daemon is reachable, its `/status`
   is queried and compared.

**Known, deliberate limitation:** this installer has **no local channel** to
reload or restart an already-running daemon. `internal/health/handler.go`
only exposes `GET /healthz`, `/readyz`, `/operationalz`, `/status` — no
admin/config-write endpoint — and the one runtime hot-swap mechanism that
does exist (`internal/agent/agent.go`'s `RuntimeAdapter`, wired through
`internal/remoteconfig`) is driven by SaaS-pushed remote configuration, not
by anything a local Wails process can invoke on a sibling daemon process.
Consequently:

- **Daemon not running:** the disk write is verified and reported as
  `SUCCESS` — "it will take effect the next time GEO CAM Edge starts." There
  is nothing running to contradict it.
- **Daemon already running:** the new config is still persisted and
  disk-verified (so it will take effect on the next real start), but the
  result is `RESTART_REQUIRED`, `Match: false`, and the live `/status` is
  reported honestly (still the old profile) — never a false `SUCCESS`. This
  is exactly the contract in the original brief's §12: "Si el servicio ya
  corre pero reiniciarlo requiere UAC/root/polkit: devolver
  ACTION_REQUIRED/RESTART_REQUIRED y no fingir éxito."

A safe, local, privileged-or-not restart channel is explicitly **out of
scope** for UX-3 (the brief excludes "instalación privilegiada del
servicio") and is the natural UX-4 prerequisite.

## UI flow

`ProcessingModeSelector.tsx` → `ProcessingModeReview.tsx`, both ephemeral
React state inside `App.tsx` (a `dashboard | select | review` wizard step).
Reopening the app **always** resets to `dashboard` and re-derives the real
current mode via `GetCurrentProcessingMode` — the wizard step itself is not
persisted anywhere (not `localStorage`, not React state across a reload),
because it is UI navigation state, not a fact about the Edge. The mode
itself is never trusted from the frontend: every screen renders only what
the backend just returned.

Selector cards show, per mode: name, description, local compute, network
dependency, inference location, and a capability badge (`Supported on this
device` / `Available with warnings` / `Not available` with its reason).
Full Edge is disabled (unclickable) exactly when `UNAVAILABLE`. The review
screen shows current vs. selected, the concrete config changes (with safe,
human labels — never raw secrets), whether a restart is required, and an
Apply button disabled while an apply is in flight. The result screen never
shows a success state when `match` is false.

## Error taxonomy

`INVALID_MODE`, `CONFIG_INVALID`, `CONFIG_WRITE_FAILED`, `ROLLBACK_FAILED`,
`APPLY_IN_PROGRESS` are returned as `installer.SafeError` (typed, no raw
stack traces, `Recoverable` flag). `ApplyStatus` (`SUCCESS`,
`RESTART_REQUIRED`, `ROLLED_BACK`, `BLOCKED`) is returned as a normal typed
result, not an error, since the frontend needs to render it, not just alert
on it.

## Concurrency and instance-lock safety

- `Service.applyMu sync.Mutex` (`TryLock`) rejects a second concurrent
  `ApplyProcessingMode` with `APPLY_IN_PROGRESS` instead of racing two writes
  (`TestApplyRejectsConcurrentApply`).
- `internal/installer` never imports `internal/instance`. `GetSystemReport`
  always reports `OwnsInstanceLock: false` (`TestSystemReportNeverOwnsInstanceLock`) —
  unchanged from UX-0/UX-1: the Wails installer never competes with the
  daemon for the exclusive instance lock.

## Security

No credential, token, or raw env dump is representable in any DTO
(`ProcessingModeOption`, `*Plan`, `*ApplyResult`) — `ConfigChanges` can only
ever contain `GEOCAM_PROCESSING_MODE`/`GEOCAM_VIDEO_PIPELINE_ENABLED`, both
already in `persistentConfigKeys`, which deliberately excludes every
credential/token setting. There is no `SetEnv(key, value)` or generic
`WriteFile(path, content)` exposed to Wails; the only mutation path is
`ApplyProcessingMode`, itself constrained to exactly two allowlisted keys.

## Tests

**Go** (`internal/installer/processing_mode_test.go`, 16 tests;
`internal/config/persisted_env_test.go`, +4 tests): mode mapping, exactly
three options with no `gateway`, Full Edge unavailable without a worker,
Full Edge supported once worker+models are configured, CUDA-requested
warning (never a block), Plan never mutates, invalid mode rejected without
writing, blocked mode rejected without writing, a full
Cloud→Hybrid→FullEdge→Cloud apply sequence (pipeline always ends up `true`,
expected == actual profile every time), write failure leaves the old config
byte-for-byte intact, concurrent Apply rejected, rehydration from config
when the daemon is down, rehydration from live `/status` when it is up,
restart-required when the daemon is already running an older mode (and the
new config is still persisted for the next real start), and instance-lock
safety. Config-package round trips: write creates/merges/rejects unknown
keys without writing, raw snapshot + restore round-trips exactly.

**Frontend** (`src/types/processingMode.test.ts`, plain `node --test`,
matching this repo's existing test convention — no jsdom/RTL is in this
toolchain): the display-mapping pure functions
(`src/utils/processingModeDisplay.ts`) that both components consume —
`capabilityBadge` for all three `CapabilityStatus` values, `describeConfigKey`,
and the `MODE_LABELS` set. `services/api.ts`'s Wails-binding wrappers are
exercised in the Wails dev/build environment (and indirectly by the Wails
build performed for this milestone), not under `node --test`, for the same
reason `installer.test.ts` never imports a live module either: the generated
bindings are extensionless relative ES module imports, which plain Node ESM
cannot resolve without a bundler.

## Build verification actually performed

- `go build ./...`, `go vet ./...` — repo-wide, before and after the Wails
  build below — both clean.
- `go test ./...` — repo-wide, twice — all green, no regressions.
- `tsc --noEmit`, `npm test` (`node --test`), `vite build` — all green.
- `git diff --check` — clean (one cosmetic whitespace artifact from Wails'
  own generator template was trimmed; see "Known gaps").
- **`wails build -platform darwin/arm64`** — actually run via
  `go run github.com/wailsapp/wails/v2/cmd/wails@v2.16.0 build` (no `wails`
  binary was preinstalled in this environment); it regenerated
  `frontend/wailsjs/go/{models.ts,main/App.{d.ts,js}}` from the real
  `App.go`/`installer` Go types and produced
  `cmd/geocam-edge-ui/build/bin/geocam-edge-ui.app` locally. The
  regenerated bindings matched this document's hand-authored stand-ins
  exactly, which is strong independent confirmation the Go-side contract is
  correct.

## Known gaps

1. **No local restart/reload channel** (see above) — `RESTART_REQUIRED` is
   reported honestly instead of a live reload, and the `ROLLED_BACK` path
   triggered by a genuine post-write read-back mismatch is exercised at the
   `internal/config` round-trip level, not forced end-to-end through
   `ApplyProcessingMode` in a test, since doing so honestly would need a
   dependency-injection seam this milestone's scope does not otherwise call
   for.
2. **Installer state machine left unchanged.** The brief suggested explicit
   `MODE_SELECTED`/`MODE_CONFIGURED` `StateCode` values. `DeriveInstallerState`
   derives state from durable host facts (platform, credentials, enrollment);
   "mode selected but not yet applied" is not a durable fact worth inventing
   persisted state for — it is transient wizard navigation, already handled
   as ephemeral React state that always resets on reopen. `ActionConfigureProcessingMode`
   was added to `StateEnrolled`'s `next_allowed_actions` instead, which is a
   real, durable signal.
3. **`installer.Service.ConfigFilePath`/`checkConfigPresent`** (UX-0/UX-1) use
   a different config-file location (`DataDir/config.env`) than
   `config.PersistentConfigPath()` (`$XDG_CONFIG_HOME/geocam-edge/edge.env`,
   or `$GEOCAM_CONFIG_FILE`) that UX-3 and the real daemon actually use. This
   pre-existing seam was not touched (out of scope for this milestone) but is
   worth reconciling before UX-4.
4. **No CI Python/Wails job** in this repository beyond what already existed;
   this milestone's Wails/tsc/vite/Go verification was run manually, once,
   locally, as this document states.

## UX-4 prerequisites

- A safe, local channel to reload or restart the daemon from the installer
  (with or without privilege elevation), so `RESTART_REQUIRED` can become a
  real live re-verification instead of a persisted-for-next-boot promise.
- Camera onboarding / ONVIF wizard, explicitly out of scope here.
- Reconciling `installer.Service`'s two divergent notions of "the config
  file" (gap 3 above).
