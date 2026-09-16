# GEO CAM Edge — Architecture

## Scope of this document

Describes the Go core as it exists today (Hito A) and the target architecture it
is being built towards. Anything marked **future** does not exist yet.

## Fundamentals

- **Core: Go.** One single codebase.
- **Targets: Linux amd64 and Linux arm64.**
- **Hardware agnostic.** Raspberry Pi is an *option*, not a requirement. Orange
  Pi, mini-PC, industrial appliance, VM and bare-metal Linux server are equally
  valid targets.
- **Three processing modes** — `cloud`, `hybrid`, `edge` — served by the same
  binary, not by three products.

## Target architecture

```
                 GEO CAM SaaS
                       ^
                       |
                  HTTPS / WSS
                       |
              GEO CAM EDGE CORE
                     GO
             _________|_________
            |         |         |
        discovery transport telemetry
            |
        cameras / RTSP
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

Mode semantics:

| Mode     | Meaning                                                       |
| -------- | ------------------------------------------------------------- |
| `cloud`  | Lightweight gateway. No local YOLO.                           |
| `hybrid` | Local preprocessing + Cloud processing.                       |
| `edge`   | Full local inference (via the future Vision Worker).          |

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

### Local health HTTP (Hito B)

The agent now binds a **localhost-only** `net/http` server (stdlib only, no
framework) exposing `/healthz`, `/readyz` and `/status`, serialising the same
`health.Reporter`/`health.Snapshot` that was internal-only in Hito A. Bind
address is `GEOCAM_HEALTH_ADDR` (default `127.0.0.1:8091`) — not exposed to
the LAN by default. It runs as the first (and so far only) `Module` (see
below).

### Persistent identity (Hito B)

`internal/identity` now persists a random UUID v4 `edge_id` to
`<data-dir>/identity.json` (schema `{edge_id, created_at, schema_version}`),
written atomically (temp file + rename), directory `0700`, file `0600`. The
id belongs to the *instance*, never derived from hardware. A corrupt file is
a hard error — the agent starts but stays `DEGRADED`, never silently
regenerating an identity. `GEOCAM_EDGE_ID` remains only as a non-persisting
dev override.

### Module lifecycle (Hito B)

`internal/agent/modules.go` defines a minimal `Module` interface
(`Name()`, `Start(ctx)`, `Stop(ctx)`) and an ordered manager: modules start
in order, a failed start unwinds whatever already started, and shutdown
stops modules in reverse order with context cancellation propagated. No real
module exists yet beyond the health HTTP server — discovery/transport/video
will implement this interface later.

### Heartbeat (Hito D)

`internal/heartbeat` is a `Module`, not a loop in `main.go`: it starts and
stops with the rest of the agent and unwinds cleanly on shutdown.

**Payload.** `edge_id, edge_version, uptime_seconds, architecture,
processing_mode, health_status, metrics{cpu_percent, memory_total_bytes,
memory_used_bytes, disk_total_bytes, disk_used_bytes, temperature_c}`.

The SaaS model is `extra="forbid"`, so an unknown field is a `422`, not a
field the server ignores. Two names were therefore reconciled against what
the SaaS already had rather than sent alongside it:

- `agent_version` → **`edge_version`**, the field the legacy Python agents
  already populate and the Edge admin UI already renders.
- `system{...}` → **`metrics{...}`**, the existing telemetry object, which
  already carried `cpu_percent`. Sending a second container beside it would
  have split resource readings across two shapes.

It deliberately carries **no tenant and no site**: the SaaS
derives those from the authenticated credential and must never take the
Edge's word for them. `edge_id` is sent only so the SaaS can *cross-check* it
against the authenticated device — it is never an identity claim.

- `uptime_seconds` is the **process's** uptime (monotonic), not the host's.
- `edge_version` comes from the single existing version source, not a literal.
- `architecture` reuses the existing `amd64`/`arm64` normalisation.

**Metrics.** Sampled by `internal/platform` with `CGO_ENABLED=0`: CPU from
procfs deltas, memory from `/proc/meminfo`, disk via `statfs` on
`GEOCAM_DATA_DIR`, temperature from `/sys/class/thermal/` when present. Every
metric degrades to "omitted" rather than panicking or sending a zero that
would read as a real measurement. **A missing sensor never means DEGRADED.**

**Scheduling.** `GEOCAM_HEARTBEAT_INTERVAL` (default `30s`, bounded `5s`–`5m`
inclusive). The first send is spread across a startup window so a fleet
restarting together does not stampede the SaaS. Transient failures back off
exponentially (1s → 60s cap) with ±10% jitter; a single success resets it.

**Error classification** decides the retry policy:

| Condition | Behaviour |
| --------- | --------- |
| timeout / network / 5xx | transient — exponential backoff |
| 429 | honour `Retry-After`, else fall back to backoff |
| 422 | payload does not match the server model; retrying an identical body cannot help |
| 401 / 403 | credential rejected — agent goes `DEGRADED`. **Never** retries aggressively, **never** discards the credential, **never** re-enrolls or generates a new one. |

**Health semantics.** Local health is independent of SaaS reachability: while
the SaaS is unreachable the Edge stays `READY` and both `/healthz` and
`/readyz` keep returning `200`, because the Edge's local function is
unaffected by an outage in a service it only reports to. The degradation is
visible in `/status` under `heartbeat` instead. A **rejected credential** is
the one SaaS-side condition that marks the whole agent `DEGRADED`, because an
Edge the SaaS refuses to recognise is genuinely not doing its job.

`/status` exposes the module's `state`, `last_success_at`, `last_attempt_at`,
`consecutive_failures` and a sanitized `last_error` **class**. It never
carries the credential, an `Authorization` header, a token or a hash — raw
transport errors are never echoed, since they can contain URLs and response
bodies and this field is served over HTTP.

Online vs offline is **not** the Edge's call: the Edge reports only its own
view of itself, and the SaaS derives connectivity from heartbeat arrival time
against its own clock.

### Camera credentials (Hito F, block 1)

`internal/cameracreds` caches per-camera ONVIF/RTSP credentials synced from
the SaaS. It is deliberately separate from `internal/credentials` (the
Edge's own SaaS enrollment credential) and from `internal/discovery` (which
this block does not touch): neither package imports the other, and no ONVIF
WS-Security/Digest auth is wired to it yet — that is the next block.

**Master key.** A random 32-byte AES-256 key is generated once and persisted
to `<data-dir>/camera_master.key` (dir `0700`, file `0600`, atomic
temp-file-then-rename, same pattern as `identity.json`). It is never derived
from the enrollment credential, so rotating that credential can never make
the camera-credentials cache unreadable. A present-but-corrupt key file
(wrong size, unreadable) is a hard error — it is never silently regenerated,
since that would permanently orphan an already-encrypted cache. The key
value and its length are never logged.

**Encrypted persistence.** Credentials live in
`<data-dir>/camera_credentials.json`, written atomically with the same
0700/0600 pattern. Each entry's password is sealed with AES-256-GCM under
the local master key as `base64(nonce || ciphertext || tag)`; username and
metadata (id, scope, target id, revision) stay in plaintext JSON since they
are not secrets. The file's schema is versioned like `identity.json` and
`credentials.json`, and a malformed file is a hard error, never silently
discarded and recreated.

**Sync contract.** `internal/cameracreds.Syncer` calls
`transport.Client.FetchCameraCredentials`, a GET against
`transport.CameraCredentialsPath` (currently
`/api/v1/gateway/camera-credentials` — **assumed**, not yet confirmed
against SaaS source; the SaaS team is building this endpoint in parallel, so
only that one constant needs to change if the real route differs). The
response is `{"credentials": [{id, scope, candidate_keys[], username, password,
revision, revoked}]}`, treated as the SaaS's full, authoritative snapshot of
currently active credentials — not an incremental diff. `Store.Apply` then
applies per-entry revision/idempotency rules:

| Incoming vs. cached (by id)        | Result                                   |
| ----------------------------------- | ----------------------------------------- |
| not cached yet, or higher revision  | replaces the cached entry                 |
| same revision                       | no-op                                     |
| lower (stale) revision              | ignored, cached entry kept                |
| id absent from the response, or `revoked: true` | removed from the local cache  |

A fetch failure (SaaS unreachable, timeout, unauthorized, ...) or a
malformed payload (bad scope, missing fields) leaves the cache exactly as it
was and is logged with a sanitized reason/error class only — the
request/response body and the credentials it carries are never logged.
`cameracreds.Module` (Name/Start/Stop, same shape as the other modules)
polls `Syncer.Sync` on a fixed interval (`DefaultSyncInterval`, 5 minutes);
it is not yet registered in the agent's module manager — that wiring is left
for whichever block first needs credentials flowing automatically, since
this block's contract works equally well driven by an explicit `Sync` call.

**Resolution.** `cameracreds.Provider.Resolve(stableIdentity, groupID)`
prefers a `DEVICE`-scoped assignment matched by Hito E's
`discovery.Candidate.StableIdentity` (never an IP address) and falls back to
a `GROUP`-scoped assignment matched by a SaaS-defined group id. It returns
`ok=false` when neither matches. It is read-only and does not import
`internal/discovery`, so wiring it into ONVIF authentication remains entirely
the next block's job.

## Current modules (Go core)

| Package             | Responsibility                                                            |
| ------------------- | ------------------------------------------------------------------------- |
| `cmd/geocam-edge`   | Thin entrypoint: flags, config load, signal context, hand off to the agent |
| `internal/agent`    | Agent core: wiring, startup sequence, run loop, graceful shutdown, version |
| `internal/config`   | Env-var configuration, safe defaults, `ProcessingMode` type and validation |
| `internal/identity` | Persistent `edge_id` (UUID v4, `identity.json`). No tokens, no certs      |
| `internal/platform` | Host detection + live resource sampling: CPU %, memory, disk, temperature  |
| `internal/health`   | Lifecycle state (`STARTING`/`READY`/`DEGRADED`/`STOPPING`) + snapshot + local HTTP (`/healthz`, `/readyz`, `/status`) |
| `internal/logging`  | `log/slog` setup: stdout, structured, base fields, no secrets              |
| `internal/transport`| SaaS HTTP client: enrollment, rotation, `me`, heartbeat, discovery next/report. Bearer auth, typed error classes |
| `internal/heartbeat`| Periodic heartbeat module: scheduling, jitter, backoff, error classification |
| `internal/discovery`| ONVIF WS-Discovery, unauthenticated SOAP enrichment, local inventory, SaaS pull module |
| `internal/cameracreds`| Per-camera credential cache: local AES-256-GCM encryption, SaaS sync, DEVICE/GROUP resolution |

### Startup sequence

1. `main` dispatches CLI subcommands (`identity`, `check`) or parses flags
   (`--version`) and loads config; an invalid value exits 1.
2. `agent.New` resolves/persists identity, detects the platform, builds the
   logger, the health reporter (state `STARTING`) and the module manager
   (currently: the health HTTP server module).
3. `agent.Run` logs version/platform/config/identity, starts every module. If
   identity failed to resolve or any module failed to start, state becomes
   `DEGRADED` and stays there — the agent keeps running (health HTTP stays
   reachable) instead of crash-looping. Otherwise state becomes `READY`. Then
   it blocks on the signal context.
4. SIGINT/SIGTERM cancels the context; the agent sets `STOPPING`, stops every
   module in reverse order (bounded by a 5s shutdown context) and returns nil
   (exit 0).

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
| `internal/cameras`     | Camera inventory, credentials, per-camera state               |
| `internal/telemetry`   | Metrics and operational telemetry to the SaaS                 |
| *(separate process)*   | **Vision Worker** — Python + YOLO/Ultralytics, `edge` mode only |

`internal/processing` (Milestone H — Video Pipeline) is no longer reserved;
it exists and is documented in its own section below.

## Video pipeline (`internal/processing`, Milestone H)

Downstream of Hito G's existing RTSP/RTP transport (`internal/rtsp`), never a
second RTSP client. Chain: `rtsp.PacketSink` (video RTP only, RTCP filtered
out in `internal/rtsp`) → H.264 depacketize (single NALU/FU-A/STAP-A,
whole-access-unit drop on packet loss) → decode → FPS sampling → resize →
bounded ring buffer → `Router`/`Sink` (only `DebugSink` in this milestone —
Cloud/Hybrid/Edge-YOLO sinks are I/J/K).

**Hook-in.** `internal/rtsp/supervisor.go`'s `streamLoop()` now calls
`session.VideoChannel()` — the interleaved channel actually negotiated in
SETUP (RFC 2326 Transport header), not a hardcoded `0` — to decide whether a
packet is video RTP before it ever reaches a `PacketSink`. RTCP on the
sibling channel never crosses this boundary. `rtsp.Manager.SetPacketSink`
applies atomically to every existing `Supervisor` and to any created
afterward, and `processing.Manager.Stop()` deregisters the sink and flips a
`stopped` flag *before* cancelling any pipeline — no in-flight `OnPacket`
call can ever land on a closed channel (see
`internal/processing/manager_test.go`'s `-race` lifecycle test).

**Decoder: FFmpeg as a subprocess, not cgo.** `internal/processing.FFmpegDecoder`
runs one `ffmpeg` OS process per camera (`os/exec`, Annex-B via stdin,
`rawvideo`/`yuv420p` via stdout) — never linked, never `import "C"`.
Alternatives considered and rejected:
- **cgo ffmpeg bindings** — would require `CGO_ENABLED=1` and libav* dev
  headers, breaking the current static `CGO_ENABLED=0` build.
- **Pure-Go H.264 decoder** — no production-grade implementation exists for
  main/high profile.
- **Hand-written decoder** — explicitly out of scope for this milestone.

This makes ffmpeg a **runtime** dependency (a binary must exist in the
container/host), not a build-time/Go-module one: `go.mod` gains nothing,
`CGO_ENABLED` stays `0`, and `make build-linux`'s pure-Go cross-compile is
unaffected. SPS/PPS are parsed from the SDP `a=fmtp sprop-parameter-sets`
attribute (`internal/rtsp/client.go`) and injected (Annex-B) at decoder
start/restart, since some cameras don't repeat them in-band.

**Codec ground truth: SDP over ONVIF.** Real TC70 validation surfaced a bug
in this repo's ONVIF `GetProfiles` handling (`internal/discovery/onvif`,
Milestone E): it mismaps video/audio encoder metadata for this camera
model, reporting `G711` (an audio codec) for the video profile regardless
of the actual video codec. Rather than patch Milestone E's ONVIF client
(out of this milestone's scope) or trust a field known to be wrong, the
video pipeline uses the codec SDP itself declares for the video payload
type (`a=rtpmap`, RFC 4566 §6) — the on-the-wire, RFC-mandated source of
truth — parsed in `internal/rtsp/client.go` and preferred by `Supervisor`
when building a `StreamDescriptor`. `CameraStreamStatus.Codec` (surfaced
elsewhere, e.g. existing health output) is untouched, keeping this a
minimal, scoped fix rather than a behavior change to Hito G's status
reporting.

**Timestamps are honest about what they are.** `rawvideo` over an ffmpeg
pipe carries no RTP timestamp. `DecodedFrame` has no `RTPTime` field —
`SourceReceivedAt` is a documented best-effort FIFO correlation with the
ingest time of the access unit presumed to have produced that frame
(accurate for baseline/no-B-frame streams like the TC70's, degrades with
B-frame reordering), and `DecodedAt` is the real wall-clock read time.
Real TC70 runs showed ~1.1-1.4s of `SourceReceivedAt`→`DecodedAt` latency,
consistent with ffmpeg's own internal one-frame buffering (confirmed
separately: it does not flush a decoded frame to stdout until either the
next frame's data arrives or stdin closes) — an inherent property of a
live, non-terminating stream, not a pipeline bug.

**Backpressure (H9).** Every stage boundary is a fixed-capacity channel or
ring buffer, never unbounded: the packet queue (drop-*new*, since FU-A
reassembly needs strict order), the access-unit queue, the decoder's own
small output queue (raw yuv420p frames are large — kept deliberately
smaller than the general queue depth via `GEOCAM_VIDEO_DECODE_QUEUE_DEPTH`),
the ring buffer (drop-oldest), and each `Sink`'s own queue in `Router`
(one queue + one worker per sink, so a slow sink never blocks the others).
`GEOCAM_VIDEO_MAX_CONCURRENT_PIPELINES` bounds the number of concurrent
ffmpeg subprocesses.

**`/status`.** A small `video_pipeline` block (`camera_count` +
`PipelineStatus` per camera: state, codec, input/decoded/output FPS,
frames received/decoded/sampled/dropped, queue depth, buffer usage, decode
latency) — no frame bytes, no per-frame history. No `video probe` CLI
subcommand: the pipeline runs inside the daemon, so polling `/status` is a
strictly better validation path than a separate short-lived CLI probe that
would have to reimplement pipeline startup.

## Docker image: ffmpeg dependency and its license (Milestone H)

The final image (`gcr.io/distroless/static-debian12:nonroot`) gains one
additional layer: a static, multi-arch `ffmpeg` binary copied from
`mwader/static-ffmpeg`, pinned by digest
(`sha256:415a41fa3167b890b9703d20bd0f00bf1e9dab8a4b6c27fef1445b2bf5f1ab4a`),
verified via `docker buildx imagetools inspect` to cover both
`linux/amd64` and `linux/arm64`. `go.mod` gains nothing — this is a
container-layer addition, not a Go dependency.

**License — reported, not hidden.** This image's ffmpeg is built with
`libx264`/`libx265` enabled (confirmed against
`github.com/wader/static-ffmpeg`'s own Dockerfile, which always builds both,
not behind an opt-in flag the way it gates `libfdk-aac`/`--enable-nonfree`).
Enabling those requires ffmpeg's `--enable-gpl` — **this is a GPL-licensed
binary, not an LGPL-only one**, even though this project only uses it for
H.264 *decode* (ffmpeg's native decoder, not the GPL-licensed encoders).
Bundling it means the shipped image contains GPL-licensed code. This was
not assumed to be LGPL-safe and silently shipped; it is an open decision:
either accept GPL distribution (with whatever source-availability
obligations that implies for this image), or replace this layer with a
Dockerfile stage that builds ffmpeg from source with `--disable-gpl
--disable-nonfree` (decode-only, no libx264/libx265, LGPL-compliant) —
not implemented in this milestone, since it is a licensing/business
decision, not an engineering one.

## Deployment

- **Targets:** `linux/amd64`, `linux/arm64`. Static, `CGO_ENABLED=0`.
- **Image:** multi-stage build onto distroless/static, non-root, binary only,
  plus a pinned static `ffmpeg` binary (Milestone H — see above; a runtime
  dependency, not a build-time/cgo one).
- **Local dev:** Rancher Desktop + containerd + K3s. Images are built with
  `nerdctl --namespace k8s.io` — Docker Engine is not used. Chart deploys into
  the `geocam-edge-dev` namespace with its own Helm release.
- **Isolation:** Edge has its own repo, chart, namespace and release. It shares
  nothing with the GEO CAM SaaS (`geocam` namespace).
