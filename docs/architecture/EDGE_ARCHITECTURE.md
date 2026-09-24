# GEO CAM Edge — Canonical Architecture

> **Purpose of this document.** This is the single entry point for understanding
> what Edge does end to end. It is intentionally an *overview*: every section
> links to the design document that owns the deep detail for that topic, so
> this file never duplicates — and therefore never drifts from — the specs
> that already exist. If this file and a linked doc ever disagree, the code is
> the tiebreaker (see `docs/README.md`), and this file should be corrected.

**Verified against:** `main` @ `6617322549e4d9ac815317a0724b92d3e4613045` (2026-09-20).

**Binary:** `geocam-edge` (`cmd/geocam-edge/main.go`) — one Go process, plus one
optional out-of-process Python worker for local inference (Full Edge only).
**Targets:** `linux/amd64`, `linux/arm64`.

---

## 1. The Go process

Everything except local YOLO inference runs inside a single `geocam-edge`
binary, wired together in `internal/agent/agent.go`. Subcommands
(`cmd/geocam-edge/main.go`): `run` (the daemon), `version`, `identity`,
`config`, `check`, `enroll`, `factory-reset`, `credential`, `discovery`,
`saas`, `ota`.

`geocam-edge run` starts the modules below, in order, and stops them in
reverse order on `SIGINT`/`SIGTERM` (graceful shutdown — see §21 and
`docs/operations/OFFLINE_AND_RECOVERY.md`).

## 2. ONVIF discovery

`internal/discovery/` runs WS-Discovery on the local LAN to find ONVIF
cameras. It is LAN-only by design — it does **not** discover across routed
subnets (`README.md`). Discovered devices feed an inventory with a
last-seen TTL; a camera that stops responding ages out of that inventory
rather than being removed immediately.

Deep dive: `docs/product/G1_CAMERA_TARGET_WIRING.md` (candidate keying,
inventory TTL, multichannel handling) and the walkthrough in
`docs/runbooks/CAMERA_FROM_SCRATCH_EDGE.md`.

## 3. Camera credentials

**`internal/credentials/`** is the Edge↔SaaS *enrollment* credential (a
rotatable secret from gateway enrollment, stored in `credentials.json`) — it
is not related to cameras. **`internal/cameracreds/`** is the actual camera
credential store, SaaS-synced and resolved by candidate key
(`internal/agent/camera_target_reconciler.go`). Credentials are never logged
or printed by any CLI command. See `docs/security/threat-model.md` for the
security posture, and `docs/product/G1_CAMERA_TARGET_WIRING.md` for the exact
resolution flow.

## 4. CameraTarget

The struct that connects discovery+credentials to the RTSP layer
(`internal/rtsp/supervisor.go`): candidate key, address, RTSP path, credentials,
stream role (main/sub), codec, resolution, FPS. Built by discovery/credential
resolution, reconciled into the running set via `rtsp.Manager.SetTargets()`
(`internal/rtsp/manager.go`), which diffs the new target list against running
supervisors — starting new ones, stopping removed ones, leaving unchanged ones
alone. Full field-by-field wiring: `docs/product/G1_CAMERA_TARGET_WIRING.md`.

## 5. RTSP Manager / Supervisor

One supervisor goroutine per camera (`internal/rtsp/supervisor.go`),
`connecting` only once at startup, then `online` / `degraded` / `auth_failed`,
plus `offline` when the supervisor itself is stopped (`internal/rtsp/types.go`).
Every dial failure — refused, timeout, or `401`/digest (`auth_failed`) — retries
automatically with exponential backoff (`InitialBackoff` 1s → `MaxBackoff` 60s)
using the *same* `CameraTarget`; `auth_failed` is not a special "stop and wait"
state, it just keeps failing the same way until `Manager.SetTargets()` replaces
the target with corrected credentials. `PacketTimeout` 5s marks a silent
stream as `degraded`, `DialTimeout` 5s bounds the TCP connect. Full detail and the exact
error taxonomy: `docs/runbooks/CAMERA_FROM_SCRATCH_EDGE.md` §F/§G.

## 6. Decode (ffmpeg)

`internal/processing/` invokes `ffmpeg` as a subprocess to decode the RTSP
stream. The appliance ships **static** ffmpeg binaries for `amd64`/`arm64`
built by `deploy/appliance/scripts/build-ffmpeg-static.sh` (see
`docs/ARCHITECTURE.md` for the exact build flags and binary sizes measured for
Hito H). A missing/failing ffmpeg is a fail-closed condition, not silently
skipped (`docs/ARCHITECTURE.md`, "H9").

## 7. Sampling

Decoded frames are sampled (resolution/FPS reduction) before being handed to
whichever sink the active profile enables — this is what keeps Gateway mode's
SaaS upload bandwidth bounded. No bandwidth-savings percentage is claimed here
unless it has actually been measured; see `docs/hito-z-bandwidth-decision.md`
for the one measured comparison that exists.

## 8. Gateway / Cloud mode

```
RTSP -> decode -> sample -> CloudSink -> SaaS -> YOLO (Cloud)
```

`GEOCAM_PROCESSING_MODE=cloud`, `GEOCAM_VIDEO_PIPELINE_ENABLED=true`. All
inference happens on the SaaS side; Edge only decodes, samples and uploads.

## 9. Hybrid mode

```
RTSP -> decode -> sample -> local motion/gating -> candidate frames -> SaaS -> YOLO (Cloud)
```

`GEOCAM_PROCESSING_MODE=hybrid`. A local evaluator filters frames before they
leave the device, but the SaaS still runs the actual YOLO inference — Edge
never claims local detection in this mode. Capability matrix and exact
gating behavior: `docs/product/COMMERCIAL_MODES.md`.

## 10. Full Edge mode

```
RTSP -> decode -> sample -> Vision Worker (local YOLO) -> event + evidence -> durable backlog -> SaaS
```

`GEOCAM_PROCESSING_MODE=edge`. Inference happens locally via the out-of-process
Python worker (§11); the Cloud never runs inference for this camera. Full
walkthrough: `docs/runbooks/FULL_EDGE_FROM_SCRATCH.md`.

There is also a **Gateway (no media)** configuration
(`GEOCAM_VIDEO_PIPELINE_ENABLED=false`): ONVIF discovery, health, control and
OTA run, but no video is decoded or uploaded at all. See
`docs/product/COMMERCIAL_MODES.md` for the full profile matrix.

## 11. Vision Worker (Python, out-of-process)

`deploy/vision-worker/{worker.py,backend.py}`. PyTorch/ultralytics are never
embedded in the Go binary — the appliance ships the worker's *sources* only,
never a Python runtime, because ultralytics/PyTorch wheels are
architecture-specific (`deploy/appliance/scripts/install.sh` comment block).
Protocol: newline-delimited JSON over a Unix domain socket
(`GEOCAM_EDGE_YOLO_SOCKET_PATH`, default `/run/geocam-edge/vision.sock`) —
`health` / `infer` / `shutdown` requests, mirrored on the Go side by
`internal/vision/`. A bad JPEG or a worker crash never panics the Go agent;
it degrades to `model_missing`/error reporting. Full setup and failure modes:
`docs/runbooks/FULL_EDGE_FROM_SCRATCH.md`.

## 12. Models

Fixed defaults, confirmed in `internal/config/config.go`: person
`yolo11s-pose.pt`, vehicle `yolo11n.pt` — both overridable via Remote Config
(§17), never auto-selected. **No auto-download exists anywhere in this repo.**
The operator places weights under `$GEOCAM_DATA_DIR/models` (default
`/var/lib/geocam-edge/models`); a missing weight is a first-class
`model_missing` state, not an error that blocks the rest of the agent.

## 13. Events

Full Edge writes one JSON per detection event under
`{GEOCAM_DATA_DIR}/events/<event_uuid>.json` (`internal/fulledge/service.go`).
Immutable once written; referenced by the durable backlog until synced.

## 14. Evidence

JPEG captures (`{GEOCAM_DATA_DIR}/evidence/captures/<uuid>.jpg`) and MP4 clips
(`{GEOCAM_DATA_DIR}/evidence/clips/<event_uuid>.mp4`, produced by ffmpeg). A
single JPEG can be referenced by more than one event record (refcounted) —
retention must never delete a JPEG that is still referenced. Full ownership
map and eviction rules: `docs/product/B3_RETENTION_DESIGN.md` and
`docs/operations/RETENTION.md`.

## 15. Durable backlog

`internal/edgebacklog/` persists pending event records to
`{GEOCAM_DATA_DIR}/local-event-backlog/pending/*.json`, strict FIFO, durable
across restarts. A pending record always protects its own evidence files from
retention, regardless of any retention flag (§ below, verified in code).

## 16. SaaS sync

Heartbeat (`internal/heartbeat/`) reports status/cameras/queues periodically;
the SaaS — not the Edge — decides when to consider an Edge offline based on
missed heartbeats. A control ledger (`internal/control/`) records SaaS
instructions durably and append-only.

## 17. Remote config

`internal/remoteconfig/` applies SaaS-pushed configuration changes (e.g.
processing mode, YOLO model selection) at runtime, without a restart, with
validation before apply. Full behavior: `docs/REMOTE_CONFIG.md`.

## 18. Heartbeat

Periodic report to the SaaS control channel; also the mechanism that keeps the
`/status` `heartbeat` module populated. See `docs/CONTROL_CHANNEL.md`.

## 19. OTA / update

Signed with Ed25519 (`geocam-edge ota sign` / `ota verify`); no artifact is
ever accepted without a valid signature and checksum. Full workflow, from
tagging a release to applying it on a device:
`docs/runbooks/EDGE_RELEASE_UPDATE_ROLLBACK.md`.

## 20. Retention

Count/byte/age bounds per artifact type (events/captures/clips), oldest-first
eviction, refcount-aware, pending-aware. Operational reference:
`docs/operations/RETENTION.md`. Full design and invariants:
`docs/product/B3_RETENTION_DESIGN.md`.

## 21. Health / status

`GET /healthz`, `/readyz`, `/operationalz`, `/status` on `127.0.0.1:8091` by default
(`GEOCAM_HEALTH_ADDR`, `internal/agent/health_module.go`) — loopback-only,
deliberately. `geocam-edge check` wraps `/status` into a human-readable
summary (`cmd/geocam-edge/main.go`). Process readiness and camera/video
operational readiness are distinct: an offline camera does not make the agent
process unhealthy, while the separate operational endpoint and CLI report
unavailable configured targets.

The standalone lifecycle command (`geocam-edge service`) uses the native
service manager: the existing systemd appliance unit on Linux, a per-user
LaunchAgent on macOS, and SCM on Windows. macOS/Windows instances also use an
OS-owned exclusive lock per canonical data directory and a bounded in-process
retry supervisor. Linux retains the package-owned unit and its existing
`Type=notify`, `WatchdogSec=60`, and start-rate limits
(`deploy/appliance/systemd/geocam-edge.service.in`). Camera, RTSP and SaaS
retries remain inside their respective modules; they do not trigger whole-agent
restarts. Operational setup and recovery: `docs/operations/EDGE_SERVICE_LIFECYCLE.md`
and `docs/operations/EDGE_RECOVERY_RUNBOOK.md`.

---

## What this document does not cover

- **Security posture and threat model** — `docs/security/`.
- **Corporate network topologies / hardware SKUs** — `docs/deployment/`.
- **Historical, chronological engineering decisions through Hito I** (exact
  ffmpeg build flags, decode latency numbers, RTSP corpus test results) —
  `docs/ARCHITECTURE.md`, kept as a decision log, not superseded by this file.
- **What is physically validated vs. only code-complete** —
  `docs/product/PHYSICAL_VALIDATION_REGISTER.md`.

See `docs/README.md` for the full documentation index.
