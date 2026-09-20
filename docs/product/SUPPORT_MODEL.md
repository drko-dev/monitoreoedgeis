# GEO CAM Edge — Support Model (Hito Z5)

## Scope of this document

Z5 defines how a deployed GEO CAM Edge appliance is operated and supported:
what an operator or support engineer can actually do from the box, the order to
diagnose in, the per-failure playbooks, and the data-safety and secret-handling
rules. Everything here is derived from code that exists at this commit; no new
capability is claimed.

Companion documents: `docs/product/RELEASE_1_0_READINESS.md` (what a 1.0
release requires), `docs/product/HARDWARE_CERTIFICATION.md` (physical
validation), `docs/deployment/appliance.md` (the installation and operating
runbook) and `docs/security/audit.md` (what is and is not logged).

**What this document deliberately does not define:** support tiers, response
times, SLAs, coverage hours, escalation targets or channels. Those are
commercial terms this repository has no basis to invent, and none may be
inferred from anything below. What is defined here is the *technical* support
surface: what exists, what to run, and what to collect.

**A caveat that governs every playbook:** per-camera connectivity does not
exist in production yet. No production code path provisions camera targets, so
the RTSP manager supervises zero cameras and `/status` omits `cameras`. Any
"camera is not working" case must first be triaged as this known gap rather
than as a camera fault. See §8.

## 1. The support surface, in three tiers

**Tier 1 — read-only, on the box, no SaaS needed.** Safe to run at any time,
including against a healthy production appliance.

| Command | What it answers |
| --- | --- |
| `geocam-edge check` | Is the agent READY? Exits non-zero if unreachable or not READY. Reads `GET /status` with a 3s timeout. Usable as a systemd/K8s exec health probe. |
| `geocam-edge version` | Version, commit, build date, platform, architecture. |
| `geocam-edge identity` | `edge_id`, identity source (`persisted` / `env-override`), data dir. On a *first* run it will persist a new identity — that is the only mutation. |
| `geocam-edge config` | Effective non-secret config: SaaS URL, processing mode, data dir, health address, heartbeat interval, discovery settings, connectivity settings, plus `enrollment_token: configured\|not configured` and `enrolled: yes\|no`. **Never** the token value or the credential. |
| `geocam-edge saas check` | Authenticated `GET /edge/me`. Distinguishes not-configured / not-enrolled / unreachable / timed out / credential-rejected. Non-zero exit on every failure class. |
| `geocam-edge discovery scan [--interface] [--timeout] [--json]` | One-shot ONVIF/WS-Discovery LAN scan. Prints IP:port/path, EPR, type, manufacturer, model, serial, firmware, auth-required, channel count. Inventory is in-memory only and never persisted. |
| `geocam-edge ota verify` | Fail-closed verification of an already-downloaded release: signature → checksum → forward-version → archive binding. In `-artifact-dir` mode it prints exactly one `artifact=<name>` line, and only on total success. |
| `curl -s localhost:8091/status` | The full JSON snapshot: lifecycle state, modules, per-subsystem blocks, host resources, queue depth/capacity/drops. |
| `journalctl -u geocam-edge` | Structured logs (`key=value`), with `version`, `processing_mode`, `edge_id` and `component` on every line. |

**Tier 2 — mutating, on the box, deliberate operator actions.**

| Command | Effect |
| --- | --- |
| `geocam-edge enroll` | Claims a one-time token and persists a locally-generated credential. Refuses if already enrolled. Token from stdin (preferred), `GEOCAM_ENROLLMENT_TOKEN`, or `--token`. |
| `geocam-edge credential rotate` | Generates a new credential locally (only its SHA-256 hash is sent), up to 3 attempts with 1s/2s/4s backoff, reusing the same `rotation_id` as an idempotency key. On failure the **old credential is left untouched on disk** — the operation is retried, never half-applied. |
| `geocam-edge factory-reset --confirm` | Destructive. See §4. |
| `sudo systemctl restart geocam-edge` | Restart the agent. |
| `sudo /opt/geocam-edge/current/scripts/rollback.sh` | Roll back to the previous release. Itself reversible. |
| `sudo /opt/geocam-edge/current/scripts/uninstall.sh [--purge]` | Remove the appliance. Preserves data and config unless `--purge` **and** an explicit typed confirmation. |

**Tier 3 — SaaS-initiated, outbound-only.** The Edge opens no inbound port and
the SaaS never connects to it. The control channel is an authenticated poll
with an allowlisted command set: `request_status`, `rediscovery`,
`reload_config`. `restart_video_pipeline` returns `UNSUPPORTED`; an unknown
command returns `UNKNOWN_COMMAND`; a non-empty payload is rejected. **There is
no shell or free-form execution path.**

**Supervision is systemd's, not the agent's:** `Type=notify`, sd_notify READY,
`WatchdogSec=60`, `Restart=on-failure`, `RestartSec=2`,
`StartLimitIntervalSec=300`, `StartLimitBurst=5`. A watchdog expiry or a crash
loop is a support event visible in `journalctl`, and a restart loop that
exhausts `StartLimitBurst` leaves the unit failed rather than flapping forever.

## 2. Triage ladder

Run in order. Each step either resolves the case or tells you which playbook in
§3 applies. Do not skip to a restart.

1. **Is the agent alive and ready?** `geocam-edge check`, then
   `curl -s localhost:8091/healthz` and `.../readyz`. `/healthz` is process
   liveness only (always 200 if the process serves). `/readyz` is 200 only when
   the state is READY **and** — in `edge` mode with the video pipeline enabled —
   the Vision Worker has reached ready.
2. **What does the box think it is?** `curl -s localhost:8091/status` and read,
   in this order: `status`, `processing_mode`, `profile`, `modules`, then the
   subsystem blocks.
3. **Is it enrolled and is the credential honored?** `geocam-edge config` for
   `enrolled`, then `geocam-edge saas check` for the authoritative answer. Local
   status alone proves nothing: only an authenticated call proves the SaaS still
   honors the credential.
4. **Can it reach the SaaS at all?** `saas check` distinguishes unreachable from
   timed-out from rejected. Check the network path and any corporate proxy
   before touching the agent.
5. **What do the logs say?** `journalctl -u geocam-edge -n 200 --no-pager`.
   Filter by `component=`.
6. **Are there resource or backpressure problems?** `resources` (`cpu`,
   `memory`, `disk`, `thermal`) and `queues` (`router[].drops`,
   `cloud_buffer`, `edge_backlog`, `vision`) in `/status`.
7. **Is it a release problem?** `geocam-edge version` against the intended
   version, plus the `update.sh` output and `$PREFIX/.previous`.

## 3. Failure playbooks

**SaaS unreachable (network down, proxy, firewall, SaaS incident).** The agent
**stays READY and `/readyz` stays 200** — local health is deliberately
independent of SaaS reachability. Degradation is visible only under
`/status` → `heartbeat`: `state=degraded`, `last_success_at`,
`last_attempt_at`, `consecutive_failures`, and a sanitized `last_error`
**class** (never a raw error string). Transient classes back off with jitter;
HTTP 429 honors `Retry-After`. Full Edge keeps inferring and persisting events
locally and cannot deliver them until the SaaS returns. **Do not restart to
"fix" this** — a restart neither helps nor clears it.

**Credential revoked or suspended (401/403).** The heartbeat module drops to a
slow poll (default 5 minutes, `GEOCAM_HEARTBEAT_AUTH_FAILURE_INTERVAL`,
bounded 100ms–30m) and the **whole agent goes DEGRADED**, so `/readyz` returns
503. The agent never deletes the credential, never generates a new one and never
re-enrolls on its own: recovery is an operator action. Re-enable the device in
the SaaS; the Edge notices on the next slow poll and returns to READY and
`/readyz` 200 **without a restart**. Worst-case delay is the auth-failure
interval. If rotation is the intended fix, a revoked device cannot self-rotate —
auth fails first — so SaaS-side admin allow-reenrollment is required.

**Corrupt `identity.json` or `credentials.json`.** Both are hard errors by
design and are never silently regenerated. The agent still starts so the health
surface stays diagnosable, but it never reaches READY and logs that a restart is
required to clear it. `geocam-edge identity` and `config` will show the problem.
Do **not** delete the files to "reset" (§4).

**Agent not READY but the process is up.** Read `modules` in `/status` — the
module map names which component failed. In `edge` mode with the pipeline
enabled, `/readyz` also waits on the Vision Worker; a slow model load is a
legitimate cause, and `vision.worker.state` distinguishes `not_configured`,
`model_missing`, `starting`, `ready`, `restarting`, `stopped` and `error`.

**Vision Worker not ready.** Check `vision.worker.state` and
`GEOCAM_EDGE_YOLO_WORKER_CMD`. Empty means not configured; the worker is
deliberately not shipped in the appliance package, so on a released appliance
this is expected until the Python runtime and weights are provisioned by hand.
`model_missing` means the weights are absent — nothing is auto-downloaded.
Check the worker-confirmed device in `vision.worker.device` rather than the
requested one: `cuda` is a request, and both layers fall back to CPU with a
visible warning when no CUDA device is present.

**Update failed or the appliance rolled back.** `update.sh` validates a
mandatory checksum, extracts and validates `ARCH`/`VERSION`, installs a new
versioned release, atomically repoints `current`, restarts the unit, then polls
`/readyz` (15 × 2s). If readiness never comes up it automatically invokes
`rollback.sh`, which swaps back to `$PREFIX/.previous` and verifies again,
finally failing with "manual intervention required" if still not ready.
`rollback.sh` is itself reversible. Data, credentials and identity are never
touched by install, update, rollback or uninstall. **Note:** on a target that is
not a real systemd host, the restart and readiness verification are skipped and
merely logged.

**Disk filling up.** Inspect `resources.disk.data_dir` and the queue drop
counters. Full Edge events and evidence have **no retention or eviction policy**,
and the minimum-free-disk guard covers JPEG captures but **not** MP4 clips — so
disk growth from evidence is unbounded and there is no automated cleanup.
Mitigation today is manual. Treat this as a known product gap, not a
misconfiguration.

**Frames or events being dropped.** Read `queues` (`router[].drops`,
`cloud_buffer`, `edge_backlog`, `vision`) and `cloud.frames_upload_failed`.
Every drop counter is a deliberate, counted policy decision, not a silent loss.
Note that the Cloud frame buffer has **no production default**: unless
`GEOCAM_CLOUD_BUFFER_MAX_BYTES` and `GEOCAM_CLOUD_BUFFER_MAX_FRAMES` are both
set positive, an unreachable SaaS means frames are dropped by policy rather than
spooled. The event backlog is different: it defaults to 100 operations /
512 MiB and **refuses** new records (counting the refusal) rather than
discarding.

**Camera not connecting.** Triage as the known gap first (§8). `discovery scan`
proves discovery works. There is no supported path today that turns a discovered
camera into an RTSP target, so `cameras` will be absent from `/status` and
heartbeats will carry an empty list even on a correctly installed appliance.

## 4. Data safety

`GEOCAM_DATA_DIR` (default `/var/lib/geocam-edge`) is persistent state that must
not be deleted to "reset" an appliance. The env example and
`docs/deployment/appliance.md` both say so explicitly, and the guarantee is
enforced by automated tests rather than by comment:
`deploy/appliance/appliance_test.go` asserts that install, update and rollback
never delete or overwrite its contents.

`factory-reset --confirm` is the one supported way to clear it, and its
behaviour is worth stating precisely:

- **Requires explicit confirmation.** Without `--confirm` it fails with
  `ErrConfirmationRequired`; there is no implicit or unattended path. It rejects
  an empty, `/` or `.` data dir, and every target is containment-checked.
- **Destroys**, via a fixed allowlist: `identity.json`, `credentials.json`,
  `camera_credentials.json`, `camera_master.key`, `remote_config_state.json`,
  `control_ledger.json`, `local-event-backlog`, `cloud-buffer`, `events`,
  `evidence`. Buffered events, evidence and camera secrets are permanently gone;
  the device returns to UNENROLLED.
- **Preserves** the installed binary, releases, systemd units,
  `/etc/geocam-edge/geocam-edge.env`, **and** — because they are not on the
  allowlist — `models/` (YOLO weights), `run/` (the worker socket) and `ota/`
  (pending releases and the apply request).
- **There is no undo, no backup and no restore command.** A lost
  `identity.json` means a new `edge_id`, i.e. a **new device identity** to the
  SaaS: identity is generated on the device and is never derived from hardware,
  so it cannot be reconstructed.
- **It does not stop the running daemon**, so it can race a live agent that still
  holds those files open. Stop the service first when practical.

`identity reset` is deliberately not implemented; `factory-reset` is the
supported operation. `uninstall.sh` preserves the data dir and config by
default, and `--purge` requires typing the literal `yes`.

## 5. Secret handling: what is guaranteed never to be logged

Support work involves handling output, so these guarantees matter:

- The raw enrollment token, the device credential (old or new), the RTSP camera
  password, the camera credential, and any `Authorization` header value are
  **never logged** anywhere in this repository. The logged fields are an
  enumerated, closed set (edge_id, device_id, tenant_id, site_id, rotation_id,
  command_id, command_type, data_dir, new_credential_version, status/error code).
- Heartbeat status carries a fixed error **class**, never a raw error string —
  raw transport errors can echo URLs and response bodies, and that field is
  served over HTTP.
- `/status` never carries the credential, a bearer header, a token or a hash;
  the vision block never carries a frame, an RTSP URI or a credential; evidence
  paths and event payloads never appear on the health endpoint; and the full
  remote-config document is never exposed because its knobs may be
  operationally sensitive.
- `geocam-edge config` and `saas check` deliberately never print the token value
  or the stored credential.
- The control channel has no shell path and its result payload is never logged.
- Enrollment-token hygiene: never persisted in plaintext, never passed on
  `argv` by `bootstrap.sh` (it is piped via stdin), and the seed file is
  shredded best-effort after use.

One honest limit: current output is **operational logging, not a tamper-evident
audit trail**. `docs/security/audit.md` records this as PARTIAL, with durable /
tamper-evident audit as NOT IMPLEMENTED — logs are not append-only, retention is
whatever host journald is configured for, and there is no separate integrity
channel. The control ledger is an idempotency record, not an audit log.

## 6. What to collect for a support case

There is **no support-bundle or diagnostics collector** in this repository, so
collection is manual. Gather, in this order:

1. `geocam-edge version`, `geocam-edge config`, `geocam-edge identity`,
   `geocam-edge saas check` output.
2. `curl -s localhost:8091/status` in full, plus `/healthz` and `/readyz`
   response codes.
3. `journalctl -u geocam-edge --since "<incident start>" --no-pager` (bounded to
   the incident window; logs contain no secrets, so they are safe to share, but
   they do contain `edge_id`).
4. `systemctl status geocam-edge` including the exit/restart history.
5. The release the unit is running: `readlink -f /opt/geocam-edge/current` and
   `cat /opt/geocam-edge/current/VERSION`, plus the last `update.sh`/
   `rollback.sh` output.
6. For upgrade issues: `geocam-edge ota verify` against the staged artifact
   directory, and `$GEOCAM_DATA_DIR/ota/state`.
7. Host context: architecture, OS image, kernel, RAM, storage, and whether the
   target is real systemd.
8. `geocam-edge discovery scan --json` when the case is about cameras — it
   establishes whether discovery sees them at all.

## 7. Honest gaps in the support surface

Each was verified by search, not assumed:

- **No support bundle / diagnostics collector.**
- **No remote shell and no inbound access path.** Control is outbound-poll only,
  allowlisted and shell-free.
- **No telemetry, monitoring or observability upload** beyond the existing
  heartbeat and contracts. No OpenTelemetry/Sentry/Datadog; the module has zero
  third-party dependencies, so no such SDK is even linkable.
- **No log shipping or aggregation.** Retention is entirely host journald.
- **No crash reporting or core-dump capture.**
- **No durable or tamper-evident audit trail**, and **no Edge→SaaS forwarding of
  Edge-side security events**.
- **No `/metrics` endpoint.** The HTTP surface is exactly `/healthz`, `/readyz`
  and `/status`, localhost-only by default.
- **No retention or eviction for events/evidence**, and no disk gate for clips.
- **No `factory-reset` undo and no backup** of identity, credentials or evidence.
- **No `status`, `logs`, `diagnostics` or `support` subcommand.**
- **No push revocation channel**; reactivation needs a SaaS-side admin flow.
- **No automatic credential-rotation schedule** (rotation is manual).
- Readiness verification during update/rollback is **skipped on non-systemd
  targets**.
- **No support/operations document** existed before this one — which is what Z5
  adds.

## 8. Known product gaps a support engineer will hit

Stated here so a support case is not misdiagnosed as a fault:

| Gap | Support impact |
| --- | --- |
| **No camera-target provisioning in production** | The RTSP manager supervises zero cameras; `/status` omits `cameras` and heartbeats carry an empty list on every profile. Discovery and inventory work. "Camera not connecting" is this, not a camera fault. |
| **The appliance does not ship the Python Vision Worker** | Full Edge cannot start from a released artifact without hand-provisioned Python, `ultralytics` and weights. `worker_not_configured` / `model_missing` are expected states on a fresh install. |
| **Remote-config reports `applied` when it cannot apply** | With the video pipeline disabled the module substitutes a no-op adapter and still ACKs success. Tuning can appear applied in the SaaS while changing nothing. |
| **Release artifacts omit the static `ffmpeg`** | The release workflow does not build it, so the video pipeline will not start without separately provisioning `ffmpeg`. |
| **No measured bandwidth or capacity numbers** | No figure in the repository may be quoted as a validated requirement; the ones that exist are synthetic or dev-host. |
| **CUDA is requested, never validated** | `cuda` silently reduces to CPU with a warning when unavailable. No GPU configuration may be treated as certified. |
| **`full_edge` status can appear in Cloud/Hybrid** | The service is pre-built for runtime mode transitions; read `profile` and `vision` to determine the actual mode rather than the presence of the block. |

## 9. Classification

| Item | Status |
| --- | --- |
| Read-only on-box diagnosis (`check`, `config`, `identity`, `version`, `saas check`, `discovery scan`, `ota verify`) | IMPLEMENTED / TESTED |
| Mutating operator actions (`enroll`, `credential rotate`, `factory-reset --confirm`) | IMPLEMENTED / TESTED |
| Local health surface (`/healthz`, `/readyz`, `/status`) | IMPLEMENTED / TESTED |
| Structured logging to stdout/journald with correlation fields | IMPLEMENTED / TESTED |
| Never-log guarantees for tokens, credentials, RTSP passwords, headers, payloads | IMPLEMENTED / TESTED (regression-tested) |
| systemd-native supervision, watchdog, bounded restart | IMPLEMENTED / TESTED (real-systemd behaviour NOT_VALIDATED) |
| Deterministic upgrade with automatic rollback | IMPLEMENTED / TESTED (physical appliance run NOT_VALIDATED) |
| Data-safety guarantees across install/update/rollback/uninstall | IMPLEMENTED / TESTED |
| Support bundle / diagnostics collector | **NOT IMPLEMENTED** |
| Remote shell / inbound access | **NOT IMPLEMENTED** (deliberate) |
| Telemetry upload, log shipping, crash reporting | **NOT IMPLEMENTED** |
| Durable / tamper-evident audit trail | **NOT IMPLEMENTED** (documented as PARTIAL) |
| `/metrics` endpoint | **NOT IMPLEMENTED** |
| Events/evidence retention | **NOT IMPLEMENTED** — see `docs/product/RELEASE_1_0_READINESS.md` §7 |
| `factory-reset` undo or backup | **NOT IMPLEMENTED** |
| Automatic credential rotation | **NOT IMPLEMENTED** |
| Recovery from a revoked credential without operator action | **NOT IMPLEMENTED** (by design) |
| **Commercial support terms** (SLA, hours, channels, escalation) | **NOT DEFINED** — deliberately out of scope; must not be inferred from this document |
| Support on real hardware / real systemd | **NOT_VALIDATED** — see `docs/product/HARDWARE_CERTIFICATION.md` |

## 10. How to close Z5

Z5 closes when this support model is accepted as the documented operating
surface and the gaps in §7 are either accepted as deliberate boundaries of the
1.0 support scope or scheduled. Two of them — the missing support bundle and the
absent events/evidence retention — are the ones most likely to generate real
support load, and `docs/product/RELEASE_1_0_READINESS.md` records the retention
decision as one that must be made explicitly rather than deferred by default.
