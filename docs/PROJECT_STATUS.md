# PROJECT STATUS — Where we stand right now

> Answers one question: **"¿Dónde estamos parados ahora?"**
> This document is the real state of the project at this moment. If it disagrees
> with anyone's memory, this document and Git win.

## Snapshot

| Field             | Value                                                     |
| ----------------- | ----------------------------------------------------------- |
| **PROJECT**       | GEO CAM Edge                                              |
| **CURRENT HITO**  | I — Modo Cloud (first slice: Edge→SaaS frame push)        |
| **STATE**         | IMPLEMENTED / UNIT-TESTED — NO real-camera validation yet — NOT REVIEWED |
| **MERGED**        | Hito G: **YES** (PRs #7/#8/#9, `main` @ `50c7de7`). Hito H: **YES** (PR #10, `main` @ `f7263b3`). Hito I: **NO** — PR open on `feature/cloud-video-sink` (Edge) / `feature/edge-frame-push` (SaaS, `monitoreoia`) |
| **Branch**        | `feature/cloud-video-sink`                                   |
| **DEPLOYED PROD** | **NO** — VPS/production untouched                         |
| **Go version**    | 1.26.2                                                    |

Hitos A through H are merged into `main` (Hito G via PRs #7/#8/#9, Hito H via
PR #10). This snapshot previously said Hito H's PR was still open — that was
stale; corrected here as part of Hito I per AGENTS.md's "keep PROJECT_STATUS
accurate" rule. Hito I's first slice (Edge→SaaS frame push, `docs/ROADMAP.md`
items I1/I3/I4/I5/I8) is implemented and unit-tested on both repos
(`go test -race ./...` clean on the Edge; SaaS unit tests clean, its
PostgreSQL-backed integration tests are written but not run in this
environment — no local Postgres). **No real-camera validation has been done
for this slice** — see the Hito I section below.

## Hito A — what was implemented (MERGED)

- `cmd/geocam-edge` — thin entrypoint
- `internal/agent` — agent core, lifecycle, graceful shutdown
- `internal/config` — environment-based configuration
- `internal/identity` — identity/enrollment abstraction (env-var based, not persisted)
- `internal/platform` — platform and hardware detection
- `internal/health` — internal health (no HTTP endpoint)
- `internal/logging` — structured logging with `log/slog`
- Typed processing modes: `cloud` / `hybrid` / `edge`
- Agent versioning
- OCI Dockerfile, distroless / non-root image, cross-build
- Local Helm chart
- K3s local deployment
- CI

## Hito B — Agent Core (this branch)

B1–B10 gap analysis against Hito A, and what closed each gap:

- **B1 (main process)** — already existed (Hito A). No change needed.
- **B2/B3 (persistent identity / edge_id)** — the real gap. Hito A's identity
  was an env-var-derived value, never written to disk; a restart with a
  different/empty env var silently produced a different identity. Closed:
  `internal/identity` now generates a random UUID v4 **once** and persists it
  to `<data-dir>/identity.json` (atomic write, `0600`/`0700` perms). A
  corrupted file is a **hard error**, never silently regenerated.
- **B4 (config)** — already existed; extended with `GEOCAM_HEALTH_ADDR` and
  documented `GEOCAM_EDGE_ID` as a non-persisting dev override (see below).
- **B5 (CLI)** — Hito A only had `--version`. Closed: added `identity` and
  `check` subcommands (stdlib `flag`/`os.Args`, no framework).
- **B6 (internal state)** — `health.Reporter` already existed as a
  thread-safe accessor; extended with per-module state tracking rather than
  introducing a second state abstraction.
- **B7 (module lifecycle)** — did not exist. Closed: `internal/agent/modules.go`,
  a minimal `Module` interface + ordered start/stop manager.
- **B8/B9/B10 (local health HTTP)** — Hito A explicitly had no HTTP server
  ("no port"). Closed: `internal/health/handler.go` (stdlib `net/http`,
  `/healthz`, `/readyz`, `/status`), run as the first (and so far only)
  `Module`.

## Files created/modified for Hito B

- `internal/identity/identity.go` (rewritten), `internal/identity/store.go` (new),
  `internal/identity/uuid.go` (new), `internal/identity/identity_test.go` (rewritten)
- `internal/config/config.go`, `internal/config/config_test.go` — `GEOCAM_HEALTH_ADDR`
- `internal/health/health.go` — module state tracking, `Modules` in `Snapshot`
- `internal/health/handler.go` (new), `internal/health/handler_test.go` (new)
- `internal/health/health_test.go` — updated for the new `Snapshot` shape
- `internal/agent/agent.go` — wires identity load, module manager, DEGRADED-on-failure
- `internal/agent/modules.go` (new), `internal/agent/health_module.go` (new),
  `internal/agent/modules_test.go` (new)
- `internal/agent/agent_test.go` — degraded-state and module-failure coverage
- `cmd/geocam-edge/main.go` — `identity` and `check` subcommands
- `cmd/geocam-edge/main_test.go` (new)
- `deploy/helm/geocam-edge/values.yaml` — `config.healthAddr`, `persistence`, `probes`
- `deploy/helm/geocam-edge/templates/configmap.yaml` — `GEOCAM_HEALTH_ADDR`
- `deploy/helm/geocam-edge/templates/deployment.yaml` — probes, container port, PVC volume
- `deploy/helm/geocam-edge/templates/pvc.yaml` (new)
- `docs/PROJECT_STATUS.md`, `docs/ROADMAP.md`, `docs/ARCHITECTURE.md`, `README.md`

## identity.json

- Path: `<GEOCAM_DATA_DIR>/identity.json` (default data dir `/var/lib/geocam-edge`).
- Schema: `{"edge_id": "<uuid-v4>", "created_at": "<RFC3339>", "schema_version": 1}`.
- No secrets. Permissions: data dir `0700`, file `0600`. Written via
  temp-file-then-rename (atomic).
- `edge_id` is generated once, is a random UUID (never derived from
  hostname/MAC/serial/machine-id), and is reused on every subsequent start.
- A corrupt file (bad JSON, invalid UUID, unsupported `schema_version`) is a
  hard error: the agent starts but never reaches `READY` (stays `DEGRADED`),
  and never silently regenerates a new identity.
- `GEOCAM_EDGE_ID` remains as an explicit **dev override**: when set, it is
  used verbatim and `identity.json` is neither read nor written. This is
  documented as non-authoritative — `identity.json` is the single source of
  truth for anything not using the override.
- `identity reset` CLI subcommand: **deliberately not implemented** (see
  scope cuts below).

## CLI

- `geocam-edge` (no args) — starts and stays alive as the agent (unchanged).
- `geocam-edge --version` / `-version` — unchanged.
- `geocam-edge identity` — resolves (and persists, on first run) identity;
  prints `edge_id`, `identity_source`, `version`, `architecture`,
  `processing_mode`, `data_dir`. No secrets.
- `geocam-edge check` — queries a running agent's local `/status` over
  `GEOCAM_HEALTH_ADDR`; exits non-zero if unreachable or not `READY`. Does
  not start a full agent itself.

## Health HTTP

- `GET /healthz` → `200` if the process is alive.
- `GET /readyz` → `200` only when state is `READY`, `503` otherwise.
- `GET /status` → JSON snapshot (status, edge_id, version, processing_mode,
  hostname, architecture, uptime_seconds, modules). No secrets.
- Bind address: `GEOCAM_HEALTH_ADDR`, default `127.0.0.1:8091`
  (localhost-only by default).
- Verified locally: `/healthz` → 200, `/readyz` → 200 once `READY` / 503 while
  `DEGRADED`, `/status` returns the documented JSON shape. A corrupted
  `identity.json` was verified end-to-end to produce `DEGRADED` +
  `/readyz` 503, with the process staying alive and shutting down cleanly on
  `SIGTERM` (exit 0) rather than crash-looping.

## Module lifecycle

Implemented: `internal/agent/modules.go` (`Module` interface: `Name()`,
`Start(ctx)`, `Stop(ctx)`; a small manager that starts modules in order and
stops them in reverse order, unwinding on a failed start, propagating context
cancellation). The only real module today is the health HTTP server
(`internal/agent/health_module.go`). No discovery/transport/heartbeat/camera
module exists yet — out of scope for B, per the task.

## Validations actually observed

| Check                   | Result                                        |
| ------------------------ | --------------------------------------------- |
| `go test ./...`         | PASS                                          |
| `go test -race ./...`   | PASS (no race-detector limitations observed)  |
| `go vet ./...`          | PASS                                          |
| `gofmt -l .`            | clean                                         |
| Local build             | PASS                                          |
| `linux/amd64` build     | PASS (`CGO_ENABLED=0`)                        |
| `linux/arm64` build     | PASS (`CGO_ENABLED=0`)                        |

## Local runtime validation

- Ran the locally built binary directly: identity created and reused across
  restarts (same `edge_id` in an isolated dev data dir), `/healthz` → 200,
  `/readyz` → 200 once `READY`, `/status` JSON verified, `identity`/`check`
  CLI subcommands verified against a live process, `SIGTERM` produced a
  clean graceful shutdown (log line `shutdown signal received`).
- Local SaaS: **not modified**.
- VPS / production: **not touched**.

## K3s local validation

- `kubectl config current-context` confirmed `rancher-desktop` before any
  cluster action.
- Image built locally with `nerdctl --namespace k8s.io build` (no Docker
  Engine) and loaded straight into containerd.
- Helm chart updated: `readinessProbe` (`GET /readyz`), `livenessProbe`
  (`GET /healthz`), a `health` container port (`8091`), `GEOCAM_HEALTH_ADDR`
  set to `0.0.0.0:8091` in the ConfigMap (so kubelet can reach the pod), and
  a new `geocam-edge-data` PVC (`local-path`, `128Mi`, RWO) replacing the
  previous `emptyDir`, mounted at `GEOCAM_DATA_DIR`.
- `helm upgrade geocam-edge` on the existing `geocam-edge-dev` release
  (revision 1 → 2) — namespace and SaaS resources untouched.
- Result: pod `1/1 Running`/`Ready`, PVC `Bound`, both probes green.
- **Critical identity test (PASS)**: captured `edge_id` from the running pod
  (`f688d30f-4b12-40e1-8409-cfe314ab1755`), ran `kubectl delete pod`, waited
  for the Deployment to recreate it, confirmed `Running`/`Ready` again, and
  re-read the identity from the new pod — **exact same `edge_id`**. Identity
  survives pod recreation because it lives on the PVC, not the container
  filesystem.
- Logs on the recreated pod: startup, identity resolved (`source=persisted`),
  `agent ready` — no secrets logged.

## Scope cuts (deliberate)

- `identity reset` CLI subcommand: not implemented. No automatic factory
  reset exists either, per the task's hard requirement. Adding a gated manual
  reset command was judged non-trivial enough (needs to be unambiguous and
  safely gated) to leave out of this milestone rather than rush it in.
- No real module beyond the health HTTP server: discovery/transport/
  heartbeat/camera modules are explicitly future work (Hito C onward).

## Current restrictions

- This branch is not merged to `main`.
- Production is out of scope until explicitly authorized.
- The SaaS repo (`monitoreoia`) is not modified from here.

## Not implemented yet

Do not infer DONE just because something appears in the architecture document.
None of the following exist yet:

FFmpeg, OpenCV, YOLO, PyTorch, Vision Worker, video pipeline (decode/sampling/
frame routing), AI processing, real WebSocket, VPN, OTA.

Through Hito G, the following ARE implemented, merged to `main`, and exposed
via the `geocam-edge` CLI: gateway enrollment and credential rotation (Hito
C), SaaS heartbeat (Hito D), ONVIF WS-Discovery/autodiscovery (Hito E),
per-device/per-group camera credential management (Hito F), and RTSP camera
connectivity with reconnection/health state (Hito G) — see Hito C through
Hito G below.

## Hito C: Enrollment con SaaS — DONE / MERGED / VALIDATED

Added on `feature/edge-enrollment` (branched from `main` at `4ce560d`, which
already has Hito B merged). The SaaS contract below was verified against the
real `monitoreoia` source (gateway enroll router, `authenticate_edge_device`,
self-service rotate-key router) — it is confirmed, not assumed.

**Zero-knowledge credential model.** The Edge — never the SaaS — generates
the device credential. The SaaS only ever receives and persists a SHA-256
hash of it:
- `geocam-edge enroll` generates a credential locally
  (`credentials.GenerateCredential`, `edg_live_<32 random bytes, base64url>`,
  `crypto/rand`), sends only `device_key_hash = sha256(credential)` to
  `POST /api/v1/gateway/enroll` together with `gateway_instance_id` (the
  agent's `edge_id`, required, 8-128 chars) and `agent_version`/`platform`/
  `architecture`. There is no `hostname` field in the real contract; the
  hostname is not sent (decision — see report). The 200/201 response has
  only `device_id`/`device_kind`, no org/site/enrolled_at.
- Once the claim succeeds, the Edge immediately calls
  `GET /api/v1/edge/me`, authenticated with the credential it just
  generated, to learn `organization_id`/`organization_name`/`site_id`/
  `site_name`. If that call fails (network/timeout), the credential — which
  is already valid server-side — is still persisted, with empty org/site and
  a warning telling the operator to run `geocam-edge check` or retry later;
  discarding it here would strand a device that is enrolled server-side but
  can never authenticate.
- `geocam-edge credential rotate` generates a new credential B locally,
  authenticates with the CURRENT credential A (`X-Device-Id` + `Authorization:
  Bearer`) and calls `POST /api/v1/edge/me/rotate-key` with
  `{device_key_hash: sha256(B), rotation_id}` (`rotation_id` is a
  client-generated UUID v4, `identity.NewUUIDv4`, reused as an idempotency
  key). On network failure it retries the SAME request (same rotation_id,
  same hash) up to 3 attempts total with 1s/2s/4s backoff, and only persists
  B after the SaaS ACKs. A 401/403 (revoked credential) is not retried. If
  all 3 attempts fail, A is left untouched on disk.
- Every authenticated call sends BOTH `X-Device-Id` and
  `Authorization: Bearer <credential>` — the SaaS's `authenticate_edge_device`
  requires the device id as its own header even when the credential arrives
  via Bearer.
- `internal/transport` — stdlib `net/http` client: `Enroll`, `Me`,
  `RotateKey`. Typed sentinel errors: `ErrTokenInvalid` (every enroll 401 —
  invalid/expired/used/mismatched token are deliberately indistinguishable,
  anti-enumeration, never inferred from response text), `ErrAlreadyEnrolled`
  (409), `ErrInvalidRequest` (422, local payload validation failure),
  `ErrUnauthorized`, `ErrSaaSUnavailable`, `ErrTimeout`, `ErrInsecureURL`.
- `internal/credentials` — `credentials.json` storage (unchanged atomic
  write mechanism: temp file + rename, dir 0700, file 0600, corrupt file =
  hard error) plus `GenerateCredential`/`HashCredential`.
- `GEOCAM_ALLOW_INSECURE_HTTP` — config flag (`internal/config`). An
  `http://` `GEOCAM_SAAS_URL` is rejected at config-load time unless this is
  explicitly `"true"`. Never disables TLS verification for `https://`.
- `health.Snapshot` gained `credential_status` (`UNENROLLED`/`ENROLLED`),
  distinct from `enrollment_status` (identity resolution, Hito B). The
  credential secret is never in `Snapshot`.
- Agent startup: a corrupt `credentials.json` puts the agent in DEGRADED,
  never READY, never crashes.

**Not done / explicitly out of scope for this pass:**
- No explicit `--force` re-enrollment flow (only the double-enrollment
  guard on `enroll`). Re-enrolling today means manually removing
  `credentials.json`.
- No cross-restart persistence for a partially-failed rotation (documented
  `ponytail:` in `cmd/geocam-edge/main.go`): rotation retries only within one
  process invocation, up to 3 attempts.

**Update (Hito D closure):** enrollment/rotation were exercised end-to-end
against the real local `monitoreoia` SaaS and validated inside K3s (pod
recreation, PVC-backed identity/credential survival) as part of the Hito D
E2E — see below. Hito C is DONE / MERGED / VALIDATED, not just code-complete.

## Hito D: Heartbeat Edge → SaaS — DONE / VALIDATED LOCAL

Added on `feature/edge-heartbeat` (branched from `main`, Hito C merged).

**Module, not a loop.** `internal/heartbeat` implements the Hito B `Module`
interface and is registered in `internal/agent`, so it starts and stops with
the rest of the agent. It is skipped entirely for an unenrolled Edge, and a
construction error is non-fatal: a misconfigured SaaS URL must not take down
the local health surface an operator would use to diagnose it (the agent does
not reach READY in that case).

**Transport.** `transport.Heartbeat` posts to the shared endpoint reusing the
Hito C client — no duplicated HTTP client — and sends both `X-Device-Id` and
`Authorization: Bearer <credential>`, exactly like every other authenticated
call.

**Payload — reconciled against the real SaaS model.** `EdgeHeartbeatPayload`
in `monitoreoia` is `extra="forbid"`, so an unknown field is a `422`. Two
names were reconciled instead of duplicated: `agent_version` → `edge_version`
(already populated by the legacy Python agents and rendered by the admin UI),
and `system{...}` → `metrics{...}` (the existing telemetry object, which
already carried `cpu_percent`).

Sent: `edge_id, edge_version, uptime_seconds, architecture, processing_mode,
health_status, metrics{cpu_percent, memory_total_bytes, memory_used_bytes,
disk_total_bytes, disk_used_bytes, temperature_c}`, plus
`boot_id`/`sequence_number` (reusing the existing legacy
fields so the SaaS can discard a stale snapshot that overtakes a newer one)
and a diagnostic-only `edge_timestamp`. It carries **no tenant and no site**:
the SaaS derives those from the credential. `edge_id` is a cross-check for the
SaaS, never an identity claim.

**Metrics** (`internal/platform`, `CGO_ENABLED=0`): CPU from procfs deltas
(the sampler primes before reporting, so the first heartbeat omits
`cpu_percent` rather than inventing one), memory from `/proc/meminfo`, disk
via `statfs` on `GEOCAM_DATA_DIR` (walking up to an existing ancestor),
temperature from `/sys/class/thermal/` picking the hottest plausible zone.
Every metric is omitted rather than zeroed when unavailable, and a missing
sensor never marks DEGRADED.

**Scheduling.** `GEOCAM_HEARTBEAT_INTERVAL`, default `30s`, bounded `5s`–`5m`
inclusive (out of range = startup error). First send spread across a startup
window so a restarting fleet does not stampede. Backoff 1s→60s with ±10%
jitter, reset on the first success.

**Error classification.** timeout/network/5xx → transient backoff; 429 →
honour `Retry-After` (falling back to backoff when absent/malformed); 422 →
own class, since retrying an identical body cannot help; 401/403 → agent
DEGRADED, no aggressive retry, credential **never** discarded, **no**
re-enrollment and **no** new credential generated.

**Health semantics.** A SaaS outage leaves the Edge `READY` with `/healthz`
and `/readyz` both `200` — local function is unaffected by an outage in a
service the Edge only reports to. The degradation appears only in `/status`
under `heartbeat` (`state`, `last_success_at`, `last_attempt_at`,
`consecutive_failures`, sanitized `last_error` **class**). A rejected
credential is the one SaaS-side condition that degrades the whole agent.
`/status` never carries the credential, a Bearer header, a token or a hash.

**Validated end-to-end against the real local `monitoreoia` SaaS** (not just
`httptest`-mocked unit tests) and in K3s:
- Online: heartbeat received, `last_seen` updated server-side, UI shows
  Online, uptime/version/architecture/CPU/RAM/disk all present.
- Offline: after the offline threshold with no heartbeat, UI shows Offline.
- Recovery: restarting the Edge resumes heartbeating with the same
  `edge_id` and credential (no re-enrollment) and the SaaS returns to
  Online.
- SaaS unavailable → degraded/recovery: stopping the local SaaS leaves the
  Edge `READY` (no crash-loop) with `heartbeat.state` reporting the
  degradation in `/status`; restarting the SaaS recovers automatically.
- Revocation → 401/403: the Edge goes DEGRADED locally, does not
  re-enroll, does not generate a new credential, and does not retry
  aggressively.
- K3s: full pod recreation exercised in Rancher Desktop K3s. Same `edge_id`
  and credential survive the pod being deleted and recreated (PVC-backed
  identity), a fresh `boot_id` is issued per process, `sequence_number`
  restarts correctly under the new `boot_id`, and the SaaS observes ≥2
  heartbeats and Online status after recreation.

**Not done / explicitly out of scope for this pass:**
- No timeseries: the SaaS stores the latest snapshot only (MVP).

## Hito E — what was implemented (THIS BRANCH)

- **Pure Go stdlib implementation**: Zero external dependencies, `CGO_ENABLED=0`, cross-compilation verified for `linux/amd64` and `linux/arm64`.
- **Architectural decoupling**: 1 IP != 1 camera; devices, video sources (channels), and profiles are separated.
- **WS-Discovery**: UDP multicast probe on `239.255.255.250:3702`, UUIDv4 message IDs, bounded XML parser rejecting DTDs and oversized payloads.
- **Strict Security Limits**: Enforces 4s timeouts, max 200 datagrams, 64 candidates, 16KB per datagram, 16 matches/datagram, 8 XAddrs/match. Rejects non-private IPs, loopback (127/8), and cloud metadata (169.254.169.254).
- **Safe SOAP Enrichment**: `GetDeviceInformation`, `GetCapabilities`, `GetVideoSources`, `GetProfiles`, `GetStreamUri`. Any 401/fault marks `AuthRequired = true` without throwing. Credentials completely stripped from RTSP URIs.
- **Thread-safe Local Inventory**: `internal/discovery/Inventory` with atomic upsert, first_seen preservation, last_seen renewal, channel tracking.
- **SaaS Pull Contract Integration**: Reuses existing `/api/v1/gateway/discovery/next` and `/api/v1/gateway/discovery/runs/{id}/report` endpoints.
- **CLI Subcommand**: `geocam-edge discovery scan [--interface <iface>] [--timeout <duration>] [--json]`. Clean terminal output with zero secrets.
- **LAN Validation**: Verified against physical camera on LAN (Tapo TC70 detected at 192.168.0.6:2020).

## Hito F — Camera Credentials (MERGED)

- Credential storage with AES-256-GCM encryption at rest.
- DEVICE and GROUP scoped credential assignment and resolution.
- ONVIF authentication and credential testing against physical hardware.
- RTSP Digest authentication test client.
- Merged into `main` via PR #7.

## Hito G — what was implemented (THIS BRANCH)

- **Pure Go stdlib RTSP Client**: `internal/rtsp` handles DESCRIBE -> SETUP -> PLAY -> ReadPacket -> TEARDOWN over TCP interleaved `RTP/AVP/TCP;unicast;interleaved=0-1`. Zero external C/FFmpeg dependencies (`CGO_ENABLED=0`).
- **Resilient Reconnection**: `Supervisor` implements bounded exponential backoff (1s -> 2s -> 4s -> ... -> max 60s) with clean context cancellation.
- **Silence & Timeout Detection**: 5s packet read timeout declarations with automatic degradation and reconnection.
- **Camera Health State Machine**: State transitions (`connecting`, `online`, `degraded`, `offline`) captured in thread-safe `CameraStreamStatus`.
- **Stream Role Selection**: Main/substream selection defaulting to `sub` (configurable via `GEOCAM_STREAM_ROLE`).
- **ONVIF Metadata Mapping**: Extracts codec, resolution (width/height), and nominal FPS from ONVIF media profiles.
- **Heartbeat Telemetry Integration**: Telemetry payload `cameras: [...]` added to `HeartbeatRequest`, ingested and persisted in PostgreSQL `edge_camera_status` table.
- **SaaS UI Enrichment**: Candidate rows in `edge_devices.js` display real-time stream status badge (`Online`, `Conectando`, `Degradado`, `Offline`) and technical stream parameters.
- **Help Center**: Added comprehensive documentation for camera connectivity under `/dispositivos-edge`.
- **Strict Credential Privacy**: No secrets, tokens, or plaintext passwords logged or returned in status payloads.
- **LAN Live Camera Validation**: Verified against physical Tapo TC70 camera (`192.168.0.6:554/stream2`), reading 20+ live interleaved packets in 1.47s.

## Hito H — Video Pipeline (MERGED, PR #10, `feature/video-pipeline`)

Independent video pipeline downstream of Hito G's existing RTSP/RTP
transport — no second RTSP client, no duplicated credentials/reconnect/health.
New package `internal/processing`.

**Architecture decisions:**
- **Decoder: FFmpeg run as an OS subprocess** (`os/exec`, one process per
  camera, Annex-B via stdin, raw yuv420p via stdout) — not cgo bindings
  (would require `CGO_ENABLED=1`, breaking the static build), not a pure-Go
  decoder (none production-grade for H.264 main/high exists), not a
  hand-written decoder (explicitly out of scope). `go.mod` gains zero
  dependencies; this is a runtime dependency only.
- **Docker image / ffmpeg — GPL→LGPL-only migration, verified on BOTH
  architectures.** Originally used the `mwader/static-ffmpeg` prebuilt
  image, found to be GPL-licensed (`libx264`/`libx265` enabled, confirmed
  both from its own Dockerfile source and directly from the running
  binary's `-version` output) — reported explicitly rather than assumed
  LGPL-safe. **Resolved**: the Dockerfile's `ffmpeg-build` stage now
  compiles ffmpeg 7.1.5 from official source with `--disable-everything`
  plus only the six components this pipeline's exact command needs, no
  `--enable-gpl`/`--enable-nonfree`/libx264/libx265. Verified on
  `linux/arm64` locally (`make image`: build PASS, decode PASS against a
  synthetic clip, `-version` confirms no GPL/nonfree flags, image shrank
  55.69MB→5MB compressed) **and on `linux/amd64` via a dedicated CI job on
  a real amd64 GitHub-hosted runner** (`.github/workflows/ci.yml`'s
  `docker-amd64-smoke`, since compiling ffmpeg from source under this dev
  machine's local QEMU emulation was too slow to run to completion —
  native CI hardware finished in ~2 minutes vs. 30+ and counting under
  emulation): architecture confirmed `amd64`, `nonroot:nonroot` confirmed,
  `-version` has none of the four disallowed flags, the exact production
  decode command produces exactly the expected byte count, and
  `geocam-edge` itself starts with the container staying up. See
  `docs/ARCHITECTURE.md` for the full recipe, component-by-component
  rationale, and the factual (non-legal-opinion) LGPL compliance notes.
  **Both architectures now verified — no remaining gap on this item.**
- **Codec ground truth bug found during real validation**: this repo's TC70
  ONVIF `GetProfiles` response mismaps video/audio encoder metadata,
  reporting `G711` for both the main and sub profiles regardless of actual
  video codec (pre-existing Hito E concern, not introduced here). Fixed
  *for the video pipeline's own decisions* by parsing the codec SDP itself
  declares (`a=rtpmap`, RFC 4566) in `internal/rtsp/client.go` and having
  `Supervisor` prefer it over the ONVIF-reported value when building the
  `StreamDescriptor` used to select a decoder — `CameraStreamStatus.Codec`
  (surfaced elsewhere) is untouched. `internal/discovery/onvif`'s own
  parsing bug itself was not touched (out of Hito H's scope).

**H1 — Ingest RTSP: DONE.** Reuses Hito G's `Supervisor.streamLoop()` via a
new `PacketSink` interface + `Session.VideoChannel()` (the interleaved
channel actually negotiated in SETUP, not a hardcoded `0` — RTCP on the
sibling channel is filtered out before `PacketSink.OnPacket` is ever
called). No second RTSP connection, no duplicated reconnect/health.

**H2 — Decode: DONE.** `VideoDecoder` interface + `FFmpegDecoder`
subprocess implementation. SPS/PPS parsed from SDP `sprop-parameter-sets`
and injected at decoder start/restart. Validated against both a synthetic
ffmpeg-generated clip (`decoder_test.go`, ffmpeg-gated) and the real TC70.

**RTP depacketization — DONE for H.264** (single NALU, FU-A, STAP-A).
Access-unit-wide sequence-gap handling: any packet loss detected while an
AU is open discards the whole AU, never a partial frame to the decoder.
H.265 explicitly unsupported (`ErrUnsupportedCodec`), not a silent no-op.

**H3/H4 — Sampling/FPS reduction: DONE.** `GEOCAM_VIDEO_TARGET_FPS`, no
duplicate frames, intentional drop via `Sampler.ShouldEmit`. Real TC70 run:
~15 FPS source sampled down to 2 FPS target, 9 frames sampled over 8s.

**H5 — Resize: DONE.** `GEOCAM_VIDEO_OUTPUT_WIDTH/HEIGHT`, validated
combination (both zero or both even+positive) at config load. Metadata
(source/output dims, codec, role, timestamps) preserved on every `Frame`.
Real TC70 run: 640x360 source resized to 320x180 output, confirmed.

**H6 — Main/substream: DONE.** Reuses `GEOCAM_STREAM_ROLE`/G's existing
selection, no second logic. Substream remains the default recommendation.

**H7 — Bounded ring buffer: DONE.** `RingBuffer`, fixed capacity, drop-oldest
on full, `-race`-tested concurrent pushes.

**H8 — Frame routing: DONE (interface + debug sink only, as scoped).**
`Sink` interface + `Router` (one bounded queue + one worker per sink) +
`DebugSink`. No Cloud/Hybrid/Edge-YOLO sinks built — that's I/J/K.

**H9 — Backpressure: DONE.** Every hop between stages is a fixed-capacity
channel or ring buffer; a full queue drops (never blocks) except the
depacketizer's input queue, which is drop-*new* (not drop-oldest) because
FU-A reassembly needs strict packet order. Real TC70 run: 0 dropped frames
at the configured queue depths.

**H10 — CPU/RAM bounds + metrics: DONE.** `GEOCAM_VIDEO_MAX_CONCURRENT_PIPELINES`
caps concurrent ffmpeg processes. `GEOCAM_VIDEO_DECODE_QUEUE_DEPTH` now
actually controls the raw-decoded-frame channel's capacity end to end
(`FFmpegDecoderConfig.QueueDepth` → `make(chan DecodedFrame, ...)` —
previously hardcoded to 4, fixed after an independent PR review caught it;
`decoder_test.go`'s `TestFFmpegDecoder_QueueDepthConfigured` asserts it).
`GEOCAM_VIDEO_DECODE_TIMEOUT` now drives a real stall watchdog
(`cameraPipeline.watchdogLoop`): if access units keep arriving from the
camera but the decoder produces no frame for that long, state → `stalled`,
the decoder subprocess is closed (the existing crash-handling path in
`run()` does the actual restart/backoff/metric-increment — the watchdog
never duplicates that logic, it only triggers it), and it recovers to
`running` on the next successful decode. A camera that simply isn't sending
RTP is explicitly NOT treated as a stall (`TestPipeline_NoWatchdogRestartWhenUpstreamIdle`).
`FFmpegDecoder.pendingTimes` (the FIFO used for the best-effort
`SourceReceivedAt` correlation) is now bounded (`maxPendingTimes = 64`,
drop-oldest on overflow) instead of growing unboundedly if Push keeps being
called without frames coming out. ffmpeg's stderr is captured in a bounded
4KB tail buffer (`tailBuffer`) instead of an unbounded `bytes.Buffer` kept
for the process's whole lifetime — stdout/video is a separate pipe, never
captured there, and no RTSP URL or credential is ever passed to ffmpeg, so
nothing secret reaches it. `/status` exposes a small `video_pipeline`
summary (see exact JSON contract below) — no frame bytes, no per-frame
history.

**Metrics semantics (corrected after review):** `frames_received` now means
completed access units produced by the depacketizer, not raw RTP packets —
raw packets are `rtp_packets_received`, a separate field, rather than
silently redefining what "frames" meant. `frames_decoded`/`frames_dropped`
are cumulative across decoder subprocess restarts
(`cameraPipeline.foldDecoderCounts` folds an outgoing decoder's final
counts into a running base before a new one starts), so they never drop
back toward zero and `decoded_fps`/`output_fps` (a delta between two
`/status` reads) never goes negative right after a restart —
`TestPipeline_MetricsCumulativeAcrossDecoderRestart` is the regression test.

**H11 — Independent of YOLO: DONE.** `pipeline_test.go`'s
`TestPipeline_EndToEndWithFakeDecoder_H11` is the acceptance test: full
chain with a fake decoder, zero YOLO/PyTorch/Vision-Worker/Cloud
involvement. The real TC70 run (below) proves the same end-to-end with a
real ffmpeg decoder.

**CLI decision: no new subcommand.** `/status`'s `video_pipeline` block
(exact contract, as specified) covers operator validation; the pipeline
runs inside the daemon, unlike `discovery scan`'s pre-enrollment one-shot
checks. See `docs/ARCHITECTURE.md`.

```json
"video_pipeline": {
  "camera_count": 1,
  "cameras": [{
    "candidate_key": "...", "state": "running", "codec": "H264",
    "input_fps": 15, "decoded_fps": 0, "output_fps": 0,
    "rtp_packets_received": 640, "frames_received": 129,
    "frames_decoded": 104, "frames_sampled": 9,
    "frames_dropped": 0, "queue_depth": 0, "buffer_usage": 9,
    "decode_latency_ms": 1397.8
  }]
}
```

`rtp_packets_received` is raw RTP packets; `frames_received` is completed
access units produced by the depacketizer (an access unit is typically
several RTP packets, e.g. one FU-A fragmentation run) — see the metrics
semantics note above.

**Tests:** `internal/processing` — `rtp_test.go`, `depacketizer_test.go`,
`decoder_test.go` (ffmpeg-gated), `sampler_test.go`, `resizer_test.go`,
`ringbuffer_test.go`, `router_test.go`, `pipeline_test.go` (H11 gate),
`manager_test.go` (lifecycle + `-race` `Stop()`-vs-`OnPacket()` concurrency
test). `internal/rtsp` extended: RTCP-never-reaches-`PacketSink` test,
`SetPacketSink` applies to existing+future supervisors, SDP
`sprop-parameter-sets`/`a=rtpmap` extraction, SDP-codec-overrides-ONVIF
regression test. `internal/config` extended: new env var parsing + combined
width/height validation.

`go build ./...`, `go vet ./...`, `gofmt -l .` (clean), `go test ./...`,
`go test -race ./...` — all pass. Cross-compiled `linux/amd64` and
`linux/arm64` via existing `make build-linux` (pure Go, `CGO_ENABLED=0`
unaffected).

**Real TC70 validation — PASS** (`internal/cameratest/video_pipeline_integration_test.go`,
gated by the `integration` build tag + `TAPO_ONVIF_USER`/`TAPO_ONVIF_PASS`,
same convention as Hito F/G's hardware tests): RTSP connected
(`192.168.0.6:554/stream2`), codec resolved as H264 via the SDP fix, 104
frames decoded in ~8s (≥100 frame acceptance met), sampling 15fps→2fps
confirmed (9 sampled), resize 640x360→320x180 confirmed, 0 dropped frames,
0 reconnects, heap RSS delta small and stable (737KB→3.5MB over the run, no
runaway growth), zero secrets in logs (verified), one diagnostic YUV frame
saved to `os.TempDir()` (outside the repo, never committed).
**Decode latency observed ~1.1-1.4s** — consistent with the documented FIFO
correlation approximation (`DecodedFrame.SourceReceivedAt`) plus ffmpeg's
own internal buffering (manually confirmed separately: ffmpeg does not
flush a decoded frame until either the next frame's data arrives or stdin
closes — an inherent ~1-frame latency for a live H.264-without-B-frames
stream, not a bug in this pipeline).

**Docker image smoke test — DONE (PASS).** Started this dev machine's
Rancher Desktop/containerd runtime with explicit authorization, then
`make image` (nerdctl, the repo's real build path, not a substitute).
Verified: image build PASS; `/usr/local/bin/geocam-edge` present and runs
(`version` subcommand: `arm64`, correct commit); `/usr/local/bin/ffmpeg`
present and runs (`-version`: **directly confirms the GPL finding from the
running binary itself** — `--enable-gpl --enable-libx264 --enable-libx265`
in its own reported configure flags, not just inferred from reading
upstream's Dockerfile source as before); image architecture `arm64/linux`
(matches host); container starts and stays up running as `nonroot:nonroot`
(`Config.User` inspected + a live run confirmed no crash — the only error
logged was an expected `permission denied` writing `/var/lib/geocam-edge`,
because an ad-hoc `nerdctl run` mounts no volume, unlike the real Helm
deployment's PVC; unrelated to Hito H); no missing libs/runtime (both
static binaries ran inside distroless with no dynamic-linker errors).
**Technical: PASS. Licensing: resolved — see below (superseded the
initial "still PENDING" note once the LGPL-only migration landed).**

**FFmpeg migrated to LGPL-only, built from source — verified on BOTH
architectures, GPL question fully resolved.** Replaced the GPL
`mwader/static-ffmpeg` prebuilt image with a `Dockerfile` stage that
compiles ffmpeg 7.1.5 from official source (`--disable-everything` +
exactly the 6 components
`ffmpeg -f h264 -i pipe:0 -f rawvideo -pix_fmt yuv420p -an -sn pipe:1`
needs, each individually confirmed to exist and be required against the
real FFmpeg source — not assumed from a snippet).

`linux/arm64`, verified locally (`make image`, real build path):
- Build PASS, image shrank **55.69MB → 5MB compressed** (119.9MB →
  15.26MB uncompressed); ffmpeg binary itself **2.82MB** static.
- `ffmpeg -version` inside the container: no `--enable-gpl`, no
  `--enable-nonfree`, no `libx264`/`libx265` — confirmed directly from the
  shipped binary, not inferred.
- Decode PASS: the exact pipeline command run inside the container against
  a synthetic H.264 clip produced the exact expected byte count.
- `geocam-edge` binary present/runs (arm64, correct commit); container
  starts and stays up as `nonroot:nonroot`; no missing libs/runtime.

`linux/amd64`, verified via a dedicated CI job on real amd64 hardware
(`.github/workflows/ci.yml`'s `docker-amd64-smoke`, `ubuntu-latest`
GitHub-hosted runner — chosen specifically because compiling FFmpeg from
source under this dev machine's local QEMU emulation was too slow to run
to completion, 30+ minutes and not close to done, and was cut short by
explicit decision rather than left running unattended). **CI run:
[35092394083](https://github.com/drko-dev/monitoreoedgeis/actions/runs/35092394083)
— PASS in ~2 minutes on real hardware** (vs. 30+ and counting under local
emulation — confirms the slowness was emulation overhead, not the recipe):
- Image architecture confirmed `amd64`; configured user confirmed
  `nonroot:nonroot`.
- `ffmpeg -version` (ffmpeg 7.1.5) confirmed to contain none of
  `--enable-gpl` / `--enable-nonfree` / `--enable-libx264` /
  `--enable-libx265`.
- Decode smoke: the exact production pipeline command against a synthetic
  clip produced exactly the expected **30720 bytes** (5 frames of 64x64
  yuv420p).
- `geocam-edge version` ran successfully (reports `architecture: amd64`);
  the container started and stayed `running`.

**Both architectures now verified end to end. No remaining gap on this
item, and no remaining licensing/business decision.**

Live-camera note: the real TC70 tests below use the **macOS host's**
`ffmpeg` (full-featured Homebrew build) via `go test`, not the new minimal
Linux binary — the minimal build's decode correctness is what the
container smoke tests above validate on both architectures (same
libavcodec H.264 decoder algorithm, architecture-independent), while the
TC70 tests validate the Go-side pipeline logic (RTP, depacketizing,
sampling, resize, metrics) end-to-end against a real camera. Together they
cover the full picture; neither alone claims to be the other.

Compliance note (factual): shipping this binary under LGPLv2.1+ requires
making the corresponding source and build recipe available and preserving
FFmpeg's notices — `docs/ARCHITECTURE.md` links directly to the pinned
upstream source and the exact configure invocation for this. Attaching
those notices to whatever channel actually distributes the built image has
not been done as part of this milestone; this is not legal advice.

**Not done / deliberately out of scope (per ticket):** Cloud upload,
video WebSocket, Vision Worker, YOLO, detection, IA events, motion
detection, ROI, Hybrid, Full Edge, GPU/NPU, long-term video storage —
Hitos I/J/K. `video probe` CLI subcommand (justified above). H.265
depacketization/decode (interface designed for it, not implemented).

Hito H merged into `main` via PR #10 (`f7263b3`), explicitly authorized by a
direct "mergeá el PR #10" instruction after four review rounds — see git
history for the full round-by-round record (protocol corrections, decode
queue/watchdog/metrics bugs, a shutdown deadlock, and the GPL→LGPL ffmpeg
migration verified on both architectures).

## Hito I — Modo Cloud (THIS BRANCH, `feature/cloud-video-sink`)

First slice only: Edge→SaaS frame push, reusing Hito H's pipeline and the
SaaS's existing Cloud Vision Worker (no RTSP duplication, no direct Cloud→LAN
connection). Full item-by-item status in `docs/ROADMAP.md`'s Hito I section.

**Edge (this repo):**
- `internal/cloudsink` (new package) — `CloudSink` implements `processing.Sink`:
  yuv420p → JPEG (quality 85) → `transport.Client.PostFrame`.
- `internal/transport`: `FramesPath` + `Client.PostFrame` (raw JPEG body,
  metadata as headers — `X-Candidate-Key`/`X-Frame-Seq`/`X-Frame-Timestamp`).
- `internal/processing.Manager`: `NewManager(..., extraSinks ...Sink)` —
  backward-compatible, `DebugSink` always first.
- `internal/agent/cloudsink_module.go`: `newCloudSink`, gated on the
  *existing* `GEOCAM_PROCESSING_MODE=cloud` (no new env var) + enrolled
  credentials — same pattern as `newHeartbeatModule`.
- Tests: `internal/cloudsink/cloudsink_test.go` (encode/upload/error paths),
  `internal/transport/client_test.go` (`TestPostFrame*`, httptest-backed).
  `go build ./...` / `go vet ./...` / `gofmt -l .` / `go test -race ./...` /
  `make build-linux` (linux/amd64 + linux/arm64) — all clean.

**SaaS (`monitoreoia`, branch `feature/edge-frame-push`, separate PR):**
- `routers/edge.py`: `POST /api/v1/edge/frames` — auth via the existing
  `authenticate_edge_device`, `candidate_key` resolved to `camera_id`
  server-side (new `db_postgres.resolve_camera_id_by_identifier`, reusing the
  existing `edge_device_cameras` table), forwards to the worker via the
  existing `cloud_vision_client` IPC helper.
- `cloud_vision_worker.py`: new IPC route `POST /internal/cameras/{camera_id}/frame`.
- `cloud_vision.py`: `CloudVisionManager.push_frame()` feeds the same
  detection path RTSP-pull uses; `CloudCameraState.edge_push` +
  `sync_cameras_from_db` gating ensure a camera never runs RTSP-pull and
  Edge-push at the same time.
- Tests: `geocam/tests/test_edge_frame_ingest.py` — 7 pure unit tests pass
  locally (no DB needed); 9 PostgreSQL-integration tests are written
  (endpoint auth/resolution/forwarding, DB resolver functions) but **could
  not be run in this environment** (no local Postgres — `STE_DB_BACKEND`
  harness skips cleanly, same as this repo's other PG-backed tests).

**Now done as code, on `feature/integrate-edge-cloud-hito-i`** (see
`docs/ROADMAP.md`): offline buffering (I6), bandwidth control/compression
tuning (I7), and real cost/bandwidth telemetry (I10) — one unified
`internal/cloudsink.CloudSink` combining all three: JPEG quality/rate
limiting applied after per-camera FIFO ordering and before POST, a disk
spool for recoverable failures that replays paced through the same rate
limiter (never dropping an already-durable frame for lack of tokens), and
one canonical `Status`/`/status` surface with no duplicate counters across
the three milestones. `go test ./...`, `-race`, `go vet`, `gofmt -l`, and
`make build-linux` (amd64+arm64) all pass. **No real-camera validation** —
this needs a live TC70 + a reachable SaaS + Postgres to verify end-to-end,
none of which were available/authorized in this session; only a local
encode+loopback-HTTP benchmark ran, documented as a synthetic-noise
worst-case upper bound in `docs/performance/`.

## NEXT

Edge PR #16 (`feature/integrate-edge-cloud-hito-i`, consolidating #11/#13/#14/#15)
went through two review rounds — fixing an oversized-frame drain-loop death
(P0), limiter-waiter starvation (P1), rolling effective rate (P1), a context-
cancel error-chain gap (P2), and two flaky replay tests (P2, made
deterministic via an explicit completion signal instead of a wall-clock
poll) — and was merged to `main` at `242d8428ed407c23a299d1945d5130d8d7fddf9c`.
`go test ./...`, `-race`, `go vet`, `gofmt -l`, and `make build-linux`
(amd64+arm64) all pass on that SHA.

Edge deploy to a real production appliance is still **not possible from this
repo**: the only documented deploy procedure (`make deploy`, `docs/README.md`
"Deploy to local K3s") explicitly targets a local Rancher Desktop/K3s dev
cluster and states it "never targets production." Real appliance
installation (`docs/ROADMAP.md` P7 Instalación / P8 systemd) is still
unbuilt. This is unchanged by the merge and is not a Hito I regression.

**Not validated against a real camera or a real SaaS/Postgres instance** —
this still needs a live TC70 + a reachable SaaS + Postgres to verify
end-to-end.

Before this can be called fully done: run the SaaS's Postgres-backed
integration tests for real, validate `POST /api/v1/edge/frames` against a
live Edge + camera + SaaS + worker, measure actual bandwidth per camera, and
build the real appliance deploy path (P7/P8 in the roadmap).

**HITO I (first slice + I6/I7/I10) — MERGED TO MAIN, NOT VALIDATED ON REAL
HARDWARE, NOT DEPLOYED (no production deploy path exists yet)**

## P7/P8 — Linux appliance install/update/rollback (branch `feature/edge-production-appliance`)

Converts GEO CAM Edge into installable/maintainable software on a bare Linux
appliance, outside K3s/Docker. See `docs/deployment/appliance.md` for the
full operational guide and `docs/ROADMAP.md`'s P section for the per-item
breakdown.

**IMPLEMENTED**:
- `deploy/appliance/scripts/{install,update,rollback,uninstall,package,build-ffmpeg-static,wait-ready}.sh`
  and `deploy/appliance/systemd/geocam-edge.service.in` + `deploy/appliance/config/geocam-edge.env.example`.
- Dedicated non-root `geocam-edge` system user/group (no login shell), versioned
  `/opt/geocam-edge/releases/<version>` + atomic `current` symlink, `GEOCAM_DATA_DIR`
  kept outside the release tree so it is never touched by install/update/uninstall.
- `update.sh` validates checksum + architecture before touching anything installed,
  keeps prior releases for rollback, restarts the service and verifies `/readyz`,
  auto-invoking `rollback.sh` on failure. `uninstall.sh` never deletes
  `GEOCAM_DATA_DIR`/config unless `--purge` is passed AND explicitly confirmed.
- `build-ffmpeg-static.sh` reuses the existing root `Dockerfile`'s `ffmpeg-build`
  stage (Hito H's pinned, LGPLv2.1+-only recipe) unmodified, as a build-time-only
  tool, to produce a standalone static ffmpeg binary for the appliance package.

**TESTED**: `go test ./deploy/appliance/...` — 9 tests, all passing on this sandbox.
Covers: expected install layout, idempotent re-install preserving identity/config,
update→rollback round-trip, rejection of wrong-architecture and bad-checksum
artifacts (with `current` provably unchanged after rejection), non-purge uninstall
preserving data, purge requiring explicit confirmation, no secret leakage in script
output, and static structure checks on the systemd unit template.
Repo-wide `go test ./...`, `go test -race ./...`, `go vet ./...`, `gofmt -l .`, and
`make build-linux` (amd64+arm64 ELF binaries confirmed via `file`) all pass clean.

**NOT VALIDATED** (no Linux/systemd machine or VM available in this sandbox —
darwin only, no docker daemon either):
- Real `useradd`/`groupadd`/`systemctl enable|start|restart` execution — the
  scripts detect "not a real Linux target" and skip these (documented gap, see
  `lib.sh:is_real_linux_target`), so this path has never actually run.
- `systemd-analyze verify` against the generated unit (only a static content/
  structure check ran).
- An actual `geocam-edge run` process receiving `SIGTERM` from systemd and
  shutting down within `TimeoutStopSec` — confirmed only by reading
  `cmd/geocam-edge/main.go`'s `signal.NotifyContext`, not by observing it.
- `build-ffmpeg-static.sh` was never executed (no Docker daemon here) — the
  Dockerfile recipe it reuses is unchanged from Hito H, but the extraction
  script itself has only been read-reviewed, not run.
- No installation happened on any real or virtual machine. No production
  infrastructure (Dattaweb/Hostinger/Geo Multa) was touched or referenced.

## Hito J — Hybrid Candidates Transport & Classifier (J6–J7)

- **J6 (Lightweight Classifier Adapter)**: Implemented decoupled `CandidateClassifier` interface, `ClassificationResult`, and `NoopClassifier` in `internal/hybrid`. Status: ADAPTER DONE / MODEL REAL OPTIONAL PENDING. Default mode is disabled.
- **J7 (Candidate Transport & Spooling)**: Extended `processing.Frame` and `cloudsink.Buffer` (`BufferedFrame`/`bufferMeta`) with candidate metadata (`ProcessingMode`, `CandidateReason`, `CandidateScore`, `CorrelationID`). Extended `internal/transport.Client` with `PostFrameWithMetadata` sending `X-Processing-Mode`, `X-Candidate-Reason`, `X-Candidate-Score`, and `X-Correlation-Id` without breaking standard cloud upload. Offline buffer preserves hybrid metadata on recoverable retries and replay.

## HOW ANOTHER AI SHOULD CONTINUE

1. Read `AGENTS.md`.
2. Read `docs/PROJECT_STATUS.md` (this file).
3. Read `docs/ARCHITECTURE.md`.
4. Read `docs/ROADMAP.md`.
5. Check `git status`.
6. Check the current branch.
7. Check the open PR (if any).
8. Work only on the next milestone.
9. Do not infer states — read them here.
10. Update the documentation when finishing.

Always distinguish, and never collapse these into each other:


---

**IMPLEMENTED** — code exists in the repo.
**TESTED** — automated tests pass.
**VALIDATED LOCAL** — observed running in the local environment.
**MERGED** — merged into `main`.
**DEPLOYED PROD** — running on the production VPS.
