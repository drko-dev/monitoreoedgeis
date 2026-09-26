# J6 — Hybrid ANPR/LPR Production Integration (Edge side)

- Edge base main: `05234f2b844724edf5e1253092c0cd440543eb10`
- PREP B (candidate extraction): `9d48029aafd1b571c2372b6d39cabf63604e9f6a`
- PREP C (cross-repo acceptance): `6e345d3dca91de1051b947c8208ef51fb1725398`
- PREP D (final PREP report): `26f6d27ed7fc0429330c986a0fdfdcba7f8b143c`
- Integration branch: `integration/j6-anpr-production`
- Current integration HEAD: `2b91851d9bb370864de1b2cc6a324f455f29ae41`

## Pipeline wiring

`vision.Sink.Route` (K1's real local YOLO inference) already calls
`EventConsumer.ConsumeInference(result, jpeg)` for any tick with at least one detection --
the one real place local detections exist. `internal/agent/fulledge_wiring.go`'s
`fullEdgeEventConsumer` is the one real implementation of that interface; J6 hooks in there,
right alongside (not instead of) the existing K5-K8 local-event pipeline, on the SAME
`result.Detections` -- no second RTSP client, no second decoder, no second model execution.

`consumeAnprCandidates` filters `vision.DetectionTypeVehicle` detections (never label-string
matching), maps them to `anpr.VehicleCandidate`, and calls the PREP's own `anpr.Registry.Submit`
-- unchanged burst/dedupe/authorization/crop-geometry logic. On an ACCEPTED result, the crop is
extracted via a new `anpr.ExtractJPEGFromEncoded` (added because `ConsumeInference` only receives
the already-encoded full-frame JPEG, not the raw `processing.Frame` `ExtractJPEG` needs -- shares
the same `cropImageToJPEG` rect-clamp/draw/encode helper, no duplicated geometry logic) and handed
to an injectable transport callback.

## Remote-config authorization

`internal/remoteconfig.CameraConfig` gained a minimal `ANPR *CameraANPRConfig` field
(`Enabled`, `HighSpeedLPR`) -- the desired effective state SaaS already resolved, carried through
the *existing* poll/ack config mechanism (no second poll loop, no J2/J3 logic re-derived on Edge).
`remoteConfigAnprAuthorizer` implements `anpr.Authorizer` by reading the live `RuntimeConfig`
snapshot (`remoteconfig.Applier.CurrentConfig()`) -- fail-closed by construction: no config yet, no
entry for a camera, or `Enabled=false` all deny, exactly `anpr.DenyAllAuthorizer`'s own default,
now backed by a real source instead of a hardcoded stub. Verified: denies with no config, allows
once a camera is enabled, denies again once flipped back -- no restart needed.

## Shared CloudSink reuse (no second spool, no second limiter)

`internal/cloudsink.Buffer`'s `BufferedFrame`/`bufferMeta` gained a `Kind` field
(`""`/`KindFrame` is the original pre-J6 format -- a legacy spool entry with no `kind` key
unmarshals to empty and is interpreted as a frame exactly as before) plus an `AnprCandidate`
JSON payload field. `Enqueue`/`Peek`/`Advance`/`Discard`/capacity accounting needed **zero**
changes -- they already operated generically on `BufferedFrame`. `CloudSink.EnqueueANPRCandidate`
mirrors `Route`'s own limiter-check -> direct-upload -> buffer-on-recoverable-failure flow;
`drainLoop` branches on `Kind` to replay ANPR entries through the same backoff/retry
classification the frame path already has. Verified with a real test: an oversized ANPR crop
exhausts the shared token budget and the *next* frame `Route()` call throttles too -- proving one
shared budget, not two independent ones.

## Transport

`internal/transport.Client.PostANPRCandidate`: multipart/form-data (`metadata` JSON +
`crop` image/jpeg, never base64-in-JSON) to `AnprCandidatesPath`
(`/api/v1/edge/anpr/candidates`, verified against the already-implemented SaaS endpoint).
Reuses the exact same device authentication (`X-Device-Id` / `Authorization: Bearer`) and status
classification (`classifyFrameStatusWithHeader` -- 200/202 success, 401/403 auth, 429
rate-limited, 408/5xx retryable, other 4xx permanent) `PostFrame` already uses -- no second auth
scheme, no second retry taxonomy.

`anprCloudTransport` (in `internal/agent`) bridges the consumer's injectable callback to whichever
`*cloudsink.CloudSink` instance is currently live -- CloudSink is rebuilt on every processing-mode
transition (`remoteconfig.WithCloudSinkFactory`), so an atomic pointer, updated at both build call
sites, means the callback always targets the live instance. A nil sink (Edge-only mode, or no SaaS
configured) makes it a documented, tested no-op.

## Cross-repo E2E (REAL, not simulated)

A throwaway smoke program (not committed) exercised the full real stack:

```
real anpr.Registry.Submit (burst/authorization/crop geometry)
  -> real anpr.ExtractJPEGFromEncoded
  -> real sha256/size computation
  -> real transport.Client.PostANPRCandidate (real HTTP multipart)
  -> a REAL running SaaS app_cloud.py process (uvicorn)
  -> real authenticate_edge_device / resolve_camera_id_by_identifier
  -> real J3 authorization (effective_capabilities.resolve_effective_camera_configuration)
  -> real AnprService bounded queue
  -> REAL PostgreSQL persistence (verified via direct SQL query afterward)
```

Result: `E2E_SMOKE_OK`, candidate persisted with the correct status and `crop_reference`. This run
**found and led to fixing a critical, real bug** on the SaaS side: the ingest endpoint's original
authorization check could never succeed in production (see the SaaS-side final report's Known
Limitations section for the full root cause) -- caught only because this was a genuinely separate
process and a genuinely separate HTTP client, not a same-process test double.

The synthetic test image carried no plate-like region, so the real OpenCV localizer correctly
produced zero observations for that specific candidate (no fabricated bbox) -- the full
OCR-through-accepted-evidence chain is proven separately, same production code, in the SaaS repo's
`test_anpr_service_postgres.py`.

## Tests / regression

- `go build ./...`, `go vet ./...`, `gofmt -l .` (clean), `go test ./... -race`: all 37 packages
  green, including the pre-existing PREP B/C suites, `internal/cloudsink`, `internal/transport`,
  `internal/processing`, `internal/vision`.
- New tests this integration: 3 (`crop.go` `ExtractJPEGFromEncoded`), 7 (`internal/agent` wiring +
  remote-config authorizer + real end-to-end CloudSink+HTTP proof), 6 (`internal/cloudsink` ANPR
  reuse, including the shared-limiter proof and the legacy-spool-format backward-compat proof), 3
  (`internal/transport` `PostANPRCandidate`, including a real `multipart.Reader` parse of the
  request server-side).

## HIGH_SPEED_LPR (real, closed)

Baseline -> burst -> automatic release, backed by the REAL `processing.Sampler` -- no
`SetTargetFPS` hack, no second sampler.

- **Baseline**: captured from the camera's live `TargetFPS` the moment its FIRST active burst
  requests a boost (whatever J4/J5's adaptive sampler, or a prior remote-config apply, had already
  set -- never assumed).
- **Burst**: `anpr.BurstManager` calls `BurstSamplingHint.RequestBurstFPS(cameraKey, burstID, fps)`
  exactly once when a burst genuinely opens (never repeated for later frames of the same burst),
  and `ReleaseBurstFPS` on every terminal transition (FULL, CLOSED, EXPIRED, and the overflow-cap
  early-FULL path) -- never duplicated, never orphaned. Dedupe/capacity-reject/unauthorized
  candidates never reach burst creation, so they never request a boost, by construction.
- **Composition**: `samplerBurstHint` (`internal/agent`) tracks every active burst's requested FPS
  per camera and applies the MAX of them; releasing one burst never drops below what another still
  needs; the last release restores the captured baseline deterministically.
- **Source of `burst_fps`/`burst_duration_ms`**: the SaaS's own canonical
  `plate_capture_burst` execution profile (`ai_capability_execution_profiles`, migration 053:
  `burst_fps=15`, `burst_duration_ms=3000`), transported through `CameraANPRConfig` -- never
  invented on the Edge.
- **Ceiling**: every requested boost is clamped to `config.MaxVideoTargetFPS`, the SAME technical
  ceiling remote-config itself already validates ordinary `TargetFPS` changes against.
- **Remote config, real (not fixture-only)**: `GET /api/v1/edge/remote-config/next` on the SaaS
  now computes and merges a live `anpr` block per linked camera on every poll, sourced from J3's
  own canonical resolver (`effective_capabilities.resolve_effective_camera_configuration`) --
  never admin-settable (`extra="forbid"` rejects a fabricated field), never a second authorization
  source. A live plan/org/camera capability change (enable, disable, or a burst-profile edit)
  reaches the Edge on its very next poll, via the existing already-idempotent
  `save_edge_remote_config` (it only bumps the document version when the canonical content
  genuinely changed) -- no second protocol, no second poll loop.
- **Revocation / config-change-in-vivo**: verified -- disabling `plate_recognition` (or its
  `HighSpeedLPR` flag) mid-burst is picked up by the projection immediately; on the Edge, the
  authorizer denies new candidates on the very next tick and any active boost releases once its
  burst closes/expires (no candidate ever opens a NEW boosted burst once denied).

## Known limitations

- Edge-only ANPR is out of scope by design (final OCR/consensus lives in the SaaS); fails closed.
- No physical camera was used anywhere in this integration -- every frame is either a real
  synthetic fixture or a controlled replay, explicitly labeled as such throughout. The real FPS
  elevation logic (baseline capture, multi-burst composition, deterministic release) is proven
  against a real `processing.Sampler`-shaped fake controller (`internal/agent`'s
  `videoFPSController` seam) rather than a live RTSP-fed pipeline -- a live-camera FPS
  measurement was not performed.

## Explicitly out of scope (J7/J8/J9/J10)

No cost estimator, no billing, no vehicle color/speed/helmet detection, no visual
embeddings/search, no ODIN integration.
