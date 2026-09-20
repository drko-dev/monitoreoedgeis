#!/usr/bin/env bash
# Builds the geocam-edge amd64+arm64 binaries (via the existing `make
# build-linux`) and packages each architecture into a self-contained
# tar.gz: binary, static ffmpeg (if available — see build-ffmpeg-static.sh),
# systemd unit template, install/update/rollback/uninstall scripts, example
# config, VERSION and ARCH marker files, and a .sha256 checksum.
#
# Every artifact always carries the Full Edge vision-worker sources
# (worker.py, backend.py, requirements.txt) under vision-worker/; the Python
# runtime itself is provisioned on the appliance, not shipped (see
# scripts/check-vision-runtime.sh).
#
# Usage: package.sh [--require-ffmpeg] [version] [dist-dir]
#
# --require-ffmpeg makes the static ffmpeg binary MANDATORY: packaging fails if
# $DIST_DIR/ffmpeg-linux-<arch> is absent, and the produced tarball is then
# verified to actually contain ffmpeg plus the rest of the required layout.
# Without the flag the historical dev/local behaviour is preserved and a missing
# ffmpeg is only a warning — so a local `package.sh` run still works on a
# machine without Docker, while a real release cannot silently ship an
# appliance that cannot decode video.
#
# The ffmpeg binaries are NOT built here; build them first with
# scripts/build-ffmpeg-static.sh <arch> <dist-dir>, which reuses the pinned
# LGPL-only Dockerfile recipe (Docker is a BUILD-time tool only).
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
APPLIANCE_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
REPO_ROOT="$(cd "$APPLIANCE_DIR/../.." && pwd)"
# shellcheck source=lib.sh
. "$SCRIPT_DIR/lib.sh"

REQUIRE_FFMPEG=0
POSITIONAL=()
for arg in "$@"; do
    case "$arg" in
        --require-ffmpeg) REQUIRE_FFMPEG=1 ;;
        -h|--help)
            sed -n '2,32p' "${BASH_SOURCE[0]}" | sed 's/^# \?//'
            exit 0
            ;;
        *) POSITIONAL+=("$arg") ;;
    esac
done
set -- ${POSITIONAL[@]+"${POSITIONAL[@]}"}

VERSION="${1:-$(cd "$REPO_ROOT" && git rev-parse --short HEAD 2>/dev/null || echo dev)}"
DIST_DIR="${2:-$REPO_ROOT/dist}"

log "building geocam-edge $VERSION for linux/amd64 and linux/arm64"
( cd "$REPO_ROOT" && make build-linux VERSION="$VERSION" )

mkdir -p "$DIST_DIR"

# verify_artifact_entries fails the build unless the finished tarball really
# carries every entry a released appliance needs. Checking the ARTIFACT (not
# just its inputs) is what makes the guarantee meaningful: it catches a staging
# regression that dropped a file, not only a missing input binary.
#
# The base layout is verified on EVERY packaging run, because every entry in it
# is reproducible from the repo alone. The static ffmpeg binary is added to the
# required set only under --require-ffmpeg, since building it needs Docker and a
# local dev run may legitimately not have it.
verify_artifact_entries() {
    local artifact="$1" require_ffmpeg="$2" listing missing=""
    listing="$(tar tzf "$artifact")"

    local files="./geocam-edge ./VERSION ./ARCH"
    [ "$require_ffmpeg" = "1" ] && files="$files ./ffmpeg"
    for entry in $files; do
        printf '%s\n' "$listing" | grep -qxF -- "$entry" || missing="$missing $entry"
    done

    # vision-worker/ carries the Full Edge Python worker (B2). It is repo content
    # like scripts/ and config/, so it is required unconditionally: an appliance
    # that cannot ship the worker cannot run Full Edge at all.
    for dir in ./scripts/ ./systemd/ ./config/ ./vision-worker/; do
        printf '%s\n' "$listing" | grep -qF -- "$dir" || missing="$missing $dir"
    done
    for entry in ./vision-worker/worker.py ./vision-worker/backend.py ./vision-worker/requirements.txt; do
        printf '%s\n' "$listing" | grep -qxF -- "$entry" || missing="$missing $entry"
    done

    [ -z "$missing" ] || die "$artifact is missing required entries:$missing"
}

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
    elif [ "$REQUIRE_FFMPEG" = "1" ]; then
        die "--require-ffmpeg: no static ffmpeg at $FFMPEG_BIN." \
            "Run scripts/build-ffmpeg-static.sh $arch \"$DIST_DIR\" first (requires Docker as a" \
            "BUILD-time tool only — not a runtime dependency of the appliance). Refusing to" \
            "publish an appliance artifact that cannot decode video."
    else
        log "warning: no static ffmpeg found at $FFMPEG_BIN — packaging without it." \
            "Run scripts/build-ffmpeg-static.sh $arch first (requires Docker as a BUILD-time" \
            "tool only — not a runtime dependency of the appliance). Without it," \
            "GEOCAM_VIDEO_PIPELINE_ENABLED will fail unless ffmpeg is separately provisioned." \
            "Pass --require-ffmpeg to make this a hard failure instead."
    fi

    # Mirrors deploy/appliance/'s own scripts/config/systemd layout exactly,
    # so install.sh's relative "$SCRIPT_DIR/../config" and
    # "$SCRIPT_DIR/../systemd" lookups work identically whether run from the
    # repo checkout or from an extracted tarball.
    mkdir -p "$STAGE/scripts" "$STAGE/systemd" "$STAGE/config"
    cp "$SCRIPT_DIR"/install.sh "$SCRIPT_DIR"/update.sh "$SCRIPT_DIR"/rollback.sh \
       "$SCRIPT_DIR"/uninstall.sh "$SCRIPT_DIR"/wait-ready.sh "$SCRIPT_DIR"/bootstrap.sh "$SCRIPT_DIR"/lib.sh "$SCRIPT_DIR"/ota-updater.sh \
       "$SCRIPT_DIR"/check-vision-runtime.sh "$STAGE/scripts/"
    cp "$APPLIANCE_DIR/systemd/geocam-edge.service.in" "$STAGE/systemd/"
    if [ -f "$APPLIANCE_DIR/systemd/geocam-edge-bootstrap.service.in" ]; then
        cp "$APPLIANCE_DIR/systemd/geocam-edge-bootstrap.service.in" "$STAGE/systemd/"
    fi
    cp "$APPLIANCE_DIR/systemd/geocam-edge-ota-updater.service.in" "$APPLIANCE_DIR/systemd/geocam-edge-ota-updater.path.in" "$STAGE/systemd/"
    cp "$APPLIANCE_DIR/config/geocam-edge.env.example" "$STAGE/config/"

    # Full Edge's out-of-process Python worker (B2). These are plain, readable,
    # ARCHITECTURE-INDEPENDENT sources -- no interpreter, no virtualenv, no
    # wheels and no model weights travel here. Shipping a "portable venv" across
    # amd64 and arm64 would be a lie: native wheels are per-architecture, so the
    # runtime is installed on the appliance by the operator and verified by
    # scripts/check-vision-runtime.sh. PyTorch never enters the Go process.
    VISION_SRC="$REPO_ROOT/deploy/vision-worker"
    [ -f "$VISION_SRC/worker.py" ] || die "missing $VISION_SRC/worker.py"
    [ -f "$VISION_SRC/backend.py" ] || die "missing $VISION_SRC/backend.py"
    [ -f "$VISION_SRC/requirements.txt" ] || die "missing $VISION_SRC/requirements.txt"
    mkdir -p "$STAGE/vision-worker"
    cp "$VISION_SRC/worker.py" "$VISION_SRC/backend.py" "$VISION_SRC/requirements.txt" "$STAGE/vision-worker/"
    chmod 0644 "$STAGE/vision-worker/"*
    log "bundled Full Edge vision worker sources for $arch (runtime installed separately)"

    ARTIFACT="$DIST_DIR/geocam-edge-$VERSION-linux-$arch.tar.gz"
    ( cd "$STAGE" && tar czf "$ARTIFACT" . )
    if have_cmd sha256sum; then
        ( cd "$DIST_DIR" && sha256sum "$(basename "$ARTIFACT")" > "$(basename "$ARTIFACT").sha256" )
    elif have_cmd shasum; then
        ( cd "$DIST_DIR" && shasum -a 256 "$(basename "$ARTIFACT")" > "$(basename "$ARTIFACT").sha256" )
    fi
    verify_artifact_entries "$ARTIFACT" "$REQUIRE_FFMPEG"
    if [ "$REQUIRE_FFMPEG" = "1" ]; then
        log "verified required artifact layout for $arch (including ffmpeg and vision-worker)"
    else
        log "verified required artifact layout for $arch (vision-worker included; ffmpeg optional in this mode)"
    fi
    log "packaged $ARTIFACT"

    rm -rf "$STAGE"
    trap - EXIT
done
