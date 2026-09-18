# Hito N — Observabilidad Edge: logs + correlación + video metrics

No stack externo nuevo. No Prometheus/Grafana/Loki. Este hito reutiliza y
completa lo que ya existía desde H/I10/J/K/M: `internal/logging` (slog),
`internal/processing.PipelineStatus`/`VideoPipelineSummary`, y
`internal/health.Reporter` (`/status`).

## 1. Logs estructurados

La base (`internal/logging.New`) ya tagea todo log con `edge_id`,
`processing_mode` y `version`; `internal/rtsp.Supervisor` ya deriva un logger
hijo con `candidate_key`/`stream_role` en su constructor, así que todo log a
través de `s.logger` los lleva sin repetir el campo en cada llamada.

Gaps reales cerrados por N (identificadores existentes que se descartaban en
el sitio del log, no un ID nuevo):

- `internal/processing/router.go` — `"sink route failed"` ahora incluye
  `candidate_key`, `seq` y `correlation_id` del `Frame` que falló al rutear
  (antes solo `sink` + `error`).
- `internal/agent/fulledge_wiring.go` — `"full edge: failed to process
  inference result"` ahora incluye `candidate_key` y `correlation_id`.
- `internal/fulledge/service.go` — los 4 sitios de "dropping detection with
  invalid bbox/confidence" (M5/M6, `ProcessInference` y
  `ProcessInferenceWithJPEG`) ahora incluyen `candidate_key` y
  `correlation_id` del `InferenceResult`, no solo el error.

`event_uuid` ya se logueaba consistentemente en el camino evidence/backlog de
`internal/agent/fulledge_wiring.go` — sin cambios ahí.

**Nunca se loguean** credenciales, tokens Bearer, contraseñas RTSP ni bytes de
evidencia — auditado en `internal/rtsp`, `internal/cameracreds`,
`internal/cloudsink` y `internal/edgebacklog`: ningún `Warn`/`Error`/`Info`
referencia `Username`/`Password`/`Authorization` directamente (test de
regresión: `internal/rtsp.TestRTSP_CredentialSafety`, ya existente).

## 2. Correlación

`correlation_id` ya era canónico desde M (`fulledge.LocalEvent.CorrelationID`,
`transport.LocalEvent.CorrelationID`, `cloudsink.buffer` entry). Lo que N
cerró: el id se **recalculaba** de forma independiente y duplicada en
`internal/agent/fulledge_wiring.go` (`candidateKey + "-" + frameSeq`) en vez
de reutilizar el que `internal/processing/pipeline.go` ya genera una sola vez
al decodificar el frame (`Frame.CorrelationID`, mismo formato — coincidencia
frágil, no garantía).

Ahora se hila explícitamente, sin re-derivar en ningún punto intermedio:

```
processing.Frame.CorrelationID          (pipeline.go, una sola vez)
  -> vision.InferRequest.CorrelationID  (sink.go: Route)
  -> vision.InferenceResult.CorrelationID (worker.go: Infer)
  -> fulledge.InferenceResult.CorrelationID (agent/fulledge_wiring.go: reuse, no recompute)
  -> fulledge.LocalEvent.CorrelationID  (ya existía)
  -> transport.LocalEvent.CorrelationID (ya existía)
```

Puede seguirse en logs desde RTSP/video pipeline (router.go) hasta
inference/local event/evidence/backlog/transport con el mismo valor.

## 3. FPS / frames

Ya estaba completo desde el Hito H y no requirió cambios:
`processing.PipelineStatus` (por cámara, publicado en `/status` bajo
`video_pipeline`) expone `input_fps`, `decoded_fps`, `output_fps`,
`frames_received` (access units, no paquetes RTP crudos —
`rtp_packets_received` es el contador separado), `frames_decoded`,
`frames_sampled`, `frames_dropped` (acumulativo across reinicios del decoder,
nunca resetea a mitad de vida) y `last_frame_at`. `router.go` cuenta drops
por sink por separado (`Router.Dropped(sinkName)`) — no un contador paralelo
con semántica distinta, es "frames descartados en el ruteo" vs. "frames
descartados en la cola/decode".

## 4. Latencias

Ya estaba completo desde H/K: `decode_latency_ms` (por cámara, en
`PipelineStatus`), `InferenceMS` (por resultado de inferencia, en
`vision.InferenceResult`/`fulledge.InferenceResult`), y
`encode_latency_avg_ms`/`upload_latency_avg_ms` (I10, en `cloudsink`). No se
inventaron percentiles — solo lo que ya se mide como promedio/acumulado.
Sampling/routing no tiene una latencia propia medible hoy (es un gate
booleano, no una etapa con duración) — no se agregó una métrica ficticia para
eso.

## 5. Status

`/status` ya integraba `video_pipeline` (`internal/health.Reporter.
SetVideoPipeline`) y `full_edge` (Hito M). Sin cambios de compatibilidad —
solo se agregó cobertura de test (`TestReporterVideoPipelineStatus`) para que
una regresión futura en el wiring `processing.VideoPipelineSummary ->
health.Snapshot` no pase desapercibida.

## Tests agregados (Hito N)

- `internal/processing.TestRouter_SinkErrorLogsCorrelation` — un sink que
  falla debe loguear `candidate_key`/`seq`/`correlation_id`, no solo el error.
- `internal/health.TestReporterVideoPipelineStatus` — `SetVideoPipeline` debe
  reflejarse en `Snapshot().VideoPipeline` con los contadores reales
  (frames/FPS/latencia), sin redondeos ni precisión inventada.

Reutilizados sin duplicar: `internal/rtsp.TestRTSP_CredentialSafety` (no
secret leakage) y `internal/processing.TestPipeline_MetricsCumulativeAcrossDecoderRestart`
(counters no duplicados/no negativos across restart).

## Qué NO está medido todavía

- Latencia de sampling/routing como etapa propia (no existe una duración real
  que medir hoy).
- Percentiles (p50/p95/p99) de ninguna etapa — solo promedios/acumulados.
- Bytes de red reales (HTTP/TLS overhead) en upload — ya documentado como
  fuera de alcance desde I10.
