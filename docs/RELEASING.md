# Releasing (Hito P)

Two independent release streams live in this repo, with separate tags:

| Stream | Tag | Workflow | Contents |
|---|---|---|---|
| Linux appliance / daemon (OTA) | `vX.Y.Z` | `release.yml` | Signed appliance tarballs consumed by OTA |
| Desktop installer GUI ("Release Stable") | `installer-vX.Y.Z` | `installer-release.yml` | Windows / macOS / Linux installer packages |

## Linux appliance / daemon

### Versioning

Git tags are the source of truth: `vX.Y.Z` (semver, `v` prefix required).
No separate `VERSION` file -- the tag itself is passed straight through as
`internal/agent.Version` (see `Makefile`'s `VERSION` variable and
`internal/agent/version.go`).

### Cutting a release

```
git tag v0.2.0
git push origin v0.2.0
```

Pushing a tag matching `v*.*.*` triggers `.github/workflows/release.yml`,
which:

1. **Requires the signing key first, fail closed.** If the `OTA_SIGNING_KEY`
   repository secret is not configured, the job fails before anything is
   published. There is no unsigned and no checksum-only release path.
2. Packages the **full appliance artefact** for linux/amd64 and linux/arm64 by
   reusing `deploy/appliance/scripts/package.sh` -- deliberately the same
   producer the local build uses, not a second or reduced tarball format. Each
   artefact carries the binary, `VERSION`, `ARCH`, the scripts, the systemd unit
   template and the example config, and is named
   `geocam-edge-vX.Y.Z-linux-<arch>.tar.gz`. Reproducibility guarantees match
   any local build: `-trimpath`, `-ldflags` embedding version/commit/build date,
   identical flags for both architectures (see `Makefile`).
3. Generates `SHA256SUMS` over the artefacts.
4. Signs it with an **Ed25519 detached signature** (`SHA256SUMS.sig`) using
   `geocam-edge ota sign`, with the key material written to a `umask 077`
   temporary file that is removed afterwards.
5. Publishes a GitHub Release with the tarballs, their `.sha256` files,
   `SHA256SUMS` and `SHA256SUMS.sig`.

Verification is fail-closed on the device side too: an appliance needs
`GEOCAM_OTA_PUBLIC_KEY_FILE` provisioned out of band, and an unset or
unreadable key rejects every candidate release rather than accepting a
checksum-only comparison. See `docs/security/ota.md` and
`docs/security/update-trust.md`.

Appliance releases (`v1.0.0`, `v1.1.0`, ...) are published with
`make_latest: false`: OTA always downloads by explicit tag, and GitHub's
"Latest" marker belongs to the desktop installer so its
`/releases/latest/download/` links stay stable.

## Desktop installer GUI -- "GEO CAM Edge vX.Y.Z — Release Stable"

### Publishing a version

From an up-to-date `main`, on the commit to release:

```
git checkout main && git pull --ff-only
git tag -a installer-v1.2.0 -m "GEO CAM Edge v1.2.0"
git push origin installer-v1.2.0
```

Nothing is published on push/merge; only an `installer-vX.Y.Z` tag starts
`.github/workflows/installer-release.yml`, which:

1. Checks the tag format, that the commit is on `main`, and that no published
   release exists for the tag (published releases are never overwritten --
   bump the version instead).
2. Runs the test suite (same gates as `ci.yml`).
3. Builds on native runners via `installer-build.yml`: Windows amd64, macOS
   arm64, Linux amd64, Linux arm64, each stamped with the same version, then
   checks the version (Windows version resource / macOS `Info.plist` /
   Linux `VERSION`) and that the app launches and stays up for 15 s.
4. `deploy/installer/assemble-release.sh` cross-checks that all four
   packages exist, carry the same version and commit, still match the
   checksum recorded at build time and have the right binary format and
   architecture, then writes `SHA256SUMS.txt`.
5. Creates a **draft** release titled `GEO CAM Edge vX.Y.Z — Release Stable`,
   uploads the packages, re-downloads and verifies them, and only then
   publishes it and marks it **Latest**.

If any step fails nothing is published and the previous Latest release stays
as it is. Re-running the failed workflow is safe: it resumes the draft left
by the failed run instead of creating a second release.

Every pull request runs the same build and assembly checks
(`installer-build-validation.yml`, stamped `0.0.0`), and keeps the packages
as workflow artifacts for 7 days.

### Downloads

Asset names do not contain the version, so these links always point to the
latest Release Stable:

- `https://github.com/drko-dev/monitoreoedgeis/releases/latest`
- `https://github.com/drko-dev/monitoreoedgeis/releases/latest/download/geocam-edge-installer-windows-amd64.exe`
- `https://github.com/drko-dev/monitoreoedgeis/releases/latest/download/geocam-edge-installer-macos-arm64.zip`
- `https://github.com/drko-dev/monitoreoedgeis/releases/latest/download/geocam-edge-installer-linux-amd64.tar.gz`
- `https://github.com/drko-dev/monitoreoedgeis/releases/latest/download/geocam-edge-installer-linux-arm64.tar.gz`
- `https://github.com/drko-dev/monitoreoedgeis/releases/latest/download/SHA256SUMS.txt`

A specific version: `.../releases/download/installer-vX.Y.Z/<asset>`.

### Unsigned builds

The installer is **not code-signed** (no Windows certificate, no Apple
Developer ID, no notarization; the macOS app carries only an ad-hoc
signature). Windows SmartScreen may show "Windows protected your PC"
(More info → Run anyway) and macOS may report that the app cannot be
verified (System Settings → Privacy & Security → Open Anyway). Users should
verify the SHA-256 against `SHA256SUMS.txt` first. This does not affect the
appliance OTA trust chain, which stays Ed25519-signed and fail-closed.

## Out of scope (later hitos)

- SBOM generation.
- Formal provenance attestation (build attestation / SLSA-style); the release is
  signed, but the build is not attested.
- Pushing the OCI image to a registry (P3's multi-arch `Dockerfile` already
  builds locally for K3s via `make image`; a registry push is a separate,
  not-yet-scoped concern).
