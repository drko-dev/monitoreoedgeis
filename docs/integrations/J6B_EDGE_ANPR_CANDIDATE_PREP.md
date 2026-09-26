# J6-B — Edge ANPR/LPR Candidate Extraction PREP

Status: **PREP only** — contracts, burst/context bookkeeping and bounded
crop math. No OCR, no plate detector model, no final plate recognition, no
merge, no deploy, no PR to main.

- Edge main base: `05234f2b844724edf5e1253092c0cd440543eb10` (monitoreoedgeis, main)
- SaaS J6-A reference (conceptual only, no code dependency):
  drko-dev/monitoreoia, `feature/j6a-anpr-domain-prep`,
  `93cba562d36bab9b8ba6f61ef6437ef0182b47db`
- Branch: `feature/j6b-edge-anpr-candidate-prep`
- New package: `internal/anpr/`

## 1. Existing pipeline audit (PASO 0)

Today, one decoded frame flows as:

```
internal/rtsp (RTP receive)
  -> processing.cameraPipeline.OnPacket        (packetCh, drop-new when full)
  -> H264Depacketizer                          (depacketizeLoop -> auCh)
  -> VideoDecoder (ffmpeg subprocess)           (feedLoop pushes AUs, readLoop reads DecodedFrame)
  -> Sampler.ShouldEmit (FPS gate, optionally Hybrid-adaptive)
  -> Resize -> processing.Frame{CandidateKey, Seq, CorrelationID, ProcessingMode, ...}
  -> [Hybrid] MotionDetector.Evaluate -> candidate/non-candidate + CandidateReason/Score
  -> RingBuffer.Push(frame)                      (per-camera bounded history)
  -> Router.Dispatch(frame)                      (bounded queue + 1 worker goroutine per Sink)
       -> DebugSink | vision.Sink (edge YOLO)   | cloudsink.CloudSink (HTTP upload, rate-limited, offline-bufferable)
```

Key files read for this audit: `internal/processing/types.go`,
`internal/processing/pipeline.go`, `internal/processing/sampler.go`,
`internal/processing/router.go`, `internal/processing/ringbuffer.go`,
`internal/processing/manager.go`, `internal/vision/sink.go`,
`internal/vision/protocol.go`, `internal/cloudsink/cloudsink.go`,
`internal/cloudsink/limiter.go`, `internal/transport/contract.go`,
`cmd/geocam-edge/main.go`.

Relevant existing surfaces `internal/anpr` reuses as-is:

- `processing.Frame` — already carries `CandidateKey`, `Seq`,
  `CorrelationID`, `ProcessingMode`, `CandidateReason`, `CandidateScore`,
  `SourceWidth/Height`, `OutputWidth/Height`. J6-B never adds fields here.
- `processing.FrameHistory` (`Manager.FrameHistory(candidateKey)` ->
  `Snapshot() []Frame`) — the **existing** bounded ring-buffer read surface.
  See §8 (pre-event context) for why this was reused unmodified instead of
  a new ring buffer.
- `vision.Detection` (`Type == vision.DetectionTypeVehicle`, `BBox [4]float64`
  in output-frame pixels) — the source of a `anpr.VehicleCandidate` when a
  real edge YOLO detection exists. J6-B implements **no vehicle detector**.
- `processing.YUV420PToImage` — reused by `anpr.ExtractJPEG` instead of a
  second YUV420P decode path.
- `processing.Sink` / `Router` — J6-B does not currently register a new
  `Sink`; wiring a `Registry`-backed sink into the router is `main.go`/J6-C
  integration work (see §14).

**Second RTSP client created: NO.** `internal/anpr` imports
`internal/processing` (for `Frame`, `FrameHistory`, `YUV420PToImage`) and
nothing from `internal/rtsp` at all.

## 2. Package layout

```
internal/anpr/
  doc.go            package doc + SaaS field-mapping table
  types.go          BBox, VehicleCandidate, PlateCandidate
  candidate.go      ID generation, validation, dedupe/group keys
  quality.go        QualityHints, deterministic ranking, TopN
  crop.go           CropPolicy, Padding, CropSpec/CropResult, ComputeCrop, ExtractJPEG
  burst.go          BurstState/Outcome, CandidateBurst, BurstManager
  provider.go       PlateRegionProvider + None/Fake implementations
  authorization.go  Authorizer + Allow/Deny/Static fakes
  context.go        PreEventContext adapter over processing.FrameHistory
  sampling.go       BurstSamplingHint interface + no-op default
  config.go         Config, FrameSelectionPolicy, HighSpeedLPRProfile
  transport.go       ANPRCandidateEnvelope, schema version, payload bound, EnvelopeTransport
  registry.go        Registry: ties everything into Submit()
  status.go          Reason, CameraStatus, Metrics
  *_test.go          B1-B35 + stress test, colocated per Go convention
```

## 3. Candidate contract & SaaS mapping

See `doc.go`'s field-by-field table. Summary: `anpr.PlateCandidate` is a
semantically-compatible, independently-defined Go type — not a struct copy
of the SaaS's Python `PlateCandidate`. `organization_id` and
`vehicle_detection_id` have no Edge-side equivalent (resolved SaaS-side, or
simply not persisted Edge-side); `crop_reference` maps to the transport
envelope's `Payload`/`PayloadRef`, kept separate from candidate metadata.

**BBox semantics: pixel coordinates**, matching
`processing.Frame.OutputWidth/Height` and `vision.Detection.BBox` exactly —
chosen so a `VehicleCandidate` built from an existing `vision.Detection`
needs zero unit conversion. Never mixed with normalized coordinates
anywhere in this package.

**Candidate ID**: `NewCandidateID(cameraKey, frameSeq, ordinal, burstID)` ->
`"<camera>:<frameSeq>:<ordinal>:<burstID>"`. Deterministic and
reproducible (same inputs -> same id, always), no timestamp involved. The
ordinal disambiguates multiple candidates from the same frame.

## 4. Crop contract

`CropSpec` (requested bbox, padding, source frame seq/timestamp/dimensions,
encoding intent) -> `ComputeCrop` -> `CropResult` (requested bbox, **clamped**
bbox, output dimensions, encoding intent). Policy, never a panic:

- Structurally invalid (inverted) bbox -> `ErrCropInvertedBBox`.
- Bbox entirely outside the frame -> `ErrCropFullyOutside`.
- Bbox that clamps to zero width/height -> `ErrCropZeroArea`.
- Partial overlap -> **clamped** to frame edges (never dropped, never left
  out of bounds).

`CropPolicy` (`plate_only` / `vehicle_context` / `plate_plus_context`) is
config-selected — never hardcoded. `plate_plus_context` degrades to
`vehicle_context` when no plate region is available (documented in
`crop.go`), rather than failing the whole candidate — the one explicit
policy choice PREP makes for that mode.

`Padding` (percentage and/or fixed pixels) expands a bbox before clamping —
exercised near frame borders in `TestB7_ContextCropBounded`.

## 5. Plate-region provider

`PlateRegionProvider.LocatePlate(candidate, w, h) (BBox, error)`. PREP ships:

- `NoneProvider` — always `ErrPlateRegionUnavailable` (safe default).
- `FakePlateRegionProvider` — a **clearly labeled, deterministic heuristic**
  (lower-middle third of the vehicle bbox) for tests only. Never sold as a
  real detector; has an `Unavailable` flag to exercise the fail-closed path.

No real plate-detector model is loaded or wired anywhere in this milestone.

## 6. Frame ownership

`ExtractJPEG(frame processing.Frame, result CropResult) ([]byte, error)`
decodes, crops (via a full pixel **copy** into a fresh `image.RGBA`, not a
`SubImage` view) and JPEG-encodes **synchronously in one call**. It never
retains `frame.Data` past return and never returns a slice backed by
`frame.Data`'s array — verified by `TestB31_FrameOwnershipSafe` (mutating
the source buffer after the call cannot change already-returned bytes).

Chosen policy from the ownership menu in the spec: **encode immediately**,
not copy-and-hold or borrow.

## 7. Burst state machine

States: `OPEN -> FULL | EXPIRED | CLOSED` (both terminal states are
final — no reopen; a new event always gets a new `BurstID`).

- **Grouping key**: `camera + TrackID` when a track id exists, else
  `camera + CorrelationID`, else `camera` alone — never camera-only when a
  track/correlation id is available (spec item 22).
- **Max frames per burst**: enforced in `BurstManager.Process` — once
  `FramesAdded == MaxFrames`, state flips to `FULL` and further candidates
  for that burst are rejected (`OutcomeCapacityFrames`).
- **Max active bursts per camera**: enforced before a new burst is created;
  denial is `OutcomeCapacityBursts`, never a silent drop of an existing
  burst.
- **TTL**: `BurstManager` takes an injectable `now func() time.Time`.
  Expiry is evaluated lazily on every `Process`/`Cleanup` call — **no
  timers, no goroutines, no sleeps**.
- **Dedupe**: keyed on `(camera, frame_seq, vehicle bbox)`, a bounded
  FIFO-evicted set (`Config.DedupeCacheSize`) — reprocessing an identical
  tuple is `OutcomeDuplicate` and touches no other state.
- **Out-of-order policy**: strictly increasing `FrameSeq` only. A seq at or
  below the highest already accepted for that burst is
  `OutcomeStaleOutOfOrder` — deterministic regardless of arrival order
  (verified with repeated 100/102/101 runs in `TestB16`).
- **Frame selection** (`FrameSelectionPolicy`): `first` (default, keeps
  what arrives, capped at `MaxFrames`) or `top_n_quality` (retains up to a
  bounded 3x overflow cap, then `CandidateBurst.FinalFrames()` keeps the
  top `MaxFrames` by `QualityHints` score via the same deterministic
  `RankQuality`/`TopN` used standalone in `quality.go`).
- **Cleanup**: `BurstManager.Cleanup()` (and `Registry.Cleanup()`) removes
  terminal bursts from memory — call it from existing periodic
  housekeeping; there's no background goroutine of its own.

## 8. Pre/post-event context

**Pre-event**: `PreEventContext(history processing.FrameHistory, triggerSeq,
maxFrames)` filters `history.Snapshot()` down to at most `maxFrames` frames
at or before `triggerSeq`. It is a thin **read-only adapter over the
existing `Manager.FrameHistory`/`RingBuffer.Snapshot()`** — no second ring
buffer was created.

*Documented extension point*: `RingBuffer.Snapshot()` copies the *whole*
ring on every call; it has no "give me N frames before seq X" method of its
own. This PREP milestone does the filtering in the adapter rather than
adding a new method to `RingBuffer` (encapsulation of an existing,
well-tested type is not broken just to serve this PREP). If a future
milestone needs a cheaper query at much larger ring sizes, the correct
change is a bounded lookup method on `RingBuffer` itself.

**Post-event**: handled by the burst itself, not a second timer/goroutine
mechanism — a burst simply keeps accepting frames (bounded by `MaxFrames`
and `BurstTTL`) after its triggering candidate, which is exactly "candidate
+ next N frames/short duration", administered by whatever calls
`BurstManager.Process` for subsequent frames (the camera pipeline's own
loop), never a per-candidate goroutine or unlimited timer.

## 9. Memory bounds

| Bound | Field | Enforcement |
|---|---|---|
| Max active bursts/camera | `Config.MaxActiveBurstsPerCamera` | `BurstManager.Process` rejects (`OutcomeCapacityBursts`) |
| Max frames/burst | `Config.MaxFramesPerBurst` | burst -> `FULL`, further frames rejected |
| Burst overflow (top_n_quality) | `3 * MaxFramesPerBurst` (fixed multiplier, `ponytail:` in `burst.go`) | treated as `FULL` once hit |
| Dedupe cache | `Config.DedupeCacheSize` | FIFO eviction, oldest key dropped first |
| Max cameras | `Config.MaxCameras` | `Registry.Submit` rejects new cameras beyond the bound (`DROPPED_CAPACITY`) |
| Max encoded crop bytes | `Config.MaxEncodedCropBytes` / `NewEnvelope`'s `maxPayloadBytes` | `ErrPayloadTooLarge`, no truncation |
| Max context frames | `Config.MaxContextFrames` | passed to `PreEventContext`'s `maxFrames` |

Approximate upper bound per camera: `MaxActiveBurstsPerCamera *
overflowCap(MaxFramesPerBurst) * sizeof(FrameRecord)` (a `FrameRecord` is a
string id + `uint64` + `time.Time` + a few floats — tens of bytes, not a
frame's pixels: **crop pixel bytes are never retained in burst state**,
only produced on demand by `ExtractJPEG`/`NewEnvelope`). Total memory across
`MaxCameras` cameras is therefore linear and bounded, with no per-frame
history retained beyond what `processing.RingBuffer` already bounds.

## 10. Camera / track isolation

`groupKey` combines camera with `TrackID` (falling back to
`CorrelationID`, then camera alone). `BurstManager.activeCount` is keyed
per camera. Verified concurrently in `TestB13_CameraIsolation` (camera A
full/closed/reopened never affects camera B) and `TestB14_GroupTrackIsolation`
(two vehicles, same camera, independent bursts) plus `TestB32` (10 cameras
submitting concurrently under `go test -race`).

## 11. HIGH_SPEED_LPR profile

`Config.HighSpeedLPR *HighSpeedLPRProfile` is `nil` by default — **zero
behavior change** unless a caller explicitly does
`cfg = cfg.HighSpeedLPR.Apply(cfg)` before constructing a `Registry`. `Apply`
returns a new `Config`; it never mutates the base. This package never
auto-detects or auto-activates the profile (`TestB25`).

## 12. Sampler / Hybrid compatibility

`internal/anpr` never imports or constructs a `processing.Sampler`. The
only integration point is `BurstSamplingHint` (`RequestBurstFPS` /
`ReleaseBurstFPS`), whose only PREP implementation is `NoopSamplingHint` (a
provable no-op). Wiring a real implementation that calls
`cameraPipeline.SetTargetFPS` — while still respecting the existing
`TargetFPS` ceiling documented in `processing.HybridConfig` — is explicit
J6-C/integration work, out of scope here. Existing `processing` and
`vision` test suites pass unmodified (see §16), confirming zero regression
to sampler/hybrid defaults.

## 13. Cloud limiter / offline buffer — documented trade-off, no bypass

`RateLimitedTransport` wraps a real `EnvelopeTransport` and calls a
caller-supplied `AllowFunc(bytes) bool` — intended to be
`cloudsink.TokenBucket.Allow` — **before** ever sending. When it denies,
`Send` returns `ErrCapacityExceeded` explicitly; there is no code path that
bypasses `AllowFunc` to force delivery (spec item 27: "no bypass de rate
limit").

**Second spool: NO.** `internal/cloudsink` already has an offline buffer
(`CloudSink`'s I6 buffering). This PREP milestone does **not** decide
whether ANPR candidates reuse that exact buffer or need a dedicated
evidence transport later — that decision needs the real SaaS ingest
contract (payload shape, retry semantics for a JPEG crop vs. a full sampled
frame) to be made well, and is explicitly left open rather than forced now.
`EnvelopeTransport` is the seam where either choice plugs in later without
changing `internal/anpr`'s public contract.

## 14. Authorization boundary

`Authorizer.ANPRAllowed(cameraKey) bool` is an external, PREP-only-faked
boundary (`AllowAllAuthorizer`, `DenyAllAuthorizer`, `StaticAuthorizer`).
`Registry`'s default (no `WithAuthorizer` option) is `DenyAllAuthorizer` —
**fail-closed by construction**, not fail-open. A denied camera produces
**zero mutation**: no burst touched, no metrics moved, no camera-status
entry created (`TestB22`).

## 15. Transport envelope

`ANPRCandidateEnvelope` (`SchemaVersion: "anpr_candidate_v1"`), metadata and
`Payload`/`PayloadRef` kept as separate fields. `NewEnvelope` enforces
`maxPayloadBytes` explicitly (`ErrPayloadTooLarge`, never silent
truncation/oversize). No HTTP call is implemented — `UnavailableTransport`
is the only always-available implementation (`ErrTransportUnavailable`),
matching the explicit "transport unavailable" fail-closed case from the
spec.

## 16. Status / observability / privacy

- `CameraStatus` (spec item 38): `active_bursts`, `candidates_created`,
  `candidates_rejected`, `crops_created`, `capacity_drops`,
  `last_candidate_at` — bounded, per-camera, no per-frame history.
- `Metrics` (spec item 39): `candidate_count`, `crop_count`,
  `candidate_bytes`, `burst_count`, `burst_expired`, `capacity_dropped`,
  `upload_envelopes` — operational counters only, no billing.
- Observability events logged via `slog` at `Registry.Submit`/`Cleanup`:
  `anpr.edge_candidate_created`, `anpr.edge_candidate_rejected`,
  `anpr.edge_burst_started`, `anpr.edge_burst_full`,
  `anpr.edge_crop_created`, `anpr.edge_crop_rejected`,
  `anpr.edge_capacity_exceeded`. `anpr.edge_burst_expired` and
  `anpr.edge_transport_unavailable` are documented event names for the
  integration layer that will actually call `Cleanup`/`EnvelopeTransport`
  in production; this PREP's own tests exercise the underlying state
  transitions (`TestB10`, `TestB24`) without requiring every named event to
  be wired through a specific logger call site yet.
- **Never logged**: raw JPEG bytes, plate text, RTSP credentials/URLs.
  `TestB35_NoCredentialsOrPayloadLeakage` asserts a real `Submit` run's log
  output contains none of `rtsp://`, `password`, or a raw JPEG SOI marker.
- **Privacy**: crops are produced on demand by `ExtractJPEG`/`NewEnvelope`
  and never retained by `Registry`/`BurstManager` — `FrameRecord` stores
  only metadata (id, seq, timestamp, quality hints), never pixel bytes.
  There is no crop cache in this package at all in PREP; a future runtime
  cache with an explicit TTL is the retention boundary a real transport
  integration must add, not something PREP invents speculatively.

## 17. Fail-closed matrix

| Case | `Reason` | Zero mutation? |
|---|---|---|
| ANPR disabled (`Config.Enabled==false`) | `SKIPPED` | yes |
| Camera not authorized | `REJECTED` (`unauthorized`) | yes |
| Invalid vehicle bbox | `REJECTED` (`invalid_bbox`) | yes |
| Duplicate candidate | `REJECTED` (`duplicate`) | yes (idempotent) |
| Stale out-of-order frame | `REJECTED` (`stale_out_of_order`) | yes |
| Max active bursts/frames reached | `DROPPED_CAPACITY` | yes |
| Max cameras reached | `DROPPED_CAPACITY` | yes |
| Plate-region provider unavailable (policy requires it) | `UNAVAILABLE` | candidate/crop not produced |
| Crop geometry invalid | `REJECTED` | yes |
| Payload too large | `ErrPayloadTooLarge` from `NewEnvelope` | envelope not built |
| Transport unavailable | `ErrTransportUnavailable` from `Send` | envelope not delivered |

## 18. Open integration work (explicitly out of scope for J6-B)

- Wiring `internal/anpr.Registry` as an actual `processing.Sink` (or a
  consumer alongside `vision.Sink`) in `internal/agent`/`cmd/geocam-edge`.
- A real `PlateRegionProvider` backed by an actual plate-detector model.
- A real `EnvelopeTransport` reaching the SaaS's definitive J6 ingest
  endpoint, and the cloudsink-offline-buffer-vs-dedicated-spool decision
  from §13.
- A real `BurstSamplingHint` implementation that reaches into
  `processing.cameraPipeline.SetTargetFPS`, respecting the existing
  ceiling.
- Wiring `anpr.edge_burst_expired` / `anpr.edge_transport_unavailable`
  logging into the concrete housekeeping loop and transport call site once
  those are real.
