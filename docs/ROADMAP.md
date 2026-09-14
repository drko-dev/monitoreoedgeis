# ROADMAP — GEO CAM Edge master backlog

> **The identifiers (A1, B7, K12, …) are STABLE.**
> Do not renumber them. Do not reinterpret them. They are referenced from
> commits, PRs, `docs/PROJECT_STATUS.md` and session handoffs.

## Block status

| Block | Status                |
| ----- | --------------------- |
| A     | DONE / VALIDATED LOCAL |
| B     | CODE DONE / NOT YET K3s-VALIDATED |
| C–Z   | PLANNED               |

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

- C1. Flujo de alta.
- C2. Código/token temporal de enrollment.
- C3. Asociación Edge → tenant/site.
- C4. Credencial propia del Edge.
- C5. Persistencia segura.
- C6. Rotación.
- C7. Revocación.
- C8. Re-enrollment.
- C9. API SaaS para Edge.
- C10. UI SaaS Edge/Gateways.

## D — Heartbeat y administración

- D1. Heartbeat Edge → SaaS.
- D2. online/offline/degraded.
- D3. Uptime.
- D4. Versión.
- D5. CPU/RAM/disco.
- D6. arm64/amd64.
- D7. Temperatura.
- D8. Última conexión.
- D9. Intervalo configurable.
- D10. Retry/backoff.
- D11. Vista de health en SaaS.

## E — Autodiscovery

- E1. ONVIF WS-Discovery.
- E2. Escaneo de dispositivos.
- E3. IP/fabricante/modelo.
- E4. Cámaras.
- E5. DVR/NVR.
- E6. Múltiples canales.
- E7. Capabilities.
- E8. Profiles.
- E9. Resolución/FPS/codecs.
- E10. URI RTSP.
- E11. Deduplicación.
- E12. Re-discovery.
- E13. Inventario local.
- E14. Inventory → SaaS.
- E15. UI dispositivos encontrados.
- E16. Confirmar/rechazar.

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

- G1. Cliente RTSP.
- G2. Reconexión.
- G3. Timeouts.
- G4. Health cámara.
- G5. Main/substream.
- G6. Codec.
- G7. FPS.
- G8. Resolución.
- G9. Stream caído.
- G10. Estado en SaaS.
- G11. Métricas de pérdida/reconexión.

## H — Video Pipeline

- H1. Ingest RTSP.
- H2. Decode.
- H3. Sampling.
- H4. Reducción FPS.
- H5. Resize.
- H6. Main/substream.
- H7. Buffer circular.
- H8. Frame routing.
- H9. Backpressure.
- H10. Límites CPU/RAM.
- H11. Independiente de YOLO.

## I — Modo Cloud

- I1. processing_mode=cloud.
- I2. Sin YOLO pesado local.
- I3. Sampling configurable.
- I4. Compresión/transporte.
- I5. Frames/substreams a Cloud.
- I6. Buffer offline.
- I7. Bandwidth control.
- I8. Worker Cloud.
- I9. Eventos al SaaS.
- I10. Métricas de costo.

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

- P1. Linux AMD64.
- P2. Linux ARM64.
- P3. OCI multi-arch.
- P4. Build reproducible.
- P5. Tags.
- P6. Releases.
- P7. Instalación.
- P8. systemd.
- P9. Persistencia.
- P10. Upgrade seguro.

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
