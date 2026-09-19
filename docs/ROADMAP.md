# ROADMAP — GEO CAM Edge master backlog

> **The identifiers (A1, B7, K12, …) are STABLE.**
> Do not renumber them. Do not reinterpret them. They are referenced from
> commits, PRs, `docs/PROJECT_STATUS.md` and session handoffs.

## Block status

| Block | Status                |
| ----- | --------------------- |
| A     | DONE / MERGED         |
| B     | DONE / MERGED         |
| C     | DONE / MERGED         |
| D     | DONE / MERGED         |
| E     | DONE / MERGED         |
| F     | DONE / MERGED         |
| G     | DONE / VALIDATED LOCAL|
| H     | NEXT                  |
| O     | CODE DONE / INTEGRATED TESTED / MERGED |
| P     | CODE DONE / INTEGRATED TESTED / MERGED |
| Q1–Q10| CODE DONE / INTEGRATED TESTED / MERGED / SAAS PROD DEPLOYED (Edge prod: N/A) |
| R7–R9 | CODE DONE / DOCUMENTED (R1–R6 not done, out of scope) |
| I–Z   | PLANNED               |

Hito A partially advanced some primitives that belong to B (process lifecycle,
config, health, identity abstraction, platform/version). **That does not mean B
is closed.** B remains open until its own acceptance criteria are met.

## Main MVP path

```
A -> B -> C -> D -> E -> F -> G
```

When G closes, GEO CAM Edge starts, has persistent identity, enrolls, sends
heartbeat, discovers cameras/DVR/NVR, manages credentials, obtains and validates
RTSP, and sends inventory and state to the SaaS — **still without YOLO**.

After that: **H + I** (Video Pipeline + Cloud Vision). Then **J** (Hybrid). Then
**K** (Full Edge).

---

## A — Base del repo y arquitectura

- A1. Crear estructura inicial de geocam-edge.
- A2. Definir arquitectura modular del Edge.
- A3. Definir targets Linux amd64 y arm64.
- A4. Definir configuración común y perfiles cloud/hybrid/edge.
- A5. Definir logging, errores y convenciones.
- A6. Definir versionado del agente.
- A7. Dockerfile/multi-arch base.
- A8. CI mínima: lint/tests/build.
- A9. Documentación de arquitectura y roadmap.
- A10. Contrato inicial Edge ↔ SaaS.

## B — Agent Core

- B1. Proceso principal del agente. **DONE** (Hito A, kept as-is).
- B2. Identidad persistente del dispositivo. **DONE** — `identity.json`, UUID v4, atomic write.
- B3. edge_id/gateway_id. **DONE** — `edge_id` only; no `gateway_id` concept introduced.
- B4. Lectura y validación de configuración. **DONE** — `GEOCAM_HEALTH_ADDR` added.
- B5. Estado interno del agente. **DONE** — `health.Reporter` extended with module state.
- B6. Gestión de módulos. **DONE** — `internal/agent/modules.go`, ordered start/stop.
- B7. Graceful shutdown/restart. **DONE** — module `Stop` wired into shutdown; restart not applicable (process-level).
- B8. Información de versión/build. **DONE** — exposed via CLI and `/status` (was already present in Hito A).
- B9. Arquitectura/capacidades del hardware. **DONE** — unchanged from Hito A (`internal/platform`), exposed via `/status`.
- B10. Health check local. **DONE** — `/healthz`, `/readyz`, `/status` over stdlib `net/http`.

**B — DONE.** Code-level criteria for B1–B10 pass locally (tests, vet, fmt,
builds) and K3s validation passed: probes green, PVC bound, `edge_id`
identical across pod recreation. See `docs/PROJECT_STATUS.md`.

## C — Enrollment con SaaS

- C1. Flujo de alta. **CODE DONE** — `geocam-edge enroll` (`cmd/geocam-edge/main.go`), token vía stdin/env/`--token` dev.
- C2. Código/token temporal de enrollment. **CODE DONE** — token nunca persistido, nunca logueado; consumido una sola vez por request.
- C3. Asociación Edge → tenant/site. **CODE DONE (cliente)** — `tenant_id`/`site_id` persistidos desde la respuesta del SaaS; nombres de campo son SUPUESTOS, ver `docs/PROJECT_STATUS.md`.
- C4. Credencial propia del Edge. **CODE DONE** — `internal/credentials`, separada de `identity.json`.
- C5. Persistencia segura. **CODE DONE** — `credentials.json`, atomic write (temp+rename), dir 0700/file 0600, nunca expuesta en `/status`/logs/CLI.
- C6. Rotación. **CODE DONE** — `geocam-edge credential rotate`, atomic write antes de invalidar la credential vieja en disco, verificación post-rotación vía `/edge/me`.
- C7. Revocación. **CODE DONE (detección, no persistida)** — un 401/403 de `/edge/me` se reporta como revocación al operador; el SaaS es quien revoca, este repo solo detecta.
- C8. Re-enrollment. **PARTIAL** — doble enrollment bloqueado explícitamente; no existe todavía un flujo explícito de re-enrollment forzado (ej. `--force`) más allá de borrar `credentials.json` a mano.
- C9. API SaaS para Edge. **OUT OF SCOPE (repo monitoreoia)**.
- C10. UI SaaS Edge/Gateways. **OUT OF SCOPE (repo monitoreoia)**.

**C — DONE / MERGED / VALIDATED.** Todo lo anterior compila, pasa
`go test ./...`, `go test -race ./...`, `go vet ./...` y `gofmt -l .`, y fue
reconciliado y validado end-to-end contra la implementación real de
`monitoreoia` (SaaS local) además de contra `httptest`, incluida su
deployment en K3s. Ver `docs/PROJECT_STATUS.md` para el detalle.

## D — Heartbeat y administración

- D1. Heartbeat Edge → SaaS. **DONE / VALIDATED LOCAL** — `internal/heartbeat`, integrado al ciclo de vida de módulos (`Module`), no un loop en `main.go`. Auth `Bearer` reutilizando `internal/transport`, sin cliente HTTP duplicado.
- D2. online/offline/degraded. **DONE / VALIDATED LOCAL** — el Edge reporta sólo su propia visión (`health_status`: READY→healthy, DEGRADED→degraded). La conectividad la deriva el SaaS del momento de llegada contra su propio reloj; el Edge nunca la declara. Validado contra el SaaS real: Online, Offline tras threshold, y recovery.
- D3. Uptime. **DONE / VALIDATED LOCAL** — uptime del **proceso** (monotónico), no del host.
- D4. Versión. **DONE / VALIDATED LOCAL** — desde la fuente única existente (`internal/agent/version.go`), no hardcodeada.
- D5. CPU/RAM/disco. **DONE / VALIDATED LOCAL** — `internal/platform`, `CGO_ENABLED=0`: CPU por deltas de procfs, RAM de `/proc/meminfo`, disco por `statfs` sobre `GEOCAM_DATA_DIR`. Degrada omitiendo la métrica, nunca con panic ni con un cero que se lea como medición real.
- D6. arm64/amd64. **DONE / VALIDATED LOCAL**.
- D7. Temperatura. **DONE / VALIDATED LOCAL (opcional)** — `/sys/class/thermal/` en Linux si existe. Su ausencia se omite del payload y **nunca** marca DEGRADED.
- D8. Última conexión. **DONE / VALIDATED LOCAL (repo monitoreoia)** — `last_seen` es server-side, con el reloj del SaaS. El Edge manda `edge_timestamp` sólo como dato de diagnóstico.
- D9. Intervalo configurable. **DONE / VALIDATED LOCAL** — `GEOCAM_HEARTBEAT_INTERVAL`, default `30s`, límites `5s`–`5m` inclusive; fuera de rango es error de arranque.
- D10. Retry/backoff. **DONE / VALIDATED LOCAL** — backoff exponencial 1s→60s con jitter ±10%, reset ante el primer éxito. 429 respeta `Retry-After`. 401/403 **no** reintenta agresivamente, **no** borra la credential, **no** re-enrola. Validado con revocación real contra el SaaS: 401/403 → DEGRADED sin re-enroll.
- D11. Vista de health en SaaS. **DONE / VALIDATED LOCAL (repo monitoreoia)**.

**D — DONE / VALIDATED LOCAL.** Compila y pasa `go build ./...`,
`go test ./...`, `go test -race ./...`, `go vet ./...` y `gofmt -l .`.
Validado end-to-end contra el SaaS real local: Online, Offline, recovery,
degraded al caer el SaaS y recovery automático, revocación (401/403), mismo
`edge_id`/credential preservados. Validado también en K3s local (Rancher
Desktop): recreación de pod, PVC-backed identity/credential, `boot_id`
nuevo por proceso, `sequence_number` reiniciando correctamente, ≥2
heartbeats post-recreation y SaaS Online. Una caída del SaaS deja al Edge
`READY` con `/healthz` y `/readyz` en `200`; la degradación se ve sólo en
`/status` bajo `heartbeat`. Ver `docs/ARCHITECTURE.md` para la semántica
completa y `docs/PROJECT_STATUS.md` para el detalle de validación.

## E — Autodiscovery
 
- E1. ONVIF WS-Discovery. **DONE** — `internal/discovery/wsdiscovery` (pure stdlib multicast `239.255.255.250:3702`, bounded XML parsing).
- E2. Escaneo de dispositivos. **DONE** — `internal/discovery/engine.go` (auto-private interface selection, UDP probe, timeout bounded).
- E3. IP/fabricante/modelo. **DONE** — passive metadata extraction + ONVIF `GetDeviceInformation` enrichment.
- E4. Cámaras. **DONE** — classified via scopes and types (`DeviceTypeCamera`).
- E5. DVR/NVR. **DONE** — classified via multichannel presence / scopes (`DeviceTypeNVR` / `DeviceTypeDVR`).
- E6. Múltiples canales. **DONE** — architectural rule: 1 IP != 1 camera; `DiscoveredDevice` hosts multiple `VideoSource` entries.
- E7. Capabilities. **DONE** — `GetCapabilities` in `internal/discovery/onvif`.
- E8. Profiles. **DONE** — `GetProfiles` in `internal/discovery/onvif`.
- E9. Resolución/FPS/codecs. **DONE** — extracted in `MediaProfile`.
- E10. URI RTSP. **DONE** — `GetStreamUri` with fail-closed sanitization (credentials strictly purged, passwords with special chars stripped).
- E11. Deduplicación. **DONE** — stable identity hierarchy: EPR UUID > Serial > normalized endpoint IP:port:path.
- E12. Re-discovery. **DONE** — background rediscovery scheduler with jitter and mutex serialization.
- E13. Inventario local. **DONE** — thread-safe `Inventory` (`internal/discovery/types.go`).
- E14. Inventory → SaaS. **DONE** — pull model: `ClaimNextDiscoveryRun` + `ReportDiscoveryRun` (`internal/transport/discovery.go`, `internal/discovery/module.go`).
- E15. UI dispositivos encontrados. **DONE (SaaS)** — `monitoreoia` `edge_devices.js` candidate rendering, device types, auth badges.
- E16. Confirmar/rechazar. **DONE (SaaS)** — migration 019 (`'discovered' | 'confirmed' | 'ignored'`), endpoints, UI confirmation without premature camera creation.

**E — DONE / MERGED.** Pure stdlib Go, `CGO_ENABLED=0`, cross-compiles to `linux/amd64` and `linux/arm64`, 100% tests passing with race detector, real device tested on LAN (Tapo TC70 detected at 192.168.0.6:2020), full E2E flow Edge ↔ SaaS validated (discovery/next + report, candidate persistence, confirmation, deduplication, auth_required, simulator scenarios). SaaS backward compatible with existing tables and models. Merged into `main` via PR #5.

## F — Credenciales de cámaras

- F1. Credenciales por dispositivo.
- F2. Credenciales por grupo.
- F3. Storage cifrado.
- F4. No passwords en logs.
- F5. Test ONVIF.
- F6. Test RTSP.
- F7. Rotación.
- F8. Sync seguro Edge.

## G — Camera Connectivity
 
- G1. Cliente RTSP. **DONE** — `internal/rtsp` (DESCRIBE -> SETUP -> PLAY -> TEARDOWN over TCP interleaved `RTP/AVP/TCP;unicast;interleaved=0-1`).
- G2. Reconexión. **DONE** — `internal/rtsp/supervisor.go` exponential backoff (1s -> 2s -> 4s -> ... -> max 60s), bounded and context-controlled.
- G3. Timeouts. **DONE** — Read timeout / silence threshold (5s), auto-degraded, auto-reconnect.
- G4. Health cámara. **DONE** — state machine `connecting`, `online`, `degraded`, `offline` in `CameraStreamStatus`.
- G5. Main/substream. **DONE** — default `sub`, configurable via `GEOCAM_STREAM_ROLE`.
- G6. Codec. **DONE** — extracted from ONVIF `MediaProfile` (H.264 / H.265 / etc.).
- G7. FPS. **DONE** — extracted from ONVIF `MediaProfile`.
- G8. Resolución. **DONE** — width & height extracted from ONVIF `MediaProfile`.
- G9. Stream caído. **DONE** — socket closure, EOF, timeout, auth failure handled cleanly with transition to degraded/offline and reconnect.
- G10. Estado en SaaS. **DONE** — heartbeat telemetry `cameras: [...]` upserted to `edge_camera_status` and exposed in discovery candidates table.
- G11. Métricas de pérdida/reconexión. **DONE** — `packets_received`, `bytes_received`, `reconnect_count`, `last_packet_at`, `last_error_safe` tracked without secret leakage.

**G — DONE / VALIDATED LOCAL.** Pure stdlib Go, `CGO_ENABLED=0`, cross-compiles to `linux/amd64` and `linux/arm64`, 100% tests passing with race detector, live Tapo TC70 camera streaming validated on LAN (`192.168.0.6:554/stream2`), 20+ packets received in 1.47s. Zero FFmpeg, zero video decoding, zero video frames. Ready for PR.

## H — Video Pipeline (IMPLEMENTED / TESTED / VALIDATED LOCAL — real TC70 — `feature/video-pipeline`, not merged)

- H1. Ingest RTSP. **DONE** — reuses Hito G's `Supervisor.streamLoop()` via `rtsp.PacketSink`, no second RTSP client.
- H2. Decode. **DONE** — H.264 via FFmpeg subprocess (`internal/processing.FFmpegDecoder`), SPS/PPS from SDP.
- H3. Sampling. **DONE** — `GEOCAM_VIDEO_TARGET_FPS`, `Sampler.ShouldEmit`.
- H4. Reducción FPS. **DONE** — validated real: TC70 ~15 FPS → 2 FPS target.
- H5. Resize. **DONE** — `GEOCAM_VIDEO_OUTPUT_WIDTH/HEIGHT`, validated real: 640x360 → 320x180.
- H6. Main/substream. **DONE** — reuses `GEOCAM_STREAM_ROLE`, no second logic.
- H7. Buffer circular. **DONE** — `RingBuffer`, drop-oldest on full, `-race`-tested.
- H8. Frame routing. **DONE** (interface + `DebugSink` only, per scope — Cloud/Hybrid/Edge-YOLO sinks are I/J/K).
- H9. Backpressure. **DONE** — bounded channels/ring buffer at every hop, drop policy documented per hop.
- H10. Límites CPU/RAM. **DONE** — `GEOCAM_VIDEO_MAX_CONCURRENT_PIPELINES`, small decode queue, `/status` `video_pipeline` summary.
- H11. Independiente de YOLO. **DONE** — `pipeline_test.go`'s fake-decoder acceptance test + real TC70 run, zero YOLO/Vision-Worker/Cloud involvement.

Not done / deliberately out of scope: `video probe` CLI (justified, `/status` covers it), H.265 decode. FFmpeg GPL-vs-LGPL licensing: **resolved** — migrated to a from-source, LGPL-only ffmpeg build (`Dockerfile`'s `ffmpeg-build` stage), verified on **both** `linux/arm64` (local, image 55.69MB→5MB compressed) and `linux/amd64` (CI on real hardware, [run 35092394083](https://github.com/drko-dev/monitoreoedgeis/actions/runs/35092394083), PASS in ~2min) — no GPL/nonfree flags, decode PASS on both. Docker image smoke build: **done, PASS** on both architectures. **No remaining pre-merge gaps.** See `docs/PROJECT_STATUS.md` for full detail, the real bugs found (ONVIF codec mismap, decode queue depth not wired, missing stall watchdog, unbounded buffers, metrics semantics, a shutdown deadlock) and how they were handled.

**HITO H — READY FOR FINAL REVIEW**

## I — Modo Cloud (first slice: Edge→SaaS frame push — `feature/video-pipeline` Edge / `feature/edge-frame-push` SaaS, not merged/deployed)

- I1. processing_mode=cloud. **DONE (gate)** — `internal/agent/cloudsink_module.go`'s `newCloudSink` reuses the existing `GEOCAM_PROCESSING_MODE` (`config.ModeCloud`), no new env var duplicating it.
- I2. Sin YOLO pesado local. **N/A by construction** — the Edge only encodes/uploads JPEG; no inference code was added to this repo.
- I3. Sampling configurable. **DONE** — reuses Hito H's `GEOCAM_VIDEO_TARGET_FPS`/`Sampler` unchanged; the cloud sink never samples independently.
- I4. Compresión/transporte. **DONE (first cut)** — `internal/cloudsink.CloudSink` encodes each sampled `Frame` (yuv420p) to JPEG (quality 85, no RGB round-trip) and uploads it as one `POST /api/v1/edge/frames` per frame (`internal/transport.Client.PostFrame`). No WebSocket/batching yet — deliberately deferred until real bandwidth numbers justify it.
- I5. Frames/substreams a Cloud. **DONE** — Router dispatches every routed `Frame` to `CloudSink` in addition to `DebugSink`; substream selection is unchanged (Hito H's `GEOCAM_STREAM_ROLE`).
- I6. Buffer offline. **DONE CODE** — a recoverable `PostFrame` failure (timeout, SaaS unavailable, or HTTP 408/429/5xx) now spools to a bounded, disk-backed FIFO (`internal/cloudsink.Buffer`) instead of being dropped; a background drain goroutine replays it with capped backoff. `ErrUnauthorized`/`ErrInvalidRequest` (permanent) still drop immediately, never spooled. Not yet validated against a real TC70→SaaS outage (see I10 real-benchmark status below).
- I7. Bandwidth control. **DONE CODE** — configurable JPEG quality (`GEOCAM_CLOUD_JPEG_QUALITY`, default 85) and an optional token-bucket rate limiter (`GEOCAM_CLOUD_MAX_BYTES_PER_SEC`/`GEOCAM_CLOUD_BURST_BYTES`/`GEOCAM_CLOUD_MAX_FPS`, all 0 = unlimited by default). A throttled frame is a policy drop (`ErrThrottled`) before POST, never a transport failure — it never enters the I6 buffer. I6 replay paces itself through the same limiter (`TokenBucket.Wait`, blocking/cancelable) rather than dropping an already-durable frame for lack of tokens.
- I8. Worker Cloud. **DONE (SaaS side)** — `cloud_vision_worker.py`'s new `POST /internal/cameras/{camera_id}/frame` IPC route + `CloudVisionManager.push_frame()` feed the existing YOLO detector/persistence path; the RTSP-pull loop is now skipped for any camera linked to an Edge device (`edge_device_cameras`), never both paths at once.
- I9. Eventos al SaaS. **Unchanged** — detection events still flow through the existing `coordinate_event_ingestion_transaction` path once `push_frame` hands a frame to the same detection loop RTSP-pull used; no new event schema.
- I10. Métricas de costo. **DONE CODE** — `internal/cloudsink.Status` (lock-free atomic counters) tracks real frames encoded/upload-attempted/succeeded/failed, JPEG bytes generated vs. actually uploaded, encode/upload latency, effective bytes/frames per second, plus I7's throttle counters and configured limits. Published under `/status`'s `Snapshot.Cloud` (`omitempty`); the separate SaaS heartbeat payload is untouched. Real TC70→SaaS benchmark: **VALIDATION PENDING** — no TC70/SaaS reachable in the environment this integration ran in; only the local encode+loopback-HTTP fallback benchmark has been run (`docs/performance/i10-edge-cloud-cost-metrics.md`), explicitly marked as a synthetic-noise worst-case upper bound, not real footage.

**Transport contract**: `POST /api/v1/edge/frames`, body = raw `image/jpeg`, auth = existing Edge Bearer credential (`X-Device-Id` + `Authorization: Bearer`), metadata via headers (`X-Candidate-Key`, `X-Frame-Seq`, `X-Frame-Timestamp`) since there is no JSON envelope. `organization_id` is always resolved server-side from the authenticated device, never sent by the Edge; `camera_id` is resolved server-side from `candidate_key` via `edge_device_cameras` (existing table, existing admin linking endpoint) — the Edge never sends a raw `camera_id`.

Done this slice (code, not yet real-hardware validated end to end): offline buffering (I6), bandwidth control (I7), cost metrics (I10). A commercial bandwidth limit for I7 is still NOT decided — that needs a real TC70→SaaS benchmark run, which this integration could not perform in this environment.

## J — Modo Hybrid

- J1. processing_mode=hybrid. DONE — real behavior: RTSP → decode → resize
  → hybrid evaluator → candidate/no-candidate → Router, reusing the same
  pipeline (no parallel pipeline). `processing_mode=cloud` is byte-for-byte
  unchanged (Hybrid.Enabled false ⇒ every sampled frame dispatched).
- J2. Motion detection local. DONE — pure Go, block-based luminance diff on
  the yuv420p Y plane of the resized frame, comparing only against the
  immediately preceding frame (bounded memory, no history). No OpenCV, no
  model. Fail-safe: a frame the detector cannot evaluate safely
  (`MotionResult.Unevaluable`) is treated as a candidate and dispatched,
  never silently dropped.
- J3. Filtro frames. DONE — `cameraPipeline.readLoop` skips `Router.Dispatch`
  for non-candidate frames when hybrid is active; separate atomic counters
  (`frames_evaluated`, `motion_candidates`, `frames_filtered`) never inflate
  `FramesDropped` (that stays error/queue-full only).
- J4. ROI. DONE — `GEOCAM_VIDEO_HYBRID_ROI`, normalized 0..1 rectangles,
  `;`-separated. No ROI ⇒ whole frame analyzed (default). An invalid ROI is
  a hard config-parse error at startup (fail-fast, consistent with every
  other `GEOCAM_VIDEO_*` value), never a runtime panic.
- J5. Sampling adaptativo. DONE — `Sampler` extended (not replaced) with
  `NewAdaptiveSampler(activeFPS, idleFPS, idleAfter)`; idle/active driven by
  the motion evaluator's own candidate decisions, with a
  `GEOCAM_VIDEO_HYBRID_IDLE_AFTER` hysteresis window on the active→idle
  edge only (idle→active is immediate). `GEOCAM_VIDEO_HYBRID_IDLE_FPS=0`
  (default) preserves today's fixed-FPS behavior exactly, hybrid or not.
- J6. Modelo liviano opcional. ADAPTER DONE / real model **PENDING
  (intentional)** — decoupled `CandidateClassifier` interface,
  `ClassificationResult`, and `NoopClassifier` in `internal/hybrid`.
  Plugging in a real model is Hito K scope.
- J7. Enviar candidatos. DONE — `processing.Frame`, `cloudsink.Buffer`, and
  `transport.Client` carry real candidate metadata (`ProcessingMode`,
  `CandidateReason`, `CandidateScore`, `CorrelationID`) end-to-end: motion
  evaluator → Frame → CloudSink → HTTP headers (`X-Processing-Mode`,
  `X-Candidate-Reason`, `X-Candidate-Score`, `X-Correlation-Id`) → offline
  buffer with metadata persisted across replay.
- J8. Segunda inferencia Cloud. **NOT in this slice.**
- J9. Correlación. DONE (transport-level) — `CorrelationID` is
  deterministic (`"<candidateKey>-<seq>"`), stable across offline-buffer
  replay. Cross-source correlation beyond this (e.g. matching against a
  second Cloud inference pass) is **NOT in this slice** (depends on J8).
- J10. Benchmark ancho de banda. HARNESS DONE — controlled-replay/synthetic
  loopback benchmark (`internal/cameratest`, build tag `localbench`); no
  real-hardware bandwidth benchmark was run (blocked, no camera available).
- J11. Benchmark precisión/costo. HARNESS DONE (synthetic selectivity
  proxy only) — not validated against real motion-detector output or live
  footage.

## HITO J — CODE DONE / INTEGRATED / TESTED (branch
`integration/hito-j-final`). Pending, not blocking: real TC70/RTSP
validation, real bandwidth measurement, real detection-frame retention
measurement — all blocked on hardware/environment unavailable in this
sandbox.

## K — Full Edge

## HITO K1–K12 — INTEGRATION FINAL (branch `integration/hito-k-final`,
merging PR #23 K1-K4, #22 K5-K8, #24 K9-K12). CODE DONE / INTEGRATED
TESTED. NOT MERGED to main, NOT DEPLOYED. See `docs/PROJECT_STATUS.md`'s
Hito K section for the full breakdown, including what this integration
pass wired/fixed (vision->fulledge, bbox semantics, device/backpressure
reconciliation, evidence path safety, K8->K12 wiring, sync status,
edge-mode-only gating, model cleanup) and what remains genuinely BLOCKED
(real Ultralytics smoke, real appliance hardware/camera).

- K1. processing_mode=edge. DONE.
- K2. YOLO local. DONE.
- K3. Model management. DONE.
- K4. CPU inference. DONE.
- K5. GPU/NPU. DONE — honest device selection (cpu/cuda/auto) as a
  Go-side *preselection*; the actually-confirmed device the Python worker
  reports after loading is what status displays.
- K6. Límites hardware. DONE — disk/memory guards; inference
  concurrency/queue reconciled with `processing.Router`'s real bounded
  queue rather than a second, disconnected counter.
- K7. Eventos locales. DONE — disk-backed atomic JSON event store,
  created only from real vision.Sink detections (zero detections = zero
  events), pending/synced/quarantined lifecycle wired to K12's backlog.
- K8. Evidencias. DONE — atomic JPEG/clip evidence persistence under
  `GEOCAM_DATA_DIR/evidence/`, path built from a validated UUID only
  (never a raw candidate_key), sha256 checksum, disk limit protection.
- K9. Clips. DONE.
- K10. Metadata/evidencia al SaaS. DONE.
- K11. Operación offline parcial. DONE.
- K12. Sync posterior. DONE.

## L — Transporte seguro Edge ↔ SaaS

- L1. HTTPS 443 — CODE DONE. Enforced/centralized in `internal/transport.New` + `internal/config.Load` fail-fast; `GEOCAM_ALLOW_INSECURE_HTTP` is the only escape hatch.
- L2. WSS si corresponde — N/A: no WebSocket channel exists in this repo.
- L3. Auth por Edge — CODE DONE. Verified uniform Bearer + X-Device-Id across every Edge→SaaS call; org_id/camera_id never trusted from Edge; no credential/token ever logged.
- L4. TLS — CODE DONE. stdlib default cert/hostname validation (no `InsecureSkipVerify`, no custom `tls.Config`), bounded HTTP timeouts; insecure-HTTP dev flag visible on `/status` (`insecure_http_allowed`).
- L5. Retry/backoff. CODE DONE — Unified error classification & backoff across transport channels (`internal/transport`: Heartbeat, Discovery, Cloud frames, Hybrid candidates, Local events/evidence). 429 parses and honors `Retry-After` delta-seconds via `RateLimitError`. 401/403 preserves durable data, avoids aggressive retries and never auto-reenrolls or wipes credentials.
- L6. Cola offline. CODE DONE — Verified dual-channel bounded disk storage without artificial merging: `internal/cloudsink.Buffer` (Cloud/Hybrid video frames) and `internal/edgebacklog.Backlog` (Full Edge events/evidence). Bounded capacities, atomic writes, restart-safe, drop-tail metrics.
- L7. Reanudación. CODE DONE — Deterministic replay upon reconnection. Cloud/Hybrid preserves metadata and `CorrelationID`; Full Edge replays in strict sequence (`metadata` -> `capture` -> `clip` -> `complete`), idempotency preserved, quarantine on unrecoverable 4xx, and clean cancellation on agent shutdown.
- L8. Comandos SaaS → Edge sobre conexión iniciada por Edge. CODE DONE — Edge-initiated authenticated poll (`GET /api/v1/edge/control/next`) and report (`POST /api/v1/edge/control/{command_id}/report`). Safe allowlist (`request_status`, `rediscovery`), durable ledger under `control_ledger.json` with fail-closed and `INDETERMINATE_AFTER_RESTART` on interrupted execution.
- L9. Sin inbound requerido en cliente. CODE DONE — Zero inbound ports required on Edge. No port forwarding, NAT traversal, or inbound firewall rules needed.
- L10. VPN site-to-site opcional. DOCUMENTED — Documented compatibility with optional site-to-site VPNs, WireGuard gateways, and Tailscale subnet routing without introducing mandatory network tunnel dependencies.

## M — Eventos y evidencia

- M1–M6: Modelo local, timestamp, tenant/site, tipo, confidence, bbox pixel-level unificado (K1–K8).
- M7. Captura JPEG. CODE DONE — Validated UUIDv4 filename under `GEOCAM_DATA_DIR/evidence/captures/<uuid>.jpg`, atomic write, SHA-256 integrity, size tracking, safe path containment (no candidate_key path traversal). Capture failures degrade safely without dropping the event.
- M8. Clip MP4. CODE DONE — Ring buffer / FrameHistory window encoding via ffmpeg (`GEOCAM_DATA_DIR/evidence/clips/<uuid>.mp4`), atomic write, SHA-256 calculation, duration tracking. Encoder failures degrade safely without losing event record.
- M9. Retry. CODE DONE — Durable replay via `internal/edgebacklog`: 3-stage progression (`metadata` -> `capture` -> `clip`), rate limiting honoring `Retry-After` (429), exponential backoff for transient issues (5xx/408/timeouts), auth backoff (401/403) with durable retention, quarantine on unrecoverable 4xx errors.
- M10. Deduplicación. CODE DONE — Idempotent retry handling across all layers: EventStore avoids double backlog count and file overwrite on identical re-save; EvidenceManager and Clipper return existing records on matching SHA-256; Backlog deduplicates in-flight entries. Explicit conflict sentinels (`ErrEventConflict`, `ErrEvidenceConflict`, `ErrClipConflict`, `ErrSubmissionConflict`) prevent silent file corruption or divergent re-submissions.
- M11. Correlación. CODE DONE — `CorrelationID` preserved end-to-end across `InferenceResult`, `LocalEvent`, and `Submission`, allowing correlation between camera stream frames, local events, and evidence artifacts.
- M12: Evidencia SaaS sync validation.

## N — Telemetría y observabilidad

- N1–N12: logs, correlation IDs, métricas agente/cámara, FPS, frames, latencia, CPU/RAM, disco, queue depth, RTSP failures, estado SaaS.
- **Slice Recursos + Colas + Confiabilidad RTSP (CODE DONE)**:
  - CPU: porcentaje de uso real vía procfs con `CPUSampler` thread-safe; `nil` cuando no está medido/primed (sin cero falso).
  - RAM: `total_bytes`, `available_bytes` (con buffers/cache recuperables) y `used_bytes`; `nil` cuando no es derivable en hosts sin procfs.
  - Disco: `total_bytes`, `available_bytes` (espacio real para usuarios no root vía `statfs`), `used_bytes`, `data_dir` observado sin rutas sensibles.
  - Colas / Backpressure: métricas segregadas por componente real (`router`, `cloud_buffer`, `edge_backlog`, `vision`) con `depth`, `capacity`, `drops`, `oldest_pending`, `degraded`, `quarantined`.
  - RTSP: `reconnect_count` monótono no reiniciable, `timeout_count`/`stall_count` para silencios de socket, sanitización estricta de `last_error_safe` y logs (redacción de credenciales en URIs y parámetros).
  - Estado del Agente: `/status` expone estado global (`READY` vs `DEGRADED`) y modular; fallas de stream de cámara no degradan globalmente el agente. 100% retrocompatible. Ver `docs/observability/n-resources-queues.md`.

## O — Configuración remota

**HITO O — CODE DONE / INTEGRATED TESTED / MERGED**

- O1–O12: config desde SaaS, versionado, push/poll, rollback, cámara, FPS,
  resolución, mode, ROI, modelos y features por plan.
- **Slice Runtime Apply (CODE DONE)**:
  - Adapter e interfaz compatible (`remoteconfig.Applier`) con ciclo transaccional estricto: `Validate`, `Apply`, `Rollback` y `CurrentConfig`.
  - Conexión de knobs reales: cámaras (`candidate_key`), FPS (`processing.Sampler`), resolución de procesamiento (`OutputWidth`/`OutputHeight`), modos (`cloud`, `hybrid`, `edge`), ROIs normalizadas, y modelos homologados (`yolo11s-pose.pt`, `yolo11n.pt`).
  - Segregación técnica: hot-reload para FPS sin reinicio de decoder vs reinicio controlado por cámara para resolución/modos/ROIs sin reiniciar el agente.
  - Rollback garantizado: reversión automática ante fallo de health check post-aplicación y soporte de rollback explícito.
  - Seguridad estricta: rechazo rotundo de credenciales en texto plano, rutas de filesystem, URLs y campos de tenant/sitio.
  - Ver `docs/remoteconfig/o-runtime-apply.md`.

## P — Packaging multi-plataforma

**HITO P — CODE DONE / INTEGRATED TESTED / MERGED** (branch `integration/hito-p-final`,
integrates #39/#40/#41). Pending, not blocking: real-hardware validation of
P7/P8/P10 (no Linux/systemd target available in this sandbox) and a first
cut release (no target version defined for this closure — the P5/P6 tag
mechanism is ready but untested end-to-end against a real tag push).

- P1. Linux AMD64. **DONE** — `make build-linux` (pre-existing, Hito A).
- P2. Linux ARM64. **DONE** — `make build-linux` (pre-existing, Hito A).
- P3. OCI multi-arch. **DONE (K3s only)** — root `Dockerfile` (pre-existing, Hito H); not the appliance's install path (see P7).
- P4. Build reproducible. **DONE** — `-trimpath` + version/commit/date `ldflags`, identical flags for linux/amd64 and linux/arm64 (`Makefile`'s `build-linux` target).
- P5. Tags. **DONE** — `vX.Y.Z` git tags as the versioning source of truth (`docs/RELEASING.md`).
- P6. Releases. **DONE** — `.github/workflows/release.yml`: a `v*.*.*` tag push builds linux-amd64/arm64, packages per-arch tar.gz, generates `SHA256SUMS`, publishes a GitHub Release. No release has been cut yet (no target version defined for this closure).
- P7. Instalación. **CODE DONE / NOT VALIDATED ON REAL HARDWARE** — `deploy/appliance/scripts/install.sh` (+`update.sh`/`rollback.sh`/`uninstall.sh`/`package.sh`): idempotent directory/user/permission setup, dedicated non-root `geocam-edge` system user, versioned `releases/<version>` + atomic `current` symlink, never overwrites existing config/identity/credentials. Validated via `go test ./deploy/appliance/...` against a staged root (`GEOCAM_INSTALL_ROOT`) on this sandbox (no Linux/systemd available here) — see `docs/deployment/appliance.md` for exactly what that does and does not prove.
- P8. systemd. **CODE DONE / NOT VALIDATED AGAINST REAL SYSTEMD** — `deploy/appliance/systemd/geocam-edge.service.in`: `Restart=on-failure`, `After=network-online.target`, `WorkingDirectory`, `EnvironmentFile`, dedicated `User=`/`Group=`, `KillSignal=SIGTERM` (the agent's own `signal.NotifyContext(SIGINT, SIGTERM)` in `cmd/geocam-edge/main.go` already does graceful in-process shutdown — confirmed by reading the code, not by a live systemd stop). Structure checked by a Go test (`TestSystemdUnitTemplateStructure`); `systemd-analyze verify` was NOT run (no systemd in this sandbox).
- P9. Persistencia. **DONE** — `GEOCAM_DATA_DIR` (identity/credentials/offline buffer) persists via atomic-write code outside the versioned release tree so install/update/uninstall never touch it (enforced by tests: `TestInstallIsIdempotentAndPreservesData`, `TestInstallNewVersionPreservesData`, `TestUninstallPreservesDataByDefault`).
- P10. Upgrade seguro. **CODE DONE / NOT VALIDATED ON REAL HARDWARE** — `update.sh`: checksum + architecture validation before touching anything installed, versioned releases kept for rollback, atomic `current` symlink swap, restart + `/readyz` verification, automatic `rollback.sh` invocation on verification failure. `rollback.sh` restores the prior release and restarts. Both covered by `go test ./deploy/appliance/...` (round-trip, readiness-failure auto-rollback, bad-arch rejection, bad-checksum rejection) against a staged root — no real systemd restart/health cycle was exercised.

## Q — Appliance residencial

**HITO Q (Q1–Q10) — CODE DONE / INTEGRATED TESTED / MERGED / SAAS PROD
DEPLOYED** (PR #47 mergeado a `main` @
`25a7d8ea27a082b957c19a7fbf691b063014bdff`, integra #44/#45/#46;
`monitoreoia` PR #121 mergeado a su `main` @
`66a5bec920f23a09dc28531db44274ad1c97df21`, integra #120 para Q8).
**SAAS PROD: DEPLOYED** — `monitoreoia` main `66a5bec9` construido con
Buildah (`geocam-cloud:66a5bec`, `geocam-cloud-worker:66a5bec`),
importado a containerd e instalado vía `helm upgrade` (release `geocam`,
namespace `geocam`, revisión 31) en `vps-6387636-x.dattaweb.com`. Este
rollout también deja en producción las migraciones 038/039 de Hito O,
pendientes desde su propio merge. Verificado post-deploy: pods
`geocam-app`/`geocam-worker` `1/1 Running` con la imagen `66a5bec`,
`schema_migrations` con 038/039 aplicadas, `/api/health` `200`,
`/api/ready` `200`, `/login` `200`, `/dispositivos-edge` `303`
(redirect a login sin sesión, no 500), `/api/v1/gateway/enrollments`
`401` (auth requerida, no 500). **EDGE PROD: N/A** — no existe un target
Edge/appliance/VM productivo real documentado distinto del VPS SaaS.
Arquitectura y evidencia de red documentadas a partir de código y
mediciones reales existentes — ver `docs/deployment/hardware.md`.
Ningún hardware físico fue validado en
este cierre; cada punto marca explícitamente qué sigue pendiente de
hardware real.

- Q1. Hardware mínimo. **PARTIAL / DOCUMENTED** — arquitectura soportada
  (`linux/amd64`/`linux/arm64`), separación de perfiles (Cloud/Hybrid
  gateway vs. Full Edge con YOLO local) y evidencia de red documentadas
  en `docs/deployment/hardware.md`. La cifra de red del gateway
  (~9.12 Mbps) es un **benchmark local sintético** (una cámara, ruido
  pseudo-aleatorio, sin red/SaaS real) — se documenta como tal, no como
  medición de producción. Los mínimos reales de RAM/storage **NOT
  ESTABLISHED, pending real hardware benchmark** — no se inventó ningún
  número; 512MB/1GB y 2GB/4GB son sólo *provisional provisioning
  assumptions*, nunca requisitos validados. El costo de inferencia CPU en
  ARM64/amd64 tampoco está medido.
- Q2. Raspberry Pi / Orange Pi (ARM64). **DOCUMENTED CANDIDATE / NOT
  HARDWARE VALIDATED** — candidato documentado para Cloud/Hybrid gateway;
  Full Edge con YOLO local NOT PERFORMANCE VALIDATED (sin medición real
  de RAM/CPU en esta clase de placa).
- Q3. Mini-PC (AMD64). **DOCUMENTED CANDIDATE / NOT HARDWARE VALIDATED** —
  mismo criterio: candidato documentado para Cloud/Hybrid gateway y Full
  Edge, sin medición real todavía.
- Q4. Imagen preparada. **CODE DONE / INTEGRATED TESTED** — physical
  appliance image validation pending. Pre-packaged release (`package.sh`)
  + `install.sh` + systemd + `bootstrap.sh` on standard Linux
  distributions (Debian 12 minimal / Ubuntu Server / Raspberry Pi OS
  Lite). No custom ISO needed; reproducible image preparation and
  artifact distinction documented in `docs/deployment/appliance.md`.
- Q5. Ethernet + power. **DOCUMENTED** — power is hardware/model
  specific. Residential baseline requires wired Ethernet (`eth0`/`enp*`)
  via DHCP as the primary supported network path (deterministic latency
  and reliable UDP multicast for WS-Discovery). Power per the selected
  hardware's own spec (vendor/model-recommended supply; PoE only via a
  compatible external adapter if applicable) — not a claim this repo
  validated any specific power supply.
- Q6. Zero-touch enrollment. **CODE DONE / INTEGRATED TESTED** — real
  first-boot hardware validation pending. Reuses existing `geocam-edge
  enroll` CLI via stdin (`--token` flag removed). On first boot,
  `geocam-edge-bootstrap.service` runs `bootstrap.sh` before
  `geocam-edge.service`, detects ephemeral seed token
  (`/boot/geocam-enroll.token`, `/boot/firmware/geocam-enroll.token`,
  `/etc/geocam-edge/enroll.token`, or `GEOCAM_ENROLLMENT_TOKEN`), pipes
  token to `geocam-edge enroll` (SHA-256 exchanged with SaaS), persists
  non-root `identity.json` and `credentials.json`, and deletes the seed
  file best-effort (no secure-erase claim). Under systemd, defers service
  startup to systemd (no synchronous start/wait deadlock). Covered by
  targeted tests in `deploy/appliance/bootstrap_test.go` — not by a real
  boot cycle.
- Q7. Discovery. **CODE DONE / REUSED EXISTING DISCOVERY** — inventory
  in-memory; real LAN hardware validation pending. Reuses existing ONVIF
  WS-Discovery engine (`internal/discovery`) and SaaS reporting
  (`ReportDiscoveryRun`). Runs automatically post-enrollment on service
  start via `discovery.Module`. CLI on-demand scanning supported via
  `geocam-edge discovery scan` and `bootstrap.sh --scan`. No redundant
  network scanner; inventory is not persisted to
  `discovery-inventory.json`.
- Q8. Onboarding QR/código. **CODE/TOKEN FLOW AVAILABLE** — SaaS
  onboarding UI integrated (`monitoreoia` PR #120): waiting/connected/
  ready states, no `claimed_device_id` in the public payload, RBAC on
  enrollment, reuses existing enrollment code/token. The existing
  code/token satisfies Q8's "código" path — **no new QR was implemented**
  in this closure, and none is claimed.
- Q9. Factory reset. **CODE DONE / INTEGRATED TESTED** — real hardware
  factory-reset validation pending. `geocam-edge factory-reset --confirm`
  preserves releases/systemd/installed software and erases only an
  explicit allowlist inside `GEOCAM_DATA_DIR` (identity, credentials,
  camera credentials/master key, remote-config state, control ledger,
  local event backlog, cloud buffer, events, evidence).
- Q10. UX no técnica. **CODE DONE / INTEGRATED TESTED** — no claim of
  validation with real non-technical users; this closure covers the
  zero-touch/discovery/onboarding code path, not a usability study.

## R — Instalación corporativa

**HITO R (R7–R9) — CODE DONE / DOCUMENTED** (branch
`feature/corporate-r-enterprise-network`). Ver
`docs/deployment/corporate-enterprise.md` para la matriz completa. R1–R6
quedan sin tocar, fuera de alcance de este cierre.

- R1–R6: VM, server Linux, appliance industrial, VPN site-to-site, subnet
  routing, VLANs. **NOT DONE** — fuera de alcance de este cierre.
- R7. Múltiples segmentos CCTV. **PARTIAL / DOCUMENTED** — acceso unicast
  a cámaras ya conocidas por IP/URL ya funciona hoy sin cambios de código
  (`internal/rtsp.Dial`/ONVIF SOAP no fijan interfaz), siempre que exista
  ruteo real entre segmentos. WS-Discovery (`239.255.255.250:3702/UDP`)
  ya corre por interfaz local (`internal/discovery.SelectInterfaces`,
  soporta multi-homed), pero **no cruza routers/VLANs sin multicast
  routing/IGMP relay a nivel de red** — gap documentado explícitamente,
  no resuelto con scanner/broadcast forwarding (deliberadamente no
  agregados). Alternativa documentada (no obligatoria): un Edge por
  segmento.
- R8. Firewall. **DOCUMENTED** — matriz exacta de tráfico outbound
  (Edge→SaaS, Edge→cámaras, WS-Discovery multicast) y local/inbound
  (health en `127.0.0.1:8091` por defecto, no expuesto en red) en
  `docs/deployment/corporate-enterprise.md`. Sin automatización de
  firewall (no iptables/nftables).
- R8. Proxy corporativo. **CODE DONE / TESTED** — auditoría confirmó que
  `internal/transport.Client` (compartido por enrollment/heartbeat/
  cloudsink) ya respeta `HTTP_PROXY`/`HTTPS_PROXY`/`NO_PROXY` estándar de
  Go (Transport nil → `http.DefaultTransport` → `ProxyFromEnvironment`);
  no fue necesario ningún cambio de código. Cobertura antes ausente,
  agregada en `internal/transport/proxy_test.go`
  (`TestClientRespectsStandardProxyEnv`). ONVIF SOAP correctamente
  excluido del proxy (tráfico LAN directo a cámaras, sin cambios). RTSP
  es un dial TCP crudo, no aplica proxy HTTP.
- R9. HA futura. **FUTURE ARCHITECTURE DOCUMENTED / NOT IMPLEMENTED** —
  boundary documentado en `docs/deployment/corporate-enterprise.md`: hoy
  un solo Edge activo por conjunto de cámaras; clonar identity/credentials
  NO es HA; dos Edges sobre la misma cámara pueden duplicar trabajo/
  eventos; estado local (buffer offline, backlog) requiere estrategia
  antes de active/passive; HA real futura debe resolver ownership/lease/
  fencing/failover. Sin cluster, sin consensus, sin etcd, sin leader
  election en este hito.

## S — Seguridad

- S1–S11: threat model, secrets únicos, TLS, cifrado local, revocación, rotación,
  enrollment seguro, replay protection, signed updates, least privilege,
  auditoría.

## T — OTA

- T1–T10: versión, update disponible, descarga, checksum/firma, upgrade, health,
  rollback, staged rollout, canaries, incompatibilidades.

## U — SaaS Control Plane

- U1–U11: Edge list, health, hardware, cameras discovered, associated cameras,
  processing mode, remote config, versions, metrics, actions, permisos
  tenant/site.

## V — Planes/cuotas/costos

- V1–V10: cámaras, FPS, Cloud processing, Hybrid, Edge processing, network,
  storage, retention, plan limits, billing telemetry.

## W — Testing

- W1–W12: unit, integration, RTSP simulator, ONVIF simulator, pérdida
  Internet/cámara, credenciales incorrectas, reboot, upgrade, ARM64, AMD64,
  soak tests.

## X — Performance/capacidad

- X1–X10: benchmarks 1/5/10/25/50 cámaras, CPU/RAM, network, decode FPS,
  inference FPS, matriz hardware/cámaras.

## Y — Resiliencia

- Y1–Y10: restart, power loss, SaaS offline, Internet offline, camera offline,
  disk full, queue overflow, config corruption, watchdog, health recovery.

## Z — Producción/evolución

- Z1. MVP residencial.
- Z2. MVP corporativo.
- Z3. Piloto 5-10 cámaras.
- Z4. Multi-site.
- Z5. Soporte.
- Z6. 1.0.
- Z7. Hardware certificado.
- Z8. Gateway comercial.
- Z9. Hybrid comercial.
- Z10. Full Edge comercial.
