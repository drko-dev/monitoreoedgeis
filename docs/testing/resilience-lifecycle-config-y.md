# Hito Y — Resiliencia: restart (Y1), power loss (Y2), config corruption (Y8)

Scope: this document covers exactly Y1, Y2 and Y8. It does not cover Y3–Y7,
Y9 or Y10 — those belong to other work (see `resilience/hito-y-resources-health`
for the sibling Y block covering resources/health). Nothing here claims
physical power-loss hardware validation; "power loss" is exercised through
deterministic abrupt-termination proxies, as this milestone's own methodology
requires.

Classification used below: **VALIDATED** (a test exercises the real code path
and passes), **PARTIALLY_VALIDATED** (the fix is real and tested, but a known
related gap remains and is documented, not silently claimed as covered), and
**NOT_VALIDATED** (a real hardware/physical-power-loss claim this milestone
does not attempt).

## Y1 — Restart: VALIDATED (pre-existing coverage, reused as-is)

Hito W already built and validated exactly what Y1 asks for. No new code or
tests were needed here — reusing existing, passing regression tests is the
correct outcome per this task's own instructions not to duplicate testing
infrastructure.

- `internal/agent/failure_lifecycle_test.go::TestW8_RestartReusesDurableIdentityAndResetsPerRunState`:
  starts process A, stops it, starts process B against the same data
  directory, and asserts: `edge_id` and `identity.json` bytes are unchanged
  across the restart; `credentials.json` bytes and the loaded credential are
  unchanged; `boot_id` changes and `sequence_number`/`uptime_seconds` reset —
  i.e. purely runtime state is reconstructed, never reused as if it were
  durable; no camera-credential files are invented since that store is not
  wired into the agent runtime.
- `internal/agent/failure_lifecycle_test.go::TestW8_AgentRunReportsOneLifecycleAndStopsCleanly`:
  a second `Run` is not started twice in-process; the health surface reports
  one lifecycle and stops cleanly.
- `internal/rtsp`'s own idempotency fixes from Hito W (`Supervisor.Start`
  idempotent, `Stop`-before-`Start` no longer deadlocks, `Manager.Stop`
  no longer deadlocks, a second `Manager.Start` no longer spawns a duplicate
  coordination goroutine) remain in place and covered by
  `internal/rtsp/failure_lifecycle_test.go`. A process-level restart always
  creates a brand-new `Manager`/`Supervisor` set in a brand-new process, so
  there is no additional "duplicate supervisor across restart" risk beyond
  what Hito W already closed within a single process's lifecycle.

Verification: `go test ./internal/agent/... ./internal/rtsp/...` pass; the
restart test itself passes at default `-count=1` (it is not flake-sensitive —
it drives real timers over ~3.6s, so it is not run at `-count=10`).

## Y2 — Power loss: PARTIALLY_VALIDATED

### Defects found and fixed

The device's durable stores follow a temp-file + rename pattern, but not all
of them synced the temp file to stable storage before the rename made it
visible. `internal/remoteconfig/store.go` and `internal/control/ledger.go`
already did this correctly (`tmp.Sync()` before `Chmod`/`Close`/`Rename`).
`internal/identity/store.go`, `internal/credentials/store.go` and
`internal/cameracreds/atomic.go` — device identity and credential
persistence — did not: on a real power loss, the just-written bytes could
still be sitting in the OS page cache, not on disk, at the moment the
rename made the "new" file visible; a crash at that exact instant could
leave data that looks committed but is not, once power returns. This is the
concrete instance of the gap already recorded in `docs/PROJECT_STATUS.md`
("most durable files use tmp+rename without fsync").

Fixed by adding `tmp.Sync()` in the same position used by the
already-correct files, in:

- `internal/identity/store.go`
- `internal/credentials/store.go`
- `internal/cameracreds/atomic.go`

`internal/edgebacklog/backlog.go`'s per-record `writeLocked` used
`os.WriteFile` (no explicit fsync at all) instead of the create+write+sync+
close+rename sequence used elsewhere. Brought in line with the same
convention.

### Deterministic abrupt-termination tests added

A real power cut cannot be reproduced in a unit test. Per this task's own
instructions, the following are deterministic proxies for "a process killed
between the temp-file write and the rename":

- `internal/identity/identity_test.go::TestLoad_SurvivesLeftoverTmpFromAbruptKill`
- `internal/credentials/store_test.go::TestLoad_SurvivesLeftoverTmpFromAbruptKill`
- `internal/edgebacklog/backlog_test.go::TestBacklogWriteLocked_SurvivesLeftoverTmpFile`

Each leaves a stale, partially-written `*.tmp` file (the exact leftover a
kill mid-write produces) in place and asserts: it is never mistaken for the
real file, `Load`/recovery still returns the last durably-committed content
unchanged, and a subsequent write still succeeds cleanly.

### Explicitly NOT VALIDATED / not fixed here

- **Directory-entry durability.** None of the writers above (old or new)
  fsync the containing directory after the rename. POSIX only guarantees a
  rename survives a crash once the directory's own metadata has been
  flushed; without that, a crash at exactly the wrong instant could still
  show either the old or the new file after reboot (never a torn/partial
  one — content fsync now prevents that — but the choice between old and
  new content is not itself guaranteed durable). Closing this fully would
  mean adding a directory-fsync step to every one of these call sites, which
  is a larger surface change than "minimal and localized" allows for this
  task, and it cannot be verified without a real power-loss rig. Documented
  as a known gap, not claimed as fixed.
- **Real hardware power-loss testing.** Not attempted; out of scope by the
  task's own definition of Y2.

### Explicitly out of scope, left untouched

`internal/evidence/clips.go`, `internal/ota/download.go` and
`internal/perf/report.go` also write without an explicit fsync, but they are
video-evidence, OTA-artifact and performance-report paths — not device
identity, credentials, config or backlog metadata — so they fall outside
this task's exclusive Y1/Y2/Y8 scope and were not touched.

## Y8 — Config corruption: VALIDATED

### Pre-existing and already correct (confirmed, not re-implemented)

- `internal/identity.Load`: corrupt JSON, invalid `edge_id`, or an
  unsupported `schema_version` is a hard `ErrCorrupt` — it never silently
  regenerates a new identity, which would silently orphan the device from
  anything already associated with the old `edge_id`. Already unit-tested.
- `internal/credentials.Load`: same fail-closed pattern for the enrollment
  credential. Already unit-tested.
- `internal/remoteconfig.OpenStore`: corrupt `remote_config_state.json` is a
  hard `ErrCorruptState`.
- `internal/control.Ledger`: a corrupt `control_ledger.json` is a hard
  error. Already agent-level tested
  (`TestAgentDegradedOnCorruptControlLedger`).
- **Agent-level fail-closed wiring**: `internal/agent.New` captures
  `identityErr` / `credentialsErr` / `controlErr` / `remoteConfigErr`, and
  `Run()` never reaches `READY` while any of them is set — but `/healthz`
  stays reachable (`StateDegraded`) for diagnosis. No crash, no
  restart/reload loop, no silent default substituted for the corrupt data,
  and the corrupt file itself is never overwritten (nothing in these load
  paths writes on a failed load), so it stays on disk for a human or a
  diagnostic tool to inspect.

### New test coverage added (closed a real blind spot)

`remoteconfig.ErrCorruptState` was already wired into the agent's fail-closed
path (`newRemoteConfigModule` wraps it into `remoteConfigErr`), but nothing
proved it at the point that matters: the running agent. Added
`internal/agent/agent_test.go::TestAgentDegradedOnCorruptRemoteConfigState`,
mirroring the existing `TestAgentDegradedOnCorruptControlLedger` — corrupts
`remote_config_state.json`, asserts the agent reaches `StateDegraded` and
never `READY`, and asserts the corrupt file is byte-identical after `Run`
returns. No code change was needed for this one: it proves an existing,
correct, previously-untested behavior.

### New defect found and fixed

`internal/edgebacklog.recover()` (the pending-record recovery scan run on
every `Open`) silently `continue`d past any record file it could not parse
(truncated JSON, or JSON missing `Sequence`/`EventUUID`). The file was never
deleted, but it also stayed invisible forever: not in the queue, not
retried, not counted in `Stats().Quarantined`, not surfaced anywhere. This is
exactly the "don't silently drop durable evidence/state on a parse error"
failure this task calls out. Fixed by routing it through the backlog's
existing `quarantine/` mechanism — already used today for records the SaaS
rejects as invalid — instead of a silent skip: the file is moved where a
diagnostic tool or operator can find it, and it is counted.
Test: `internal/edgebacklog/backlog_test.go::TestBacklogRecover_QuarantinesCorruptPendingFile`.

## Verification run

```
gofmt -l .                                                    # clean
go vet ./...                                                  # clean
go build ./...                                                # clean
go test ./...                                                 # 33 packages, all pass, no pre-existing failures
go test -race ./internal/identity/... ./internal/credentials/... \
  ./internal/cameracreds/... ./internal/edgebacklog/... \
  ./internal/remoteconfig/... ./internal/agent/...             # all pass
go test ./internal/identity/... ./internal/credentials/... \
  ./internal/edgebacklog/... ./internal/agent/... \
  -run 'TestLoad_SurvivesLeftoverTmpFromAbruptKill|TestBacklogRecover_QuarantinesCorruptPendingFile|TestBacklogWriteLocked_SurvivesLeftoverTmpFile|TestAgentDegradedOnCorruptRemoteConfigState' \
  -count=10                                                    # stable, 10/10
```

## Gaps outside Y1/Y2/Y8 (recorded, not acted on)

- Directory-entry fsync after rename (see Y2) — a genuine, small remaining
  power-loss edge case, left documented rather than expanded into a
  repo-wide durability refactor.
- `internal/evidence`, `internal/ota`, `internal/perf` writers without
  fsync — out of this task's exclusive scope (not identity/credentials/
  config/backlog).
- Everything already recorded under Hito W's own gap list in
  `docs/PROJECT_STATUS.md` that is not Y1/Y2/Y8 (auth-specific RTSP
  terminal state, `rollback.sh` re-verification, staged-target readiness
  gate, `rtsp.Config.Enabled` never read, boot-id guard on durable
  sequences) remains open and is not this task's responsibility.
