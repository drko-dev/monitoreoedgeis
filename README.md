# GEO CAM Edge

Lightweight Go agent for GEO CAM edge gateways (Raspberry Pi, Orange Pi, mini-PC,
server). It runs on-site, next to the cameras, and talks to the GEO CAM SaaS.

This repository is on **Hito B: Agent Core**, built on the merged Hito A
foundation. It contains the agent core only — config, persistent identity,
platform detection, health (now with a local HTTP surface), logging, module
lifecycle. There is no camera, vision, RTSP, ONVIF or YOLO functionality yet
(see [Not implemented yet](#not-implemented-yet)).

## Project documentation

This README is the entry point only. The project's persistent memory lives in
these documents — read them in this order:

| Document                                             | Answers                            |
| ---------------------------------------------------- | ---------------------------------- |
| [AGENTS.md](AGENTS.md)                               | Working rules and constraints      |
| [docs/PROJECT_STATUS.md](docs/PROJECT_STATUS.md)     | Where the project stands right now |
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)         | Architecture and technical decisions |
| [docs/ROADMAP.md](docs/ROADMAP.md)                   | Master backlog A–Z and block status |

## Why Go

- Single compiled binary, no runtime to install on the gateway
- Low RAM footprint and fast startup — fits a Raspberry Pi
- Trivial cross-compilation to the deployment targets
- Good concurrency primitives for a long-lived daemon
- Static binaries (`CGO_ENABLED=0`) run on minimal/distroless base images

**Go version: 1.26.2** (current stable, matches the local toolchain).

**Deployment targets: `linux/amd64` and `linux/arm64`.** macOS is a development
environment only — no code is coupled to it.

## Architecture

```
GEO CAM Edge Core (Go)  --->  Vision Worker (Python + YOLO)   [does not exist yet]
```

The Go core owns the agent lifecycle, identity, config, transport, health and
telemetry. Local inference is deliberately kept **out** of the Go core: when it
is eventually needed, it will live in a separate Python Vision Worker process.

See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for the full picture.

### Processing modes

Modeled from day one, with no functional difference between them yet:

| Mode     | Intent                                                 |
| -------- | ------------------------------------------------------ |
| `cloud`  | All processing in the SaaS. Agent stays minimal.        |
| `hybrid` | Light local processing, heavy lifting in the SaaS.      |
| `edge`   | Local inference via the (future) Python Vision Worker.  |

Default: `cloud`. An invalid value aborts startup with a clear error.

## Configuration

All configuration comes from environment variables:

| Variable                   | Default                 | Notes                                 |
| -------------------------- | ----------------------- | ------------------------------------- |
| `GEOCAM_EDGE_ID`           | *(empty)*               | **Dev override only.** When set, used verbatim and `identity.json` is never read/written. Leave empty in normal use. |
| `GEOCAM_PROCESSING_MODE`   | `cloud`                 | `cloud` \| `hybrid` \| `edge`         |
| `GEOCAM_LOG_LEVEL`         | `info`                  | `debug` \| `info` \| `warn` \| `error`|
| `GEOCAM_SAAS_URL`          | *(empty)*               | Not used yet — no SaaS I/O            |
| `GEOCAM_HEARTBEAT_INTERVAL`| `30s`                   | Go duration; must be positive         |
| `GEOCAM_DATA_DIR`          | `/var/lib/geocam-edge`  | Holds `identity.json`                 |
| `GEOCAM_HEALTH_ADDR`       | `127.0.0.1:8091`        | Local health HTTP bind (localhost-only by default) |

No secrets or credentials are read, stored or logged.

## Identity

On first run the agent generates a random UUID (`edge_id`) and persists it to
`identity.json` under `GEOCAM_DATA_DIR`, reusing it on every later start. It
belongs to this agent *instance*, never to the underlying hardware. A
corrupted `identity.json` is a hard error: the agent starts but stays
`DEGRADED` rather than silently generating a new identity.

## Run locally

```bash
go run ./cmd/geocam-edge
GEOCAM_LOG_LEVEL=debug go run ./cmd/geocam-edge
go run ./cmd/geocam-edge --version
go run ./cmd/geocam-edge identity   # print edge_id/version/arch/mode/data_dir, no secrets
go run ./cmd/geocam-edge check      # query a running agent's local health status
```

The agent starts, logs its version/platform/mode/identity, starts its health
HTTP server, becomes `READY`, and stays alive as a daemon. `Ctrl-C` (SIGINT)
or SIGTERM shuts it down cleanly with exit code 0.

## Local health HTTP

```bash
curl http://127.0.0.1:8091/healthz   # 200 if the process is alive
curl http://127.0.0.1:8091/readyz    # 200 once READY, 503 otherwise
curl http://127.0.0.1:8091/status    # JSON snapshot, no secrets
```

## Test

```bash
make test    # go test ./...
make vet     # go vet ./...
```

## Build

```bash
make build        # host binary -> bin/geocam-edge
make build-linux  # bin/geocam-edge-linux-amd64 and -linux-arm64 (static, CGO off)
```

Version, commit and build date are injected via `-ldflags`.

## Build the OCI image (no Docker Desktop)

The local runtime is **Rancher Desktop + containerd + K3s**. Docker Engine is
**not** required or used. The image is built with `nerdctl` straight into the
`k8s.io` containerd namespace, so K3s can run it without a registry:

```bash
make image
# nerdctl --namespace k8s.io build -t geocam-edge:dev .
```

The final image is distroless, non-root, and contains only the static Go binary
— no Go toolchain, no Python, no YOLO.

## Deploy to local K3s

> Local development only. This never targets production. The unrelated GEO CAM
> SaaS lives in the `geocam` namespace and must not be touched.

```bash
kubectl config current-context          # must be rancher-desktop
make helm-lint
make helm-template
make deploy    # helm upgrade --install geocam-edge -n geocam-edge-dev --create-namespace
make status    # expect: geocam-edge  1/1  Running
```

The chart deploys a Deployment and a ConfigMap. There is **no Service**: the
agent listens on no port in this milestone.

Resources: requests `10m` CPU / `16Mi` RAM, limits `250m` CPU / `128Mi` RAM.

### Logs

```bash
make logs
# kubectl logs -n geocam-edge-dev -l app.kubernetes.io/name=geocam-edge --tail=50
```

### Uninstall (Edge only)

```bash
make uninstall   # helm uninstall geocam-edge -n geocam-edge-dev
```

This removes only the Edge release. It never affects the `geocam` namespace or
the SaaS release.

## Layout

```
cmd/geocam-edge/   entrypoint (thin) + identity/check CLI subcommands
internal/agent/    agent core: lifecycle, startup, shutdown, module lifecycle, version
internal/config/   env configuration + ProcessingMode type
internal/identity/ persistent edge_id (identity.json, UUID v4)
internal/platform/ host and runtime detection
internal/health/   lifecycle state + runtime snapshot + local HTTP (/healthz, /readyz, /status)
internal/logging/  log/slog setup
deploy/helm/       local K3s chart
```

## Not implemented yet

ONVIF, WS-Discovery, RTSP, FFmpeg, OpenCV, YOLO, PyTorch, the Vision Worker,
real SaaS enrollment, real heartbeat, HTTP API, WebSockets, VPN, OTA, camera
credentials and video processing are all **out of scope for this milestone**.
They belong to Hito B and later.
