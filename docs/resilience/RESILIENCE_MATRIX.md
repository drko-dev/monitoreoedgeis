# Hito Y — resilience matrix (Y6 disk full, Y7 queue overflow, Y9 watchdog, Y10 health recovery)

Scope: the four resilience slices of Hito Y. This document is the mechanism
inventory and the validation ledger for them. It **does not close Hito Y**, does
not modify `docs/ROADMAP.md`, and does not update the `docs/PROJECT_STATUS.md`
snapshot.

Base: `main @ d6d29f539017c504e56ae2f14f358d04abd839da` (Hito X merged).
Branch: `resilience/hito-y-resources-health`.

Method: audit the mechanisms that already exist, then close the real failures
with minimal changes. No parallel observability system, no parallel scheduler, no
new daemon. Every fix below is a change to code that was already on the request
path, and every one is listed with the test that would fail without it.

### Status vocabulary

| Status | Meaning |
| --- | --- |
| **VALIDATED** | Exercised end to end by a test in this repository, on this branch, and observed passing. |
| **PARTIALLY_VALIDATED** | The logic is exercised, but a boundary the mechanism depends on is not (a real device, a real init system, real hardware). The validated part and the missing part are both stated. |
| **NOT_VALIDATED** | Not exercised here at all. Stated so it cannot be mistaken for covered. |

---

## 1. Y6 — disk full

### 1.1 Write sites audited

Every filesystem write the Edge runtime performs. `AF` = temp file + rename
(atomic ordering); `AF+F` = temp file + rename + `fsync` before the rename
(atomic ordering **and** durability).

| Area | Site | Pattern | Payload | Partial can replace valid? |
| --- | --- | --- | --- | --- |
| Config / state | `internal/identity/store.go` | AF+F | edge id (no secret) | No |
| Identity / credentials | `internal/credentials/store.go` | AF+F | **credential** | No |
| Camera credentials | `internal/cameracreds/atomic.go` | AF+F | **encrypted passwords, master key** | No |
| Control ledger | `internal/control/ledger.go` | AF+F | command audit | No |
| Remote config | `internal/remoteconfig/store.go` | AF+F | config state | No |
| Backlog | `internal/edgebacklog/backlog.go` | AF+F* | event metadata | No |
| Cloud spool | `internal/cloudsink/buffer.go` | AF+F | JPEG frame | No |
| Events | `internal/fulledge/store.go` | AF+F | event JSON | No |
| Evidence | `internal/fulledge/evidence.go` | AF+F | JPEG | No (overwrite guard) |
| Clips | `internal/evidence/clips.go` | AF | MP4 | No (overwrite guard) |
| OTA staging | `internal/ota/download.go` | AF (dir rename) | release artifact | No (after this branch) |
| Logs | `internal/logging/logging.go` | — | none | The process writes no log files; stdout goes to journald. |

\* `edgebacklog` writes JSON that is rewritten in place on every stage advance;
the temp file is now removed when the write fails (see 1.2).

### 1.2 Mechanisms and defects closed

| # | Mechanism | Trigger | Expected behaviour | Recovery | Status |
| --- | --- | --- | --- | --- | --- |
| 1 | `platform.ErrDiskFull` / `IsDiskFull` / `WrapDiskError` (`internal/platform/diskfull*.go`) | A write returns `ENOSPC` or `EDQUOT` | Classified as disk-full, raw errno still reachable via `errors.Is`, original message preserved byte for byte | n/a (pure classification) | **VALIDATED** — table tests plus a real kernel `ENOSPC` via `/dev/full` on Linux (skipped where absent) |
| 2 | `edgebacklog.Enqueue` write failure | Persisting a new record fails | Error propagated and classified; the submission is **not** queued; `drops` is **not** incremented (that counter is for the capacity bound); `LastError` is bounded and carries no credential | Next `Enqueue` after space returns succeeds | **VALIDATED** |
| 3 | `edgebacklog` stage-advance / retry-state write (`ProcessOne`) | The record is persisted after a send | The failure is counted (`persist_errors`), classified (`disk_full`), and reported; the record stays in memory so the event is not lost | `disk_full` clears on the next successful persist | **VALIDATED** |
| 4 | `edgebacklog` temp-file leak | A write fails mid-way | `writeLocked` removes its own temp file; `recover()` also sweeps leftovers from a crash | n/a | **VALIDATED** |
| 5 | `edgebacklog` quarantine move | The archive rename fails | Falls back to a delete so a permanently-rejected record cannot resurrect on restart; the outcome is counted | n/a | **VALIDATED** |
| 6 | OTA staging replacement | A release is re-staged over an existing staged directory | The old directory is displaced by rename first and restored if the staging rename fails: fully applied or a no-op, never a lost release | n/a | **VALIDATED** |
| 7 | OTA artifact download | The write fails mid-download | Error classified; the staging directory is removed, so no partial artifact, `apply.request`, or scratch dir survives | Next check retries | **VALIDATED** |
| 8 | `identity` / `credentials` / `cameracreds` writes | Power loss between rename and data flush | `tmp.Sync()` before the rename, so the new name cannot point at a zero-length file (all three readers treat truncation as fatal corruption and never regenerate) | n/a | **VALIDATED** (durability ordering asserted by code path, not by cutting power) |
| 9 | `fulledge.EvidenceManager.SaveJPEG` | Free space below `GEOCAM_EDGE_MIN_FREE_DISK_BYTES` (default 100 MiB) | Refuses with `ErrDiskSpaceBelowMinimum`; the event is still persisted with `EvidenceRef.ErrorMessage`, and `limits.disk_saturated` marks the vision queue degraded | Next attempt after space returns | **VALIDATED** (pre-existing; `mockDiskChecker`) |
| 10 | Cloud spool `Enqueue` | The spool write fails | Error classified as disk-full; the frame is refused, **not** counted as `ErrBufferFull`, and no temp file survives | Next enqueue after space returns | **VALIDATED** |

### 1.3 Properties checked

`ENOSPC` propagated and classified · no panic · no silent corruption · no valid
file replaced by a partial one · components that can continue do · components
that cannot degrade explicitly · recovery when space returns · logs expose no
payload or secret.

Secrets specifically: `Status.LastError` is asserted in
`TestProcessOnePersistFailureIsVisibleNotSilent` not to contain the credential the
send path was handed, and to stay within `maxErrorBytes`.

### 1.4 How ENOSPC is injected

Deterministically, without root and without filling a real disk: each writer
exposes a per-instance filesystem seam (the same pattern `cloudsink.Buffer`
already used for its clock), and a test replaces it with one returning a genuine
`syscall.ENOSPC`. The real `writeLocked` / `recover` / `quarantine` code paths run
unchanged; only the filesystem's answer differs. On Linux,
`TestIsDiskFullRealKernelNoSpace` additionally validates the classifier against a
real kernel `ENOSPC` from `/dev/full`.

### 1.5 Y6 gaps left open (NOT_VALIDATED / out of scope)

| Gap | Why it is not closed here | Status |
| --- | --- | --- |
| No retention for `events/`, `evidence/captures/`, `evidence/clips/` | Retention is Hito V's slice. Full disk is made explicit and survivable here; it is not prevented. | **NOT_VALIDATED** |
| `evidence.Clipper.Capture` has no free-space pre-check (unlike `SaveJPEG`) | It has `EdgeMaxClipSizeBytes` but no `MinFreeDiskBytes`. Adding one means deciding a second threshold, which is a policy call, not a bug fix. The failure is explicit (`ErrDiskFull` from the encode/write) and the caller logs it. | **NOT_VALIDATED** |
| `fulledge.EventStore.Save` has no free-space pre-check while evidence does | Deliberate asymmetry: events are small durable metadata and gating them would lose events; evidence is large and reconstructible at the edge. Documented, not changed. | **PARTIALLY_VALIDATED** |
| OTA `pending/<release-id>/` is never pruned and the artifact body has no size cap | Pruning releases is retention; the size cap is a release-engineering decision. A failed download is now a clean no-op. | **NOT_VALIDATED** |
| No directory `fsync` after the rename | File data and metadata are flushed before the rename; the rename itself is not flushed. The rename is atomic, so the worst case is the previous valid file, not a partial one. | **PARTIALLY_VALIDATED** |
| `evidence/clips/*.mp4.tmp` orphaned by a crash mid-encode | No recovery pass scans that directory. The clip guard prevents corrupting an existing clip. | **NOT_VALIDATED** |

---

## 2. Y7 — queue overflow

### 2.1 Real queues, as audited

No global queue was invented. This is what exists.

| # | Queue | Item | Capacity | Policy | Order | Persisted | Signal |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 1 | `cameraPipeline.packetCh` | raw RTP payload | `GEOCAM_VIDEO_QUEUE_DEPTH` (64) | reject newest | FIFO | no | `frames_dropped` |
| 2 | `cameraPipeline.auCh` | access units | same | reject newest | FIFO | no | `frames_dropped` |
| 3 | `FFmpegDecoder.frames` | decoded frames | `GEOCAM_VIDEO_DECODE_QUEUE_DEPTH` (4) | reject newest | FIFO | no | `frames_dropped` |
| 4 | `Router.queues[]` | sampled frames per sink | `GEOCAM_VIDEO_QUEUE_DEPTH` (64) | reject newest, per sink | FIFO/sink | no | `queues.router[].drops` |
| 5 | `RingBuffer.buf` | history frames | `GEOCAM_VIDEO_RINGBUFFER_SIZE` (30) | overwrite oldest | FIFO | no | `ring_buffer_dropped` (**new**) |
| 6 | `H264Depacketizer.auNALUs` | NALUs of an open AU | **was unbounded** → 8 MiB / 4096 NALUs | degrade: abandon the AU | in order | no | `oversized_aus_dropped` (**new**) |
| 7 | `H264Depacketizer.fuBuf` | FU-A fragments | **was unbounded** → 8 MiB | degrade: abandon the run | in order | no | `reassembly_errors` + `oversized_aus_dropped` |
| 8 | `cloudsink.Buffer` | spooled JPEG | `GEOCAM_CLOUD_BUFFER_MAX_FRAMES`/`_BYTES` (off by default) | reject newest | FIFO | **yes** | `dropped_buffer_full` |
| 9 | `cloudsink.Buffer` recovery | recovered entries | same | **was unbounded** → drop oldest | FIFO | **yes** | `dropped_over_capacity` (**new**) |
| 10 | `cloudsink.Buffer` age eviction | stale entries | n/a | drop by age | oldest | **yes** | `dropped_age` (**new**) |
| 11 | `edgebacklog.Backlog.queue` | event + evidence refs | `LOCAL_EVENT_BACKLOG_MAX_OPERATIONS` (100) / `_MAX_BYTES` (512 MiB) | reject newest | strict FIFO | **yes** | `drops` |
| 12 | `edgebacklog` quarantine | permanently-rejected records | **was unbounded** → `MaxOperations` | drop oldest | FIFO | **yes** | `quarantine_evicted` (**new**) |
| 13 | `control.Module.executed` | command outcomes | **was unbounded** → 256 / ledger max | drop oldest | insertion | no (ledger is durable) | n/a (**new** bound) |
| 14 | `control.Ledger` | command audit | 100 (hardcoded) | drop oldest, silent | FIFO | **yes** | none |
| 15 | `fulledge.LimitsManager.sem` | inference slots | `GEOCAM_EDGE_MAX_CONCURRENT_INFERENCE` (1) | reject | n/a | no | `queue_dropped` |
| 16 | `discovery.Inventory.devices` | LAN devices | `MaxInventoryDevices` (512) + TTL | reject newest, silent | n/a | no (RAM) | none |
| 17 | `discovery.wsdiscovery` candidates | probe matches | 64 / 200 datagrams | stop at cap | arrival | no | none |
| 18 | `processing.Manager.unsupported` | non-H264 keys | **unbounded in theory**, bounded by the camera inventory | never pruned | n/a | no | none |

### 2.2 Policy choices, by data semantics

The policy was chosen from what the item *is*, not from what was convenient.

- **Live media** (1–5, 7, 8): reject newest / drop oldest / abandon. Realtime
  wins; a late frame is worthless. All are counted.
- **Recovered spool entries** (9): drop oldest, because these frames already
  failed one upload and the newest are the ones still likely to matter. Bounding
  them is what makes the configured bound a bound rather than a suggestion.
- **Durable event metadata** (11, 12): never dropped silently. `Backlog.Enqueue`
  refuses new work with `ErrFull` (counted) when full; recovery deliberately
  loads *more* than the bound rather than discard durable unsynced events, and
  reports the violation explicitly (`over_capacity`). Quarantined
  permanently-rejected records are the one exception, and they are pruned
  oldest-first against the same `MaxOperations` with a counter.
- **Corrupt/unusable wire data** (6, 7): degrade. A truncated access unit cannot
  be decoded, so it is abandoned — but the accumulator is emptied rather than
  merely capped, and the event is counted separately from packet loss so "this
  sender never closes a frame" is not hidden behind a loss number.
- **Idempotency bookkeeping** (13): drop oldest, bounded to at least the durable
  ledger's own bound so a command the ledger still remembers can never execute
  twice.

### 2.3 Honest metrics

The vision queue block reported `capacity = GEOCAM_EDGE_INFERENCE_QUEUE_DEPTH`
(default 32), a bound **no code path enforces**. `depth` is the in-flight
inference count, whose real bound is the `MaxConcurrentInference` admission
semaphore (default 1), and the real frame-level queue for that sink is its
`processing.Router` channel sized by `GEOCAM_VIDEO_QUEUE_DEPTH` — already reported
under `queues.router[]` with the real capacity and its own drop counter. The
block now reports the bound that actually applies, and
`GEOCAM_EDGE_INFERENCE_QUEUE_DEPTH` is documented as reported-for-visibility-only.
Removing or wiring that knob to a real queue is left open (see 2.4).

Three silent drops were made visible: the ring buffer's overwrite counter had no
production reader at all; the cloud spool's age eviction removed entries with no
counter while the corrupt-entry branch beside it did count; and the
depacketizer's `UnsupportedNALTypes` was incremented but never republished.

### 2.4 Y7 gaps left open

| Gap | Status |
| --- | --- |
| `GEOCAM_EDGE_INFERENCE_QUEUE_DEPTH` is still a configured value with no queue behind it | **NOT_VALIDATED** (documented; left for a decision on wiring or removing it) |
| `control.Ledger` prunes audit history with no counter and no log | **NOT_VALIDATED** |
| `discovery.Inventory` rejects at `MaxInventoryDevices` with no counter or log | **NOT_VALIDATED** |
| `processing.Manager.unsupported` is never pruned (bounded in practice by the camera inventory) | **NOT_VALIDATED** |
| `Router.Stop()` waits on workers with no deadline; a hung `evidence.Clipper` ffmpeg encode can delay shutdown | **NOT_VALIDATED** (pre-existing; not a queue-full path) |
| On-disk retention for events/evidence/clips/OTA pending (rows 22–24 of the audit) | **NOT_VALIDATED** (Hito V's slice) |

---

## 3. Y9 — watchdog

### 3.1 What already existed

`deploy/appliance/systemd/geocam-edge.service.in` already declared
`Restart=on-failure`, `RestartSec=2`, `StartLimitIntervalSec=300`,
`StartLimitBurst=5`, `KillSignal=SIGTERM`, `TimeoutStopSec=30`, plus
`ProtectSystem=strict`, `ReadWritePaths=` and `NoNewPrivileges=true`. systemd is
the supervisor; no second daemon was added.

`PrivateDevices=` is **absent** and stays absent: Full Edge's CUDA profile needs
`/dev/nvidia*`. `deploy/appliance/appliance_test.go` asserts both the absence of
`PrivateDevices=true` and the reason, and that assertion was extended, not
relaxed.

### 3.2 What was missing, and what was added

`Type=simple` meant systemd considered the unit started the instant `exec`
returned, and there was no `WatchdogSec=`, no `NotifyAccess=` and no sd_notify
implementation anywhere in the tree. A process that kept running while no longer
serving was invisible to systemd — and invisible to its own internal restart
loops, whose goroutines are part of the same wedge.

| Mechanism | Trigger | Expected behaviour | Recovery | Status |
| --- | --- | --- | --- | --- |
| `Type=notify` + `READY=1` | The health HTTP surface begins serving | systemd completes startup | — | **PARTIALLY_VALIDATED** — READY=1 asserted on a real unix socket; real systemd `NOT_VALIDATED` |
| `WatchdogSec=60` + `WATCHDOG=1` | Armed only when systemd sets `WATCHDOG_USEC` | A ping every 20s (a third of the interval) while a liveness check passes | Pings stop → systemd's timer expires → restart, bounded by `StartLimitBurst=5`/300s | **PARTIALLY_VALIDATED** — ping/withhold/resume asserted on a real socket; the systemd kill is `NOT_VALIDATED` |
| Liveness check | Each ping period | A real HTTP round trip to the process's own `/healthz` over loopback | Resumes on its own when the surface answers again | **VALIDATED** |
| `STOPPING=1` | Shutdown begins | systemd cancels the watchdog timer, so a slow deliberate stop is not a hang | — | **PARTIALLY_VALIDATED** — datagram asserted; timer cancellation `NOT_VALIDATED` |
| `NotifyAccess=main` | Always | `NOTIFY_SOCKET` is not exported to ffmpeg or the vision worker, so no child can complete startup or feed the watchdog | — | **PARTIALLY_VALIDATED** (static unit assertion) |
| Opt-in boundary | `NOTIFY_SOCKET` / `WATCHDOG_USEC` unset | Everything is a no-op: `go run`, Kubernetes (HTTP probes) and tests are unaffected | — | **VALIDATED** |

Design notes that matter for False-positive avoidance:

- The liveness probe is a real round trip, not a flag. A wedged process usually
  holds a lock or the runtime; a flag would report "alive" while `/healthz` hangs.
- The probe is a **process** signal. A slow camera, a broken decoder or an
  unreachable SaaS deliberately do **not** trip it — those have their own
  supervised reconnect loops with their own backoff (RTSP 1s→60s, vision worker
  1s→30s, decoder 500ms→10s), and restarting the whole Edge for them is what Y10
  forbids.
- A DEGRADED agent still sends `READY=1`. Refusing to would make systemd kill and
  restart it forever over a corrupt credential file that only an operator fixes.
  This is asserted directly (`TestAgentDegradedStillNotifiesReady`).
- The watchdog is a `Type=notify` watchdog only. `PrivateDevices` was not
  introduced, and the privileged OTA updater unit is a `oneshot` with no watchdog
  — asserted, so it cannot be added by accident.
- A health surface that cannot bind means no pings, so systemd restarts the unit.
  Restarting is the standard remedy (the port may have been released) and
  `StartLimitBurst` bounds it to five attempts per five minutes before the unit
  is left failed for an operator. This is the intended, visible outcome rather
  than a silent DEGRADED process with no health surface.

### 3.3 OTA interaction

Checked, not assumed:

- The privileged updater is a separate `oneshot` unit in its own cgroup;
  `systemctl restart geocam-edge.service` cannot kill it, and no watchdog exists
  that could.
- The agent sends `STOPPING=1` before `TimeoutStopSec` can elapse, so the new
  watchdog cannot kill the agent mid-shutdown.
- `update.sh` runs `systemctl restart geocam-edge.service` and then polls
  `/readyz`. Under `Type=notify`, `restart` now blocks until `READY=1`, which is
  sent when the health surface comes up — **not** after a slow Full Edge vision
  worker finishes. Waiting for readiness remains `wait-ready.sh`'s job, so the
  update path's timing semantics are unchanged.
- `TimeoutStartSec=30` bounds how long a start that never reaches the health
  surface can hold `systemctl restart`; startup is local work only.

**NOT_VALIDATED:** real systemd. No test in this repository runs on a host where
PID 1 is systemd, and `systemd-analyze verify` is not available. The unit was
checked by static assertion only. Validating the kill-and-restart path requires
an appliance or a systemd container and is left explicitly open.

---

## 4. Y10 — health recovery

### 4.1 Health model, as audited

Four aggregate states: `STARTING`, `READY`, `DEGRADED`, `STOPPING`. Per
component, richer vocabulary already existed and is unchanged: cameras
`connecting|online|degraded|offline`; heartbeat
`idle|running|degraded|unauthorized|stopped`; vision worker
`not_configured|model_missing|starting|ready|restarting|stopped|error`; pipeline
`starting|running|stalled|error|skipped_limit`. The aggregate has no
`UNHEALTHY`/`OFFLINE` value, and adding one would change the heartbeat's
`HealthStatus` contract with the SaaS and the `/readyz` semantics; the
per-component vocabularies already carry the three levels where they apply, so
this is recorded as a deliberate boundary rather than a rename.

There is no contributor registry and no aggregation rule: `Snapshot.Status` was
set imperatively by exactly two callers.

### 4.2 The real defect

`heartbeat.OnUnauthorized` called `reporter.Set(StateDegraded)` on a 401/403, and
**nothing ever wrote `READY` again** — the only `Set(StateReady)` in the tree was
in `Agent.Run`'s startup switch. Once the SaaS rejected a credential, the Edge
stayed `DEGRADED` and `/readyz` stayed 503 until the process restarted, even after
an operator re-enabled the device. This is not cosmetic: `/readyz` is what
`geocam-edge check` exits on and what the appliance's own `wait-ready.sh` polls,
and `update.sh` rolls back a good release when it does not come up in time.

### 4.3 Mechanisms

| Mechanism | Trigger | Expected behaviour | Recovery | Status |
| --- | --- | --- | --- | --- |
| `agentHealthGate` (`internal/agent/health_gate.go`) | Any health transition | The aggregate state is derived from its causes instead of written by each caller | Re-derived on every cause change | **VALIDATED** |
| Credential-revocation cause | `OnUnauthorized` (401/403) | `DEGRADED`, `/readyz` 503 | `OnRecovered` (first success after a failure) clears it → `READY`, `/readyz` 200 | **VALIDATED** |
| Startup cause | Identity, credentials, module startup or module construction failure | `DEGRADED` | Never clears: a restart is required. A healthy heartbeat cannot mask it | **VALIDATED** |
| Shutdown guard | `STOPPING` | A late recovery or revocation cannot overwrite it | — | **VALIDATED** |
| Pre-activation guard | A hook firing before `Run` publishes | Nothing is published; the initial state is decided once | — | **VALIDATED** |
| `heartbeat.OnRecovered` | First success after a non-running outcome | Fires **exactly once** per recovery, not on every successful heartbeat | — | **VALIDATED** |
| Per-camera reconnect | RTSP dial/stream error | Backoff 1s→60s, resets to 1s after a healthy connect | Resumes on its own | **VALIDATED** (pre-existing tests) |
| Vision worker restart | Subprocess death or failed handshake | `restarting` with backoff 1s→30s, resets to 1s after a handshake | Returns to `ready` | **VALIDATED** (pre-existing tests) |
| Decoder restart | ffmpeg exit or stall | Backoff 500ms→10s | Resumes | **VALIDATED** (pre-existing tests) |

### 4.4 Required properties, and the evidence for each

| Property | Evidence | Status |
| --- | --- | --- |
| A component returns to healthy after the cause disappears | `TestY10_CredentialRevocationRecoversWithoutRestart`: real agent, switchable fake SaaS, 401 → `DEGRADED` → `/readyz` 503 → credential accepted → `READY` → `/readyz` 200 | **VALIDATED** |
| It is not incorrectly latched | Same test, ten consecutive cycles; plus the gate's own contract tests | **VALIDATED** |
| A startup fault is not masked by a component recovery | `TestY10_StartupFaultIsNotClearedByAHeartbeatRecovery`, `TestAgentHealthGate_Contract/startup_cause_is_sticky` | **VALIDATED** |
| The whole Edge is not restarted when reconnecting a component suffices | Uptime strictly increases across the ten cycles (same process served all of them), and the goroutine count stays flat — no duplicated supervisors or workers, no restart loop | **VALIDATED** |
| No restart loop | systemd `StartLimitBurst=5`/300s; in-process supervisors all have capped exponential backoff; the recovery path itself performs no restart at all | **PARTIALLY_VALIDATED** (the systemd limit itself is not exercised) |
| No duplicated supervisors/workers | Goroutine-count assertion across ten failure/recovery cycles; `rtsp.Supervisor.Start` is documented idempotent; `vision.Worker.Stop` is idempotent; remote-config mode changes stop the previous worker before starting the next | **VALIDATED** for the recovery path; the mode-change path is pre-existing and asserted by its own tests |

### 4.5 Y10 gaps left open

| Gap | Status |
| --- | --- |
| No aggregate `UNHEALTHY`/`OFFLINE` state | **NOT_VALIDATED** by design: adding one changes the SaaS `HealthStatus` contract and `/readyz`. Per-component vocabularies already carry the three levels. |
| A camera fleet that is entirely offline leaves the aggregate `READY` | **PARTIALLY_VALIDATED** — deliberate and asserted (`TestCameraFailureDoesNotDegradeAgent`): the Edge's local function does not depend on cameras, and the per-camera state is reported. |
| Disk saturation does not degrade the aggregate state | **PARTIALLY_VALIDATED** — it is surfaced under `queues.vision.degraded` and `full_edge.limits.disk_saturated`, and evidence writes fail explicitly, but the aggregate stays `READY`. Making storage degrade the aggregate is a policy decision, not a bug fix. |
| Remote-config `restart_video_pipeline` is `UNSUPPORTED` | **NOT_VALIDATED** (pre-existing; out of Y10 scope) |
| Real systemd restart-on-unhealthy | **NOT_VALIDATED** (needs a systemd appliance, see §3.3) |

---

## 5. Defects found and fixed

Each is its own commit with a regression test.

| # | Defect | Impact | Fix | Test |
| --- | --- | --- | --- | --- |
| 1 | No code classified `ENOSPC`; it was indistinguishable from a permission error | No component could degrade explicitly on a full disk | `platform.ErrDiskFull` + `IsDiskFull` + `WrapDiskError` | `internal/platform/diskfull_test.go` (incl. real kernel `ENOSPC`) |
| 2 | `edgebacklog` discarded four of its five write errors with `_ =` | Under sustained `ENOSPC` the on-disk record silently reverted to an older stage (duplicate uploads) and orphan `.json.tmp` files accumulated forever | Errors counted, classified and reported; temp file removed on failure; recovery sweeps leftovers | `internal/edgebacklog/diskfull_test.go` |
| 3 | `edgebacklog` quarantine move swallowed its error | A permanently-rejected record could resurrect on restart and block the FIFO | Delete fallback completes the move; outcome counted | `TestQuarantineMoveFailureDoesNotResurrectTheRecord` |
| 4 | `edgebacklog/quarantine/` grew without bound | Disk exhaustion on a small appliance | Bounded to `MaxOperations`, oldest-first, re-established on `Open`, eviction counted | `TestQuarantineArenaIsBoundedAcrossRestart` |
| 5 | OTA staging did `_ = os.RemoveAll(dest)` before renaming | A failed removal destroyed the previously-verified staged release and the new one never landed | Displace by rename, restore on failure, reclaim the backup unconditionally | `internal/ota/staging_resilience_test.go` |
| 6 | `identity`/`credentials`/`cameracreds` used temp+rename with **no** `fsync` | Power loss could leave a zero-length secret file, which all readers treat as fatal corruption and never regenerate | `tmp.Sync()` before the rename | covered by the packages' existing round-trip tests; ordering is asserted by the code path |
| 7 | `H264Depacketizer.auNALUs` was unbounded | A sender that never closes a frame grew it until OOM; reachable from the wire, one pipeline per camera | Bounded (8 MiB / 4096 NALUs), accumulator emptied, `oversized_aus_dropped` counted | `internal/processing/depacketizer_bound_test.go` |
| 8 | `H264Depacketizer.fuBuf` was unbounded | Same, via an FU-A run that never sets its end bit | Bounded (8 MiB), run abandoned and counted | Same |
| 9 | `cloudsink.Buffer.recover()` ignored `maxFrames`/`maxBytes` | A spool written under a larger configuration replayed above its own bound | Trim oldest-first, `dropped_over_capacity` counted | `internal/cloudsink/buffer_bound_test.go` |
| 10 | Cloud spool age eviction had no counter | Frames vanished from the spool with nothing moving in `/status` | `dropped_age` counted and surfaced | `TestBuffer_AgeEvictionIsCounted` |
| 11 | `RingBuffer.Dropped()` had no production reader | Clip/debug history loss was invisible in `/status` and in logs | `ring_buffer_dropped` published and folded into `frames_dropped` | `TestPipelineRingBufferDroppedIsObservable` |
| 12 | `UnsupportedNALTypes` was incremented but never republished | Wire data the Edge could not use was dropped with no signal | `unsupported_nal_types` on `/status` | `internal/processing/depacketizer_test.go` |
| 13 | `control.Module.executed` had no delete path | Every distinct command ever dispatched was retained for the process lifetime | Bounded oldest-first to at least the ledger's own bound | `internal/control/module_bound_test.go` |
| 14 | `/status` reported a vision queue capacity no code enforced | Operators sized against a bound that did not exist | Reports the real admission bound; the phantom knob is documented | `TestSnapshot_QueuesDistinctAndCoherent` |
| 15 | Agent-level `DEGRADED` was latched forever after a 401 | `/readyz` stayed 503 after the cause was gone; the appliance's readiness gate and `update.sh` rollback were affected | `agentHealthGate` derives the state from causes; `OnRecovered` clears the credential cause | `internal/agent/recovery_y10_test.go`, `internal/heartbeat/recovery_test.go` |
| 16 | No readiness or liveness signal to systemd | A running-but-wedged agent was invisible to its own supervisor | `internal/systemd` sd_notify + `Type=notify` + `WatchdogSec=60` gated on a real liveness probe | `internal/systemd/notify_test.go`, `internal/agent/watchdog_test.go` |

## 6. Tests

| Command | Result on this branch |
| --- | --- |
| `gofmt -l .` | clean |
| `go vet ./...` | clean |
| `go test ./...` | 33 packages ok, 0 FAIL |
| `go test -race <modified packages>` | see the validation record in the PR |
| `go test ./internal/edgebacklog -count=10` | focal repeat: queue/disk-full determinism |
| `go test ./internal/cloudsink -count=10` | focal repeat: spool bound and overflow determinism |
| `go test ./internal/agent -count=3` | focal repeat: health failure → recovery |
| `go test ./internal/heartbeat -count=5` | focal repeat: exactly-once recovery hook |

Deterministic fault injection used: an injected `syscall.ENOSPC` through a
per-instance filesystem seam; a real kernel `ENOSPC` via `/dev/full` where
available; over-capacity spools seeded on disk before `Open`; scripted
failure→recovery sequences driven through the real heartbeat loop; and a real
unix socket standing in for `NOTIFY_SOCKET`.

## 7. What needs a real appliance, systemd, or hardware

Stated so it cannot be mistaken for covered:

1. **Real systemd supervision.** `WatchdogSec` kill-and-restart, `Type=notify`
   startup completion, `StartLimitBurst` exhaustion, and `STOPPING=1` cancelling
   the timer. Requires a host where PID 1 is systemd. Static unit assertions and
   real-socket datagram assertions are the substitute here.
2. **Real `ENOSPC` on the appliance filesystem.** The classification and every
   component's behaviour are exercised against injected and (on Linux)
   kernel-generated `ENOSPC`; the appliance's own overlay/quota layout is not.
3. **Power-loss durability.** `fsync` ordering is asserted by code path, not by
   cutting power to a real device.
4. **OTA staging and rollback on a real appliance**, including the interaction
   between `systemctl restart` blocking on `READY=1` and `wait-ready.sh`'s 30s
   budget.
5. **Long-run resource behaviour** (1/5/10/25/50 cameras, global CPU/RAM, total
   network). Out of Hito Y's scope.
6. **`systemd-analyze verify`** on the unit templates.
