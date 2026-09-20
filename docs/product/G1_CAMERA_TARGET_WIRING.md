# G1 — Camera Target Wiring: audit, contracts and implementation design

> **STATUS: G1-A (preconditions) + G1-B (wiring) IMPLEMENTED / TESTED LOCAL.**
> G1 is closed at the code level: the production Agent now actually calls
> `rtsp.Manager.SetTargets` with real, discovered, credentialed cameras. See
> §11 for exactly what that does and does not mean — **REAL CAMERA and
> DVR/NVR stay NOT_VALIDATED**, no physical pilot has run, and Software 1.0
> readiness still depends on the other B2–B12 blockers. Sections 1–10 below
> are G1-A's original audit and design; they are left as written because
> every decision they made is still the one G1-B implemented.

Branch: `feature/hito-z-camera-target-wiring`, stacked on
`product/hito-z-commercial-modes` @ `16efdfe`. G1-B itself is
`feature/hito-z-camera-target-wiring-g1b`, stacked on G1-A
@ `4894316d4b63104e72b234801784672cafb56417`.

## 1. The gap, restated with evidence

`rtsp.Manager.SetTargets` has **no production call site**. Its only non-test
callers are `internal/perf/scale.go:178` and `internal/perf/decode.go:690`. The
agent constructs the manager (`internal/agent/agent.go:185`) and exposes it
(`agent.go:330`) but never feeds it, so every profile supervises **zero**
cameras: `/status.cameras` is absent and heartbeats carry an empty list.

## 2. What already works (do NOT rebuild any of this)

- **`rtsp.Manager.SetTargets`** already reconciles add / remove / address / path /
  credential change, and restarts only the affected supervisor
  (`internal/rtsp/manager.go:148-194`). Removal calls `sup.Stop()`; a change to
  `Addr`, `RTSPPath`, `Username` or `Password` replaces that one supervisor.
- **The RTSP → processing link already exists and needs no new code.**
  `processing.Manager.Start` registers itself as the RTSP `PacketSink`
  (`internal/processing/manager.go:116`), and `OnPacket` creates a pipeline
  lazily from `rtsp.DescriptorFor(candidateKey)` (`manager.go:166-206`). So
  calling `SetTargets` is *sufficient* to make the path non-empty. Proven today
  by the untagged, green `internal/perf/harness_test.go:218`
  (`TestDecodePipelineHarnessSmoke`, which calls the real `SetTargets` against
  `rtsptest.NewSimulator`).
- **`rtsp.ParseTarget`** (`internal/rtsp/auth.go:20-37`) already splits an RTSP
  URI into `addr` + `path`, discards userinfo, and **rejects any scheme other
  than `rtsp`** — so `rtsps://` correctly yields no target.
- **Discovery already produces per-profile `StreamURI` and `Role`**
  (`internal/discovery/engine.go:307-329`), sanitized on ingest.
- **`cameracreds`** is complete and unused: `LoadOrCreateMasterKey`,
  `OpenStore`, `NewProvider`→`Resolve(candidateKey) (Credential, bool)`,
  `NewSyncer` (`*transport.Client` satisfies its `Fetcher`), `NewModule`.
- **Test fixtures**: `rtsptest.NewSimulator` (real RTSP/RTP, Digest auth,
  `CutStream`), `onviftest.NewSimulator` + `Device{RequireAuth, Username,
  Password, Profiles}` (real WS-Security PasswordDigest verification), and
  `internal/cameratest/onvif_media_chain_test.go:23` as a worked
  Provider→authenticated-ONVIF example.

## 3. The contract that dictates the design

**`CameraTarget.CandidateKey` must be `discovery.DiscoveredDevice.StableIdentity`.**
This is not a choice:

- `cameracreds.Credential.CandidateKeys` is documented as "the
  stable_identity/group-id strings this credential applies to — never an IP
  address" (`internal/cameracreds/types.go:30-34`).
- `Provider.Resolve(candidateKey)` keys credentials on exactly that string, in
  both scopes (`provider.go`).
- `CandidateKey` is simultaneously the RTSP supervisor map key
  (`manager.go:22,155`), the processing pipeline map key
  (`processing/manager.go:172,196`), the `PacketSink.OnPacket` first argument
  (`supervisor.go:304`), and the health `cameras[].candidate_key`.

Inventing a new key would break credential matching and every downstream
consumer. So: **do not invent one.**

**Consequence for multichannel.** Identity exists **per device only** — there is
no per-channel identity (`VideoSource` has only `SourceToken`; nothing links a
profile to a source). Worse, the engine collapses every profile onto
`VideoSources[0].Profiles` regardless of channel count
(`internal/discovery/engine.go:328-338`), so channel structure cannot be
recovered. Two channels under one `CandidateKey` would collapse into one
supervisor, one pipeline and one status row — a silent data-loss bug. Therefore:

- **G1 supports single-source devices only.**
- **DVR/NVR/multi-source devices are NOT_VALIDATED and skipped**, with a safe
  diagnostic. Channels are never collapsed under one key.
- Z2 already documents DVR/NVR as NOT_VALIDATED; G1 does not have to solve it.

**The real SaaS wire shape (confirmed, not assumed).** Auditing
`monitoreoia` (`geocam/routers/camera_credentials.py`, table
`camera_credentials`) established the exact payload, and the Edge was wrong on
two points that together made every sync impossible:

```json
{"credentials": [{"id": 42, "name": "Camara Entrada", "scope": "device",
                  "username": "admin", "password": "...", "revision": 2,
                  "candidate_keys": ["a1b2c3..."]}]}
```

- **`id` is a NUMBER** (`BIGSERIAL`). The Edge declared `ID string`, so decoding
  a numeric id failed and `FetchCameraCredentials` returned a decode error on
  every call. The transport payload is now `int64`, converted once to the
  canonical decimal string at the boundary.
- **`scope` is LOWERCASE** (`device` | `group`, matching the DB column). The
  Edge compared against `DEVICE`/`GROUP`, so `validate()` would reject every
  entry. Normalization now happens in exactly one place (`parseScope`), and any
  other value rejects the whole payload instead of caching an uninterpretable
  credential.
- There is **no `revoked` field** on the wire; the old field was dead and has
  been removed.
- A GROUP credential carries the **real per-camera candidate keys** of its
  assigned cameras — not a group id.

**GROUP credentials resolve by candidate key, not by a group id.**
The SaaS resolves DEVICE-over-GROUP precedence server-side and sends genuine
candidate keys for both scopes. `Provider.Resolve` therefore takes only a
candidate key and tries DEVICE first, then GROUP. There is no group identifier
for the Edge to supply, and none is invented.


## 4. Defects found by the audit, and their disposition

Each was found during the audit. D-A was initially flagged and then reclassified
as NOT A DEFECT once the SaaS contract was read; D-B, D-C and D-D were real and
are fixed in G1-A.

**D-A — NOT A DEFECT. Reclassified after auditing the SaaS contract.**
`Store.Apply` drops any cached entry absent from `incoming`
(`internal/cameracreds/store.go`). This was initially flagged as destructive,
but the SaaS contract is explicit: `GET /api/v1/gateway/camera-credentials` is a
**full authoritative snapshot**, so a successful `{"credentials":[]}` legitimately
means "this gateway has no active credentials", and revocation is expressed by
**omission** (the endpoint never emits a revoked entry).

Retaining absent entries would resurrect revoked camera access — a security
regression. There is therefore deliberately **no empty-snapshot protection**.
Last-good-cache behaviour applies only to fetch and payload failures, which is
where the Syncer enforces it. Verified against monitoreoia
`docs/saas/19-camera-credentials.md`, `geocam/routers/camera_credentials.py`
(`sync_camera_credentials`) and the `status='revoked'` soft-delete semantics
covered by `test_revoke_removes_from_sync`.

**D-B — FIXED (G1-A). `Inventory.List()` does not purge expired devices.**
Eviction runs only inside `Upsert` (`types.go:126,193-200`); `List()`, `Get()`
and `Count()` have no TTL check. Reconciliation would keep feeding targets for
devices that stopped being seen up to 24h ago. Fixed by adding the deterministic,
tested `Inventory.PruneExpired(now) int`, called at the end of every successful
`Engine.RunScan` (including a scan that found nothing). `DeviceTTL` is
unchanged, so a single multicast miss never removes a device.

**D-C — FIXED (G1-A). `rtsp.ParseTarget` silently drops the URI query string.**
It returns `u.EscapedPath()` (`auth.go:36`) and discards `u.RawQuery`. Many ONVIF
`StreamURI`s carry `?channel=1&subtype=0`; dropping it produces a target that
connects to the wrong resource or fails. Fixed by preserving the query in
`ParseTarget`, so `rtsp://10.0.0.20:554/stream?channel=1&subtype=0` round-trips
into `addr=10.0.0.20:554` and `path=/stream?channel=1&subtype=0`. `rtsps://`
stays rejected — the client dials plain TCP and implements no TLS.

**D-D — FIXED (G1-A). Exceeding `MaxConcurrentPipelines` is completely silent.**
`processing.Manager.OnPacket` drops excess cameras with no log, no counter and no
status row (`processing/manager.go:178-181`); the documented `skipped_limit`
state is never set. With the default of 4, a 5–10 camera site silently runs 4
cameras and `/status` shows no hint. Fixed with a bounded diagnostic: an
`skipped_limit` counter (unbounded count), a capped set of at most 32 distinct
`skipped_limit_keys`, the configured `pipeline_limit`, and a one-line log per
newly-seen key — no per-frame log storm, no unbounded map. The default of 4 is
deliberately unchanged; the 5-10 camera pilot must configure it explicitly.

## 5. Design

**One pure function plus one thin reconciler.** No new packages, no second
discovery loop, no second credential store, no second RTSP client.

```
discovery.Module (scan success callback) ──┐
                                           ├─> reconciler.Reconcile(ctx) ─> rtsp.Manager.SetTargets
cameracreds.Module (sync success callback) ┘
```

**Target builder (pure, exhaustively unit-testable).** In `internal/agent`:

```go
func buildCameraTargets(
    devices []discovery.DiscoveredDevice,
    resolve func(stableIdentity string) (cameracreds.Credential, bool),
    opts    targetOptions,           // StreamRole, logger
) (targets []rtsp.CameraTarget, skipped []skipReason)
```

Deterministic rules, in order, per device:

1. Skip when `AuthRequired` and no credential resolves. Never guess, never try
   defaults, never block the other cameras.
2. Skip multi-source devices (`len(VideoSources) > 1`) as multichannel
   (NOT_VALIDATED), with a diagnostic. Never collapse channels.
3. Select exactly one usable profile from the single source, deterministically:
   the first profile (sorted by `Token`) whose `Role` matches
   `cfg.StreamRole`; else, if exactly one profile has a usable URI, that one;
   else skip with a diagnostic. No map-order dependence, no fabricated target.
4. `rtsp.ParseTarget(profile.StreamURI)`; skip unless the scheme is `rtsp`;
   skip if `Addr` is empty. Never log the raw URI.
5. Credentials from `resolve(stableIdentity)`; `StreamRole` recorded from the
   selected profile's actual role; `Codec`/`Width`/`Height`/`FPS` from the
   profile. No hardcoded `/stream1`, `554`, `admin`, `H264` or FPS.

**Reconciliation is event-driven.** Add an optional success callback to
`discovery.ModuleOptions` and to `cameracreds.Module`; a nil callback preserves
exactly today's behaviour. After either fires, rebuild and call `SetTargets`.
No additional polling loop. Callbacks must be invoked **outside** the modules'
internal locks.

**Authenticated ONVIF enrichment lives inside `internal/discovery`.** The
`*onvif.Client` is private with no getter (`engine.go:17-24`), and the
fail-closed `ValidateXAddr`/SSRF guards are unexported — enriching from
`internal/agent` would risk bypassing them. Add an optional
`CredentialResolver func(stableIdentity string) (username, password string, ok bool)`
and, for `AuthRequired` devices, use the existing `*Auth` methods
(`wssecurity.go:65-133`). Preserve every existing guard; never relax security to
make a camera work. Note there is **no `GetVideoSourcesAuth`** — a gap for
authenticated multichannel, which is another reason multichannel stays
NOT_VALIDATED.

## 6. Implementation order

1. `internal/discovery`: `ListActive()` (D-B); optional scan-success callback;
   optional credential resolver + authenticated enrichment.
2. `internal/cameracreds`: sync-success callback. (D-A is closed — the
   snapshot semantics are correct as they stand; do NOT add empty-snapshot
   protection.)
3. `internal/rtsp`: preserve the query string in `ParseTarget` (D-C) with a test.
4. `internal/processing`: log/count the admission cap (D-D).
5. `internal/agent`: `buildCameraTargets` + reconciler; retain the
   `*discovery.Module` and `*cameracreds.Store` on `Agent`; wire
   `LoadOrCreateMasterKey`/`OpenStore`/`NewProvider`/`NewSyncer`/`NewModule`
   (module only when enrolled, so an unenrolled Edge still raises its local
   surface); call reconcile once at startup and from both callbacks.
6. Tests, docs, CI.

## 7. Test plan (mapped to the brief's A–J, reusing existing fixtures)

| # | Test | Fixture |
|---|---|---|
| A | inventory + profile → target: addr/path/metadata correct, no secret in output | pure builder + synthetic `DiscoveredDevice` |
| B | DEVICE beats GROUP; username/password reach the target; never in logs/status/errors | `cameracreds` provider tests + a `syncBuffer` slog capture |
| C | scan discovers camera → `SetTargets` receives it → supervisor appears | `rtsptest.Simulator` + fake discovery module |
| D | device expires → target removed → supervisor stopped | `ListActive` + `SetTargets` |
| E | credential rotation → only that supervisor replaced, no duplicate | `TestY5_...RecoversAfterCredentialUpdate` pattern |
| F | missing credentials → no panic, no guessed password, explicit behaviour | pure builder |
| G | anonymous 401 → resolved credential → WS-Security path → sanitized URI | `onviftest.NewSimulator(Device{RequireAuth:true})` |
| H | discovery → target → RTSP → packets → pipeline active | `rtsptest.Simulator`; assert `video_pipeline.camera_count` ≥ 1 and `frames_received/frames_decoded/frames_sampled > 0` |
| I | `-race` on agent/discovery/cameracreds/rtsp/processing | — |
| J | add/remove/rotate at `-count=10` | — |

Plus a dedicated secret-leak regression asserting that no password, RTSP URI
userinfo, or raw SaaS credential payload reaches a log, `/status`, heartbeat or
error.

## 8. Risks to handle during implementation

- **Lock-cycle hazard — FIXED (G1-A).** `SetTargets` used to hold `rtsp.mu`
  while calling `sup.Stop()`, while a supervisor goroutine could be inside
  `processing.Manager.OnPacket` holding `processing.mu` and calling
  `rtsp.DescriptorFor` — a cycle reachable in the first-packet window. `SetTargets`
  now computes the diff under `mu`, mutates the map, releases `mu`, and only then
  performs the blocking stop/start. A separate `reconcileMu` serializes whole
  reconciliations so concurrent calls cannot interleave and cannot create two
  supervisors for one candidate key. Covered by a race/stress test.

- **Still true for the wiring (G1-B):** reconciliation callbacks must not run
  under a module's internal lock, and `SetTargets` must never be called from
  inside `OnPacket`.
- **Secret leaks.** `Credential` has no `String()`/`MarshalJSON`, so `%+v` or
  `json.Marshal` prints the password verbatim; `Store.Snapshot()` returns full
  credentials; `transport.CameraCredentialPayload.Password` is plaintext.
  Log only counts and classes.
- **An existing test encodes the pre-G1 state.**
  `internal/agent/failure_lifecycle_test.go:540-546` asserts that
  `camera_credentials.json` and `camera_master.key` are **not** created by an
  agent run. Wiring `cameracreds` will break it. That assertion must be updated
  deliberately — with the reason recorded — not deleted quietly.
- **`SetTargets` after `Stop`** silently creates supervisors against a cancelled
  context; do not reconcile during shutdown.
- **Non-credential target fields are ignored on re-declaration**
  (`manager.go:179-190`): a `Codec`/`Width`/`Height`/`FPS`/`StreamRole` change
  alone will not propagate. Do not rely on it.

## 9. What must NOT change

Do not mark real-camera validation, DVR/NVR validation, hardware certification,
a completed physical pilot, commercial readiness, or Software 1.0 READY. Do not
touch IA2 / PR #84 (B1 is closed there only at final integration). Do not
change `DeviceTTL`, do not add default credentials, do not relax the XAddr/SSRF
guards, and do not create a second camera stack.

## 10. G1-A closure status (preconditions only)

G1-A resolved the contract and lifecycle preconditions. **It did not implement
the wiring, and G1 is still BLOCKED.**

| Item | Status |
| --- | --- |
| D-A destructive-snapshot semantics | **RECLASSIFIED — NOT A DEFECT** (authoritative snapshot confirmed against SaaS) |
| Real SaaS wire shape (numeric `id`, lowercase `scope`, no `revoked`, GROUP candidate keys) | **FIXED** — boundary corrected, with a real-HTTP JSON test |
| D-B inventory TTL | **FIXED** — `PruneExpired` + prune on every successful scan |
| D-C RTSP query preservation | **FIXED** — query preserved; `rtsps://` still rejected |
| D-D admission-ceiling observability | **FIXED** — bounded `skipped_limit` + keys + `pipeline_limit` |
| GROUP resolution by candidate key | **FIXED** — `Resolve(candidateKey)`, DEVICE over GROUP |
| `rtsp.Manager.SetTargets` lock cycle | **FIXED** — diff under `mu`, blocking stop/start outside it, `reconcileMu` serializes |
| `rtsp.Manager.Stop` lock cycle | **FIXED** — marks `stopping` under `mu`, detaches supervisors, then stops them outside `mu`; serialized against `SetTargets` by `reconcileMu`; shutdown is terminal |
| `discovery` → `rtsp.Manager.SetTargets` production wiring | **NOT IMPLEMENTED** — this is G1-B |
| Authenticated ONVIF enrichment | **NOT IMPLEMENTED** — G1-B |
| Scan/sync success callbacks | **NOT IMPLEMENTED** — G1-B |
| `internal/agent/failure_lifecycle_test.go` (asserts no credential files exist) | **UNCHANGED** — its update belongs to G1-B, when the agent lifecycle actually changes |

Not claimed: G1 closed, camera wiring complete, real camera validated, pilot
complete, hardware certified, commercial-ready, Software 1.0 READY.

============================================================
## 11. G1-B — implementation status (this PR)
============================================================

Branch: `feature/hito-z-camera-target-wiring-g1b`
Base: `feature/hito-z-camera-target-wiring` @ `4894316d4b63104e72b234801784672cafb56417` (G1-A, PR #85)

### What changed, by section of this document

- **§1 Camera credential lifecycle in Agent** — `internal/agent/cameracreds_module.go`
  (`newCameraCredsModule`) wires `cameracreds.LoadOrCreateMasterKey` /
  `OpenStore` / `NewProvider` / `NewSyncer` / `NewModule` into the production
  Agent, gated on SaaS URL configured + enrolled + DeviceID + credential
  present. `New()` does no network I/O (`OpenStore` only reads the local
  encrypted cache). A corrupt master key or cache is fail-closed: reported as
  `Agent.cameraCredsErr`, never regenerated/deleted, health-http keeps
  serving. Status: **IMPLEMENTED / TESTED**
  (`TestG1B_CorruptCameraMasterKeyNeverRegeneratedAndHealthStaysUp`,
  `TestG1B_CorruptCameraCredentialsCacheNeverDeleted`).
- **§2 Module order** — `newCameraCredsModule`/`newDiscoveryModule` are still
  *constructed* early (their variables are needed by later wiring), but are
  only *appended* to `agent.go`'s `mods` slice after the RTSP/video pipeline
  block, so `moduleManager` (start in order, stop in reverse — unchanged,
  see `modules.go`) starts RTSP+processing before either can drive a
  reconciliation, and stops both before RTSP tears down. Status:
  **IMPLEMENTED** (structural; exercised indirectly by every test that runs
  `Agent.Run` end to end without deadlocking).
- **§3 Discovery scan-success callback** — `discovery.ModuleOptions.OnScanSuccess`
  (`internal/discovery/module.go`), invoked only via the new
  `executeScanAndNotify` wrapper that `Rediscover`, `periodicScanLoop`, and
  `saasPullLoop` all call instead of `executeScan` directly — one
  implementation, not three. Fires after `RunScan` succeeds (including 0
  devices), outside `scanMu`/`m.mu`. Status: **IMPLEMENTED / TESTED**
  (`TestG1B_FullPipeline_LateCredentialConvergence`; existing discovery
  package tests unaffected).
- **§4 Camera-creds sync-success callback** — `cameracreds.SyncOptions.OnSuccess`
  (`internal/cameracreds/sync.go`), invoked at the end of a successful
  `Sync` (after `Store.Apply`), never on a transport/auth/payload/persist
  failure, never under Store's lock, and even when nothing changed. Status:
  **IMPLEMENTED / TESTED** (`TestSyncer_OnSuccessFiresAfterApply`,
  `TestSyncer_OnSuccessNeverFiresOnFailure`).
- **§5 Authenticated ONVIF** — `internal/discovery/engine.go`'s
  `enrichSingleDevice` now checks `errors.Is(err, onvif.ErrAuthRequired)`
  (dropped the old `"401"`/`"403"` substring check now that the sentinel
  exists) and, when a `CredentialResolver` is set
  (`Engine.SetCredentialResolver`, wired from `internal/agent` over
  `cameracreds.Provider.Resolve`), retries the full enrichment via
  `tryAuthenticatedEnrich` using exclusively the existing
  `GetDeviceInformationAuth` / `GetCapabilitiesAuth` / `GetProfilesAuth` /
  `GetStreamUriAuth`. `GetVideoSourcesAuth` did not exist and was added
  (`internal/discovery/onvif/wssecurity.go`), reusing `GetVideoSources`'s
  own parser (factored out as `parseVideoSourcesResponse`) so establishing
  single-source cardinality on an authenticated device needs no new
  parsing. No credential, or the authenticated retry itself failing, leaves
  `AuthRequired=true` with no profiles — same as before, never guesses
  `admin`/`admin`, never fails the rest of the scan. Status:
  **IMPLEMENTED / TESTED** (`TestG1B_FullPipeline_LateCredentialConvergence`
  drives this against a fake ONVIF WS-Security responder; existing
  `internal/discovery/onvif` and `internal/cameratest` WS-Security
  coverage — digest correctness, no-secret-in-logs, wrong-password
  rejection — is unchanged and still the authority for the *auth
  primitive's* correctness. This PR's own coverage is the *wiring*: that
  the passive scan path now reaches those primitives at all).
- **§6 Eventual convergence creds ↔ discovery** — solved in
  `internal/agent/camera_target_reconciler.go`
  (`cameraTargetReconciler.onCredentialsSynced`): every credential sync
  success reconciles immediately (cheap, handles rotation/revoke for
  already-enriched devices) and additionally triggers a **one-shot**
  background `disc.Rediscover(ctx)` — never a second discovery loop — but
  only when Inventory holds an auth-required device with **zero**
  VideoSources (never successfully authenticated-enriched) that a
  credential now resolves for. Deliberately checked as "zero", not "not
  exactly one": a genuinely multichannel authenticated device would never
  satisfy "== 1" and would otherwise re-trigger rediscovery forever. An
  `atomic.Bool` guard prevents stacking overlapping catch-up rediscoveries.
  Status: **IMPLEMENTED / TESTED**
  (`TestG1B_FullPipeline_LateCredentialConvergence`, phase 2: credential
  arrives after the first scan already recorded the device as
  auth-required with no profiles; convergence happens without any Agent
  restart).
- **§7–§9 Pure target builder** — `internal/agent/camera_target_builder.go`
  (`buildCameraTargets` + `selectProfile`), exactly the pure/testable
  function this document specified: `CandidateKey` is always
  `DiscoveredDevice.StableIdentity`, output sorted deterministically,
  single-source-only, profile selection prefers the configured
  `StreamRole`, falls back to a lone usable profile, and refuses (skips
  with a safe diagnostic) when multiple usable profiles disagree. Status:
  **IMPLEMENTED / TESTED** — 13 unit tests in
  `internal/agent/camera_target_builder_test.go` cover every branch
  (single-source, main/sub selection, single-profile fallback, ambiguous
  profiles, no usable profile, query-string preservation, invalid
  `rtsps://`, multichannel, credential resolution, auth-required without a
  credential, no-auth-required without a credential, never-hardcodes
  defaults, deterministic ordering).
- **§10 Reconciler** — `cameraTargetReconciler.reconcile()`: Inventory
  snapshot → `Provider.Resolve` → `buildCameraTargets` →
  `rtsp.Manager.SetTargets`. No second mutable copy of the targets, no lock
  of its own around `SetTargets` (already idempotent/serialized/
  shutdown-safe — unmodified). Status: **IMPLEMENTED / TESTED**.

### Additional required coverage (§14)

| Case | Status | Test |
| --- | --- | --- |
| A. Pure builder | TESTED | `camera_target_builder_test.go` (13 cases) |
| B. Credentials (DEVICE/GROUP precedence, password in memory only) | TESTED | pre-existing `internal/cameracreds` suite (unchanged); DEVICE-wins precedence is G1-A's own contract, not reopened |
| C. No creds → no target, others still reconcile | TESTED | `TestBuildCameraTargets_AuthRequiredNoCredentialSkipped`; multi-device case implicit in `buildCameraTargets`'s per-device loop (one skip never aborts the rest) |
| D. Auth ONVIF (anon 401 → resolve → WS-Security success → sanitized StreamURI → target) | TESTED | `TestG1B_FullPipeline_LateCredentialConvergence` |
| E. Late credential (no restart) | TESTED | `TestG1B_FullPipeline_LateCredentialConvergence`, phase 2 |
| F. Add | TESTED | same, phase 3 (`KnownCameras` goes 0 → 1) |
| G. Remove (inventory expiry → scan success → reconcile) | IMPLEMENTED, NOT_VALIDATED by a dedicated TTL test | `Inventory.PruneExpired` is pre-existing and unit-tested in `internal/discovery`; this PR did not add a new expiry-driven removal test on top of the reconciler specifically |
| H. Rotation (same key, new secret → one restart, others untouched) | TESTED | `TestG1B_FullPipeline_LateCredentialConvergence`, phase 4 |
| I. Revoke | TESTED | same, phase 5 (`KnownCameras` goes 1 → 0) |
| J. RTSP → processing | TESTED at the RTSP/Supervisor layer | same test, via a real `internal/rtsptest.Simulator` (`StateOnline`, `PacketsReceived > 0`). **Not** re-verified through `processing.Manager`/YOLO in this PR — that wiring (`rtsp.SetPacketSink(processing.Manager)`, `OnPacket → DescriptorFor → lazy pipeline`) is unmodified by G1-B and already covered by `internal/processing`'s own suite; this PR did not add a new test threading a G1-B-built target all the way through the vision pipeline. |
| K. Multichannel | TESTED | `TestBuildCameraTargets_MultichannelSkipped` |
| L. Secret leak | TESTED for the paths this PR touches | `TestGetDeviceInformationAuth_*` (`onvif` package, pre-existing, unchanged) covers the WS-Security request; `TestG1B_Corrupt*` assert the sanitized `cameraCredsErr` never contains the enrollment credential. No new assertion was added scanning `/status`/heartbeat payloads specifically for a *camera* credential, since neither surface serializes `CameraTarget` or `cameracreds.Credential` anywhere (verified by reading, not by a new test). |
| M. Shutdown/race | TESTED | `go test -race` clean across `internal/agent`, `internal/discovery` (+ `onvif`, `wsdiscovery`), `internal/cameracreds`, `internal/rtsp`, `internal/processing`; `-race -count=10` clean on `internal/agent`, `internal/rtsp`, `internal/processing` |

### Sensitivity (§15)

One representative, executed sensitivity check: temporarily disabling the
authenticated-retry branch in `enrichSingleDevice` (§5) makes
`TestG1B_FullPipeline_LateCredentialConvergence` fail exactly as expected
(times out waiting for the target to appear, phase 2) — confirmed, then the
change was reverted. The remaining guards listed in §15 were not each
individually revert-tested; the full-pipeline test's five phases each
depend on a different piece of the wiring (scan callback, sync callback,
authenticated ONVIF, rotation, revoke), so breaking any one of them is very
likely to fail that same test, but that likelihood was not empirically
confirmed guard-by-guard the way the one executed check above was.

### Tests (§16)

```
gofmt -l .                    → clean
go vet ./...                  → clean
go build ./...                → clean
go test ./...                 → ok, 33/33 packages
go test -race ./internal/agent ./internal/discovery ./internal/discovery/onvif \
  ./internal/discovery/wsdiscovery ./internal/cameracreds ./internal/rtsp \
  ./internal/processing        → ok, no data races
go test -race ./internal/agent ./internal/rtsp ./internal/processing -count=10
                               → ok
```

### G1 final status

- **G1: IMPLEMENTED / TESTED** (local; not merged, not deployed).
- **REAL CAMERA: NOT_VALIDATED** — every test above runs against fakes/simulators.
- **DVR/NVR: NOT_VALIDATED** — unchanged from G1-A; still explicitly out of scope.
- **PHYSICAL PILOT: NOT EXECUTED.**
- **HARDWARE CERTIFIED: NO.**
- **SOFTWARE 1.0: STILL BLOCKED** by the remaining non-G1 blockers (B2–B12).

No merge, no deploy, no tag, no SaaS change.
