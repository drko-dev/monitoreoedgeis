#!/usr/bin/env bash
# Local upgrade: activates a new version from an artifact already present on
# disk (a tarball produced by package.sh). No remote/OTA distribution
# mechanism exists or is implied here — that is a separate, not-yet-built
# concern (see docs/deployment/appliance.md).
#
# Usage: update.sh <artifact.tar.gz>
#
# Validates the artifact's checksum (mandatory: a sibling <artifact>.sha256
# must exist and verify, and a SHA256 tool must be available — anything
# short of that rejects the update) and architecture BEFORE touching
# anything installed, records the
# currently active release so rollback.sh has something deterministic to
# restore, installs the new release into its own versioned directory
# (previous versions are never deleted), atomically swaps the `current`
# symlink, restarts the service (best-effort — see below), and verifies
# /readyz. On verification failure it automatically invokes rollback.sh.

set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
. "$SCRIPT_DIR/lib.sh"

ARTIFACT="${1:-}"
[ -n "$ARTIFACT" ] || die "usage: update.sh <artifact.tar.gz>"
[ -f "$ARTIFACT" ] || die "artifact not found: $ARTIFACT"

# --- 1. Checksum (mandatory: package.sh writes <artifact>.sha256; OTA
#        pending releases may instead provide the IA1-compatible SHA256SUMS
#        file in the same directory. Both are verified by the same SHA-256
#        tool; signature verification belongs to ota-updater.sh's current
#        `geocam-edge ota verify` command.) -------------------------------
CHECKSUM_FILE="${ARTIFACT}.sha256"
CHECKSUM_DIR="$(dirname "$ARTIFACT")"
CHECKSUM_ARG="$(basename "$CHECKSUM_FILE")"
if [ ! -f "$CHECKSUM_FILE" ] && [ -f "$CHECKSUM_DIR/SHA256SUMS" ]; then
    CHECKSUM_FILE="$CHECKSUM_DIR/SHA256SUMS"
    CHECKSUM_ARG="SHA256SUMS"
fi
[ -f "$CHECKSUM_FILE" ] || die "no checksum file found — checksum verification is mandatory, artifact rejected, nothing changed"

if have_cmd sha256sum; then
    ( cd "$CHECKSUM_DIR" && sha256sum -c "$CHECKSUM_ARG" ) \
        || die "checksum verification failed for $ARTIFACT — artifact rejected, nothing changed"
elif have_cmd shasum; then
    ( cd "$CHECKSUM_DIR" && shasum -a 256 -c "$CHECKSUM_ARG" ) \
        || die "checksum verification failed for $ARTIFACT — artifact rejected, nothing changed"
else
    die "no sha256sum/shasum available — cannot verify checksum, artifact rejected, nothing changed"
fi

# --- 2. Extract to a scratch dir, validate BEFORE installing anything ------
WORKDIR="$(mktemp -d)"
trap 'rm -rf "$WORKDIR"' EXIT
tar xzf "$ARTIFACT" -C "$WORKDIR" || die "artifact is not a valid tar.gz: $ARTIFACT"

BIN="$WORKDIR/geocam-edge"
[ -f "$BIN" ] || die "artifact missing geocam-edge binary — rejected, nothing changed"

ARTIFACT_ARCH="$(cat "$WORKDIR/ARCH" 2>/dev/null || echo "")"
HOST_ARCH="$(host_arch)"
if [ -n "$ARTIFACT_ARCH" ] && [ "$ARTIFACT_ARCH" != "$HOST_ARCH" ]; then
    die "artifact architecture ($ARTIFACT_ARCH) does not match host ($HOST_ARCH) — rejected, nothing changed"
fi

VERSION="$(cat "$WORKDIR/VERSION" 2>/dev/null || true)"
[ -n "$VERSION" ] || VERSION="${GEOCAM_VERSION:-}"
[ -n "$VERSION" ] || die "artifact missing VERSION file — rejected, nothing changed"

PREFIX="$(root_path "$GEOCAM_PREFIX")"
if [ -d "$PREFIX/releases/$VERSION" ]; then
    log "release $VERSION already installed, re-activating it"
fi

# --- 3. Install the new release (install.sh already preserves everything --
#        else: data dir, config file, previously installed releases).
FFMPEG_ARG=""
[ -f "$WORKDIR/ffmpeg" ] && FFMPEG_ARG="$WORKDIR/ffmpeg"
GEOCAM_VERSION="$VERSION" "$SCRIPT_DIR/install.sh" "$BIN" $FFMPEG_ARG

# --- 4. Restart & verify (best-effort: only on a real systemd target) ------
if is_real_linux_target && have_cmd systemctl; then
    systemctl restart geocam-edge.service
    log "restarted geocam-edge.service"

    if ! "$SCRIPT_DIR/wait-ready.sh"; then
        err "geocam-edge did not become ready after update to $VERSION — rolling back"
        "$SCRIPT_DIR/rollback.sh"
        die "update to $VERSION failed and was rolled back"
    fi
    log "update to $VERSION verified ready"
else
    log "no systemd on this target (or staged/test install): activated $VERSION, restart/health verification skipped"
fi

log "update complete: now running $VERSION"
