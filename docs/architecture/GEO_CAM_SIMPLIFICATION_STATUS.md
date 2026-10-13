# GEO CAM Simplification & Hardening Status

**Tracking Document**
Last updated: 2026-10-13

## Current Phase
Phase 2: SaaS Transactional Reconciler & Atomic Onboarding

## Completed
- Baseline verification: Verified 2 physical cameras online on Edge daemon (`discovered_device_count = 2`, `expected_camera_count = 2`, `active_pipeline_camera_count = 2`).
- Production audit: Confirmed no production mutation is permitted without explicit authorization.
- Git cleanliness: Synchronized both `monitoreoedgeis` and `monitoreoia` with upstream `main` (`installer-v1.1.6` on Edge, `9ef21a0` on SaaS).
- Git identity hardening: Fixed repo-local identity in `monitoreoedgeis` to `drko-dev` <gustavitomarcon@gmail.com> (eliminated SIDOM author leakage).
- Verified real-world corruption fixture: Confirmed live `/Users/gustavomarcon/Library/Application Support/geocam-edge/data/models/yolo11s-pose.pt` is 20 bytes (truncated pickle stub), directly matching test scenario 34.
- Architecture docs created:
  - `docs/architecture/ZERO_TOUCH_EDGE_CAMERA_ONBOARDING.md` (SaaS)
  - `docs/architecture/FULL_EDGE_SELF_CONTAINED.md` (Edge)
- Camera Wall layout and refresh persistence completed and tested:
  - Added scoped `localStorage` persistence in `geocam/static/js/wall.js` for layout (`geocam_wall_layout`) and refresh (`geocam_wall_refresh`).
  - Added user isolation support (`geocam_wall_layout_<username>`).
  - Added 6 DOM unit tests in `geocam/tests/test_wall_preferences_js_dom.cjs` (all PASS).
  - Branch: `fix/wall-layout-and-refresh-persistence` in `monitoreoia`.

## In Progress
- Finalizing architecture documents:
  - `docs/architecture/ZERO_TOUCH_EDGE_CAMERA_ONBOARDING.md`
  - `docs/architecture/FULL_EDGE_SELF_CONTAINED.md`
- Completing Part 8: Adversarial self-review against the 48-scenario test matrix.

## Next
- PR 1 (SaaS): Atomic ACID camera onboarding & candidate reconciliation (`reconcile_discovered_camera`) with tenant isolation.
- PR 2 (SaaS): Camera wall layout (`pageSize`) and refresh rate (`intervalMs`) persistence via scoped `localStorage`.
- PR 3 (SaaS): Live UI updates without manual F5 (adaptive polling / refetch).
- PR 4 (Edge): Model manifest (`models-manifest.json`), SHA-256 integrity validator, and atomic self-healing from bundle.
- PR 5 (Edge): Automated discovery reporting to SaaS with idempotent reconciliation.
- PR 6 (Edge): Release gating (`MODEL_REDISTRIBUTION_APPROVED`) for model weights redistribution.

## Architectural Decisions
1. **Camera Identity**: StableIdentity remains canonical (`epr:<uuid>` for ONVIF WS-Discovery, `endpoint:<host>:<port><path>` fallback, `channel_number` for multi-channel). IP addresses are transient transport hints, never primary identity.
2. **ACID Onboarding in SaaS**: Consolidate camera creation, edge binding, credential creation/rotation, and credential assignment into a single PostgreSQL transaction (`BEGIN ... COMMIT`) with complete rollback on any error.
3. **Camera Wall Storage Scope**: No personal user preference table exists in DB. Scoped `localStorage` (`geocam_wall_layout_${userId}`, `geocam_wall_refresh_${userId}`) preserves visual layout without requiring schema migration for visual-only client preferences.
4. **Realtime UI Strategy**: Controlled polling / targeted refetch with ETags / JSON status diffing. Simple, robust, self-healing, zero WebSocket infrastructure overhead.
5. **Model Integrity & Self-Heal**: Models are verified by regular file check, expected size threshold, pinned SHA-256 hash, and torch unpickle test. If local is corrupt and bundled is valid, atomic replacement (`temp -> check -> fsync -> rename`).
6. **Release Model Licensing Gate**: `MODEL_REDISTRIBUTION_APPROVED` environment flag controls whether weights are bundled in public release builds. If false, bundle stubs/manifest only, preventing legal exposure.

## Blockers
- None currently blocking implementation.
- Model redistribution license review remains pending for public commercial releases (`MODEL_REDISTRIBUTION_APPROVED=false` default).

## PRs
- Planned:
  - SaaS PR 1: `feat(onboarding): transactional camera reconciliation and tenant enforcement`
  - SaaS PR 2: `fix(wall): persist layout and refresh interval across reloads`
  - SaaS PR 3: `feat(ui): live status updates without manual reload`
  - Edge PR 4: `feat(vision): model manifest verification and atomic self-heal`
  - Edge PR 5: `feat(discovery): automated candidate reporting and reconciliation`
  - Edge PR 6: `ci(release): enforce MODEL_REDISTRIBUTION_APPROVED license gate`

## Tests
- Baseline Edge health status: PASS (2 cameras streaming).
- Unit tests: To be executed per PR.
