#!/usr/bin/env bash
# Produces a standalone static ffmpeg binary for the appliance package, by
# reusing — not re-deriving — the exact same build recipe already reviewed
# and pinned for Hito H licensing (LGPLv2.1+-only ./configure flags, pinned
# ffmpeg source + SHA256) in the repo root Dockerfile's `ffmpeg-build`
# stage. This script does not modify that Dockerfile and does not change
# any of its licensing decisions — it just extracts that stage's build
# output instead of shipping it inside a container image, because the
# appliance runs on bare Linux, not in Docker/K3s (see docs/deployment/
# appliance.md for why Docker is a BUILD-time tool here only, never a
# runtime requirement).
#
# Usage: build-ffmpeg-static.sh <amd64|arm64> [dist-dir]
# Requires Docker with BuildKit (buildx) on the BUILD machine only.
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"
# shellcheck source=lib.sh
. "$SCRIPT_DIR/lib.sh"

ARCH="${1:-}"
DIST_DIR="${2:-$REPO_ROOT/dist}"
case "$ARCH" in
    amd64|arm64) ;;
    *) die "usage: build-ffmpeg-static.sh <amd64|arm64> [dist-dir]" ;;
esac

have_cmd docker || die "docker is required to build the static ffmpeg binary (build-time only, see script header)"

mkdir -p "$DIST_DIR"
OUT="$DIST_DIR/.ffmpeg-build-$ARCH"
rm -rf "$OUT"

log "building ffmpeg-build stage from the root Dockerfile for linux/$ARCH (Hito H recipe, unmodified)"
DOCKER_BUILDKIT=1 docker build \
    --platform "linux/$ARCH" \
    --target ffmpeg-build \
    -f "$REPO_ROOT/Dockerfile" \
    --output "type=local,dest=$OUT" \
    "$REPO_ROOT"

# The ffmpeg-build stage installs via `make install DESTDIR=/out`, so
# inside that stage's own filesystem the binary lives at
# /out/usr/local/bin/ffmpeg. `--output type=local,dest=$OUT` exports that
# whole stage filesystem, so it lands here as $OUT/out/usr/local/bin/ffmpeg
# (note the extra "out/" from DESTDIR) — not $OUT/usr/local/bin/ffmpeg.
FFMPEG_OUT="$OUT/out/usr/local/bin/ffmpeg"
[ -f "$FFMPEG_OUT" ] || die "ffmpeg-build stage did not produce out/usr/local/bin/ffmpeg — Dockerfile output layout changed?"

cp "$FFMPEG_OUT" "$DIST_DIR/ffmpeg-linux-$ARCH"
chmod 0755 "$DIST_DIR/ffmpeg-linux-$ARCH"
rm -rf "$OUT"
log "wrote $DIST_DIR/ffmpeg-linux-$ARCH"
