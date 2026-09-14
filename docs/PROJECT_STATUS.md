# PROJECT STATUS — Where we stand right now

> Answers one question: **"¿Dónde estamos parados ahora?"**
> This document is the real state of the project at this moment. If it disagrees
> with anyone's memory, this document and Git win.

## Snapshot

| Field             | Value                                                     |
| ----------------- | ----------------------------------------------------------- |
| **PROJECT**       | GEO CAM Edge                                              |
| **CURRENT HITO**  | B — Agent Core                                            |
| **STATE**         | IMPLEMENTED, TESTED, **VALIDATED LOCAL (binary + K3s)**    |
| **MERGED**        | **NO** — this branch is not merged to `main`               |
| **Branch**        | `feature/edge-agent-core` (based on `main`, Hito A merged, HEAD `0eef45a`) |
| **DEPLOYED PROD** | **NO** — VPS/production untouched                         |
| **Go version**    | 1.26.2                                                    |

Hito A (`feature/edge-foundation-go`) is merged into `main`. This document now
tracks Hito B, built on top of it on `feature/edge-agent-core`.

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

real enrollment, SaaS heartbeat, real transport, ONVIF, WS-Discovery,
autodiscovery, RTSP, FFmpeg, OpenCV, YOLO, PyTorch, Vision Worker, real
WebSocket, VPN, OTA, real camera credential management, video pipeline, AI
processing.

## NEXT — Hito C: Enrollment con SaaS

Hito B is DONE: code-level criteria pass locally (tests, vet, fmt, builds,
manual endpoint verification) **and** K3s validation passed (probes green,
PVC bound, `edge_id` identical across pod recreation). A PR is open against
`main` but **not merged** (explicit user instruction: do not merge). The
next milestone is **C — Enrollment** (see `docs/ROADMAP.md`) — do not start
it until the user authorizes it.

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
