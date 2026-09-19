# Hito W — failure lifecycle tests (W5–W9)

Scope: **W5** loss of Internet / unreachable SaaS, **W6** loss of camera,
**W7** wrong credentials, **W8** reboot / process restart, **W9** upgrade.

Base: `main @ cd7378e8a981ea1a62fb20662a86cb05d8e8f4f6`.
Branch: `test/hito-w-failure-lifecycle`.

This milestone **validates behaviour that already exists**. It does not add an
offline policy, an RTSP state, a credential-retry strategy or an OTA mechanism.
Where a test found a real defect in the existing contract the fix is minimal and
is listed under [Defects found and fixed](#defects-found-and-fixed). Where a
test found a capability that does not exist, it is recorded under
[Gaps that belong to a later milestone](#gaps-that-belong-to-a-later-milestone)
rather than invented here.

## How to run it

```bash
go test ./internal/heartbeat/ -run TestW5 -count=1
go test ./internal/rtsp/      -run 'TestW6|TestW7' -count=10
go test ./internal/agent/     -run 'TestW5|TestW8' -count=1
go test ./internal/edgebacklog/ ./internal/cameracreds/ ./internal/cameratest/ -run 'TestW5|TestW7|TestW8'
go test ./deploy/appliance/   -run TestW9 -count=1
go test -race ./...
```

Every test is hermetic: `httptest.Server` for the SaaS, a test-local RTSP server
bound to `127.0.0.1:0`, `t.TempDir()` for the data directory and the install
root. No test reaches the real Internet, binds a fixed port, requires root,
requires systemd, or depends on execution order.

## W5 — loss of Internet / unreachable SaaS

Files: `internal/heartbeat/saas_outage_test.go`,
`internal/agent/failure_lifecycle_test.go`,
`internal/edgebacklog/saas_outage_test.go`.

The gap these close: the pre-existing scheduler tests all injected a fake
`Sender`, and the pre-existing transport tests each made a single request.
Nothing drove the **real** transport through the **real** scheduler.

| Failure | Observed class | Observed state | Next attempt |
| --- | --- | --- | --- |
| Connection dropped mid-flight | `unreachable` (`ErrSaaSUnavailable`) | `degraded` | jittered exponential 1s→60s |
| Client deadline exceeded | `timeout` (`ErrTimeout`) | `degraded` | jittered exponential |
| 5xx | `server_error` | `degraded` | jittered exponential |
| 401/403 | `unauthorized` (`ErrUnauthorized`) | `unauthorized` | slow poll, `AuthFailureInterval` = 5m |
| Recovery | — | `running`, counters cleared | nominal `Interval` |

Invariants asserted:

- The process keeps running and `/healthz` keeps answering 200 throughout an
  outage. A 5xx outage leaves the agent **READY**; only a 401 degrades it.
- `identity.json` and `credentials.json` are byte-identical before and after an
  outage, and after a 401. Nothing deletes, rotates or rewrites them.
- Zero requests ever reach the enrollment endpoint. Re-enrollment is an operator
  CLI action, not a reaction to a transport failure.
- Every attempt carries the same stored credential verbatim.
- Transient failures back off (`1s, 2s, 4s, …` capped at `MaxBackoff`) rather
  than hammering, and a single success resets the sequence.
- The credential, the `Authorization` header and a response body that echoes
  request material appear in no log line, no status field and no error string.
- Durable queues keep exactly what the design calls durable: a queued local
  event is retained through the outage (not quarantined — an outage is
  transient, `ErrInvalidRequest` is not), is delivered exactly once per stage
  when the SaaS returns, metadata strictly before its evidence, and a repeated
  `Enqueue` of the same pending `event_uuid` does not create a second copy.

## W6 — loss of camera

File: `internal/rtsp/failure_lifecycle_test.go` (with a test-local fake RTSP
server implementing only the Digest/DESCRIBE/SETUP/PLAY subset the production
client negotiates).

| Wire event | `status` | `reconnect_count` | `timeout_count` | `stall_count` |
| --- | --- | --- | --- | --- |
| Peer closes the socket / EOF | `degraded` | ++ | unchanged | unchanged |
| Stream goes silent (read deadline) | `degraded` | ++ | ++ | ++ |
| Dial refused | `degraded` | ++ | unchanged | unchanged |
| Stream resumes | `online` | — | — | — |
| Supervisor stopped | `offline` | — | — | — |

The single most important semantic, and the one most easily misread:
**`offline` is the supervisor's stopped state, never the unreachable-camera
state.** A camera that disappears produces `degraded` (or `connecting` while a
retry is in flight) and the supervisor keeps retrying. A test asserts this
explicitly so a future change to `offline` cannot go unnoticed.

Also asserted: automatic reconnection with no operator action; recovery back to
`online` with `packets_received` advancing and `last_error_safe` cleared;
`Stop()` returns within a bounded time (no stuck supervision goroutine) and
releases the goroutine; the manager creates exactly one supervisor and exactly
one RTSP session per camera no matter how many times targets are re-declared.

## W7 — wrong credentials

Files: `internal/rtsp/failure_lifecycle_test.go`,
`internal/cameratest/credentials_failure_test.go`,
`internal/cameracreds/credentials_failure_test.go`.

Camera / RTSP:

- A wrong password is surfaced as an authentication denial. The error text
  names the 401 and is **not** a timeout, so the two cases are distinguishable
  at the API boundary, and `SanitizeError` scrubs it further.
- Neither the username nor the password appears in `CameraStreamStatus` JSON,
  in `LastErrorSafe`, or in any supervisor log line.
- Retries are backed off exponentially (~60ms, 120ms, 240ms, 480ms in the
  test), never a tight loop.

Camera / ONVIF:

- HTTP 401 and a WS-Security SOAP auth fault both classify as `INVALID`.
- An unreachable device classifies as `UNREACHABLE`, distinctly from `INVALID`
  — that distinction is what lets an operator tell a wrong password from a
  camera that is off.
- Exactly one request is issued per credential test; there is no auth retry
  loop. The password appears in no result field or error.

Edge → SaaS:

- A 401 from the SaaS returns `ErrUnauthorized` after exactly one request, and
  leaves the cached camera credentials untouched both in memory and on disk
  (byte-identical encrypted file, and a reopen from disk still resolves the same
  credential).

## W8 — reboot / process restart

Files: `internal/agent/failure_lifecycle_test.go`,
`internal/edgebacklog/saas_outage_test.go`.

The test is a real two-phase restart in one process: `agent.New` + `Run` →
clean shutdown → `agent.New` + `Run` against the **same** data directory.

| State | Where | Across a restart |
| --- | --- | --- |
| `edge_id` | `<DataDir>/identity.json` | stable, and the file is not rewritten |
| Enrollment credential | `<DataDir>/credentials.json` | stable, byte-identical |
| `boot_id` | in memory, per run | new value every run |
| Heartbeat `sequence_number` | in memory, per run | restarts at 1 |
| `uptime_seconds` | in memory (`Reporter.startedAt`) | resets to ~0 |
| Local-event backlog | `<DataDir>/local-event-backlog/pending/*.json` | survives, no duplication |
| Camera credentials | not wired into the agent runtime | never created |

`edge_id` being stable while `boot_id` changes is the property the SaaS's
stale-snapshot detection depends on: the pair `(boot_id, sequence_number)` is
what disambiguates a restarted Edge from a stale snapshot, so a `boot_id`
derived from the stable `edge_id` would break it.

Also asserted: phase B reaches `READY` promptly (startup does not deadlock);
`Run` returns `nil` on cancellation in both phases; each module appears exactly
once in the reported module map; and durable queue contents are not replayed or
duplicated by the new process.

Not asserted, because it is false today: `identity.json`,
`credentials.json`, `edgebacklog` records and `clips` are written with
tmp+rename but **without `fsync`**, and nothing fsyncs a parent directory.
Surviving a `SIGKILL` is therefore guaranteed; surviving sudden power loss is
not. That belongs to Hito Y.

## W9 — upgrade

Files: `deploy/appliance/ota_upgrade_lifecycle_test.go`.

Hito T already covers each step in isolation: `internal/ota`'s unit tests for
version/signature/checksum/archive binding, and `deploy/appliance`'s tests for
install/update/rollback with stub verifier binaries. The gap this closes is the
**join**: a candidate release signed with a real Ed25519 key, staged in the
layout the daemon produces, verified by the *current* release's real
`geocam-edge ota verify`, and then activated by the privileged updater — plus
the failure paths — all inside a throwaway install root with no root, no
systemd and no network.

Validated:

- `current 1.0.0 → candidate 1.1.0 → SHA256SUMS verified against the Ed25519
  signature → checksum matches → forward-version eligible → VERSION/ARCH bound
  to the release descriptor → activated`: `current → releases/1.1.0`, state
  `succeeded:1.1.0`, apply request consumed to `apply.request.processed`.
- The release upgraded from stays on disk, which is what makes rollback
  possible at all.
- A candidate signed by an **unknown key** is not activated: state
  `failed:verification`, `current` still `releases/1.1.0`, its binary still
  present and executable.
- A candidate whose artifact does not declare its architecture is not
  activated.
- With **no public key configured**, a perfectly valid, validly signed
  candidate is still rejected (`failed:verification`) and `current` is
  unchanged. Ed25519 verification is fail-closed with no checksum-only
  fallback.
- The public key reaches the privileged verifier only through the appliance env
  file, and a test asserts the rendered
  `geocam-edge-ota-updater.service` actually passes that file
  (`EnvironmentFile=`), with no unsubstituted placeholder.
- No private signing key material ships anywhere under `deploy/`, and
  `internal/agent` never references `LoadPrivateKey`/`SignManifest`.
- Rollback refuses safely when there is nothing usable to return to (no
  recorded previous release, or a recorded release whose directory has since
  been removed), and a refusal never disturbs the running release.

## Defects found and fixed

Each was reproduced by a test first, then fixed minimally, with the test kept as
the regression guard.

| # | Where | Defect | Fix |
| --- | --- | --- | --- |
| 1 | `internal/rtsp/supervisor.go` | After a successful session the retry delay was reset to the raw `cfg.InitialBackoff`. With `InitialBackoff` unset (documented default 1s, normalised at loop entry) that reset the delay to **0**, so a flapping camera produced an unthrottled reconnect storm. | Reset to the already-normalised local value. |
| 2 | `internal/rtsp/supervisor.go` | `Config.PacketTimeout` documents a 5s default, and the supervisor substitutes defaults for `InitialBackoff`, `MaxBackoff` and `DialTimeout` — but a non-positive `PacketTimeout` disabled the read deadline entirely, so a camera that stopped sending looked healthy forever. | Substitute the documented default in the read loop. |
| 3 | `internal/rtsp/supervisor.go` | `Supervisor.Start` was unguarded: a second call spawned a second supervision goroutine and both closed `doneChan` on exit (`close of closed channel` panic). `Stop` before `Start` blocked forever on a channel no goroutine would close. | `Start` is idempotent; `Stop` returns immediately when the supervisor was never started. |
| 4 | `internal/rtsp/manager.go` | The agent registers camera targets during construction and calls `Start` afterwards. `SetTargets` skipped `Start` when the manager had no context yet, so those supervisors never ran **and** `Manager.Stop` blocked forever waiting on them; a second `Manager.Start` spawned a second coordination goroutine. | `Manager.Start` starts pre-registered supervisors and is idempotent; `Manager.Stop` on a never-started manager returns instead of blocking. |
| 5 | `deploy/appliance/systemd/geocam-edge-ota-updater.service.in`, `deploy/appliance/scripts/install.sh`, `deploy/appliance/config/geocam-edge.env.example` | The privileged OTA updater unit had no `EnvironmentFile=` and `install.sh` never substituted `@GEOCAM_ENV_FILE@` into it. The root verifier therefore ran with `GEOCAM_OTA_PUBLIC_KEY_FILE` unset, `LoadPublicKey("")` returned `ErrMissingSignature`, and **every real upgrade would end as `failed:verification`** — including correctly signed ones. The variable was also undocumented in the shipped env example. | Wire the env file into the updater unit (optional `-` so the unit still loads), substitute the placeholder in `install.sh`, and document the variable. |

Defect 5 was invisible to the existing tests because every privileged-updater
test injects a shell stub as the verifier; the bug lives precisely in the
handoff between the unit and the real binary.

## Gaps that belong to a later milestone

Recorded, not implemented. Each is a capability the current contract does not
have.

- **RTSP credential rejection has no distinct state or terminal stop.** A wrong
  camera password is indistinguishable from an unreachable camera at the
  supervisor level (both are `degraded` + `reconnect_count++`) and is retried
  forever with exponential backoff. The error text carries the 401, and
  `ErrAuthFailed` is used only for an unparseable challenge, so nothing
  upstream can act on "bad password" specifically. W6/W7 pin the current
  behaviour; adding an auth-specific state or a stop after N rejections is a
  resilience decision (Hito Y), not a test fix.
- **Rollback is not re-verified.** `rollback.sh` requires only that the recorded
  previous release directory exists; it checks no signature, checksum, arch or
  version, which is weaker than `docs/security/update-trust.md` requirement 7.
  Its safety currently rests on the release tree being root-owned. Making
  rollback re-verifiable needs a release layout that carries its own signed
  manifest, so it is a design change.
- **The readiness gate does not run on a staged target.**
  `update.sh` performs the restart + `/readyz` check + automatic rollback only
  when a real Linux target with systemd is detected; otherwise it logs
  "verification skipped" and returns success. Automated coverage of readiness
  therefore relies on the fake-command harness in Hito T's
  `TestUpdateReadinessFailureRollsBackAndPreservesData`, not on a real
  appliance.
- **No `fsync` for most durable files.** Only the cloud buffer, full-edge event
  store, evidence captures, remote-config store and control ledger call
  `Sync()`; `identity.json`, `credentials.json`, backlog records and clips do
  not, and no directory is ever fsynced. Process-crash safe, power-loss
  unsafe. Hito Y.
- **No boot-id guard on durable sequences.** The local-event backlog and the
  cloud buffer resume their counters from the maximum surviving on-disk value
  and restart at 1 once drained, with no `boot_id` attached. Hito Y, if the
  SaaS ever needs cross-boot monotonicity.
- **Dead configuration.** `rtsp.Config.Enabled` is documented as the
  supervision switch but is never read inside `internal/rtsp` (the caller gates
  on `cfg.ConnectivityEnabled`). `rtsp.ErrClosed` and `rtsp.ErrNoVideoTrack` are
  declared but never returned.
- **`internal/rtsptest` is not a fake camera.** It is a one-shot RTSP Digest
  *credential client*. The fake RTSP servers used by the tests are unexported
  per-package fixtures; the W6 tests add one more, test-local by design. If a
  third consumer needs it, extract a shared simulator instead of a third copy.

## Deliberately out of scope

`disk full`, queue overflow by capacity, config-corruption recovery, watchdog,
abrupt power loss, clustering/HA, performance benchmarks, ARM hardware
benchmarks and soak runs. Those are W3, X or Y.
