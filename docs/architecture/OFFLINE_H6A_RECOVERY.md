# Offline H6A — Crash Recovery & Atomic Activation

## Scope and status

Milestone H6A implements the transactional core, persistent write-ahead journal (`recovery.journal`), staging isolation, dual-layer concurrency locking, directory confinement, atomic activation, and idempotent crash reconciliation for GEO CAM Edge runtime directories.

H6A does **not** declare H6 complete, `RUNTIME_READY`, or `INFERENCE_READY`. It does not bypass model redistribution constraints (`MODEL_REDISTRIBUTION_APPROVED=false`). It provides the crash-safe foundation that allows Python runtimes, wheelhouse packages, models, and workers to be installed, upgraded, or rolled back without leaving the edge node in an unbootable or corrupted state.

---

## POSIX Directory Swap Semantics & The Crash Window

In POSIX file systems (macOS APFS, Linux ext4), `rename(2)` / `os.Rename` cannot overwrite a non-empty directory (`ENOTEMPTY` / `EEXIST`). Consequently, upgrading a directory containing executable runtimes or libraries requires a multi-step sequence:
1. `active` directory is renamed to `backup` (`.backup.<target>.<txid>`).
2. `staging` directory (`.staging.<target>.<txid>`) is renamed to `active`.
3. Post-activation verification executes smoke tests against `active`.
4. `backup` directory is pruned.

### The Crash Window Problem
Between step 1 and step 2, the `active` directory does not exist on disk. If the process receives a `SIGKILL`, the machine loses power, or an unexpected OS reboot occurs during this window, a subsequent process start would find `active` missing and fail.

Without persistent state, a recovering process cannot distinguish between:
- A clean first-time installation that was interrupted before completion.
- An interrupted upgrade where the previous runtime is in a backup directory.
- A failed rollback where both directories contain divergent files.

### The Write-Ahead Journal Solution
`RecoveryManager` solves this problem by maintaining a write-ahead journal (`recovery.journal`) on disk. Every state transition is written and synchronized (`fsync`) before executing filesystem renames. On startup, `RecoveryManager.Reconcile()` reads the journal, validates cryptographic integrity, inspects filesystem evidence, and idempotently rolls back or commits the pending transaction.

---

## Transaction Lifecycle & State Machine

The transaction lifecycle is modeled by an explicit 11-state machine:

| State | Description | Recovery Behavior on Crash |
| --- | --- | --- |
| `IDLE` | No active transaction; system is quiescent. | No-op. Remove stale staging dirs if found. |
| `PREPARING` | Staging directory created; files being unpacked/prepared. | Remove staging directory; revert to `IDLE`. `active` was untouched. |
| `STAGED` | Staging verified and ready for activation swap. | Remove staging directory; revert to `IDLE`. `active` was untouched. |
| `BACKING_UP` | Initiating rename from `active` to `backup`. | If `backup` exists and `active` is missing, restore `backup` → `active`. Revert to `ROLLED_BACK`. |
| `BACKED_UP` | `active` moved to `backup`; `active` currently missing on disk. | Restore `backup` → `active`. Clean staging. Revert to `ROLLED_BACK`. |
| `ACTIVATING` | Initiating rename from `staging` to `active`. | Staging was uncommitted. Restore `backup` → `active`. Revert to `ROLLED_BACK`. |
| `ACTIVATED` | `staging` moved to `active`; awaiting post-activation checks. | Verification unacknowledged. Restore `backup` → `active`. Revert to `ROLLED_BACK`. |
| `ROLLING_BACK` | Rollback in progress due to test failure or error. | Complete restoring `backup` → `active`. Clean staging. Advance to `ROLLED_BACK`. |
| `ROLLED_BACK` | Rollback completed cleanly; previous active restored. | Advance to `IDLE`. Clean leftover staging. |
| `COMMITTED` | Activation verified successfully; backup pruned. | Advance to `IDLE`. Clean leftover staging. |
| `FAILED` | Rollback failed (e.g. disk I/O error). Manual recovery needed. | **Fail-closed.** Preserves all backups. Emits operator manual command. |

---

## Durability & Fsync Guarantees

File and directory metadata durability is guaranteed via strict sync boundaries:

1. **Atomic Journal Updates**:
   - The updated journal record is serialized to JSON and formatted.
   - A SHA-256 checksum over all record payload fields is computed and embedded in the record.
   - The record is written to a temporary file (`recovery.journal.tmp.<pid>`).
   - `f.Sync()` is called to flush dirty buffers to physical storage.
   - The file is atomically renamed to `recovery.journal`.
   - `syncDir(baseDir)` is called to flush parent directory metadata to storage.

2. **Directory Sync (`syncDir`)**:
   - Renaming directories in POSIX updates directory entry catalogs in the parent directory.
   - `syncDir()` opens the parent directory with `os.Open` and issues `d.Sync()`. On Windows, where directory syncing is a no-op, this safely returns `nil`.

---

## Backup Preservation Invariant

The core safety rule of `RecoveryManager` is: **Never delete a backup under ambiguous or corrupt state.**

1. **Post-Activation Verification Requirement**:
   A backup directory is pruned **only** after:
   - Staging is renamed to `active`.
   - All post-activation verifications and smoke tests pass.
   - The journal record has transitioned to `COMMITTED`.

2. **Ambiguity Handling (`RECOVERY_AMBIGUOUS_BACKUP`)**:
   If both `active` and `backup` exist with non-empty content and the journal is missing or ambiguous, `Reconcile()` refuses to overwrite or delete either. It returns `ErrRecoveryAmbiguous` and leaves all files intact for operator diagnosis.

3. **Failed Rollback Handling (`RECOVERY_ROLLBACK_FAILED`)**:
   If an error occurs while restoring `backup` → `active` during rollback:
   - The transaction state transitions to `FAILED`.
   - The backup is strictly preserved.
   - The system returns a structured `SafeError` with a non-destructive manual recovery instruction:
     `mv <backup> <active>`

4. **Corrupted Journal Handling (`RECOVERY_JOURNAL_CORRUPT`)**:
   If `recovery.journal` is empty, truncated, or fails SHA-256 record verification, `Reconcile()` fails closed immediately without modifying the filesystem.

---

## Directory Confinement & Symlink Security

`RecoveryManager` validates all path targets via `validateConfinement`:
- Targets are resolved against the canonicalized `baseDir` via `filepath.EvalSymlinks`.
- Any relative escape (`..`) is rejected.
- Any symlink pointing outside `baseDir` (either at the leaf or in any parent segment) is rejected with `ErrSecurityEscapingSymlink`.
- Persisted journal paths are revalidated before cleanup or rename. A malformed or truncated journal does not authorize deleting staging or backup data; journal entries that are not regular files are rejected.

---

## Dual-Layer Concurrency Locking

To prevent race conditions and concurrent installer/recovery execution:
1. **In-Process Layer**: An in-memory mutex (`sync.Mutex`) is indexed by canonicalized `baseDir`. Multiple goroutines attempting concurrent recovery within the same process are serialized or rejected.
2. **Cross-Process Layer**: Advisory file locking via `fileLock` (`flock` on Unix, `LockFileEx` on Windows) is acquired on `.recovery.lock`. If another installer or background updater holds the lock, `AcquireLock` immediately fails with `ErrCodeRecoveryLockHeld`.

`Begin`, `CommitStaged`, and `Reconcile` require the caller to hold this lock; they fail closed otherwise. `ReleaseLock` is idempotent and never unlocks a lock owned by another manager. `CommitStaged` requires a non-nil verifier and returns directory-sync errors instead of advancing the journal as though the filesystem operation were durable.

For an interrupted first install (`ACTIVATING`/`ACTIVATED` with no backup), there is no previous runtime to restore and the post-activation verifier may not have completed. Reconciliation preserves the active files and journal and returns a recovery-ambiguous error; it does not mark the runtime committed or ready. The caller must resolve this state explicitly.

Journal records are validated semantically as well as by checksum: target names must be single safe path components; active, staging, and backup paths must match the transaction naming contract; and resolved paths must remain under the canonical recovery root.

---

## Crash Recovery & Reconciliation Matrix

| Disk State at Startup | Journal State | Target Directory Action | Resulting State |
| --- | --- | --- | --- |
| `active` ok, no backups | `IDLE` / `COMMITTED` | None | `IDLE` |
| `active` ok, staging exists | `PREPARING` / `STAGED` | Remove `staging` | `IDLE` |
| `active` missing, backup exists | `BACKING_UP` / `BACKED_UP` | `os.Rename(backup, active)` | `ROLLED_BACK` |
| `active` unverified, backup exists | `ACTIVATING` / `ACTIVATED` | Remove `active`, `os.Rename(backup, active)` | `ROLLED_BACK` |
| `active` missing, backup exists | `ROLLING_BACK` | `os.Rename(backup, active)` | `ROLLED_BACK` |
| `active` ok, backup exists | Corrupted / No journal | Preserves both; returns `RECOVERY_AMBIGUOUS_BACKUP` | Error (Safe) |
| Any | `FAILED` | Preserves all; instructions logged | Error (Safe) |

H6A tests include a child process forcibly terminated after activation rename and a separate child process holding the advisory lock, in addition to injected state/rename/fsync failures. These are synthetic filesystem tests; they do not invoke the active Full Edge runtime or certify readiness.
