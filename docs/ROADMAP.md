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

- J1. processing_mode=hybrid.
- J2. Motion detection local.
- J3. Filtro frames.
- J4. ROI.
- J5. Sampling adaptativo.
- J6. Modelo liviano opcional.
- J7. Enviar candidatos.
- J8. Segunda inferencia Cloud.
- J9. Correlación.
- J10. Benchmark ancho de banda.
- J11. Benchmark precisión/costo.

## K — Full Edge

- K1. processing_mode=edge.
- K2. YOLO local.
- K3. Model management.
- K4. CPU inference.
- K5. GPU/NPU.
- K6. Límites hardware.
- K7. Eventos locales.
- K8. Evidencias.
- K9. Clips.
- K10. Metadata/evidencia al SaaS.
- K11. Operación offline parcial.
- K12. Sync posterior.

## L — Transporte seguro Edge ↔ SaaS

- L1. HTTPS 443.
- L2. WSS si corresponde.
- L3. Auth por Edge.
- L4. TLS.
- L5. Retry/backoff.
- L6. Cola offline.
- L7. Reanudación.
- L8. Comandos SaaS → Edge sobre conexión iniciada por Edge.
- L9. Sin inbound requerido en cliente.
- L10. VPN site-to-site opcional.

## M — Eventos y evidencia

- M1–M12: modelo evento, timestamp, tenant/site/cámara, tipo, confidence, bbox,
  captura, clip, retry, deduplicación, correlación y evidencia SaaS.

## N — Telemetría y observabilidad

- N1–N12: logs, correlation IDs, métricas agente/cámara, FPS, frames, latencia,
  CPU/RAM, disco, queue depth, RTSP failures, estado SaaS.

## O — Configuración remota

- O1–O12: config desde SaaS, versionado, push/poll, rollback, cámara, FPS,
  resolución, mode, ROI, modelos y features por plan.

## P — Packaging multi-plataforma

- P1. Linux AMD64. **DONE** — `make build-linux` (pre-existing, Hito A).
- P2. Linux ARM64. **DONE** — `make build-linux` (pre-existing, Hito A).
- P3. OCI multi-arch. **DONE (K3s only)** — root `Dockerfile` (pre-existing, Hito H); not the appliance's install path (see P7).
- P4. Build reproducible. **PARTIAL (pre-existing)** — `-trimpath` + version/commit/date `ldflags`; not otherwise revisited here.
- P5. Tags. **NOT DONE.**
- P6. Releases. **NOT DONE.**
- P7. Instalación. **CODE DONE, NOT VALIDATED ON REAL HARDWARE** — `deploy/appliance/scripts/install.sh` (+`update.sh`/`rollback.sh`/`uninstall.sh`/`package.sh`): idempotent directory/user/permission setup, dedicated non-root `geocam-edge` system user, versioned `releases/<version>` + atomic `current` symlink, never overwrites existing config/identity/credentials. Validated via `go test ./deploy/appliance/...` against a staged root (`GEOCAM_INSTALL_ROOT`) on this sandbox (no Linux/systemd available here) — see `docs/deployment/appliance.md` for exactly what that does and does not prove.
- P8. systemd. **CODE DONE, NOT VALIDATED AGAINST REAL systemd** — `deploy/appliance/systemd/geocam-edge.service.in`: `Restart=on-failure`, `After=network-online.target`, `EnvironmentFile`, dedicated `User=`/`Group=`, `KillSignal=SIGTERM` (the agent's own `signal.NotifyContext(SIGINT, SIGTERM)` in `cmd/geocam-edge/main.go` already does graceful in-process shutdown — confirmed by reading the code, not by a live systemd stop). Structure checked by a Go test (`TestSystemdUnitTemplateStructure`); `systemd-analyze verify` was NOT run (no systemd in this sandbox).
- P9. Persistencia. **DONE (documented, not newly built)** — `GEOCAM_DATA_DIR` (identity/credentials/offline buffer) already persists via the pre-existing atomic-write code in `internal/identity`/`internal/credentials`/`internal/cloudsink`; the appliance layout keeps it outside the versioned release tree so install/update/uninstall never touch it (enforced by tests: `TestInstallIsIdempotentAndPreservesData`, `TestUninstallPreservesDataByDefault`).
- P10. Upgrade seguro. **CODE DONE, NOT VALIDATED ON REAL HARDWARE** — `update.sh`: checksum + architecture validation before touching anything installed, versioned releases kept for rollback, atomic `current` symlink swap, restart + `/readyz` verification, automatic `rollback.sh` invocation on verification failure. `rollback.sh` restores the prior release and restarts. Both covered by `go test ./deploy/appliance/...` (round-trip, bad-arch rejection, bad-checksum rejection) against a staged root — no real systemd restart/health cycle was exercised.

## Q — Appliance residencial

- Q1–Q10: hardware mínimo, Raspberry/Orange Pi, mini-PC, imagen preparada,
  Ethernet+power, zero-touch enrollment, discovery, QR/código, factory reset,
  UX no técnica.

## R — Instalación corporativa

- R1–R9: VM, server Linux, appliance industrial, VPN site-to-site, subnet
  routing, VLANs, múltiples segmentos CCTV, firewall/proxy, HA futura.

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
