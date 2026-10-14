# Offline H6A — Transactional Runtime Recovery

## Scope and status

H6A provides a recovery primitive for runtime directory activation. It is not connected to active provisioning and does not certify `PREPARATION_READY`, `RUNTIME_READY`, or `INFERENCE_READY`. `MODEL_REDISTRIBUTION_APPROVED=false` remains unchanged.

The manager uses a persistent journal, isolated staging, versioned backup, transaction-bound tree digests, directory confinement, a dual-layer lock, and idempotent reconciliation. The caller must hold the recovery lock for `Begin`, `CommitStaged`, and `Reconcile`.

## Integrity evidence and trust boundary

- `ExpectedHash` is caller-supplied release metadata. It is **not** accepted as proof that a runtime tree is intact.
- Before activation, H6A calculates a deterministic SHA-256 over relative paths, file types, permission bits, sizes, and file contents. It rejects symlinks and special files. `StagingHash`, `BackupHash`, and `ActiveHash` are written into the transaction journal and compared on restore/reconcile.
- A rollback requires `HadActive=true`, a transaction-bound `BackupHash`, and a confined real backup directory whose tree digest matches. Missing, malformed, or altered backup evidence fails closed without deleting active, backup, staging, or quarantine data.
- The journal checksum detects accidental truncation/changes. It is unkeyed and does not authenticate a journal against a malicious local administrator. The tree digest likewise proves consistency with the journal, not provenance or code safety; release signature/manifest validation belongs to the caller and later integration.
- `COMMITTED` and `IDLE` are transaction states, not readiness claims. When active integrity evidence exists, reconciliation verifies it. `COMMITTED` without valid evidence, or a missing/altered active tree, returns a non-certified recovery error and preserves backups. A first install interrupted before verification has no prior runtime and is not reported as a successful rollback.

## Write and rename ordering

For a successful activation, the intended order is:

1. `Begin` verifies and hashes an existing active tree (if any), creates staging, then writes the `PREPARING` journal. Journal replacement uses a temporary regular file, file `Sync`, atomic rename, and parent-directory `Sync`.
2. The caller populates staging. Before activation, `CommitStaged` runs the caller verifier, hashes and syncs each regular staged payload file, then syncs staged directories bottom-up. Symlinks and special files are rejected.
3. The `STAGED` journal records the computed staging digest. For an upgrade, H6A re-hashes active against the baseline captured by `Begin`, writes `BACKING_UP` with the required backup path/hash, renames active to backup, syncs the parent, then writes `BACKED_UP`.
4. H6A writes `ACTIVATING`, renames staging to active, syncs the parent, and writes `ACTIVATED`. These are **two separate renames**, not one atomic directory swap; the journal makes the gap recoverable.
5. The caller verifier runs again. H6A re-hashes/syncs active and requires it to equal the staged digest. Only then does it write `COMMITTED`. Backup pruning occurs only after this verified commit; an uncertain or failed recovery never prunes a backup.
6. The final `IDLE` journal is written only after cleanup. Failed journal/file/directory syncs return errors; they do not count as a successful commit.

Rollback verifies the backup *before* changing active. An unverified active tree is renamed to a transaction-specific quarantine path (never `RemoveAll`'d), the parent directory is synced, the verified backup is renamed into active, the parent is synced again, and the restored tree is verified before `ROLLED_BACK` is persisted. If any step fails, the journal stays recoverable where possible and the artifacts are retained for retry/diagnosis.

## Filesystem guarantees and limits

A same-filesystem POSIX rename is atomic for one directory entry. The active-to-backup and staging-to-active renames are not jointly atomic; crash consistency comes from the write-ahead state and recovery rules. File `Sync` flushes file data/metadata as provided by the OS; directory `Sync` requests persistence of entry changes. The code uses Go `File.Sync` and does not issue macOS `F_FULLFSYNC`. APFS, device caches, virtualized disks, and hardware may weaken guarantees under sudden power loss. Therefore H6A does not claim an absolute guarantee against every power failure. Windows directory sync is currently a no-op. Physical power-loss qualification is outside this synthetic test suite.

The manager serializes cooperating processes. It is not a defense against an uncooperative privileged process mutating the same tree concurrently; tree reads perform lstat/open identity checks, but H6A is not implemented with descriptor-relative `openat` traversal. The trusted boundary is the locked, product-owned runtime directory.

## Reconciliation behavior

| Journal and disk evidence | Action | Result |
|---|---|---|
| `PREPARING`/`STAGED`, previous runtime digest matches | Remove only confined staging; retain/record verified active digest | `IDLE` |
| `BACKING_UP`, active and backup both exist | Preserve both; require diagnosis | Error / recovery required |
| `BACKING_UP`/`BACKED_UP`/`ACTIVATING`/`ACTIVATED`/`ROLLING_BACK`, backup exists and matches `BackupHash` | Quarantine active if present; restore and verify backup; sync parent after each rename | `ROLLED_BACK` |
| Rollback state, backup missing but active matches the transaction's prior digest | Treat as an already-completed restore; persist `ROLLED_BACK` | Idempotent success |
| Rollback state, required backup missing/corrupt/unreadable, or active does not match prior digest | Preserve journal and all available artifacts | Error / recovery required |
| First install interrupted during activation; no previous runtime | Preserve active/staging/journal; do not claim rollback or readiness | Error / operator recovery |
| `ROLLED_BACK`, active equals prior digest | Verify before advancing to `IDLE`; preserve quarantine | `IDLE` |
| `COMMITTED`/`IDLE`, active digest matches journal | Verify active; do not remove a backup merely due to state | Transaction reconciled; readiness still separate |
| `COMMITTED`/`IDLE`, active missing/altered or evidence absent | Preserve artifacts; return non-certified error | Error / recovery required |
| No journal, unjournaled backup exists | Do not infer trust from its filename; preserve backup and active | Error / operator review |
| Invalid journal, unsafe path, symlink/special runtime entry, or unsupported state | Do not mutate referenced artifacts | Error / recovery required |

## Test coverage

Tests use temporary directories and small synthetic trees. They cover normal first install/upgrade, process termination during activation, missing/corrupt backups, active+backup ambiguity, quarantine before restoration, interrupted restore and retry, repeated reconciliation, altered finalized runtime, path/journal symlinks, malformed journal, lock contention, and payload/directory sync failures.

They do not certify macOS/APFS behavior under physical power loss, protection from a privileged non-cooperating writer, release authenticity, runtime compatibility, or operational readiness. H6A remains disconnected from the installer/runtime; integration and readiness certification require later milestones.
