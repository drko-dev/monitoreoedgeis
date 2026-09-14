# PROJECT STATUS — Where we stand right now

> Answers one question: **"¿Dónde estamos parados ahora?"**
> This document is the real state of the project at this moment. If it disagrees
> with anyone's memory, this document and Git win.

## Snapshot

| Field             | Value                                                     |
| ----------------- | --------------------------------------------------------- |
| **PROJECT**       | GEO CAM Edge                                              |
| **CURRENT HITO**  | A — Foundation                                            |
| **STATE**         | IMPLEMENTED, TESTED, VALIDATED LOCAL                      |
| **MERGED**        | **NO** — not while PR #1 remains open                     |
| **PR**            | #1 — https://github.com/drko-dev/monitoreoedgeis/pull/1 (OPEN) |
| **Branch**        | `feature/edge-foundation-go`                              |
| **HEAD**          | `9c2ebe736e7ac9c4a0dc4d9815d85e3fac3c5095`                |
| **DEPLOYED PROD** | **NO** — VPS/production untouched                         |
| **Go version**    | 1.26.2                                                    |

## Hito A — what was implemented

- `cmd/geocam-edge` — thin entrypoint
- `internal/agent` — agent core, lifecycle, graceful shutdown
- `internal/config` — environment-based configuration
- `internal/identity` — identity/enrollment abstraction
- `internal/platform` — platform and hardware detection
- `internal/health` — internal health (no HTTP endpoint)
- `internal/logging` — structured logging with `log/slog`
- Typed processing modes: `cloud` / `hybrid` / `edge`
- Agent versioning
- OCI Dockerfile, distroless / non-root image, cross-build
- Local Helm chart
- K3s local deployment
- CI

## Validations actually observed

| Check                   | Result |
| ----------------------- | ------ |
| `go test ./...`         | PASS   |
| `go vet ./...`          | PASS   |
| `gofmt -l .`            | clean  |
| Local build             | PASS   |
| `linux/amd64` build     | PASS   |
| `linux/arm64` build     | PASS   |
| `CGO_ENABLED=0`         | PASS   |

## Local runtime validation

- Image `geocam-edge:dev` built with `nerdctl --namespace k8s.io`.
  **Docker Engine was not used.**
- Local environment: Rancher Desktop + K3s + containerd.
- Namespace: `geocam-edge-dev`. Helm release: `geocam-edge`.
- Pod `1/1 Running`. Self-healing: PASS.
- Local SaaS: **not modified**.
- VPS / production: **not touched**.

## Incidents

**`.gitignore` unanchored patterns (minor, already fixed).**
`.gitignore` contained the patterns `geocam-edge` and `geocam-edge-*` without
anchoring, which made Git accidentally ignore `cmd/geocam-edge/` and
`deploy/helm/geocam-edge/`. Detected before the final commit and fixed by
anchoring the patterns to the repository root (`/geocam-edge`,
`/geocam-edge-linux-*`).

## Current restrictions

- PR #1 is OPEN. Nothing from this branch is merged to `main`.
- Production is out of scope until explicitly authorized.
- The SaaS repo (`monitoreoia`) is not modified from here.

## Not implemented yet

Do not infer DONE just because something appears in the architecture document.
None of the following exist yet:

real enrollment, SaaS heartbeat, real transport, ONVIF, WS-Discovery,
autodiscovery, RTSP, FFmpeg, OpenCV, YOLO, PyTorch, Vision Worker, real
WebSocket, VPN, OTA, real camera credential management, video pipeline, AI
processing.

## NEXT — Hito B: Agent Core

The next milestone is **B — Agent Core** (see `docs/ROADMAP.md`).

Hito A intentionally brought forward some pieces that belong to B: process
lifecycle, config, health, identity abstraction, platform/version.

> Foundation implementation intentionally included primitives required by Agent
> Core. Hito B remains open until its own acceptance criteria are met.

Before writing code for B, compare B1–B10 against what A already advanced and do
not duplicate implementation. B must close the real gaps. Priorities: persistent
identity, stable `edge_id`/`gateway_id`, internal state, module lifecycle,
required local persistence, definitive health.

Then C — Enrollment. Then D — Heartbeat. Then E — Autodiscovery.
**Do not jump straight to ONVIF.**

## HOW ANOTHER AI SHOULD CONTINUE

1. Read `AGENTS.md`.
2. Read `docs/PROJECT_STATUS.md` (this file).
3. Read `docs/ARCHITECTURE.md`.
4. Read `docs/ROADMAP.md`.
5. Check `git status`.
6. Check the current branch.
7. Check the open PR.
8. Work only on the next milestone.
9. Do not infer states — read them here.
10. Update the documentation when finishing.

Always distinguish, and never collapse these into each other:

**IMPLEMENTED** — code exists in the repo.
**TESTED** — automated tests pass.
**VALIDATED LOCAL** — observed running in the local K3s environment.
**MERGED** — merged into `main`.
**DEPLOYED PROD** — running on the production VPS.
