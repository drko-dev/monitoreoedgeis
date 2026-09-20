# GEO CAM Edge

Lightweight Go agent for GEO CAM edge gateways (Raspberry Pi, Orange Pi, mini-PC,
server). It runs on-site, next to the cameras, and talks to the GEO CAM SaaS.

Through Hito Y, merged to `main`: agent core (config, persistent identity,
platform detection, health, logging, module lifecycle), SaaS enrollment and
credential rotation, SaaS heartbeat, ONVIF/WS-Discovery LAN autodiscovery,
per-device camera credential management, the RTSP connectivity subsystem
(reconnection and per-camera health state), the ffmpeg video pipeline, Cloud
frame push with offline buffering and bandwidth control, local motion gating,
local YOLO inference through an **out-of-process Python Vision Worker**, local
events/evidence with durable sync, signed OTA, remote configuration, appliance
packaging, and systemd watchdog integration.

This binary serves **three commercial profiles** — Gateway, Hybrid and Full
Edge — from one codebase, selected by configuration rather than by three
products. Which one an Edge is actually running is reported on `/status` as
`profile`; see [docs/product/COMMERCIAL_MODES.md](docs/product/COMMERCIAL_MODES.md)
for the capability matrix, each profile's readiness, and the known gaps.

**Not operational yet, and not claimed**: per-camera RTSP connectivity. The
subsystem is implemented and unit-tested, but no production code path
provisions camera targets, so it supervises zero cameras today (gap G1 in the
document above). Also not implemented in this repository: a VPN or
subnet-routing client (the tunnel is deliberately customer-side
infrastructure), and cross-subnet camera-target provisioning.

## Project documentation

This README is the entry point only. The project's persistent memory lives in
these documents — read them in this order:

| Document                                             | Answers                            |
| ---------------------------------------------------- | ---------------------------------- |
| [AGENTS.md](AGENTS.md)                               | Working rules and constraints      |
| [docs/PROJECT_STATUS.md](docs/PROJECT_STATUS.md)     | Where the project stands right now |
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)         | Architecture and technical decisions |
| [docs/product/COMMERCIAL_MODES.md](docs/product/COMMERCIAL_MODES.md) | What Gateway / Hybrid / Full Edge are, and how far each is verified |
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
GEO CAM Edge Core (Go)  --->  Vision Worker (Python + YOLO)   [Full Edge profile only]
                                   Unix socket, newline-delimited JSON
```

The Go core owns the agent lifecycle, identity, config, transport, health and
telemetry. Local inference is deliberately kept **out** of the Go core: it lives
in a separate Python Vision Worker process that the agent `exec`s and supervises
over a Unix domain socket. **PyTorch is never embedded in the Go binary** — the
module has no third-party dependencies, there is no `import "C"` anywhere, and
every target builds `CGO_ENABLED=0`. The worker is provisioned only where the
`edge` mode needs it; as of this branch the appliance package does **not** ship
it, so Full Edge requires manual Python provisioning (see
[docs/product/COMMERCIAL_MODES.md](docs/product/COMMERCIAL_MODES.md) §3).

See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for the full picture.

### Processing modes and commercial profiles

`GEOCAM_PROCESSING_MODE` selects **where inference runs**; it does not by itself
enable the local media path. `GEOCAM_VIDEO_PIPELINE_ENABLED` (default `false`)
decides whether the local video pipeline is built at all. The two together
determine the effective commercial profile:

| Mode     | With the video pipeline enabled                                                                                                                              | With it disabled |
| -------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------ | ---------------- |
| `cloud`  | **Gateway** — light local media path (decode/resize/sample); every sampled frame is uploaded and the **Cloud runs YOLO**. No local model.                     | no media path at all |
| `hybrid` | **Hybrid** — a local motion evaluator runs ahead of the Router and only motion candidates are uploaded; the **Cloud remains the sole inference engine**. Not local object detection. | no media path at all |
| `edge`   | **Full Edge** — local YOLO through the out-of-process Python Vision Worker. Cloud does no inference.                                                          | no media path at all |

With the pipeline disabled the Edge still runs ONVIF discovery, camera
connectivity, health, heartbeat, control and OTA, but decodes and uploads
nothing — `/status` reports that as `profile: gateway-no-media`. That is what a
default install does, and it is **not** the commercial Gateway.

Default mode: `cloud`. An invalid mode aborts startup with a clear error. See
[docs/product/COMMERCIAL_MODES.md](docs/product/COMMERCIAL_MODES.md) for the
per-profile matrix and the configuration examples in
`deploy/appliance/config/geocam-edge.env.{gateway,hybrid,fulledge}.example`.

## Configuration

All configuration comes from environment variables:

| Variable                   | Default                 | Notes                                 |
| -------------------------- | ----------------------- | ------------------------------------- |
| `GEOCAM_EDGE_ID`           | *(empty)*               | **Dev override only.** When set, used verbatim and `identity.json` is never read/written. Leave empty in normal use. |
| `GEOCAM_PROCESSING_MODE`   | `cloud`                 | `cloud` \| `hybrid` \| `edge`         |
| `GEOCAM_LOG_LEVEL`         | `info`                  | `debug` \| `info` \| `warn` \| `error`|
| `GEOCAM_SAAS_URL`          | *(empty)*               | Base URL for `enroll`/`credential rotate`. Must be `https://` unless `GEOCAM_ALLOW_INSECURE_HTTP=true`. |
| `GEOCAM_ALLOW_INSECURE_HTTP` | `false`               | **Dev only.** Allows `GEOCAM_SAAS_URL` to use `http://` instead of `https://`. Never weakens TLS verification for an `https://` URL — it only permits the plaintext scheme. Never set this in production. |
| `GEOCAM_SAAS_TIMEOUT`      | `10s`                   | Timeout for every SaaS HTTP request (enroll/rotate/me/heartbeat) |
| `GEOCAM_HEARTBEAT_INTERVAL`| `30s`                   | Go duration between heartbeats. Must be within `5s`–`5m` (inclusive); anything outside is a startup error. |
| `GEOCAM_DATA_DIR`          | `/var/lib/geocam-edge`  | Holds `identity.json` and `credentials.json` |
| `GEOCAM_HEALTH_ADDR`       | `127.0.0.1:8091`        | Local health HTTP bind (localhost-only by default) |
| `GEOCAM_ENROLLMENT_TOKEN`  | *(empty)*               | One-time enrollment token for `geocam-edge enroll`. Prefer piping via stdin instead. |
| `GEOCAM_DISCOVERY_ENABLED` | `true`                  | Enables/disables ONVIF WS-Discovery and local inventory scanning |
| `GEOCAM_DISCOVERY_INTERVAL`| `5m`                    | Interval between periodic background discovery scans (1m–24h) |
| `GEOCAM_DISCOVERY_TIMEOUT` | `4s`                    | Probe timeout per interface during WS-Discovery (1s–30s) |
| `GEOCAM_DISCOVERY_INTERFACES` | *(empty)*            | Comma-separated interface names to scan (defaults to auto-private RFC 1918/3927) |
| `GEOCAM_CONNECTIVITY_ENABLED` | `true`               | Enables the RTSP connectivity subsystem (Milestone G). Currently supervises zero cameras — no production path provisions targets (gap G1) |
| `GEOCAM_STREAM_ROLE`       | `sub`                   | `sub` \| `main` — which ONVIF profile's stream to connect to |
| `GEOCAM_STREAM_TIMEOUT`    | `5s`                    | RTSP packet silence threshold before reconnecting (1s–60s) |
| `GEOCAM_VIDEO_PIPELINE_ENABLED` | `false`            | Enables the video pipeline (Milestone H). No effect unless `GEOCAM_CONNECTIVITY_ENABLED=true` too |
| `GEOCAM_VIDEO_TARGET_FPS`  | `5`                     | Sampled output FPS (0.1–30) |
| `GEOCAM_VIDEO_OUTPUT_WIDTH` / `_HEIGHT` | `640` / `360` | Resize output dimensions. Both `0` disables resize; otherwise both must be even |
| `GEOCAM_VIDEO_RINGBUFFER_SIZE` | `30`                 | Bounded ring buffer capacity, in frames (1–300) |
| `GEOCAM_VIDEO_QUEUE_DEPTH` | `64`                    | Bounded queue depth for packet/access-unit stages (4–512) |
| `GEOCAM_VIDEO_DECODE_QUEUE_DEPTH` | `4`               | Bounded queue depth for raw decoded frames — deliberately small (1–16) |
| `GEOCAM_VIDEO_MAX_CONCURRENT_PIPELINES` | `4`         | Max concurrent ffmpeg decode subprocesses (1–16) |
| `GEOCAM_VIDEO_FFMPEG_PATH` | `ffmpeg`                | ffmpeg binary path, resolved via `PATH` lookup |
| `GEOCAM_VIDEO_DECODE_TIMEOUT` | `10s`                | Decoder-related timeout bound (1s–60s) |
| `GEOCAM_VIDEO_HYBRID_MOTION_THRESHOLD` | `8`         | Milestone J. Min per-block luma change (0–255) to count a block as "changed". No effect unless `GEOCAM_PROCESSING_MODE=hybrid` |
| `GEOCAM_VIDEO_HYBRID_MIN_CHANGED_AREA` | `0.05`      | Milestone J. Min fraction (0–1) of evaluated blocks that must change for a frame to be a motion candidate |
| `GEOCAM_VIDEO_HYBRID_BLOCK_SIZE` | `16`              | Milestone J. Block edge length in pixels for the block-based luma diff (4–128) |
| `GEOCAM_VIDEO_HYBRID_ROI`  | *(empty)*               | Milestone J. `;`-separated rectangles `x_min,y_min,x_max,y_max` in normalized 0–1 coords. Empty analyzes the whole frame |
| `GEOCAM_VIDEO_HYBRID_IDLE_FPS` | `0` (disabled)      | Milestone J. Adaptive sampling: emit rate once idle (no motion candidate for `..._IDLE_AFTER`). `0` disables adaptation |
| `GEOCAM_VIDEO_HYBRID_IDLE_AFTER` | `5s`             | Milestone J. Time with no motion candidate before dropping to `..._IDLE_FPS`. Only meaningful when it is `>0` |

No secrets or credentials are ever logged. The enrollment token and the
device credential only ever touch: the request to the SaaS, and
`credentials.json` (credential only, 0600, never the token).

## Identity

On first run the agent generates a random UUID (`edge_id`) and persists it to
`identity.json` under `GEOCAM_DATA_DIR`, reusing it on every later start. It
belongs to this agent *instance*, never to the underlying hardware. A
corrupted `identity.json` is a hard error: the agent starts but stays
`DEGRADED` rather than silently generating a new identity.

`edge_id` is **permanent**: it never changes because of enrollment,
credential rotation, revocation, re-enrollment, a restart, or the pod being
recreated. The SaaS enrollment credential (below) is a completely separate,
rotatable concept.

## SaaS enrollment

**Zero-knowledge model:** the Edge generates its own device credential
locally (`edg_live_<random>`) and NEVER sends it to the SaaS in plaintext —
only its SHA-256 hash. The SaaS never generates or returns a credential.

`geocam-edge enroll` claims a one-time enrollment token, generates the
credential, sends `device_key_hash = sha256(credential)` to the SaaS, and on
success stores the credential in `credentials.json` under `GEOCAM_DATA_DIR`
(0600, atomic write). It always reuses the existing `edge_id` — it never
generates a new one. The credential is never printed, logged, or exposed
over `/status`. Immediately after a successful claim it also calls
`GET /edge/me` (authenticated with the just-generated credential) to learn
the assigned organization/site, since the enroll response itself carries
neither; if that follow-up call fails, the credential — already valid
server-side — is still persisted, with empty organization/site and a
warning telling you to run `geocam-edge check` or retry later.

```bash
# preferred: pipe the token, keeps it out of shell history
echo "$TOKEN" | GEOCAM_SAAS_URL=https://saas.example.com go run ./cmd/geocam-edge enroll

# or via env var
GEOCAM_ENROLLMENT_TOKEN=$TOKEN GEOCAM_SAAS_URL=https://saas.example.com go run ./cmd/geocam-edge enroll

# dev-only convenience (exposes the token in shell history / process list)
go run ./cmd/geocam-edge enroll --token "$TOKEN"
```

Running `enroll` again while already enrolled fails on purpose ("already
enrolled, use re-enrollment flow") — it never silently overwrites an
existing credential.

Rotate the stored credential (requires already being enrolled):

```bash
go run ./cmd/geocam-edge credential rotate
```

Rotation generates a new credential locally, sends only its hash plus a
client-generated `rotation_id` (idempotency key) to the SaaS, authenticated
with the CURRENT credential. On a network failure it retries the exact same
request (same `rotation_id`, same hash) up to 3 attempts with a short
backoff (1s/2s/4s) before giving up; a rejected/revoked current credential
(401/403) is not retried. The new credential is only persisted — atomically,
replacing the old one on disk only via a final successful rename — after the
SaaS acknowledges the rotation, and is then verified against `/edge/me`. If
all attempts fail, the previous credential on disk is left completely
untouched and still works.

Every authenticated call (`/edge/me`, rotate-key) sends both an
`X-Device-Id` header and `Authorization: Bearer <credential>` — the SaaS
requires the device id as its own header even though the credential is
carried by Bearer.

The SaaS contract (`internal/transport/contract.go`) has been verified
against the real `monitoreoia` implementation — see `docs/PROJECT_STATUS.md`
for the full reconciliation notes.

## CLI

```
geocam-edge [command] [flags]

  run                  Start the edge agent daemon (default when no command is given)
  version              Print version, commit, build date and platform
  identity             Print this Edge's identity (edge_id, source, version, data dir)
  config               Print effective, non-secret configuration
  check                Check a running agent's health over its local HTTP surface
  enroll               Enroll this Edge against the SaaS using a one-time token
  credential rotate    Rotate the locally stored SaaS credential
  discovery scan       Scan the LAN for ONVIF/RTSP camera devices
  saas check           Verify SaaS connectivity and authentication
```

Run `geocam-edge --help` or `geocam-edge <command> --help` for details. An
unknown command (e.g. `geocam-edge pepito`) fails with a clear error instead
of silently starting the daemon.

## Run locally

```bash
go run ./cmd/geocam-edge --help
go run ./cmd/geocam-edge config     # effective non-secret config
go run ./cmd/geocam-edge identity   # print edge_id/version/arch/mode/data_dir, no secrets
go run ./cmd/geocam-edge enroll     # enroll against SaaS (see Quick Start below)
go run ./cmd/geocam-edge saas check # verify real SaaS connectivity/auth
go run ./cmd/geocam-edge run        # start the daemon (same as no command at all)
go run ./cmd/geocam-edge check      # in another terminal: query the running agent's health
go run ./cmd/geocam-edge discovery scan
```

The agent starts, logs its version/platform/mode/identity, starts its health
HTTP server, becomes `READY`, and stays alive as a daemon. `Ctrl-C` (SIGINT)
or SIGTERM shuts it down cleanly with exit code 0.

### Quick Start

```bash
# 1. See what commands exist and what flags they take
geocam-edge --help

# 2. Inspect effective, non-secret configuration
geocam-edge config

# 3. See this Edge's identity
geocam-edge identity

# 4. Enroll against the SaaS (requires GEOCAM_SAAS_URL; token via stdin is
#    preferred over --token, which is dev-only and insecure)
echo "$ENROLLMENT_TOKEN" | geocam-edge enroll

# 5. Verify real SaaS connectivity and authentication
geocam-edge saas check

# 6. Run the agent daemon (in one terminal)...
geocam-edge run

# 7. ...and check its health from another terminal
geocam-edge check

# 8. Discover ONVIF/RTSP cameras on the LAN
geocam-edge discovery scan
```

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
cmd/geocam-edge/   entrypoint (thin) + identity/check/enroll/credential/discovery CLI subcommands
internal/agent/    agent core: lifecycle, startup, shutdown, module lifecycle, version
internal/config/   env configuration + ProcessingMode type
internal/credentials/ local zero-knowledge credential store (credentials.json)
internal/discovery/   ONVIF WS-Discovery, SOAP client, local inventory, SaaS pull module
internal/health/   lifecycle state + runtime snapshot + local HTTP (/healthz, /readyz, /status)
internal/heartbeat/ SaaS heartbeat & telemetry reporting loop
internal/identity/ persistent edge_id (identity.json, UUID v4)
internal/logging/  log/slog setup
internal/platform/ host and runtime detection
internal/transport/ SaaS HTTP client (enroll, me, rotate-key, heartbeat, discovery)
deploy/helm/       local K3s chart
```

## Not implemented yet

**No VPN, tunnel or subnet-routing client.** This is deliberate, not pending:
the architecture treats the tunnel as customer-side infrastructure. The only
thing the code contributes is that discovery *excludes* tunnel interfaces
(`wg`, `tun`, `tap`, `tailscale`, `zt`, …) so it does not scan the tunnel.
Cross-subnet camera-target provisioning and automatic discovery across subnets
are likewise not implemented.

**Real WebSockets.** Not present — the current contracts are HTTP plus the
Edge-initiated control channel.

Everything else this file used to list here now exists and is merged: FFmpeg
decode and the video pipeline (Hito H), Cloud frame push with offline buffering
and bandwidth control (Hito I), local motion gating (Hito J), YOLO local
inference through the out-of-process Python Vision Worker plus events, evidence
and durable sync (Hito K), signed OTA (Hito T), remote configuration (Hito O)
and the residential appliance (Hito Q).

What remains is **not** a list of missing milestones but a set of concrete,
per-profile gaps and unvalidated claims — camera-target provisioning, the
Python worker not being packaged, the absence of any measured Hybrid bandwidth
saving, CUDA never having been exercised, and more. They are enumerated in
[docs/product/COMMERCIAL_MODES.md](docs/product/COMMERCIAL_MODES.md) §6 and §7.
