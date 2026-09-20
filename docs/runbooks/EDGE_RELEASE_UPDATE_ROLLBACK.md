# Edge Release / Update / Rollback

> The full lifecycle: cutting a signed release on GitHub, applying it to a
> running appliance, and reverting if it goes wrong. `docs/RELEASING.md`
> already documents the release-cutting side accurately — this document adds
> the device-side update/rollback/OTA half that it doesn't cover, and points
> back to it rather than restating it.

**Verified against:** `main` @ `6617322549e4d9ac815317a0724b92d3e4613045`,
`.github/workflows/release.yml`, `deploy/appliance/scripts/{update,rollback,ota-updater}.sh`.

## 1. Cutting a release (GitHub side)

See `docs/RELEASING.md` for the full, current process. Summary, confirmed
against `.github/workflows/release.yml`:

1. Push tag `v*.*.*`.
2. CI fails closed immediately if the `OTA_SIGNING_KEY` secret is missing.
3. Static `ffmpeg` is built for both `amd64` and `arm64`
   (`deploy/appliance/scripts/build-ffmpeg-static.sh`).
4. `deploy/appliance/scripts/package.sh --require-ffmpeg` packages both
   architectures.
5. `SHA256SUMS` generated over the tarballs.
6. `SHA256SUMS` signed with Ed25519 via `geocam-edge ota sign` (the private
   key is read from the `OTA_SIGNING_KEY` secret into a `umask 077` temp
   file — its value is never in this document, and never printed by CI).
7. GitHub Release created with the tarballs, checksums and signature attached.

**This repo does not create tags/releases as part of documentation work** —
this document only explains the existing, already-implemented pipeline.

## 2. Local update (artifact already on disk)

```bash
sudo deploy/appliance/scripts/update.sh <artifact.tar.gz>
```

No remote/OTA distribution mechanism is invoked here — this is "I already
have the tarball, activate it." Sequence (`update.sh`):

1. **Checksum is mandatory.** Looks for `<artifact>.sha256` (from
   `package.sh`), falling back to a sibling `SHA256SUMS` file (the OTA-staged
   layout). No checksum file, or no `sha256sum`/`shasum` available →
   rejected, nothing changed.
2. Extracts to a scratch dir, validates the binary and architecture (against
   the artifact's `ARCH` file and the host's real architecture) **before**
   touching anything installed.
3. Reads `VERSION` from the artifact; installs via the same `install.sh` used
   for a first install (`GEOCAM_VERSION=<version> install.sh <bin> [ffmpeg]`)
   — which is what preserves the data dir, config file, and every previously
   installed release.
4. Restarts `geocam-edge.service`, then runs `wait-ready.sh`
   (polls `/readyz`).
5. **If readiness verification fails, `update.sh` automatically invokes
   `rollback.sh` and dies with a clear error** — a failed update never leaves
   the appliance on a broken version silently.

## 3. Rollback

```bash
sudo deploy/appliance/scripts/rollback.sh
```

Reverts `current` to whatever `/opt/geocam-edge/.previous` records (written
by `install.sh`/`update.sh` every time `current` is repointed — first install
has nothing to write, so there's nothing to roll back to yet). Restarts the
service, verifies `/readyz`. **Never touches** `GEOCAM_DATA_DIR`, credentials
or identity — only the `current` symlink and the running process.

A rollback is itself reversible: rolling back records the version you just
rolled back *from* as the new "previous", so running `update.sh`/`rollback.sh`
again swaps back.

If there's nothing recorded, or the recorded release no longer exists on
disk, `rollback.sh` fails loudly (`die`) rather than guessing.

## 4. Privileged OTA apply path (`ota-updater.sh`)

The Edge daemon **never** executes this script directly — it is the
root-only boundary a privileged systemd path unit invokes when the daemon
(running unprivileged) requests an update. It:

1. Reads a single fixed-path request file (`{DATA_DIR}/ota/apply.request`),
   rejecting anything that isn't a bare alphanumeric release id (no
   symlinks, no path traversal, length-capped).
2. Copies the pending release into a private, root-owned staging snapshot
   the daemon cannot modify after the copy.
3. Requires `SHA256SUMS`, `SHA256SUMS.sig` and `metadata.json` to be present
   and non-symlinked in that snapshot.
4. Re-verifies the snapshot's signature by shelling out to the **currently
   installed** `geocam-edge ota verify` — a second, independent crypto check
   is never invented here; it reuses the same Ed25519 policy the binary
   already owns.
5. Only on successful verification does it hand the verified artifact to the
   same `update.sh` used for a manual local update.
6. Every step writes an atomic state file (`applying:<id>`,
   `succeeded:<id>`, or a specific `failed:<reason>`) so an operator or the
   agent can inspect exactly where an OTA attempt stood.

## `OTA_SIGNING_KEY`

A GitHub Actions secret, Ed25519 private key. Read only inside the release
workflow, written to a `umask 077` temp file for the duration of `geocam-edge
ota sign`, never logged, never checked into the repo, never referenced by
value in any document — including this one.

## Distinguishing the four different "done" claims

Do not conflate these — they are checked independently:

| Claim | What it actually means |
|---|---|
| **SOFTWARE 1.0** | Code complete, locally tested |
| **RELEASE 1.0** | An actual, validated GitHub Release with signed artifacts exists |
| **DEPLOYED PROD** | Running on a real production target — a separate, explicit-authorization action |
| **PHYSICAL VALIDATION** | Tested against real hardware/cameras — tracked entirely separately, see `docs/product/PHYSICAL_VALIDATION_REGISTER.md` |

This document does not create a tag, a release, or a deployment.

---

See also: `docs/RELEASING.md` (release-cutting detail), `docs/security/ota.md`,
`docs/security/update-trust.md`, `docs/runbooks/EDGE_INSTALL_FROM_SCRATCH.md`,
`docs/README.md`.
