# H6D-R11 — Signed Runtime Pack → transactional provisioner, explicit opt-in only

**Status: SYNTHETIC INTEGRATION IMPLEMENTED / RELEASE STILL BLOCKED.** Issue #160, approved for non-production preparatory work. This does not create a sealed pack, publisher release key, or production installer.

## Why this boundary is necessary

The H6B transactional provisioner historically accepted an independently supplied `ExpectedManifestSHA256`; the #166 `signed_manifest.go` module implemented Ed25519/P-256 verification separately. A digest **authenticates only relative to a trusted independent source**, while a signature proves publication under a trusted publisher key. Merely having those modules compiled into the same application did not enforce either on the other's input.

### New opt-in APIs

- `installer.NewSignedRuntimePackPreparationBackend(options, signed, trust)` accepts an explicitly enabled H6B options structure, a publisher **signed manifest supplied from outside the pack**, and a publisher `TrustStore` supplied from outside the pack.
- Requires a correctly formatted 64-hex **independent manifest pin** as well as a signed envelope, trusted key, and valid manifest. A key embedded in pack bytes is not a publisher trust anchor.
- Clones the signed envelope at construction; consults the live trust store again at every Prepare/Recover, including key revocation and expiry. **Signatures do not automatically override permissions.**
- Before the H6B transaction lock, ensures canonical/disjoint pack and data paths, reads bounded signed manifest bytes through a non-symlink path, verifies SHA-256 against the externally provided pin and **compares the parsed on-disk `runtime-manifest.json` with the signed manifest payload**. No signature of a different pack is accepted.
- Calls `ValidateSignedManifest` with `RequireRelease=true`, target architecture/OS, artifact digest validation and revocation/expiry checks. H6B then independently rechecks the pinned SHA-256/allowlist while staging/committing. Failed authentication is `SIGNED_MANIFEST_REJECTED`, with no persistent transaction mutation.
- Revalidates during Recover. A revoked signer **cannot be treated as READY during startup**, even when a prior on-disk transaction was committed. The backend continues to return `COMMITTED_UNCERTIFIED` on valid synthetic commits; `RuntimeReady=false`, `InferenceReady=false` in all cases.

- `cmd/geocam-edge-ui.NewAppWithSignedRuntimePack(options, signed, trust)` wires the protected backend through the existing Wails preparation adapter behind **explicit release-owner opt-in**, retaining the existing bounded startup reconciliation. The default shipping `NewApp()` is **unchanged** and continues to use `UnavailableFullEdgePreparation{}`.
- The older, independently pinned but unsigned `NewAppWithPinnedRuntimePack` still exists for its pre-existing opt-in cases; it has **not** silently acquired a trusted publisher key. A future authorized signed release must explicitly select the new signature-gated constructor.

## Test matrix (synthetic only)

- Signed Ed25519 release + externally supplied matching pin/TrustStore and complete manifest: transactional Prepare and Recover succeed **uncertified**; no READY.
- Disabled or missing trusted digest, signature or trust anchor: reject without creating data directories.
- Corrupted signature, unknown or revoked key, correctly signed different manifest with its own correct digest, tampered Worker artifact, independently pinned digest mismatch: reject before transaction.
- Revocation **after** prior committed synthetic pack blocks a subsequent Recover.
- Caller mutation of `SignedRuntimeManifest` after construction cannot change the bound, cloned signature.
- Wails default app remains fail closed. New explicit constructor rejects missing signature, key or pin, and malformed envelope.
- Existing H5/H6 transactional recovery/rollback tests and security suites should pass unchanged.

## Remains blocked

1. Publisher-owned independently authenticated trust anchors (including secure update, rotation and key lifecycle) and a legally cleared **real** signed manifest are **NOT CONFIGURED**. The test keys are ephemeral synthetic fixtures and are never exported as product keys.
2. This is not a solution for path-based execution TOCTOU across all components. Final activated pack immutability/file-descriptor based exec and true release trust chain remain outstanding.
3. Physical H8 macOS14 clean-host test, H7 installed-product isolation, installer rollback under real package activation, notarization/Developer ID, licenses (Ultralytics/FFmpeg/36 wheels) and model redistribution rights all remain **NO-GO**.
4. `MODEL_REDISTRIBUTION_APPROVED=false`, `READY_FOR_INSTALLER=false`, `READY_FOR_RELEASE=false`. No default Wails runtime activation, model weights, binary packaging, deploy, merge, release or production actions.

**This PR implements the signed-to-transaction security integration boundary, not commercial certification.**

## H6D-R11.1 — Late authorization recheck (2026-10-14)

Security review identified a bounded **revocation/time-of-check-to-time-of-use window**: the initial signed manifest check happens before H6B begins transactional staging, so a publisher key could be revoked while the release files were being copied. The original H6D-R11 adapter would not consult the updated trust store again before treating staged bytes as committed.

This follow-up adds an **opt-in internal pre-commit verification callback** to the transactional provisioner. Only the new signed Runtime Pack backend attaches it; legacy digest-only H6B behaviors remain unchanged. The signed backend now revalidates all checks including signer revocation, signature expiry, pinned source manifest, signed payload identity and artifact hashes at three relevant boundaries:

1. **At staged verification, before RecoveryManager's atomic rename:** if the signature/trust status changed since initial preflight, H6A rejects and cleans the staged candidate without installing it.
2. **Before acknowledging an already-active, pin-matching runtime:** if the signer was revoked during the existing-runtime check, return failure without success flags.
3. **After Reconcile and before returning a Recover result:** a key revoked during recovery cannot be silently reported as trusted after reconciliation.

Added adversarial Go tests for revocation injected during staging, revocation immediately before already-active short-circuit, and source Worker modification between preflight and final staging verification. These tests verify false readiness flags, no successful commit, and absent active install for rejected first-time transactions. Targeted signed-manifest security tests pass with the race detector.

**Limitations:** This closes the long staging interval but **does not provide global atomic key revocation** between the final validation and rename across processes, replace signed-release ownership infrastructure, or make previously installed executables immutable. A complete protection against filesystem race/TOCTOU attacks requires a defined immutable installed-pack trust model or descriptor-based verification/execution. Recover may still reconcile journal state before a late revalidation; the result remains fail-closed and not READY. No live release identity or production deployment was created.

## H6D-R11.2 — Post-install file-integrity recheck (2026-10-14)

The signed pre-commit checks authenticate bytes at the moment the transactional installer verifies and activates a pack, but they do not make an installed runtime immutable. The on-disk active directory can later be modified by a privileged or same-user process.

The new explicit read-only method **`SignedRuntimePackPreparationBackend.VerifyInstalledRuntimePack(ctx)`** validates the installed *active* directory instead of its original source media:

- Requires an existing real, non-symlinked persistent data directory and a direct real active pack directory; no missing or symlink-substituted installed pack may pass.
- Checks the installed manifest raw bytes against the **independently provided SHA-256 pin** and verifies its parsed contents against the externally signed payload (Ed25519/P-256), currently trusted publisher key, expiry and revocation.
- Revalidates **all declared installed artifacts** and the entire allowlisted installed tree, including the executable/Worker-backend contract. Missing, changed, unlisted or symlinked installed files are rejected.
- Remains usable **when the original source pack media is removed** after installation, because installed integrity is authenticated from the active copy and publisher-trusted manifest, not re-read from the USB/tarball/source directory.
- Never executes Python, FFmpeg, Worker or inference, and **does not grant model redistribution rights or set READY**. No production launch path is wired to this method; release-owned runtime orchestration must call it at launch/restart before it could become a security gate. A verification result must not be cached forever.

Adversarial Go tests (race detector) now cover: uninstalled pack denial, valid installed bytes, valid installed bytes with removed source media, later Worker tampering, unlisted injected file, changed installed manifest, active directory symlink replacement, revoked publisher key, and cancelled verification. All synthetic tests pass.

**Limitations:** Per-path hash checks are not an atomic snapshot across concurrent process mutation and cannot prevent an attacker replacing bytes **after the final recheck but before or during execution**. A final authorization architecture will need write-access separation (read-only owner-controlled installed tree and updater boundary), signature/descriptor-based process execution or equivalent platform-specific protection, trusted clean macOS14 H8, code signing, notarization and model licensing approval. The signed recovery path has since been updated by R11.3 below to verify the installed bytes without requiring original source media. No actual shipped runtime activation was performed here.


## H6D-R11.3 — Offline recovery after source media removal

An offline desktop installer cannot require its original removable/source pack to remain mounted forever. The initial signed `Recover` incorrectly began by verifying the *source* directory, which made previously installed, signed and pinned runtimes impossible to reconcile after the source had been removed.

The signed backend now verifies **publisher signature, key revocation and expiry** using the independently supplied signed envelope and trust root **before journal mutation**, without requiring any source media. H6B then performs its existing locked journal reconciliation. A dedicated opt-in `preRecoverVerify` callback checks the **installed** runtime through `VerifyInstalledRuntimePack` before a trusted recovery result can be returned. The unsigned/digest-only legacy recovery behavior is unchanged.

Adversarial tests under Go race detection confirm:

1. Successfully committed synthetic signed pack, original source directory subsequently removed, **Recover returns `COMMITTED_UNCERTIFIED`**, with release digest and installed bytes independently validated, and with `RuntimeReady=false`/`InferenceReady=false`.
2. Same setup but signing key revoked before recovery: **fail closed**, no committed or READY result.
3. Same setup but installed Worker artifact tampered: **fail closed**, no committed or READY result.

**Limits:** This validates recovery of an already committed synthetic pack; crash recovery while no active tree exists, upgrades and concurrent filesystem attacks still need separately adversarial test coverage. Journal reconciliation may change transaction metadata before the installed-tree check, but signature/key status is authenticated before that operation. The result remains UNCERTIFIED. This does not activate Wails or authorize model redistribution or release.
