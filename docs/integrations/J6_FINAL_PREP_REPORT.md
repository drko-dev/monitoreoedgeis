# J6 — Edge Final Prep Report (B + C + D)

Scope: `internal/anpr/` only. Edge side of the J6 ANPR/LPR milestone. No
production wiring, no second RTSP client, no OCR, no deploy, no merge/PR.

- J6-B (Edge) HEAD: `9d48029aafd1b571c2372b6d39cabf63604e9f6a`
- J6-C (Edge) branch `feature/j6c-edge-cross-repo-acceptance`, HEAD: `6e345d3dca91de1051b947c8208ef51fb1725398`
- J6-D integration branch: `integration/j6-anpr-prep` (created below, fast-forward
  of the above — no rebase/merge conflicts existed, see §D1)
- SaaS reference (external, not independently verifiable from this repo):
  drko-dev/monitoreoia, `feature/j6c-cross-repo-acceptance`, based on `93cba562`

## D1 — Reconciliation (J6-B + J6-C)

Checked `internal/anpr/` for duplicate/contradictory contracts, naming
conflicts, lifecycle conflicts, error-state conflicts. **None found requiring
resolution.** Specifically:

- Error taxonomy: J6-B's `Reason` constants (`status.go`) already use the
  exact canonical names `SKIPPED, REJECTED, UNAVAILABLE, DROPPED_CAPACITY,
  ACCEPTED` — no mapping/renaming needed (D3 is a no-op as a result).
- No duplicate type/function definitions were introduced by J6-C; all C-work
  is additive (new fields with `omitempty`, one new function `DecodeEnvelope`,
  one new file `cross_contract_test.go`).
- The one behavior-affecting change (adding `json` tags to `BBox` and
  `QualityHints`) does not conflict with any existing J6-B code path — both
  types are private to `internal/anpr` and no other J6-B test or production
  code asserted on their previous (untagged) JSON shape (verified: `go test
  ./internal/anpr/...` before and after, identical PASS set plus new C-tests).

## D2 — Canonical envelope confirmation

`internal/anpr/transport.go`'s `ANPRCandidateEnvelope`, as it stands after
J6-C, matches the shared canonical contract in full — see
`docs/integrations/J6C_CROSS_REPO_ACCEPTANCE.md` §2 for the field-by-field
mapping table. Summary of what changed to reach that match:

| Contract requirement | Before J6-C | After J6-C |
|---|---|---|
| lower-snake-case wire keys for bbox/quality | Go default capitalized keys | explicit `json` tags |
| payload content-type / size | absent | `PayloadContentType`, `PayloadSizeBytes` (auto-set by `NewEnvelope`) |
| payload hash (optional) | absent | `PayloadHash` field exists, left empty (optional per contract) |
| model metadata (optional, future) | absent | `PlateModelVersion` field exists, left empty (no real model in PREP) |
| schema version fail-closed parse | no parse path existed | `DecodeEnvelope` + `ErrUnsupportedSchemaVersion` |

`track_id` is the one contract field **not** on the wire envelope — see
J6C report §2 for why (no J5 runtime integration yet; documented gap,
additive to fix later).

## D3 — Error taxonomy unification

No-op: already unified before J6-D started (see D1). `SubmitResult.Reason`
values map 1:1 onto the canonical taxonomy's Edge-applicable subset
(`SKIPPED, REJECTED, UNAVAILABLE, DROPPED_CAPACITY, ACCEPTED` — the
remaining `PENDING, AMBIGUOUS` are explicitly post-SaaS and never emitted
by Edge, confirmed by `TestC11_LowQualityStillForwardedNeverFabricatedAccepted`).

## D4 — Edge lifecycle documentation

**Candidate lifecycle** (`VehicleCandidate` -> `PlateCandidate` ->
`ANPRCandidateEnvelope`):
1. A `vision.Detection` (vehicle) is mapped into a `VehicleCandidate` by the
   caller (outside this package — PREP defines only the type, not the
   mapping site).
2. `Registry.Submit(v)`: `Config.Enabled` check (SKIPPED if off) ->
   `Authorizer.ANPRAllowed` check (REJECTED if denied, **zero mutation** —
   C19) -> `ValidateVehicleCandidate` (REJECTED on structurally invalid bbox)
   -> camera capacity check (DROPPED_CAPACITY) -> `BurstManager.Process`
   (dedupe/out-of-order/capacity — REJECTED or DROPPED_CAPACITY) ->
   `PlateRegionProvider.LocatePlate` (best-effort, UNAVAILABLE only affects
   `plate_bbox` presence, not the whole candidate unless the crop policy
   requires it) -> `SelectCropBBox` + `ComputeCrop` (REJECTED/UNAVAILABLE on
   geometry failure) -> `PlateCandidate` built, `Reason=ACCEPTED`.
3. A caller turns the accepted `PlateCandidate` + its crop bytes
   (`ExtractJPEG`) into an `ANPRCandidateEnvelope` via `NewEnvelope`, which
   enforces `MaxEncodedCropBytes` (fails closed, no `SubmitResult`/`Reason`
   involved — a build-time error, never a silently-sent oversized payload).
4. The envelope is (in a real deployment, not PREP) handed to an
   `EnvelopeTransport.Send`.

**Burst lifecycle** (`burst.go`): a burst opens on the first accepted
candidate for a given camera+group key (track/correlation/camera-only
fallback — `groupKey`), accepts subsequent frames up to
`Config.MaxFramesPerBurst` (`BurstFull` once reached, tracked but not
rejected), and closes/expires on `Config.BurstTTL` or explicit `Close`.
`Registry.Cleanup()` releases terminal (CLOSED/EXPIRED) burst memory — no
internal timer; a caller's housekeeping loop must call it periodically (not
wired to any real loop in PREP, by design — no production wiring, D14).

**Transport lifecycle**: `EnvelopeTransport` is a pure interface;
`UnavailableTransport` (always fails explicit, the safe default) and
`RateLimitedTransport` (wraps a real limiter's `Allow`, fails explicit on
denial, never bypasses it) are the only implementations PREP ships. No
concrete network/HTTP/cloudsink-backed implementation exists yet — see D6.

## D5 — Future authorization chain (doc only, no code)

Planned chain: J4 `RuntimeCapabilityPlan` -> J5 runtime camera capability
context -> J6 ANPR authorization -> Edge/SaaS processing. The current
`Authorizer` interface (`authorization.go`) is a single method,
`ANPRAllowed(cameraKey string) bool`, with a fail-closed default
(`DenyAllAuthorizer`) when nothing is wired. This is compatible with
plugging in a future capability-plan-backed implementation without any
contract change: a real implementation would simply consult J4/J5 state
internally and still return a `bool` per camera key — `Registry` never
needs to know how that decision was made, and the zero-mutation guarantee
(C19) is enforced entirely on the `Registry` side of the interface, so it
holds regardless of which `Authorizer` is wired in later.

## D6 — Production integration plan (Edge side)

What is still needed for production, none of it started here:

1. **Real `PlateRegionProvider`**: a real plate-detection model (only
   `NoneProvider`/`FakePlateRegionProvider` exist in PREP). `PlateModelVersion`
   on the envelope (C27) is ready to receive its identity once it exists.
2. **Real transport to the SaaS's definitive ingest endpoint**: no HTTP/gRPC
   client exists; `EnvelopeTransport` is the seam to implement against once
   the SaaS side publishes its endpoint contract.
3. **Offline buffer decision**: reuse `internal/cloudsink`'s existing offline
   buffer vs. a dedicated evidence spool — explicitly undecided in J6-B and
   still undecided here (out of scope for both C and D; a real architectural
   choice, not a PREP-scope task).
4. **`BurstSamplingHint` wiring**: connecting burst state back to the camera
   pipeline's adaptive sampler is not wired in PREP.
5. **`Registry` as a `processing.Sink`**: `cmd/geocam-edge` does not
   instantiate or wire a `Registry` into the live camera pipeline; that
   integration is future work, deliberately untouched here (no production
   wiring, D14).

## D7-D11 — Performance/bandwidth (doc only, no values invented)

No real dataset, no pricing, no productive latency numbers exist to report.
`ExtractJPEG` is a synchronous decode-crop-encode call
(`processing.YUV420PToImage` + `image/jpeg` at fixed quality 90) — its cost
scales with the source frame's decoded size, not the crop size, since the
whole frame is decoded first (a known, documented ceiling — see `crop.go`).
Candidate/envelope metadata itself is small (a few hundred bytes of JSON);
bandwidth is dominated by whatever payload delivery mode a real transport
picks (inline JPEG bytes vs. `PayloadRef` to object storage) — undecided,
see D6 item 2/3.

## D12 — Security (Edge side)

- No credentials/RTSP passwords/API keys/base64 image data found in
  `internal/anpr/*.go` (grep-based, `TestC23_NoSecretsOrRawImageDataInSource`).
- Payload is bounded before any transport call (`MaxEncodedCropBytes`,
  `ErrPayloadTooLarge`, C17).
- Envelope schema is validated on decode (`DecodeEnvelope`, C26).
- Camera identity is already embedded in `candidate_id`/`camera_key`
  (C6/C28) — no separate identity plumbing needed for evidence traceability.

## D13 — Full suite

```
go build ./...                -> clean, no errors
go vet ./internal/anpr/...    -> clean
go test ./internal/anpr/...          -> ok (B1-B35 + stress + C1-C30)
go test ./internal/anpr/... -race    -> ok
go test ./...                        -> ok, whole repo (processing, vision,
                                         cloudsink, transport, rtsp, etc.)
go test ./... -race                  -> ok, whole repo
```

Zero regressions. 6 pre-existing environment-conditional skips exist
elsewhere in the repo (kernel-disk-full test, vision runtime preflight,
soak/scale/helper-process tests) — none in `internal/anpr`, none introduced
by this work. Zero xfails.

## D14 — Static scope confirmation

- No second RTSP client: only `internal/rtsp`'s existing client exists;
  `internal/anpr` never imports it.
- No OCR/final plate-read implementation.
- No deployment/manifest changes.
- No service activation (`cmd/geocam-edge` untouched).
- No new production endpoint (`EnvelopeTransport` remains an unwired
  interface with only `UnavailableTransport`/`RateLimitedTransport`).
- `internal/rtsp`, `internal/processing`, `internal/vision`,
  `internal/cloudsink`, `internal/transport` were **not modified** by J6-C/D
  (confirmed: `git diff 9d48029..HEAD --stat` touches only `internal/anpr/`
  and `docs/integrations/`).

## D15/D16 — This report + integration branch

This file is `docs/integrations/J6_FINAL_PREP_REPORT.md`. Integration branch
`integration/j6-anpr-prep` is created as a direct pointer at this report's
own commit — a fast-forward, since J6-B -> J6-C -> J6-D was already linear on
`feature/j6c-edge-cross-repo-acceptance`. No merge/rebase was needed;
history is preserved exactly as committed (three commits on top of
`05234f2`: the J6-B PREP commit, the J6-C acceptance commit, then this D
report commit). Neither `feature/j6b-edge-anpr-candidate-prep` nor any other
existing branch was force-pushed or altered.

## D17 — No PR, no merge, no deploy

Confirmed: no PR opened to `main`, no merge performed, no deploy triggered.

## D18 — Final verdict

All Edge-side gates (J6-G1..G30, applicable subset) PASS with evidence (see
`J6C_CROSS_REPO_ACCEPTANCE.md` §19). B1-B35 + stress regression: PASS. Full
`go test ./...` (with and without `-race`): PASS, zero regressions. Contract
compatibility with the documented SaaS-facing shape: documented and fixed at
the transport boundary (§D2). Bounded state: proven under load and
concurrency (C24/C25). Fail-closed paths: proven, especially C19
(auth-deny zero mutation) and C17/C26 (payload/schema fail-closed). Privacy:
documented and grep-verified (C23/D12). No scope creep: only `internal/anpr/`
and `docs/integrations/` changed. No productive dependency violated (no
wiring, no endpoint, no second RTSP client, no deploy).

**J6_EDGE_FINAL_PREP_STATUS = PASS**
