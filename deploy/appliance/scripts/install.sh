#!/usr/bin/env bash
# Installs (or upgrades in place) the geocam-edge appliance on a Linux host,
# outside K3s/Docker. Safe to re-run: it never overwrites identity,
# credentials, or GEOCAM_DATA_DIR contents, and never touches an existing
# /etc/geocam-edge/geocam-edge.env.
#
# Usage: install.sh <path-to-geocam-edge-binary> [path-to-ffmpeg-binary]
#
# Layout created (all paths honor GEOCAM_INSTALL_ROOT for staged/test runs,
# see scripts/lib.sh):
#   /opt/geocam-edge/releases/<version>/geocam-edge[,ffmpeg]
#   /opt/geocam-edge/current -> releases/<version>   (symlink, what systemd runs)
#   /etc/geocam-edge/geocam-edge.env                 (non-secret config; created ONLY if absent)
#   /var/lib/geocam-edge                             (GEOCAM_DATA_DIR: identity/credentials/buffer)
#   /etc/systemd/system/geocam-edge.service
#
# Requires a dedicated, unprivileged system user (GEOCAM_SERVICE_USER,
# default "geocam-edge") to own GEOCAM_DATA_DIR and run the service. Created
# with no login shell and no home directory contents. Skipped when not
# targeting a real Linux system (see lib.sh:is_real_linux_target) — a gap
# this script documents rather than papers over: on such runs the directory
# layout is still fully created, but files stay owned by the invoking user.

set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
. "$SCRIPT_DIR/lib.sh"

BIN_SRC="${1:-}"
FFMPEG_SRC="${2:-}"
[ -n "$BIN_SRC" ] || die "usage: install.sh <path-to-geocam-edge-binary> [path-to-ffmpeg-binary]"
[ -f "$BIN_SRC" ] || die "binary not found: $BIN_SRC"

HOST_ARCH="$(host_arch)"
log "target architecture: $HOST_ARCH"

# Version resolution order: explicit GEOCAM_VERSION (used by update.sh/
# package.sh, which always know it), a sibling VERSION file shipped next to
# the binary in the packaged tarball, then — only as a last resort, for a
# native-arch dev install with neither — actually running the binary. That
# last path cannot work for a cross-arch install (e.g. packaging arm64
# binaries on an amd64 build host), which is exactly why the first two
# sources exist.
VERSION="${GEOCAM_VERSION:-}"
if [ -z "$VERSION" ] && [ -f "$(dirname "$BIN_SRC")/VERSION" ]; then
    VERSION="$(cat "$(dirname "$BIN_SRC")/VERSION")"
fi
if [ -z "$VERSION" ]; then
    VERSION="$("$BIN_SRC" version 2>/dev/null | head -1 | awk '{print $2}')"
fi
[ -n "$VERSION" ] || VERSION="unknown"
log "installing geocam-edge $VERSION"

PREFIX="$(root_path "$GEOCAM_PREFIX")"
CONFIG_DIR="$(root_path "$GEOCAM_CONFIG_DIR")"
DATA_DIR="$(root_path "$GEOCAM_DATA_DIR")"
SYSTEMD_DIR="$(root_path "$GEOCAM_SYSTEMD_DIR")"
RELEASE_DIR="$PREFIX/releases/$VERSION"

# --- 1. Dedicated service user/group (idempotent, real Linux targets only) --
if is_real_linux_target; then
    if ! getent group "$GEOCAM_SERVICE_GROUP" >/dev/null 2>&1; then
        groupadd --system "$GEOCAM_SERVICE_GROUP"
        log "created group $GEOCAM_SERVICE_GROUP"
    fi
    if ! getent passwd "$GEOCAM_SERVICE_USER" >/dev/null 2>&1; then
        useradd --system --gid "$GEOCAM_SERVICE_GROUP" --no-create-home \
            --shell /usr/sbin/nologin --home-dir "$DATA_DIR" \
            --comment "GEO CAM Edge service account" "$GEOCAM_SERVICE_USER"
        log "created system user $GEOCAM_SERVICE_USER (no login, no home)"
    fi
else
    log "not a real Linux install target: skipping useradd/groupadd (documented gap, see docs/deployment/appliance.md)"
fi

# --- 2. Directories (idempotent; never recurse-delete anything) ------------
mkdir -p "$RELEASE_DIR" "$CONFIG_DIR" "$DATA_DIR"
DATA_DIR_PRE_EXISTING=1
[ -e "$DATA_DIR/identity.json" ] || DATA_DIR_PRE_EXISTING=0

# --- 3. Install this release's binaries (each version gets its own dir; --
#        never overwrites a previously installed version, so rollback.sh
#        can always point back at it).
cp "$BIN_SRC" "$RELEASE_DIR/geocam-edge"
chmod 0755 "$RELEASE_DIR/geocam-edge"
if [ -n "$FFMPEG_SRC" ]; then
    [ -f "$FFMPEG_SRC" ] || die "ffmpeg binary not found: $FFMPEG_SRC"
    cp "$FFMPEG_SRC" "$RELEASE_DIR/ffmpeg"
    chmod 0755 "$RELEASE_DIR/ffmpeg"
fi

# --- 4. Point `current` at this release. Record the prior target first so --
#        rollback.sh has something deterministic to restore, but only if
#        `current` already existed (first install has nothing to roll back
#        to).
if [ -L "$PREFIX/current" ]; then
    readlink "$PREFIX/current" > "$PREFIX/.previous"
fi
atomic_symlink_swap "$PREFIX/current" "releases/$VERSION"
log "activated release $VERSION at $PREFIX/current"

# --- 5. Non-secret config: created ONLY on first install. An existing file --
#        is left completely untouched — install.sh never overwrites operator
#        edits or already-provisioned config.
ENV_FILE="$CONFIG_DIR/geocam-edge.env"
if [ ! -e "$ENV_FILE" ]; then
    cp "$SCRIPT_DIR/../config/geocam-edge.env.example" "$ENV_FILE"
    chmod 0640 "$ENV_FILE"
    log "wrote default config to $ENV_FILE (edit before enrolling)"
else
    log "config already present at $ENV_FILE, left untouched"
fi

# --- 6. systemd unit (always refreshed: it's not operator-editable state) --
mkdir -p "$SYSTEMD_DIR"
sed \
    -e "s|@GEOCAM_EXEC_PATH@|$GEOCAM_PREFIX/current/geocam-edge|g" \
    -e "s|@GEOCAM_ENV_FILE@|$GEOCAM_CONFIG_DIR/geocam-edge.env|g" \
    -e "s|@GEOCAM_SERVICE_USER@|$GEOCAM_SERVICE_USER|g" \
    -e "s|@GEOCAM_SERVICE_GROUP@|$GEOCAM_SERVICE_GROUP|g" \
    -e "s|@GEOCAM_DATA_DIR_PLACEHOLDER@|$GEOCAM_DATA_DIR|g" \
    "$SCRIPT_DIR/../systemd/geocam-edge.service.in" > "$SYSTEMD_DIR/geocam-edge.service"
log "wrote systemd unit to $SYSTEMD_DIR/geocam-edge.service"

# --- 7. Ownership: service user owns its data dir and the release tree; --
#        config dir stays root:service-group readable only (0640 file above).
if is_real_linux_target; then
    chown -R "$GEOCAM_SERVICE_USER:$GEOCAM_SERVICE_GROUP" "$DATA_DIR" "$PREFIX"
    chown "root:$GEOCAM_SERVICE_GROUP" "$CONFIG_DIR" "$ENV_FILE"
fi

# --- 8. Enable (never force-start here — 'geocam-edge saas check' / enroll --
#        typically needs to happen first) on real Linux targets only.
if is_real_linux_target && have_cmd systemctl; then
    systemctl daemon-reload
    systemctl enable geocam-edge.service
    log "enabled geocam-edge.service (not started — run 'systemctl start geocam-edge' after enrollment)"
else
    log "skipping systemctl enable (no systemd on this target, or staged/test install)"
fi

if [ "$DATA_DIR_PRE_EXISTING" = "1" ]; then
    log "existing identity/credentials in $DATA_DIR preserved"
fi
log "install complete: $PREFIX/current -> releases/$VERSION"
