# GEO CAM Edge — Architecture

## Scope of this document

Describes the Go core as it exists today (Hito A) and the target architecture it
is being built towards. Anything marked **future** does not exist yet.

## Target architecture

```
                 SaaS
                  ^
                  |
              HTTPS/WSS
                  |
            GEO CAM EDGE
             CORE — GO
        _________|_________
       |         |         |
 discovery   transport   health
       |
 cameras/RTSP
       |
 video pipeline
       |
 processing mode
     /     |      \
 cloud  hybrid   edge
                 |
           Vision Worker
          Python / YOLO
```

> The Vision Worker **does not exist yet**. No Python, no YOLO, no PyTorch is
> present in this repository or in the container image.

## Design decisions

### Go for the core

The core is a long-lived daemon on constrained hardware. Go gives a single
static binary, low idle RAM, fast startup, simple cross-compilation to
`linux/amd64` and `linux/arm64`, and good concurrency primitives for the
fan-out work (many cameras, heartbeats, buffering) that is coming.

### Inference stays out of the Go core

Deliberately. YOLO/Ultralytics/PyTorch is a Python ecosystem; embedding it would
force a heavy image and a Python runtime onto every gateway, including the
`cloud`-mode ones that never run inference. Instead the core will talk to a
separate **Vision Worker** process, deployed only where the processing mode
requires it.

Consequence: the `cloud`-mode agent stays tiny (a static binary on distroless),
which is the common case.

### One codebase, three modes

`cloud`, `hybrid` and `edge` are a typed enum (`config.ProcessingMode`), not
three products. The mode is validated at startup, carried in the runtime context
and surfaced in the health snapshot. This milestone implements **no functional
difference** between them — only the modeling.

### No HTTP server yet

The agent exposes no port. Health is an internal, testable
`health.Reporter`/`health.Snapshot` rather than an endpoint. A future HTTP or
gRPC surface serialises the same snapshot; adding a listener now would be an
unused attack surface.

## Current modules (Go core)

| Package             | Responsibility                                                            |
| ------------------- | ------------------------------------------------------------------------- |
| `cmd/geocam-edge`   | Thin entrypoint: flags, config load, signal context, hand off to the agent |
| `internal/agent`    | Agent core: wiring, startup sequence, run loop, graceful shutdown, version |
| `internal/config`   | Env-var configuration, safe defaults, `ProcessingMode` type and validation |
| `internal/identity` | Enrollment abstraction: `UNENROLLED` / `ENROLLED`. No tokens, no certs     |
| `internal/platform` | Host detection: hostname, OS, GOOS/GOARCH, kernel, CPU count, total RAM    |
| `internal/health`   | Lifecycle state (`STARTING`/`READY`/`DEGRADED`/`STOPPING`) + snapshot      |
| `internal/logging`  | `log/slog` setup: stdout, structured, base fields, no secrets              |

### Startup sequence

1. `main` parses flags (`--version`) and loads config; an invalid value exits 1.
2. `agent.New` derives identity, detects the platform, builds the logger and the
   health reporter (state `STARTING`).
3. `agent.Run` logs version/platform/config/identity, sets state `READY`, and
   blocks on the signal context.
4. SIGINT/SIGTERM cancels the context; the agent sets `STOPPING`, shuts down and
   returns nil (exit 0).

### Platform detection portability

`internal/platform` uses only `runtime`, `os.Hostname` and procfs reads
(`/etc/os-release`, `/proc/sys/kernel/osrelease`, `/proc/meminfo`). Nothing is
Raspberry-specific and nothing needs cgo. Values unavailable on the host — total
RAM and kernel on macOS, for example — are reported as `unknown`/`0` rather than
causing a failure. An unsupported `GOARCH` is reported via `arch_supported=false`
and a warning, never a panic.

## Future modules

Reserved package names, none of which exist yet — they will be created when they
carry real code:

| Package                | Responsibility (future)                                       |
| ---------------------- | ------------------------------------------------------------- |
| `internal/discovery`   | ONVIF / WS-Discovery camera discovery on the local network     |
| `internal/cameras`     | Camera inventory, credentials, per-camera state               |
| `internal/transport`   | SaaS transport: enrollment, heartbeat, HTTPS/WSS, buffering    |
| `internal/telemetry`   | Metrics and operational telemetry to the SaaS                 |
| `internal/processing`  | Video pipeline and mode-specific behavior                      |
| *(separate process)*   | **Vision Worker** — Python + YOLO/Ultralytics, `edge` mode only |

## Deployment

- **Targets:** `linux/amd64`, `linux/arm64`. Static, `CGO_ENABLED=0`.
- **Image:** multi-stage build onto distroless/static, non-root, binary only.
- **Local dev:** Rancher Desktop + containerd + K3s. Images are built with
  `nerdctl --namespace k8s.io` — Docker Engine is not used. Chart deploys into
  the `geocam-edge-dev` namespace with its own Helm release.
- **Isolation:** Edge has its own repo, chart, namespace and release. It shares
  nothing with the GEO CAM SaaS (`geocam` namespace).
