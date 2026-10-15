# GEO CAM — Wails Full Edge startup binding and macOS LaunchAgent remediation

**Scope:** commit 280ad30 and review fixes, branch `fix/edge-installer-audit-20261015`. This is a code/CI review milestone, NOT a deployed or released installer.

## Root cause and correction

The production Wails main called `NewApp()`, which always injects `UnavailableFullEdgePreparation`; `PREPARATION_NOT_INTEGRATED` therefore described an unwired UI, not necessarily absent H3/H4/H6 code. The new `resolveApp()` only activates the already integrated `NewAppWithSignedRuntimePack` bridge when **both** an explicit operator opt-in AND a separately release-built trust root exist.

### Important trust boundary

The initial startup implementation accepted `GEOCAM_RUNTIME_TRUST_ANCHOR` and `GEOCAM_RUNTIME_MANIFEST_SHA256` from the environment or nearby candidate pack files. This could allow an attacker/operator with local write access to substitute their own key and matching self-signed manifest, then treat it as a publisher authorization. Review corrected that before opening the PR.

The current startup requires **all three release-controlled values embedded via build-time linker flags**:

- `main.releaseRuntimeManifestSHA256`: exact SHA-256 of the independently approved release `runtime-manifest.json`;
- `main.releaseRuntimeKeyID`: exact publisher key identifier;
- `main.releaseRuntimePublicKeySHA256`: SHA-256 of that publisher's independent raw Ed25519 key or DER-encoded P-256 key.

A file at `GEOCAM_RUNTIME_TRUST_ANCHOR` (or `Contents/Resources/trust-anchors.json`) is merely a public-key transport: **its key ID and public-key bytes must match the build-time values**, other keys are discarded, and the trust file may not be under the candidate pack directory. The signed manifest may be read from an operator-selected external path or bundle Resources, but its signature and the on-disk source manifest are authenticated against the exact pinned release identity during H6D processing.

The optional environment `GEOCAM_RUNTIME_MANIFEST_SHA256` can only match the baked-in manifest digest; it may not substitute a new one. `GEOCAM_RUNTIME_PACK_PINNED=1` no longer enables the weaker digest-only constructor from the Wails startup path. The explicit Go constructors remain available to controlled tests and other owner-approved release code.

**Default build with no embedded publisher root stays disabled, even if all opt-in env variables are set.** No real publisher key, trust anchor or model authorization is supplied here; a production binary cannot silently promote itself to READY.

For an independently approved release owner only, the build configuration format is:

```text
-ldflags "-X main.releaseRuntimeManifestSHA256=<APPROVED_MANIFEST_SHA256> -X main.releaseRuntimeKeyID=<APPROVED_PUBLISHER_KEY_ID> -X main.releaseRuntimePublicKeySHA256=<APPROVED_PUBLIC_KEY_SHA256>"
```

These are **only public fingerprints**, never private signing keys. The release operator must supply a real signed manifest and a matching public key from a trusted external source.

## LaunchAgent behavior

The macOS service installer now writes plist bytes atomically **before** disrupting a loaded job, normalizes whitespace during plist comparison, and skips needless `bootout/bootstrap` when configuration is identical. When an existing job is loaded and an update or restart is performed, `serviceCommand` and `installMacOSService` wait for launchd to complete asynchronous job unload before re-bootstrapping, and retry `launchctl bootstrap` with backoff when encountering transient launchd error 5 (`Bootstrap failed: 5: Input/output error`). An unchanged plist still triggers a non-mutating `launchctl print` query; when the job is not loaded, it explicitly instructs the operator to use `geocam-edge service start`, rather than claiming it is running or unexpectedly starting it on install.

Tests exercise unchanged loaded/unloaded paths, whitespace normalization, an already loaded service undergoing unload/reload, transient EIO bootstrap retry, unload wait timeout, and write failures preserving previous plist/daemon. **A real service-install smoke on a sacrificial lab machine remains separate.**

## Validation and readiness

Use focused Wails and native service regression, installer signed-gate and broader Go `-race` packages, frontend build and native ARM64 compile before review. No service or production installation is performed by these tests.

The existing status invariants remain: `RUNTIME_READY=false`, `INFERENCE_READY=false`, `MODEL_REDISTRIBUTION_APPROVED=false`. Successful transaction verification is `COMMITTED_UNCERTIFIED`, never certified real-camera inference.

**No merge, deploy, release, production camera or SaaS mutation authorized.**
