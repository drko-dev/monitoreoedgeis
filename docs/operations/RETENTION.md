# Retention — Operations Reference

> Operational quick-reference for running retention in production. For the
> full design, invariants and edge-case analysis (refcounting, ownership map,
> the F-A/F-B problems), see `docs/product/B3_RETENTION_DESIGN.md` — this
> document does not duplicate that content, only points at it and states the
> configuration surface an operator actually touches.

**Verified against:** `main` @ `6617322549e4d9ac815317a0724b92d3e4613045`
(`internal/fulledge/retention.go`, `internal/config/config.go`).

## What retention manages

Three artifact types, each independently bounded:

| Artifact | Path | Bounds |
|---|---|---|
| Event metadata (JSON) | `{GEOCAM_DATA_DIR}/events/<uuid>.json` | count / bytes / age |
| Evidence captures (JPEG) | `{GEOCAM_DATA_DIR}/evidence/captures/<uuid>.jpg` | count / bytes / age |
| Evidence clips (MP4) | `{GEOCAM_DATA_DIR}/evidence/clips/<event_uuid>.mp4` | count / bytes / age |

Eviction is **oldest-first**, by timestamp (source/saved-at for JSON, mtime
for files), with filename as tie-break. **`0` means disabled**, not
"unlimited by a hidden default" — every bound is independently configurable
and nothing is enabled unless you set it.

## Environment variables

```
GEOCAM_EDGE_RETENTION_MAX_EVENTS
GEOCAM_EDGE_RETENTION_MAX_EVENT_BYTES
GEOCAM_EDGE_RETENTION_MAX_EVENT_AGE
GEOCAM_EDGE_RETENTION_MAX_CAPTURES
GEOCAM_EDGE_RETENTION_MAX_CAPTURE_BYTES
GEOCAM_EDGE_RETENTION_MAX_CAPTURE_AGE
GEOCAM_EDGE_RETENTION_MAX_CLIPS
GEOCAM_EDGE_RETENTION_MAX_CLIP_BYTES
GEOCAM_EDGE_RETENTION_MAX_CLIP_AGE
GEOCAM_EDGE_RETENTION_SWEEP_INTERVAL
GEOCAM_EDGE_RETENTION_EVICT_PENDING
```

## `GEOCAM_EDGE_RETENTION_EVICT_PENDING` — exact semantics

**This is the one operators most often get wrong, so it is stated precisely
here and confirmed directly against the source, not against a prior
document's claim:**

- **Default: `false`** (`internal/config/config.go:1064-1065`).
- When `true`: retention is allowed to delete the **event JSON** of a record
  that is still pending (not yet synced to SaaS), once that record is
  otherwise eligible by its own bounds
  (`internal/fulledge/retention.go:41-45,358` — the `protected` check this
  flag controls only guards the event-JSON eviction loop).
- **It never affects evidence.** A JPEG or MP4 referenced by a pending record
  stays protected from eviction regardless of this flag's value — that
  protection comes from a separate mechanism (surviving events' capture
  references), not from `EvictPending`. Setting this to `true` does **not**
  let retention delete pending JPEGs/MP4s.
- Turning it on is a recovery/cleanup knob (drop stale, never-synced event
  metadata), not a way to reclaim evidence storage.

If you need to reclaim evidence storage, tighten the capture/clip bounds
instead — the pending-protection behavior is unaffected by this flag.

## Disk gate

Full Edge checks free disk before writing new evidence
(`GEOCAM_EDGE_MIN_FREE_DISK_BYTES`); see
`docs/product/B3_RETENTION_DESIGN.md` for the exact gating behavior and what
happens when the disk is full versus merely low.

## Ownership — what retention may and may not touch

**May delete** (once eligible by bounds): `events/*.json`,
`evidence/captures/*.jpg`, `evidence/clips/*.mp4`, and stale temp files
(`.event-*.tmp`, `.evidence-*.tmp`, `*.mp4.tmp`).

**Must never delete**: `identity.json`, `credentials.json`,
`camera_credentials.json`, `remote_config_state.json`, `control_ledger.json`,
`local-event-backlog/`, `cloud-buffer/`, `ota/`, `models/`, `run/`. See
`docs/product/B3_RETENTION_DESIGN.md §4` for the authoritative list.

## Diagnosing retention behavior

```bash
curl -s http://127.0.0.1:8091/status   # look for backlog/queue/disk fields
df -h $GEOCAM_DATA_DIR                 # confirm actual free space
```

If evidence directories keep growing unbounded, check that you actually set
non-zero bounds — the defaults are all `0` (disabled).

---

See also: `docs/product/B3_RETENTION_DESIGN.md` (full design),
`docs/architecture/EDGE_ARCHITECTURE.md` §13–15,
`docs/operations/OFFLINE_AND_RECOVERY.md` (interaction with the durable
backlog), `docs/README.md`.
