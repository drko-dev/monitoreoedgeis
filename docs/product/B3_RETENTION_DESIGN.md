# B3 — Bounded Full Edge Retention: audit and implementation design

> **STATUS: B3-A IMPLEMENTED (events + captures) · B3 STILL OPEN (clips).**
> Events and JPEG evidence are now bounded and evictable; MP4 clips are not yet
> (B3-B), and the free-disk gate still does not cover clips. B3 is therefore
> **not** closed.
> This document is the persisted audit for B3. It contains **no implemented
> retention**, and nothing here may be read as B3 being closed. It exists so
> implementation can proceed from the repository alone.
>
> Retention **permanently deletes evidence**. A wrong eviction is unrecoverable,
> which is why this slice is specified before it is written.

Branch: `fix/hito-z-b3-retention`, stacked on
`release/hito-z-b2-vision-worker-package-v2` @ `a2acbf0`.

## 1. The gap, with evidence

There is **no retention of any kind** for the three artifact kinds Full Edge
produces. Verified by searching `Prune|evict|retention|MaxAge|TTL|os.Remove`
across `internal/fulledge`, `internal/evidence` and `internal/edgebacklog`:

| Artifact | Path | Count bound | Byte bound | TTL | Free-disk gate |
| --- | --- | --- | --- | --- | --- |
| event metadata | `<DataDir>/events/<event_uuid>.json` | none | none | none | **NOT gated** |
| JPEG capture | `<DataDir>/evidence/captures/<capture_uuid>.jpg` | none | none | none | gated |
| MP4 clip | `<DataDir>/evidence/clips/<event_uuid>.mp4` | none | none | none | **NOT gated** |

`EventStore` has no limits field at all (`store.go:20-24`), `EvidenceManager`
exposes only `SaveJPEG`/`SaveFrame` (`evidence.go:78,182`), and `Clipper` only
`Capture` (`clips.go:81`). Nothing can list, size or delete any of the three.
The MP4 write is additionally **ungated on free disk** (`clips.go` never
references limits), so a clip can be written into a full filesystem.

Consequence: nothing bounds disk growth under sustained detections.

## 2. Two findings that make this larger than "add three knobs"

These are the reason this document exists rather than an implementation.

### F-A — One JPEG is SHARED by N event records, so capture eviction needs refcounting

`service.go:103` generates a **fresh capture UUID** per inference result, saves
one JPEG, and then builds **one event JSON per detection** all pointing at that
same JPEG (`service.go:98-124` then `:146`; `evidence.go:104`). So:

* JPEG filenames are capture UUIDs, **not** event UUIDs;
* an eviction that deletes the oldest `evidence/captures/*.jpg` can delete a
  file still referenced by several live `events/*.json` records.

Therefore capture eviction **must be refcounted against every event JSON that
names it**, or it will silently destroy evidence that is still referenced. This
is not an optimization; it is a correctness requirement.

### F-B — Deleting referenced evidence while its record is pending QUARANTINES it

`edgebacklog.Enqueue` `os.Stat`s the referenced file and requires an exact size
match (`backlog.go:353-362,394-406`). `send` re-validates before each stage
(`:488-494`) and maps any failure to `transport.ErrInvalidRequest`, which
`ProcessOne` turns into `quarantineLocked` (`:450-457`) — the remaining stages
are then never attempted.

So retention deleting a JPEG/MP4 that is still referenced by a **pending**
backlog record does not merely free disk: it **destroys that event's sync** and
quarantines it. Retention must not touch evidence referenced by any pending
record, and the only source of truth for that set is
`local-event-backlog/pending/*.json` (absolute evidence paths, `backlog.go:38-51`).

Related: `events/*.json` deletion under a pending record does not crash or stall
anything (`send` uploads from its own submission; only the terminal
`MarkSynced` callback fails and is logged) — it is a silent local/SaaS **desync**,
so it must be an explicit policy, never a side effect.

## 3. Invariants that must not be broken

1. Clips are named by **event** UUID; JPEGs by a **fresh capture** UUID
   (`clips.go:108`, `evidence.go:104`). Never swap them.
2. Every evidence write is temp+rename with SHA256 recorded; rewriting an
   existing UUID with different bytes returns `ErrEvidenceConflict` /
   `ErrClipConflict` and must **never overwrite** (`evidence.go:110-127`,
   `clips.go:143-157`). Retention must delete, never rewrite.
3. `edgebacklog` pending records are **durable**: capacity overflow returns
   `ErrFull` and recovery reports `OverCapacity` rather than trimming
   (`backlog.go:92-102,374-377,658`). Retention must not contradict that.
4. `edgebacklog` is strict FIFO on `queue[0]` and preserves sequence order
   across restart (`:278,410-415`).
5. `EventStore.backlogCount` is maintained by ±1 on transitions
   (`store.go:122-124,237-239`). Evicting event JSONs must keep it coherent.
6. UUIDs are validated against `eventUUIDPattern` before `filepath.Join`
   (`evidence.go:30,79`) and the backlog resolves+contains evidence paths
   (`fulledge_wiring.go:188-200`). Retention must preserve both.
7. Never delete outside the three owned trees below; never follow a symlink out
   of the data dir.

## 4. Ownership map

**Full Edge owns — the ONLY things retention may delete:**

* `<DataDir>/events/<event_uuid>.json` (`store.go:28-29,71`)
* `<DataDir>/evidence/captures/<capture_uuid>.jpg` (`evidence.go:57-58,99-105`)
* `<DataDir>/evidence/clips/<event_uuid>.mp4` (`clips.go:104-110`)
* stale temp orphans inside those trees: `events/.event-*.tmp`,
  `evidence/captures/.evidence-*.tmp`, `evidence/clips/*.mp4.tmp`

**Owned by others — must never be touched:** `identity.json`,
`credentials.json`, `camera_credentials.json`, `camera_master.key`,
`remote_config_state.json`, `control_ledger.json`, `local-event-backlog/`,
`cloud-buffer/`, `ota/`, `models/`, `run/`, and anything else at the data-dir
root. `factoryreset`'s `StatePaths` allowlist (`reset.go:17-28`) is the
authoritative enumeration of known state and confirms this split (it notably
omits `models/`, `run/`, `ota/`).

## 4b. Additional audit notes the implementer must know

* **The clip file has NO explicit permissions.** `clips.go:105` creates the dir
  `0o750`, but ffmpeg creates the file itself, so its mode is ffmpeg's default
  `& umask` — unlike the JPEG, which is explicitly `chmod 0o600`
  (`evidence.go:156`). Retention deletes clips; it must not assume `0o600`.
* **`EdgeMaxClipSizeBytes` cannot express "disabled" via env** — its parse
  requires `> 0` or `Load()` errors (`config.go:784-786`); only the zero-value
  field reaches `clips.go:115` where `0` means "no check". New retention knobs
  must NOT copy that: the brief requires `0 = disabled` to be settable from the
  environment, so they need `>= 0` with an explicit "zero disables" range check.
* **`CanWriteEvidence` has a latent nil-error path** — `if !ok || err != nil
  { return nil, err }` (`evidence.go:86-92`) returns `(nil, nil)` when
  `ok == false && err == nil`. Unreachable with the current implementation, but
  extending the gate to clips should switch on `ok` explicitly rather than
  copy the pattern.
* **`factoryreset.StatePaths` already lists `events` and `evidence`**
  (`reset.go:17-28`), which independently confirms they are Full Edge-owned
  state — and that `models/`, `run/` and `ota/` deliberately are not.
* **`edgebacklog`'s own comment reserves this work**: it states capture and clip
  bytes "remain in the evidence retention area" and that it deletes only
  quarantine metadata, never evidence (`backlog.go:1-3,325-326`). B3 is the
  other half of that split, not a competitor to it.

## 5. Design

> **Implemented in B3-A** for the events and captures trees:
> `internal/fulledge/retention.go` (`RetentionManager`), wired from
> `internal/agent/fulledge_module.go` through `fulledge.ServiceConfig.Retention`,
> with a startup sweep in `NewService` and a rate-limited `MaybeSweep` for the
> write path. The clip tree and the clip free-disk gate remain open (B3-B).

**One retention component, three owned trees, no new daemon.**

A `RetentionManager` owning a validated root per tree, with:

```go
func (r *RetentionManager) Sweep(now time.Time) (RetentionReport, error)
```

* **Deterministic, oldest-first.** Order by the file's own timestamp
  (event JSON: parsed `SourceTimestamp`/`SavedAt`; evidence: mtime), tie-broken
  by filename, so the result never depends on directory order.
* **Bounds, all `0 = disabled`** (no invented commercial default), per the
  house rule already used by `EdgeMaxClipSizeBytes`/`CloudBufferMaxAge`:
  count, bytes, and TTL per tree.
* **Refcounted capture eviction (F-A).** Build the referenced-capture set from
  `events/*.json` first; only delete a capture that no surviving event names.
* **Pending-record aware (F-B).** Build the pending-evidence set from
  `local-event-backlog/pending/*.json` and never delete a referenced file while
  its record is pending. Deleting a *pending event JSON* is a separate, explicit
  policy flag defaulting to **off**.
* **Path safety.** Every candidate must be a direct entry of the expected
  directory, with `Lstat` (no symlink following), a non-temp filename, and a
  resolved path still inside the tree root. A path that escapes is refused and
  reported, never deleted.
* **Crash-safe and idempotent.** Deleting is `os.Remove` of a single fully
  written file; a crash mid-sweep leaves a consistent store, and a re-run simply
  continues. Temp orphans are swept by a separate, explicitly-safe rule (older
  than a small grace period, so a concurrent write is never removed).
* **Concurrent-safe.** Sweep takes the store's own mutex, or a dedicated
  retention mutex, and never holds it across a caller's write.
* **Delete failures visible, never destructive.** A failed `os.Remove` is
  counted and reported and **stops that tree's eviction** rather than continuing
  to delete around it.
* **Sweep trigger.** No new goroutine: sweep once at construction (bounding any
  pre-existing growth) and then rate-limited after writes, so repeated writes do
  not become O(n²).

**Free-disk gate extended to clips (required).** `CanWriteEvidence` must be
consulted before a clip is encoded/written, matching the JPEG path. Note the
existing gate's deliberate semantics: `MinFreeDiskBytes == 0` disables it, and a
`FreeBytes` **error allows the write** (`limits.go:160-170`) — that behaviour is
preserved, not changed, and must be documented rather than quietly "fixed".

**Metrics.** A sanitized report (counts per tree: scanned, evicted, failed,
refused-unsafe; plus bytes reclaimed) exposed through `/status`. Counts and
paths-relative-to-owner only — never a raw absolute path.

## 6. Configuration

Explicit knobs, `0 = disabled`, following the existing scalar convention
(`strings.TrimSpace(os.Getenv(...))`, parse, range-check with an error naming
the raw value) and using one word per group like `GEOCAM_VIDEO_RINGBUFFER_SIZE`:

**Shipped in B3-A** (six knobs, all `>= 0` with `0 = disabled` — deliberately
unlike `GEOCAM_EDGE_MAX_CLIP_SIZE_BYTES`, whose parse rejects 0 and so cannot
express "disabled" from the environment):

```
GEOCAM_EDGE_RETENTION_MAX_EVENTS            int64,    0 = disabled
GEOCAM_EDGE_RETENTION_MAX_EVENT_BYTES       int64,    0 = disabled
GEOCAM_EDGE_RETENTION_MAX_EVENT_AGE         duration, 0 = disabled
GEOCAM_EDGE_RETENTION_MAX_CAPTURES          int64,    0 = disabled
GEOCAM_EDGE_RETENTION_MAX_CAPTURE_BYTES     int64,    0 = disabled
GEOCAM_EDGE_RETENTION_MAX_CAPTURE_AGE       duration, 0 = disabled
```

**Deliberately NOT shipped: an "evict pending" knob.** The design sketch offered
one; the B3-A brief requires that metadata still pending sync is *never* evicted,
so this is a safety rule rather than a configurable policy. The clip knobs are
B3-B.

A separately configurable sweep interval was also not needed: the manager
exposes `MaybeSweep(now, minInterval)` so the caller controls cadence, and the
sweep trigger is a startup sweep plus a rate-limited call on the write path —
no new goroutine.

**No `7 days`, no `30 days`, no GB figure, no per-tenant quota, no SLA is
invented anywhere.** Every bound ships disabled until an operator sets it.

## 7. Test plan (the brief's required cases)

| # | Case | Approach |
| --- | --- | --- |
| 1 | count bound | N files, bound < N, assert exact survivors |
| 2 | byte bound | sized files, assert oldest-first removal to fit |
| 3 | TTL bound | backdated mtimes/timestamps, assert age eviction |
| 4 | oldest-first | interleaved creation order vs eviction order |
| 5 | JPEG eviction | capture older than survivors |
| 6 | MP4 eviction | clip tree independent of capture tree |
| 7 | event metadata eviction | with `backlogCount` staying coherent |
| 8 | disk-full / min-free-disk | `mockDiskChecker` seam (`fulledge_test.go:47`); JPEG **and** MP4 |
| 9 | no path traversal / symlink escape | symlink pointing outside; assert refused+reported, target intact |
| 10 | concurrent create+evict | `-race`, writers and sweeper in parallel |
| 11 | restart/reopen accounting | reopen on same dir, assert deterministic result |
| 12 | delete failure does not corrupt | inject a failing remove; assert store consistent and eviction stops |
| 13 | no unbounded growth | repeated writes with bounds set, assert size plateaus |
| 14 | shared capture not evicted while referenced (F-A) | one JPEG + 3 event JSONs; assert kept |
| 15 | pending evidence not evicted (F-B) | pending backlog record; assert its JPEG/MP4 kept |

Use real `t.TempDir()` filesystems; no physical disk test needed.
Seams to reuse: `mockDiskChecker`/`mockMemoryChecker`
(`fulledge_test.go:47,57`), the `fsFault` remove-injection pattern
(`edgebacklog/diskfull_test.go:30`), `fixedHistory` (`evidence/clips_test.go:15`).

## 8. Implementation order

1. `internal/config`: the knobs above, defaults disabled, with tests.
2. `internal/fulledge`: `RetentionManager` (count/bytes/TTL, oldest-first,
   refcounted, path-safe) + temp-orphan sweep + sanitized report.
3. `internal/evidence`: retention support for the **clip** tree (it owns that
   directory) — or an explicitly shared helper, without inventing a new package.
4. Wire the free-disk gate into the clip path.
5. Wire sweep-on-construction and rate-limited sweep-on-write in the agent.
6. Metrics on `/status`.
7. Tests 1–15, then docs.

## 9. Docs to update, and what must not be claimed

Update only B3/retention statements on this branch (the fulledge env example,
`docs/deployment/appliance.md`, `docs/product/COMMERCIAL_MODES.md`). Remove the
"no retention/eviction exists" claim once it is false, and keep explicitly
**NOT_VALIDATED**: real hardware, physical pilot, real PyTorch inference, CUDA.

**Not claimed:** commercial-ready, Software 1.0 READY, any commercial retention
period, any validated capacity figure.
