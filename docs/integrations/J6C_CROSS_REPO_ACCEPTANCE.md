# J6-C — Edge Cross-Repo Acceptance

Status: **contract acceptance only** — no infrastructure, no productive
transport, no real camera/model. Fixtures are deterministic JSON, no real
imagery/PII.

- Edge repo: `monitoreoedgeis`, branch `feature/j6c-edge-cross-repo-acceptance`
- Edge base: J6-B, commit `9d48029aafd1b571c2372b6d39cabf63604e9f6a`
- SaaS reference (external, not verifiable from this repo/session):
  drko-dev/monitoreoia, branch `feature/j6c-cross-repo-acceptance`,
  based on `93cba562`
- New test file: `internal/anpr/cross_contract_test.go`
- New fixtures: `internal/anpr/testdata/candidate_v1_{valid,duplicate,oversize_metadata,multiple_burst}.json`

## 1. Canonical contract: ANPRCandidateEnvelope v1

The contract shared with the SaaS side (verbatim from the cross-repo task)
is reproduced here so this document is self-contained:

`schema_version`, `candidate_id`, `camera_id`/`camera_key`, `frame_seq`,
`timestamp`, `vehicle_bbox`, `plate_bbox` (optional), `track_id` (optional),
`burst_id`, `correlation_id`, `processing_mode`, `quality_hints`, payload
metadata (content-type, size in bytes, hash optional). Error taxonomy:
`SKIPPED, REJECTED, UNAVAILABLE, DROPPED_CAPACITY, PENDING, AMBIGUOUS,
ACCEPTED`.

## 2. C1 — Contract field mapping

| Contract field                  | Go struct field (`ANPRCandidateEnvelope`) | JSON wire key            | Notes |
|----------------------------------|--------------------------------------------|---------------------------|-------|
| `schema_version`                 | `SchemaVersion`                             | `schema_version`          | exact string `"anpr_candidate_v1"`, see §5 |
| `candidate_id`                   | `CandidateID`                               | `candidate_id`             | see §4 |
| `camera_id` / `camera_key`       | `CameraKey`                                 | `camera_key`               | Edge only ever had one identity concept (camera key); no separate `camera_id` exists internally — documented as the single source of truth for both contract aliases |
| `frame_seq`                      | `FrameSeq`                                  | `frame_seq`                 | `uint64`, monotonic within session, not required consecutive (C10) |
| `timestamp`                      | `Timestamp`                                 | `timestamp`                 | Go `time.Time`, RFC3339Nano with explicit offset; see §6 |
| `vehicle_bbox`                   | `VehicleBBox`                               | `vehicle_bbox` (`x0,y0,x1,y1`) | pixel space of the original frame, never normalized; see §7 |
| `plate_bbox`                     | `PlateBBox`                                 | `plate_bbox` (`*BBox`, `omitempty`) | omitted entirely when unavailable — never a fabricated zero-value bbox |
| `track_id`                       | *(not on the envelope)*                     | — | carried on the internal `PlateCandidate.TrackID` but **deliberately not forwarded onto the wire envelope** in PREP — no J5 runtime track integration exists yet. Documented gap, not a silent drop; adding it later is additive (new optional field). |
| `burst_id`                       | `BurstID`                                   | `burst_id`                  | see §4 |
| `correlation_id`                 | `CorrelationID`                             | `correlation_id`            | preserved verbatim across retries (C21) |
| `processing_mode`                | `ProcessingMode`                            | `processing_mode`           | forwarded from `processing.Frame.ProcessingMode`, never reinterpreted |
| `quality_hints`                  | `QualityHints`                              | `quality_hints`              | see below |
| payload metadata (content-type, size, hash) | `Payload`, `PayloadRef`, `PayloadContentType`, `PayloadSizeBytes`, `PayloadHash` | `payload`, `payload_ref`, `payload_content_type`, `payload_size_bytes`, `payload_hash` | see §8 |
| model metadata (future)          | `PlateModelVersion`                         | `plate_model_version` (`omitempty`) | see §9 (C27) |

`QualityHints` sub-fields: `width`, `height`, `area`, `brightness_hint`
(optional), `blur_hint` (optional), `relative_plate_size` (optional),
`vehicle_confidence`, `plate_region_confidence` (optional).

### Naming incompatibility found and fixed

Before this milestone, `BBox` and `QualityHints` had **no `json` struct
tags**, so `encoding/json` fell back to Go's default capitalized field names
(`X0`, `Y0`, `VehicleConfidence`, ...) — incompatible with the contract's
lower-snake-case wire format the SaaS side expects. Fixed by adding explicit
`json` tags to both types (`internal/anpr/types.go`,
`internal/anpr/quality.go`). This is a **transport-boundary-only** fix: it
changes only how these types marshal to JSON, not any internal Go call site
(neither type is used outside `internal/anpr`, confirmed by repo-wide
search before the change). No other domain code (`internal/processing`,
`internal/vision`, `internal/cloudsink`, `internal/transport`) was touched.

## 3. Payload metadata (C1, cross-repo contract "payload" item)

The contract asks for "reference (not bytes obligatory inline) to the crop —
content-type, size in bytes, hash optional." `ANPRCandidateEnvelope`
already kept `Payload`/`PayloadRef` as separate fields (J6-B); this
milestone adds `PayloadContentType` and `PayloadSizeBytes`, auto-populated
by `NewEnvelope` whenever inline bytes are supplied (`"image/jpeg"`, the
only encoding this package produces — see `crop.go`'s `ExtractJPEG`).
`PayloadHash` exists as an optional field but is **not computed** in PREP
(no hashing implemented) — left empty, per the contract's own "hash
optional" language.

## 4. C4/C5/C6 — Candidate ID, burst ID, camera isolation

Format (unchanged from J6-B, `candidate.go`'s `NewCandidateID`):
`camera:frameSeq:ordinal:burstID`. Deterministic (same inputs -> same
output), and the camera segment is always the literal `CameraKey`, so a
candidate/burst id can never collide across cameras structurally (the
camera prefix differs), verified for real registry-produced ids in
`TestC5_C6_BurstIDSharedWithinBurstIsolatedAcrossCameras` and
`TestC25_ConcurrentCamerasNoCrossContamination` (10 concurrent cameras,
`-race`, id uniqueness checked globally).

## 5. C26 — Schema version fail-closed

`transport.go` adds `DecodeEnvelope([]byte) (ANPRCandidateEnvelope, error)`
and `ErrUnsupportedSchemaVersion`. Edge does not currently need to parse its
own envelopes in production (build-only, never read back), so this exists
purely so any future local-replay/inspection path fails explicit on an
unrecognized `schema_version` rather than silently continuing — verified in
`TestC26_UnknownSchemaVersionFailsExplicit` by tampering a real fixture's
version string.

## 6. C3 — Timestamp contract

Edge's `Timestamp` field is a plain Go `time.Time` (whatever value
`processing.Frame.Timestamp`/the caller supplies — no forced `.UTC()`
conversion anywhere in `internal/anpr`). `encoding/json`'s default
`time.Time` marshaling is RFC3339Nano **with the value's own offset always
explicit** (e.g. `Z` for UTC, `-03:00` for a fixed zone) — Go's
`time.Time` JSON marshaling never produces a naive/offset-less string.
`TestC3_TimestampRoundtrip` exercises both a UTC value and a non-UTC fixed
offset and confirms `Marshal -> Unmarshal` resolves to the exact same
instant (`time.Time.Equal`) in both cases, and that the wire string always
carries an explicit `Z`/`±HH:MM` suffix.

No production code change was needed for this item — the existing
mechanism already satisfies the contract; this section documents that fact
so the SaaS side can rely on it without further negotiation.

## 7. C2 — BBox pixel semantics

`vehicle_bbox`/`plate_bbox` are always pixel coordinates of the *original*
source frame (`x0,y0,x1,y1`, `X1>X0`, `Y1>Y0` — see `types.go`'s `BBox`
doc), never normalized `0..1`. `TestC2_BBoxPixelSemantics` uses frame-scale
values (e.g. `x1=1280` on a 1920px-wide frame) and asserts they round-trip
byte-for-byte unscaled, plus an explicit `> 1` sanity check that would catch
an accidental normalization regression.

## 8. C12/C13 — Plate-region-unavailable never fabricates a bbox

Edge-side half only (OCR/detector cloud consensus is SaaS-side, N/A here).
When `PlateRegionProvider.LocatePlate` returns
`ErrPlateRegionUnavailable`, `Registry.Submit` leaves `PlateCandidate.PlateBBox`
`nil`; `NewEnvelope` then serializes with `plate_bbox` **omitted from the
wire entirely** (`omitempty` on a `*BBox`), never a zero-value or fabricated
rectangle. Verified in `TestC12_C13_PlateUnavailableNeverFabricatesBBox`.

## 9. C27 — Model metadata (future-proofing only)

Added `ANPRCandidateEnvelope.PlateModelVersion string
`json:"plate_model_version,omitempty"``. Always empty in PREP (no real
`PlateRegionProvider` model exists — see `provider.go`'s doc comment); the
field exists solely so a future real provider has somewhere to report its
model/version without another envelope shape change. `omitempty` confirmed
to drop it from the wire when unset (`TestC27_ModelMetadataFieldExists`).

## 10. C28 — Evidence chain, Edge side

Given only a serialized envelope (no log lookup), `candidate_id` decomposes
via `strings.SplitN(id, ":", 4)` into `camera, frameSeq, ordinal, burstID`,
and this must agree with the envelope's own `camera_key`/`burst_id` fields —
verified in `TestC28_EvidenceChainSelfContained`. The envelope is
self-contained for this reconstruction; no external state is needed.

## 11. C19 (CRITICAL) — Auth deny, zero mutation

`DenyAllAuthorizer` (already existed, `authorization.go`) plus
`TestC19_AuthDenyZeroMutation`: an unauthorized `Submit` call produces
`Reason == REJECTED`, `Detail == "unauthorized"`, `Candidate == nil`,
`Crop == nil`, zero bursts in the `BurstManager`, all zero `Metrics`, and no
`CameraStatus` recorded at all. This is the strongest guarantee in the
suite and it holds.

## 12. C21 — Offline/retry, correlation_id integrity

`RateLimitedTransport` (existing, `transport.go`) denies a `Send` when
`AllowFunc` returns false (`ErrCapacityExceeded`) — modeling an
outage/backpressure window — then a retry with the *same* envelope value
succeeds once the limiter reopens. `TestC21_OfflineRetryKeepsCorrelationIDIntact`
confirms `correlation_id` and `candidate_id` are byte-identical across both
attempts (they are the same Go value; nothing regenerates them on retry).

## 13. C22 — HIGH_SPEED_LPR opt-in

Unchanged from J6-B (`config.go`'s `HighSpeedLPRProfile.Apply`): never
auto-invoked by `NewRegistry`/`Submit`; a caller must explicitly call
`profile.Apply(base)` and use the *returned* config. `TestC22_HighSpeedLPROptInOnly`
re-confirms `Apply` does not mutate its input in place either.

## 14. C23 — Privacy grep

`TestC23_NoSecretsOrRawImageDataInSource` scans every `.go` file directly in
`internal/anpr/` for credentialed RTSP URIs, `password=`/`api_key=`
literals, and inline base64 image data URIs. Zero matches. (This
complements, not replaces, J6-B's existing `TestB35`-style runtime log
check — see B-suite.)

## 15. C24/C25 — Memory bounds & concurrency

`TestC24_MemoryBoundsUnderSustainedLoad` extends the existing stress
pattern (20 cameras x 50 frames each, `MaxCameras=5`) and asserts total
burst/candidate counts stay within the configured bounds regardless of
input volume. `TestC25_ConcurrentCamerasNoCrossContamination` runs 10
cameras concurrently (goroutines, `-race`), each producing multiple bursts,
and asserts every observed `candidate_id` (a) carries the correct camera
prefix for the goroutine that produced it and (b) is globally unique across
all goroutines — no id or state leaked across cameras under concurrency.

## 16. C8/C9/C10/C16/C17/C18 — burst/dedupe/payload bound

All reuse the existing J6-B mechanisms (`registry.go` dedupe cache,
`burst.go` out-of-order/capacity policy, `transport.go`'s
`MaxEncodedCropBytes` check) exercised through the *envelope-facing* path
rather than internal-only assertions:

- C8/C16: `TestC8_DuplicateDeliveryDoesNotDoubleCount`,
  `TestC16_ReplayAlreadyEmittedBurstDeduped` — resubmitting an identical
  `VehicleCandidate` (simulating a redelivered/replayed envelope) is
  rejected as `duplicate`, `Metrics.CandidateCount` never increases.
- C9: `TestC9_OutOfOrderDoesNotCorruptState` — frames arriving
  `100, 102, 101` accept exactly `100` and `102`; camera status stays
  internally consistent (`CandidatesCreated=2`, `CandidatesRejected=1`).
- C10: `TestC10_DroppedFrameDoesNotBreakBurst` — frames `1,2,4,5` (gap at 3)
  all accepted into **one** burst; no consecutiveness requirement exists.
- C17: `TestC17_OversizePayloadFailsBeforeTransport` — a crop payload over
  the configured bound fails `NewEnvelope` with `ErrPayloadTooLarge`; there
  is no envelope value produced to hand any transport.
- C18: `TestC18_BurstFrameCapHoldsUnder100Inputs` — 100 simulated frames
  against `MaxFramesPerBurst=5` yields exactly 5 accepted/transported
  candidates.

## 17. C30 — Failure matrix (Edge side)

| Edge failure mode                    | Taxonomy state     | Test |
|----------------------------------------|---------------------|------|
| `Config.Enabled == false`             | `SKIPPED`            | `TestC30_FailureMatrix/disabled` |
| Unauthorized camera (`Authorizer` denies) | `REJECTED`        | `TestC30_FailureMatrix/unauthorized`, `TestC19` |
| Invalid vehicle bbox (inverted/zero-area) | `REJECTED`        | `TestC30_FailureMatrix/invalid_bbox` |
| Duplicate candidate                   | `REJECTED` (`detail=duplicate`) | `TestC8`, `TestC16` |
| Out-of-order/stale frame              | `REJECTED` (`detail=stale_out_of_order`) | `TestC9` |
| Burst/camera capacity hit             | `DROPPED_CAPACITY`   | `TestC30_FailureMatrix/capacity`, `TestC18` |
| Plate region unavailable              | `UNAVAILABLE`         | `TestC12_C13` |
| Invalid crop geometry                 | `REJECTED`            | `TestC30_FailureMatrix/invalid_crop_geometry` |
| Oversize crop payload                 | *(build-time error from `NewEnvelope`, no `SubmitResult`/`Reason` — never reaches a transport)* | `TestC17` |
| Transport not wired                   | *(transport error `ErrTransportUnavailable`, not a `Reason` — caller-level concern)* | `TestC30_FailureMatrix/transport_not_wired` |

No silent success and no panic in any row (every `ComputeCrop`,
`SelectCropBBox`, `NewEnvelope`, `PlateRegionProvider.LocatePlate` path
returns an explicit `error` on failure — confirmed by reading each; none of
these ever call `panic`).

## 18. Out of Edge scope, N/A here (see SaaS report)

- C14/C15 — plate consensus/OCR ranking (post-ingest, SaaS-side).
- C20 — SaaS-side entitlement verification once an envelope lands.
- C29 — consensus benchmarking (SaaS-side).

## 19. Gates (J6-G1..J6-G30, Edge side)

| Gate | Status | Evidence |
|------|--------|----------|
| G1 Contract mapping documented (C1) | PASS | §2 |
| G2 BBox pixel semantics (C2) | PASS | §7, `TestC2` |
| G3 Timestamp roundtrip (C3) | PASS | §6, `TestC3` |
| G4 Candidate ID roundtrip (C4) | PASS | `TestC4` |
| G5 Burst ID shared within burst (C5) | PASS | `TestC5_C6` |
| G6 Camera isolation (C6) | PASS | `TestC5_C6`, `TestC25` |
| G7 Multi-vehicle separation (C7) | PASS | `TestC7` |
| G8 Duplicate delivery deduped (C8) | PASS | `TestC8` |
| G9 Out-of-order handled (C9) | PASS | `TestC9` |
| G10 Dropped frame handled (C10) | PASS | `TestC10` |
| G11 Low-quality forwarded, no fabricated ACCEPTED (C11) | PASS | `TestC11` |
| G12/G13 Plate unavailable, no fabricated bbox | PASS (Edge half only) | `TestC12_C13` |
| G14/G15 Consensus/OCR | N/A — SaaS-side, see SaaS report | — |
| G16 Replay dedupe (C16) | PASS | `TestC16` |
| G17 Payload bound before transport (C17) | PASS | `TestC17` |
| G18 Burst frame cap under load (C18) | PASS | `TestC18` |
| G19 Auth deny zero mutation (C19, CRITICAL) | PASS | `TestC19` |
| G20 SaaS auth | N/A — SaaS-side, see SaaS report | — |
| G21 Offline/retry correlation_id (C21) | PASS | `TestC21` |
| G22 HIGH_SPEED_LPR opt-in (C22) | PASS | `TestC22` |
| G23 Privacy grep (C23) | PASS | `TestC23` |
| G24 Memory bounds (C24) | PASS | `TestC24` |
| G25 Concurrency, no cross-contamination, `-race` (C25) | PASS | `TestC25` |
| G26 Schema version fail-closed (C26) | PASS | `TestC26` |
| G27 Model metadata field (C27) | PASS | `TestC27` |
| G28 Evidence chain self-contained (C28) | PASS | `TestC28` |
| G29 Consensus benchmark | N/A — SaaS-side, see SaaS report | — |
| G30 Failure matrix (C30) | PASS | §17, `TestC30` |

No BLOCKER found. All Edge-side gates PASS with evidence (test names
above, all in `internal/anpr/cross_contract_test.go`, run via
`go test ./internal/anpr/... -race`).

## 20. Full test run (this milestone)

```
go build ./...                       -> clean
go vet ./internal/anpr/...           -> clean
go test ./internal/anpr/... -race    -> ok (B1-B35 + stress + C1-C30, all PASS)
```
