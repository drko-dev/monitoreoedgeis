# Offline H6B — Transactional Runtime Pack Integration

## Status and scope

H6B is **PARTIAL**. This change adds an opt-in, synthetic-testable bridge from
the existing Runtime Manifest and H4A pack contracts to the H6A
`RecoveryManager`. It does not wire the desktop `FullEdgePreparationAPI`,
launch the Vision Worker, install dependencies, run FFmpeg, load models, or
certify `RUNTIME_READY` / `INFERENCE_READY`.

`MODEL_REDISTRIBUTION_APPROVED=false` remains a hard release gate. Model files
and unmanifested artifacts are rejected. No model weights or real runtime
artifacts are included.

## Main-branch dependencies found

- **H4A is merged:** `RuntimeManifest`, `ResolveRuntimePackMedia`, and the
  non-executing `BuildRuntimePackWorkerLaunchSpec` provide manifest, platform,
  executable, and no-host-fallback contract checks.
- **H5A is merged:** `FullEdgePreparationAPI` is a UI boundary, but the
  production-safe adapter remains `UnavailableFullEdgePreparation`; its
  `Start`/`Recover` return `PREPARATION_NOT_INTEGRATED`.
- **H6A is merged:** `RecoveryManager` owns the journal, inter-process lock,
  confined staging, activation, rollback, and idempotent reconciliation.
- **H4B is not integrated in this branch's `main`:** the H4A document explicitly
  says media artifacts are not bundled and the Worker contract is inert. There
  is no hermetic launch/runtime dependency to call here. A prior H4B worktree
  is not treated as an integrated dependency.
- **H7 certification is pending:** integrity and activation do not prove imports,
  Worker readiness, model authorization, or inference.

## H6B integration seam

`TransactionalRuntimePackProvisioner` is an internal backend seam, not yet
registered with Wails/UI. `Enabled` must be explicitly true; disabled mode
returns `RUNTIME_PACK_PROVISIONING_DISABLED` before creating or changing any
filesystem path. Product wiring remains disabled until H4B and the actual
release-pack build pipeline are integrated.

Before acquiring the H6A transaction lock, preflight requires:

1. A real source directory and persistent data path that do not overlap.
2. A 64-character manifest digest pinned by the embedding release configuration.
   The embedded H1 template is never accepted as a release.
3. Exact manifest digest match, `manifest_type: release`, schema and platform
   compatibility, artifact size/hash validation, bundled executable checks,
   and the H4A worker launch-contract checks (without execution).
4. No symlinks/special files and no artifacts not declared by the manifest.
   Artifact names under `models/` or model-weight extensions are rejected.

The manifest pin is the trust anchor for the current pack. H6A's `ExpectedHash`
field is metadata only; this integration does not use it as integrity proof.
H6A's journaled tree digest is used for crash consistency and rollback, not
release provenance. A future signed release manifest may strengthen supply-chain
authenticity; neither the journal nor a self-declared manifest hash is a digital
signature.

After preflight, the provisioner acquires the H6A lock, calls `Reconcile`,
returns a no-op for the exact already-active pinned pack, or calls `Begin` with
the fixed target and runtime version. It copies only the manifest and declared
artifacts into H6A staging. `CommitStaged` rechecks the pinned manifest and all
artifact/platform/media/launch-contract validations both before and after
activation. Errors leave recovery evidence in place; the next `Prepare` or
explicit `Recover` reconciles before any new activation.

The copy uses regular-file checks, source descriptor identity checks, exclusive
destination creation, file sync, and H6A's staged-tree sync/hash and atomic
directory-entry renames. H6A's two renames are not one atomic operation; crash
recovery depends on its persistent journal. No claim is made of absolute APFS or
power-loss durability beyond the OS sync semantics documented in H6A.

## States and readiness distinction

Provisioning results are backend diagnostics, not H5A states:

| Result state | Meaning |
| --- | --- |
| `DISABLED` | No attempt; no filesystem mutation. |
| `CHECKING` / `STAGING` | Work is in progress or staging was attempted. |
| `RECOVERY_REQUIRED` | Transaction or sync outcome is uncertain; preserve artifacts and reconcile. |
| `FAILED` | Preflight or active-runtime validation failed closed. |
| `COMMITTED_UNCERTIFIED` | H6A committed and manifest/artifacts passed integrity checks. Not runtime readiness. |
| `ROLLED_BACK_UNCERTIFIED` | H6A restored a previous internally consistent pack. It is not the newly pinned release and is not readiness. |

`RuntimeReady` and `InferenceReady` remain false on every path. The desktop
adapter must not map `COMMITTED_UNCERTIFIED` to H5A `READY`; complete
certification still requires the H5A backend `RuntimeCertification` checks and
later H4B/H7 evidence.

`PinnedReleaseVerified` distinguishes the desired release (exact external
manifest pin) from a previously installed release recovered after rollback.
`RuntimeIntegrityVerified` on `ROLLED_BACK_UNCERTIFIED` only means the H6A
transaction evidence and the prior release's own manifest/artifact checks
agree; it does not assert that the prior manifest is pinned by the current
installer build.

## Model governance

There is no signed/approved model-pack schema in H6B main. The pack installer
therefore rejects model paths/weight extensions and any undeclared files, and
the release-level constant remains `ModelRedistributionApproved = false`.
Persistent model storage is neither created nor modified. An approved model
manifest, rights review, pack compatibility policy, and model-specific evidence
must be implemented in a later authorized milestone before model packaging.

## Test matrix

All tests use temporary directories and tiny synthetic files; none proves real
macOS runtime behavior.

| Scenario | Expected result |
| --- | --- |
| Opt-in disabled | No persistent directory is created. |
| Missing pin / mismatched pin / malformed or template manifest | Fail before transaction. |
| Truncated declared artifact / wrong platform | Fail before activation. |
| Unlisted model weight / symlink | Governance/security failure; source is not copied. |
| Valid synthetic release | H6A commit, result remains uncertified. |
| Repeated provisioning of same release | Verified no-op. |
| Upgrade interrupted after activation rename | `RECOVERY_REQUIRED`; verified prior pack is preserved/restored. |
| Rollback interrupted | Backup remains; subsequent reconciliation is idempotent. |
| Injected file/directory sync failure | No successful commit/readiness. |
| Invalid persistent path / concurrent calls | Fail closed or serialize through H6A lock; no duplicate/ready state. |

Existing H6A tests remain responsible for lower-level corrupt journal, missing
backup, quarantine, rename, permissions/path confinement, and low-level sync
failure behavior. H6B tests exercise the provisioning-to-recovery seam with
synthetic packs and injected failures.

## Remaining gates and limitations

1. Merge/integrate H4B's actual bundled Worker, FFmpeg/FFprobe, launch
   implementation, compatible pack assembly, source/hashes, and redistribution
   review for FFmpeg and other bundled components. No files from another
   worktree were copied.
2. Integrate the provisioner into the H5A production adapter only after H4B
   provides a complete pack contract. Until then `UnavailableFullEdgePreparation`
   remains the production behavior.
3. Establish a trusted release build mechanism that supplies the pinned manifest
   digest; no release pack or pin is shipped in this change.
4. Define approved model manifest/licensing governance; no models are allowed.
5. Run H7 offline certification, then physical macOS Apple-Silicon checks. No
   Worker startup, imports, MPS, or inference has been validated here.
6. The source pack must be immutable or product-owned during preflight/copy.
   The implementation re-verifies the staged copy, but it is not a defense
   against a privileged, non-cooperating process modifying the destination tree.

Issue #149 has no approval labels in the observed GitHub snapshot; this work is
therefore local and no PR should be published until issue governance is
explicitly satisfied.
