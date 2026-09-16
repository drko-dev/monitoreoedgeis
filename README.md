# GEO CAM Edge

Lightweight Go agent for GEO CAM edge gateways (Raspberry Pi, Orange Pi, mini-PC,
server). It runs on-site, next to the cameras, and talks to the GEO CAM SaaS.

Through Hito G, merged to `main`: agent core (config, persistent identity,
platform detection, health, logging, module lifecycle), SaaS enrollment and
credential rotation, SaaS heartbeat, ONVIF/WS-Discovery LAN autodiscovery,
per-device camera credential management, and RTSP camera connectivity with
reconnection and health state. There is still no local vision/YOLO inference,
video pipeline, OTA, or VPN (see [Not implemented yet](#not-implemented-yet)).

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
| `GEOCAM_CONNECTIVITY_ENABLED` | `true`               | Enables RTSP camera connectivity (Milestone G) |
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

FFmpeg, OpenCV, YOLO, PyTorch, the Vision Worker, real WebSockets, VPN, OTA,
and video pipeline/AI processing (decode, sampling, frame routing) belong to
subsequent milestones (Hito H and later). Camera credential management
(Hito F) and RTSP camera connectivity (Hito G) are already implemented and
merged to `main`.
