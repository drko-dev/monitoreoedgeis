# Releasing (Hito P)

## Versioning

Git tags are the source of truth: `vX.Y.Z` (semver, `v` prefix required).
No separate `VERSION` file -- the tag itself is passed straight through as
`internal/agent.Version` (see `Makefile`'s `VERSION` variable and
`internal/agent/version.go`).

## Cutting a release

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

**No release has been cut with this workflow yet** -- there is no `v1.0.0` tag.
`docs/product/RELEASE_1_0_READINESS.md` records what must close before there is.

## Out of scope (later hitos)

- SBOM generation.
- Formal provenance attestation (build attestation / SLSA-style); the release is
  signed, but the build is not attested.
- Pushing the OCI image to a registry (P3's multi-arch `Dockerfile` already
  builds locally for K3s via `make image`; a registry push is a separate,
  not-yet-scoped concern).
