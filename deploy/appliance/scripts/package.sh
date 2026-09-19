#!/usr/bin/env bash
# Builds the geocam-edge amd64+arm64 binaries (via the existing `make
# build-linux`) and packages each architecture into a self-contained
# tar.gz: binary, static ffmpeg (if available — see build-ffmpeg-static.sh),
# systemd unit template, install/update/rollback/uninstall scripts, example
# config, VERSION and ARCH marker files, and a .sha256 checksum.
#
# Usage: package.sh [version] [dist-dir]
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
APPLIANCE_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
REPO_ROOT="$(cd "$APPLIANCE_DIR/../.." && pwd)"
# shellcheck source=lib.sh
. "$SCRIPT_DIR/lib.sh"

VERSION="${1:-$(cd "$REPO_ROOT" && git rev-parse --short HEAD 2>/dev/null || echo dev)}"
DIST_DIR="${2:-$REPO_ROOT/dist}"

log "building geocam-edge $VERSION for linux/amd64 and linux/arm64"
( cd "$REPO_ROOT" && make build-linux VERSION="$VERSION" )

mkdir -p "$DIST_DIR"

for arch in amd64 arm64; do
    STAGE="$(mktemp -d)"
    trap 'rm -rf "$STAGE"' EXIT

    cp "$REPO_ROOT/bin/geocam-edge-linux-$arch" "$STAGE/geocam-edge"
    chmod 0755 "$STAGE/geocam-edge"
    echo "$VERSION" > "$STAGE/VERSION"
    echo "$arch" > "$STAGE/ARCH"

    FFMPEG_BIN="$DIST_DIR/ffmpeg-linux-$arch"
    if [ -f "$FFMPEG_BIN" ]; then
        cp "$FFMPEG_BIN" "$STAGE/ffmpeg"
        chmod 0755 "$STAGE/ffmpeg"
        log "bundling static ffmpeg for $arch"
    else
        log "warning: no static ffmpeg found at $FFMPEG_BIN — packaging without it." \
            "Run scripts/build-ffmpeg-static.sh $arch first (requires Docker as a BUILD-time" \
            "tool only — not a runtime dependency of the appliance). Without it," \
            "GEOCAM_VIDEO_PIPELINE_ENABLED will fail unless ffmpeg is separately provisioned."
    fi

    # Mirrors deploy/appliance/'s own scripts/config/systemd layout exactly,
    # so install.sh's relative "$SCRIPT_DIR/../config" and
    # "$SCRIPT_DIR/../systemd" lookups work identically whether run from the
    # repo checkout or from an extracted tarball.
    mkdir -p "$STAGE/scripts" "$STAGE/systemd" "$STAGE/config"
    cp "$SCRIPT_DIR"/install.sh "$SCRIPT_DIR"/update.sh "$SCRIPT_DIR"/rollback.sh \
       "$SCRIPT_DIR"/uninstall.sh "$SCRIPT_DIR"/wait-ready.sh "$SCRIPT_DIR"/bootstrap.sh "$SCRIPT_DIR"/lib.sh "$STAGE/scripts/"
    cp "$APPLIANCE_DIR/systemd/geocam-edge.service.in" "$STAGE/systemd/"
    if [ -f "$APPLIANCE_DIR/systemd/geocam-edge-bootstrap.service.in" ]; then
        cp "$APPLIANCE_DIR/systemd/geocam-edge-bootstrap.service.in" "$STAGE/systemd/"
    fi
    cp "$APPLIANCE_DIR/config/geocam-edge.env.example" "$STAGE/config/"

    ARTIFACT="$DIST_DIR/geocam-edge-$VERSION-linux-$arch.tar.gz"
    ( cd "$STAGE" && tar czf "$ARTIFACT" . )
    if have_cmd sha256sum; then
        ( cd "$DIST_DIR" && sha256sum "$(basename "$ARTIFACT")" > "$(basename "$ARTIFACT").sha256" )
    elif have_cmd shasum; then
        ( cd "$DIST_DIR" && shasum -a 256 "$(basename "$ARTIFACT")" > "$(basename "$ARTIFACT").sha256" )
    fi
    log "packaged $ARTIFACT"

    rm -rf "$STAGE"
    trap - EXIT
done
