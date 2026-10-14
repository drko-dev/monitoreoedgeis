# H6C — Wails bridge fail-closed hardening (partial)

**Status:** IMPLEMENTED / VALIDATED LOCAL (synthetic UI bridge tests); release provisioning **NOT ENABLED**, `RUNTIME_READY=false`. Tracks issue #155 (already approved for development); base includes H7B PR #163 merged into `main` at `3ab57fc08456dd2a2eaa4a4ea2903be8578d660c`.

## Preexisting wiring (not reinvented here)

- H5B `RuntimePackPreparationAdapter` and H6B `TransactionalRuntimePackProvisioner` are implemented and merged.
- H5C `NewAppWithPinnedRuntimePack(options)` already provides an **explicit release-owned** constructor; H5D restores from H6A/H6B transactional evidence after startup.
- Shipping `cmd/geocam-edge-ui/main.go` calls `NewApp()`. It deliberately uses `UnavailableFullEdgePreparation`. The product remains incapable of activating an untrusted local lab pack from its normal UI.
- A manifest SHA-256 provided by the **same untrusted pack** is not a trusted external pin. Nothing in this slice fabricates a trust root or model redistribution approval.

## New H6C safeguards

1. The Wails `validatedPreparationSnapshot` boundary now validates the snapshot **whether or not the backend also returns an error**. Previously, on `err != nil`, it returned potentially malformed or falsely `READY` backend data to the frontend without validation.
2. Invalid snapshots are replaced with a safe `FAILED / INVALID_PREPARATION_STATUS` result and a fresh set of pending components. Even a completely shaped `READY`/certification snapshot is replaced by `FAILED / PREPARATION_BACKEND_ERROR` if its backend operation failed. The original backend error remains returned, never silently converted to success.
3. Valid `RECOVERY_REQUIRED` snapshots are preserved with their backend error for diagnosis, but never reported as `READY`. Cancellation remains unsupported during filesystem activation rather than acknowledged without proof.
4. Explicit H5C startup recovery now uses a cooperative 15-second context deadline for lock acquisition and cancellable work. This is **not a hard wall-clock guarantee** against non-cancellable filesystem calls. A timeout or any recovery error leaves an uncertified snapshot and never implies successful recovery.
5. Synthetic Wails tests cover all four bridge operations (`Status`, `Start`, `Cancel`, `Recover`) with adversarial invalid status, fake ready with backend error, valid-shaped certification despite backend error, and recovery-required with error. The default shipping `NewApp()` is tested to remain fail-closed.

## Tests / limitations

- `go test -count=1 ./cmd/geocam-edge-ui ./internal/installer` — PASS locally.
- `go test -race -count=1 ./cmd/geocam-edge-ui ./internal/installer` — run and report result before publication.
- `go vet ./cmd/geocam-edge-ui ./internal/installer` — run and report result before publication.
- No real Runtime Pack activation, filesystem journal recovery against a customer runtime, model inference, Wails package installation, network change, deployment or release performed.

**Remaining hard gates:** release-owned signed/provenanced pack and independently authenticated manifest pin; model rights and all dependency license clearance; H4/H6 binary integrity and TOCTOU review; H7 full offline certification (including localhost relay) and H8 clean-machine/physical hardware; explicit product/bootstrap opt-in only after all gates, plus independent PR-specific merge approval.

This is not an H5/H6 whole milestone completion. `MODEL_REDISTRIBUTION_APPROVED=false`.
