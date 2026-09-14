# GEO CAM Edge

Lightweight Go agent for GEO CAM edge gateways (Raspberry Pi, Orange Pi, mini-PC,
server). It runs on-site, next to the cameras, and talks to the GEO CAM SaaS.

This repository is **Hito A: the foundation**. It contains the agent core only —
config, identity, platform detection, health, logging and lifecycle. There is no
camera, vision, RTSP, ONVIF or YOLO functionality yet (see
[Not implemented yet](#not-implemented-yet)).

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
| `GEOCAM_EDGE_ID`           | *(empty)*               | Empty => agent reports `UNENROLLED`   |
| `GEOCAM_PROCESSING_MODE`   | `cloud`                 | `cloud` \| `hybrid` \| `edge`         |
| `GEOCAM_LOG_LEVEL`         | `info`                  | `debug` \| `info` \| `warn` \| `error`|
| `GEOCAM_SAAS_URL`          | *(empty)*               | Not used yet — no SaaS I/O            |
| `GEOCAM_HEARTBEAT_INTERVAL`| `30s`                   | Go duration; must be positive         |
| `GEOCAM_DATA_DIR`          | `/var/lib/geocam-edge`  |                                       |

No secrets or credentials are read, stored or logged.

## Run locally

```bash
go run ./cmd/geocam-edge
GEOCAM_EDGE_ID=edge-001 GEOCAM_LOG_LEVEL=debug go run ./cmd/geocam-edge
go run ./cmd/geocam-edge --version
```

The agent starts, logs its version/platform/mode/enrollment status, becomes
`READY`, and stays alive as a daemon. `Ctrl-C` (SIGINT) or SIGTERM shuts it down
cleanly with exit code 0.

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
cmd/geocam-edge/   entrypoint (thin)
internal/agent/    agent core: lifecycle, startup, shutdown, version
internal/config/   env configuration + ProcessingMode type
internal/identity/ enrollment status abstraction (UNENROLLED / ENROLLED)
internal/platform/ host and runtime detection
internal/health/   lifecycle state + runtime snapshot
internal/logging/  log/slog setup
deploy/helm/       local K3s chart
```

## Not implemented yet

ONVIF, WS-Discovery, RTSP, FFmpeg, OpenCV, YOLO, PyTorch, the Vision Worker,
real SaaS enrollment, real heartbeat, HTTP API, WebSockets, VPN, OTA, camera
credentials and video processing are all **out of scope for this milestone**.
They belong to Hito B and later.
