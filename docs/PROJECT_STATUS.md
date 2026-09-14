# PROJECT STATUS — Where we stand right now

> Answers one question: **"¿Dónde estamos parados ahora?"**
> This document is the real state of the project at this moment. If it disagrees
> with anyone's memory, this document and Git win.

## Snapshot

| Field             | Value                                                     |
| ----------------- | ----------------------------------------------------------- |
| **PROJECT**       | GEO CAM Edge                                              |
| **CURRENT HITO**  | D — Heartbeat Edge → SaaS                                 |
| **STATE**         | DONE / VALIDATED LOCAL — full E2E against the real SaaS + K3s pod recreation, both PASS. |
| **MERGED**        | **NO** — this branch is not merged to `main`               |
| **Branch**        | `feature/edge-heartbeat` (based on `main`, Hito C merged)  |
| **DEPLOYED PROD** | **NO** — VPS/production untouched                         |
| **Go version**    | 1.26.2                                                    |

Hito A (`feature/edge-foundation-go`), Hito B (`feature/edge-agent-core`) and
Hito C (`feature/edge-enrollment`) are merged into `main`. This document now
tracks Hito D, built on top of them on `feature/edge-heartbeat`.

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

SaaS heartbeat, real transport for video, ONVIF, WS-Discovery,
autodiscovery, RTSP, FFmpeg, OpenCV, YOLO, PyTorch, Vision Worker, real
WebSocket, VPN, OTA, real camera credential management, video pipeline, AI
processing.

Gateway enrollment (`geocam-edge enroll` / `credential rotate`) and heartbeat
(Hito D) ARE exercised end-to-end against the real SaaS and in K3s — see
Hito C and Hito D below.

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

## NEXT

Hito D is DONE / VALIDATED LOCAL. Do not start Hito E (ONVIF, cameras,
RTSP, autodiscovery, YOLO, video) until explicitly authorized. Do not merge
`feature/edge-heartbeat` until explicitly authorized.

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

**IMPLEMENTED** — code exists in the repo.
**TESTED** — automated tests pass.
**VALIDATED LOCAL** — observed running in the local K3s environment.
**MERGED** — merged into `main`.
**DEPLOYED PROD** — running on the production VPS.
