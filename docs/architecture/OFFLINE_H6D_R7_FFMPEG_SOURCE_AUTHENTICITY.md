# H6D-R7 — FFmpeg 8.1.3 source signature verification

**Status: SIGNED SOURCE AUTHENTICATED (Mac M4, 2026-10-14). BUILD PROVENANCE / MODEL REDISTRIBUTION / RELEASE STILL BLOCKED.** Tracks approved issue #160 and review PR #162. This is **not a software-release signoff**.

## Trust anchor, source and signature

FFmpeg's official release-verification instructions: https://ffmpeg.org/download.html

- Official release public-key URL: https://ffmpeg.org/ffmpeg-devel.asc
- Authoritative key fingerprint published by FFmpeg: `FCF986EA15E6E293A5644F10B4322F04D67658D8`
- Source archive: `ffmpeg-8.1.3.tar.xz` (published by https://ffmpeg.org/releases/)
- Local source SHA-256: `7138d28c96d9d3e3af4ee3d8cad72741f8ffb40da90c1112235dea3ecd3178a3`
- Official detached signature URL: https://ffmpeg.org/releases/ffmpeg-8.1.3.tar.xz.asc
- Local source archive and signature: ignored files under `runtime-lab/ffmpeg-source/`.
- Official signature bytes and pre-existing local `.asc` were compared byte-for-byte: **MATCH**.
- GnuPG 2.5.24 in an **isolated temporary keyring**: status `GOODSIG`, `VALIDSIG FCF986EA15E6E293A5644F10B4322F04D67658D8`, verification exit 0.

This establishes that the local tarball bytes correspond to a source archive signed with the private key for the public-key fingerprint FFmpeg publishes on its official site. GnuPG's `[unknown]` Web-of-Trust owner certification is expected because the key was **not** introduced into the operator's personal trusted keyring. Identity grounding is through the website-published exact fingerprint checked before temporary import. HTTPS/publisher compromise remains outside this verification's assurance scope.

## Audited reproducible verifier

`deploy/installer/verify-ffmpeg-source-pgp.py` never downloads data or accesses the operator's GnuPG keyring. It pins the release archive SHA-256 and official public-key fingerprint, requires regular non-symlink inputs, rejects publisher signature mismatches when provided, and inspects `GOODSIG`/`VALIDSIG` with zero invalid/revoked/expired statuses. Fails closed if GPG is unavailable; returns `release_eligible=false` and `redistribution_approved=false` even on success.

Reproduce on an authorized Mac with GnuPG already installed, from the approved worktree:

```bash
W=/Users/gustavomarcon/Documents/proyectos/monitoreoedgeis-worktrees/offline-h6d
TEMP_KEY_DIR="$(mktemp -d /tmp/geocam-ffmpeg-key.XXXXXX)"
trap 'rm -rf "$TEMP_KEY_DIR"' EXIT
curl -fsSL --proto '=https' --tlsv1.2 \
  https://ffmpeg.org/ffmpeg-devel.asc -o "$TEMP_KEY_DIR/release-key.asc"
curl -fsSL --proto '=https' --tlsv1.2 \
  https://ffmpeg.org/releases/ffmpeg-8.1.3.tar.xz.asc \
  -o "$TEMP_KEY_DIR/upstream-signature.asc"
python3 "$W/deploy/installer/verify-ffmpeg-source-pgp.py" \
  --source "$W/runtime-lab/ffmpeg-source/ffmpeg-8.1.3.tar.xz" \
  --signature "$W/runtime-lab/ffmpeg-source/ffmpeg-8.1.3.tar.xz.asc" \
  --publisher-public-key "$TEMP_KEY_DIR/release-key.asc" \
  --publisher-signature "$TEMP_KEY_DIR/upstream-signature.asc" \
  --gpg /opt/homebrew/bin/gpg
```

The official URL retrieval is a separate online step; the verifier itself is offline. Never trust a fingerprint supplied **only** by the downloaded key or the local source archive.

## Tests and known limitations

- Eight tests using a newly generated **ephemeral synthetic** keypair: valid detached signature, wrong trusted fingerprint, wrong archive SHA-256, modified publisher signature, cryptographically malformed signature, missing GPG, symlinked archive and unpinned filename. All **8/8 PASS** locally.
- Linux CI runs only synthetic PGP tests. No archive, private keys, models or FFmpeg binary is committed or uploaded.
- A verified **source tarball is not proof of provenance for the compiled macOS portable FFmpeg/FFprobe binary**. No reproducible build equivalence, signed build provenance/attestation, source-tree-to-binary chain, code-signing release identity, official upstream approval of custom flags or third-party license clearance was demonstrated here.
- The binary's observed `-buildconf` uses `--disable-network` and no `--enable-gpl`/`--enable-libx264` for this specific lab binary. Existing application clip encoder references to libx264, FFmpeg's GPL/LGPL obligations and transitive dependencies still require review.
- The 36 wheel license-text inventory was audited technically in H6D-R6, but no authoritative package-origin signatures or legally approved redistribution decision have been established. Ultralytics/model-weight permissions remain unresolved.
- No independently authenticated **release manifest pin**, whole-pack code-signing, Wails provisioning, H7 full airgap or H8 clean-machine test. Keep `MODEL_REDISTRIBUTION_APPROVED=false`, `RUNTIME_READY=false` and no release.

**Operational note:** The verification host previously lacked GnuPG. GnuPG 2.5.24 was added through Homebrew as local verification tooling, which also upgraded six shared Homebrew dependencies. Existing brew versions were not automatically cleaned. This changes development-tool state only; no services, production hosts or release artifacts were changed.
