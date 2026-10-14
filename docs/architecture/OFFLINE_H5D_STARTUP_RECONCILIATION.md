# H5D — Startup reconciliation from persistent H6A/H6B evidence

Status: PARTIAL. Tracks issue #155.

In the explicit release-owned H5C constructor, after validating that the caller enabled provisioning and provided a SHA-256 manifest pin, H5B invokes `RestoreFromTransaction` once. It calls H6B `Recover` under H6A's cross-process lock, which may reconcile an interrupted transaction. It then maps the verified transactional result into an H5A snapshot.

The in-memory default `NOT_PREPARED` is not used to represent an installed runtime at startup. Missing, corrupted or unverified active packs remain `FAILED`; unpinned rollbacks stay `RECOVERY_REQUIRED`; pinned, integrity-verified active packs remain `FAILED / RUNTIME_UNCERTIFIED` pending functional H7/H8 certification. No state is promoted to `READY`, and no request ID is reconstructed without durable request evidence.

**Security boundary:** this reconciliation may mutate H6A's journal/backup/activation paths. It runs only through the explicit opt-in H5C constructor, not `NewApp()`. The normal distributed desktop bootstrap remains `UnavailableFullEdgePreparation`. The bootstrap does not download a pack, run Python, start FFmpeg, models, Worker or inference. Errors are represented by the safe startup snapshot and diagnostics; they must not be swallowed as readiness.

Tests exercise repeat construction and recovery from deterministic mocked transaction evidence, rollback ambiguity, corrupt recovery, and Wails startup with missing active pack. They do not replace physical crash/restart verification against a real authorized pack.

Still pending: durable request-id deduplication across restarts, release provenance/pin authority (#160), full UI bootstrap selection, legal approval of binaries/models, H7B and H8.
