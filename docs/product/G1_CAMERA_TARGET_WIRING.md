# G1 — Camera Target Wiring: audit, contracts and implementation design

> **STATUS: AUDIT COMPLETE · IMPLEMENTATION NOT STARTED · G1 STILL BLOCKED.**
> This document is the persisted audit for the G1 slice (blocker B1). It exists
> so implementation can proceed from the repository alone. It contains **no
> implemented wiring**, and nothing here may be read as G1 being closed. The G1
> row in `docs/product/COMMERCIAL_MODES.md` stays `BLOCKED / NOT IMPLEMENTED`
> until the production flow is implemented *and* tested.

Branch: `feature/hito-z-camera-target-wiring`, stacked on
`product/hito-z-commercial-modes` @ `16efdfe`.

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
  `OpenStore`, `NewProvider`→`Resolve(stableIdentity, groupID) (Credential, bool)`,
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
- `Provider.Resolve(stableIdentity, groupID)` keys credentials on exactly that
  string (`provider.go:24-38`).
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

**GROUP credentials are unreachable.** `Resolve` matches `ScopeGroup` by
`groupID`, and discovery carries no group assignment for a device. G1 therefore
resolves **DEVICE scope only** — call `Resolve(stableIdentity, "")`. This must be
documented, not papered over with a fabricated group id.

## 4. Four real defects G1 must fix (beyond the wiring itself)

These were found during the audit and each one breaks a hard requirement in the
slice brief.

**D-A — `Store.Apply` is destructively snapshot-based.**
`Apply` rebuilds the cache from `incoming` alone and **drops any cached entry
whose ID is absent** (`internal/cameracreds/store.go:111-153`). A perfectly valid
`{"credentials":[]}` 200 response therefore **wipes every cached credential**,
and a well-formed but *shortened* list silently drops the missing entries.
This violates "nunca borrar/crear credenciales por error" and "preservar último
cache bueno si SaaS falla". Fix required in `internal/cameracreds`: refuse to
apply an empty payload, and treat a shortening payload as a diagnostic rather
than a silent mass revocation (exact policy to be decided and tested; it is a
SaaS-contract question, so it must be explicit).

**D-B — `Inventory.List()` does not purge expired devices.**
Eviction runs only inside `Upsert` (`types.go:126,193-200`); `List()`, `Get()`
and `Count()` have no TTL check. Reconciliation would keep feeding targets for
devices that stopped being seen up to 24h ago. Fix: add a narrow, tested
TTL-respecting accessor (e.g. `ListActive()`), and **do not change `DeviceTTL`**
(`security.go:31`).

**D-C — `rtsp.ParseTarget` silently drops the URI query string.**
It returns `u.EscapedPath()` (`auth.go:36`) and discards `u.RawQuery`. Many ONVIF
`StreamURI`s carry `?channel=1&subtype=0`; dropping it produces a target that
connects to the wrong resource or fails. Either preserve the query (a genuine
bug fix, and the correct behaviour) or refuse such targets with a diagnostic —
but never emit a silently-wrong target.

**D-D — exceeding `MaxConcurrentPipelines` is completely silent.**
`processing.Manager.OnPacket` drops excess cameras with no log, no counter and no
status row (`processing/manager.go:178-181`); the documented `skipped_limit`
state is never set. With the default of 4, a 5–10 camera site silently runs 4
cameras and `/status` shows no hint. At minimum this must be logged/counted so a
G1 integration test can assert it.

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
2. `internal/cameracreds`: non-destructive `Apply` policy (D-A); sync-success
   callback.
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

- **Lock-cycle hazard.** `SetTargets` holds `rtsp.mu` while calling `sup.Stop()`,
  and a supervisor goroutine can simultaneously be inside
  `processing.Manager.OnPacket`, which holds `processing.mu` and calls
  `rtsp.DescriptorFor`. Adding a callback that calls `SetTargets` widens the
  window. Reconcile from a path that holds **no** rtsp/processing lock, and
  never call `SetTargets` from inside `OnPacket`.
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
