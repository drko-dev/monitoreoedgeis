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
#   /usr/libexec/geocam-edge/ota-updater.sh           (root-only OTA boundary)
#   /etc/systemd/system/geocam-edge-ota-updater.{service,path}
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

# Reject an incompatible-architecture binary BEFORE touching anything
# installed. The ARCH sidecar file a packaged tarball ships is only
# metadata; the binary's REAL architecture (inspected via `file`/`readelf`,
# see lib.sh:detect_binary_arch) is the source of truth and must agree with
# it. On a real Linux install target, if that inspection is inconclusive
# (tools unavailable, unrecognized format), this fails closed — an
# unverifiable binary must never be activated on a production target — same
# check/style as update.sh's artifact validation.
ARTIFACT_ARCH="$(normalize_arch "$(cat "$(dirname "$BIN_SRC")/ARCH" 2>/dev/null || echo "")")"
BINARY_ARCH="$(detect_binary_arch "$BIN_SRC")"
if [ -n "$BINARY_ARCH" ]; then
    [ "$BINARY_ARCH" = "$HOST_ARCH" ] || die "binary architecture ($BINARY_ARCH) does not match host ($HOST_ARCH) — rejected, nothing installed"
    if [ -n "$ARTIFACT_ARCH" ] && [ "$ARTIFACT_ARCH" != "$BINARY_ARCH" ]; then
        die "ARCH marker ($ARTIFACT_ARCH) does not match the binary's actual architecture ($BINARY_ARCH) — rejected, nothing installed"
    fi
elif arch_check_required; then
    die "cannot verify the binary's real architecture on this target ('file'/'readelf' unavailable or inconclusive) — refusing to install (fail closed)"
elif [ -n "$ARTIFACT_ARCH" ] && [ "$ARTIFACT_ARCH" != "$HOST_ARCH" ]; then
    die "binary architecture ($ARTIFACT_ARCH) does not match host ($HOST_ARCH) — rejected, nothing installed"
fi

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
mkdir -p "$RELEASE_DIR" "$CONFIG_DIR" "$DATA_DIR" "$DATA_DIR/ota/pending" "$DATA_DIR/ota" "$PREFIX" "$(root_path "$GEOCAM_LIBEXEC_DIR")" "$(root_path "$GEOCAM_OTA_STAGING_DIR")"
chmod 0700 "$(root_path "$GEOCAM_OTA_STAGING_DIR")"
chmod 0750 "$CONFIG_DIR"
chmod 0700 "$DATA_DIR"
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
mkdir -p "$RELEASE_DIR/scripts" "$RELEASE_DIR/systemd" "$RELEASE_DIR/config"
cp "$SCRIPT_DIR"/*.sh "$RELEASE_DIR/scripts/"
cp "$SCRIPT_DIR/../systemd"/*.in "$RELEASE_DIR/systemd/"
cp "$SCRIPT_DIR/../config"/* "$RELEASE_DIR/config/"
chmod 0755 "$RELEASE_DIR/scripts"/*.sh 2>/dev/null || true
cp "$SCRIPT_DIR/ota-updater.sh" "$(root_path "$GEOCAM_LIBEXEC_DIR")/ota-updater.sh"
chmod 0755 "$(root_path "$GEOCAM_LIBEXEC_DIR")/ota-updater.sh"

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
# If this release bundles ffmpeg, point GEOCAM_VIDEO_FFMPEG_PATH at it
# deterministically (via `current`, so it keeps working across future
# releases) instead of relying on a global "ffmpeg" on $PATH. It's emitted
# as an Environment= line ahead of EnvironmentFile= below, so an operator
# override of GEOCAM_VIDEO_FFMPEG_PATH in geocam-edge.env still wins (later
# definitions of the same variable override earlier ones in systemd).
FFMPEG_ENV_LINE=""
if [ -f "$RELEASE_DIR/ffmpeg" ]; then
    FFMPEG_ENV_LINE="Environment=GEOCAM_VIDEO_FFMPEG_PATH=$GEOCAM_PREFIX/current/ffmpeg"
fi

mkdir -p "$SYSTEMD_DIR"
sed \
    -e "s|@GEOCAM_EXEC_PATH@|$GEOCAM_PREFIX/current/geocam-edge|g" \
    -e "s|@GEOCAM_FFMPEG_ENV_LINE@|$FFMPEG_ENV_LINE|g" \
    -e "s|@GEOCAM_ENV_FILE@|$GEOCAM_CONFIG_DIR/geocam-edge.env|g" \
    -e "s|@GEOCAM_SERVICE_USER@|$GEOCAM_SERVICE_USER|g" \
    -e "s|@GEOCAM_SERVICE_GROUP@|$GEOCAM_SERVICE_GROUP|g" \
    -e "s|@GEOCAM_DATA_DIR_PLACEHOLDER@|$GEOCAM_DATA_DIR|g" \
    "$SCRIPT_DIR/../systemd/geocam-edge.service.in" > "$SYSTEMD_DIR/geocam-edge.service"
log "wrote systemd unit to $SYSTEMD_DIR/geocam-edge.service"

if [ -f "$SCRIPT_DIR/../systemd/geocam-edge-bootstrap.service.in" ]; then
    sed \
        -e "s|@GEOCAM_BOOTSTRAP_EXEC@|$GEOCAM_PREFIX/current/scripts/bootstrap.sh|g" \
        -e "s|@GEOCAM_ENV_FILE@|$GEOCAM_CONFIG_DIR/geocam-edge.env|g" \
        -e "s|@GEOCAM_DATA_DIR_PLACEHOLDER@|$GEOCAM_DATA_DIR|g" \
        "$SCRIPT_DIR/../systemd/geocam-edge-bootstrap.service.in" > "$SYSTEMD_DIR/geocam-edge-bootstrap.service"
    log "wrote systemd unit to $SYSTEMD_DIR/geocam-edge-bootstrap.service"
fi

if [ -f "$SCRIPT_DIR/../systemd/geocam-edge-ota-updater.service.in" ]; then
    sed \
        -e "s|@GEOCAM_OTA_UPDATER_EXEC@|$GEOCAM_LIBEXEC_DIR/ota-updater.sh|g" \
        -e "s|@GEOCAM_DATA_DIR_PLACEHOLDER@|$GEOCAM_DATA_DIR|g" \
        -e "s|@GEOCAM_PREFIX_PLACEHOLDER@|$GEOCAM_PREFIX|g" \
        -e "s|@GEOCAM_CONFIG_DIR_PLACEHOLDER@|$GEOCAM_CONFIG_DIR|g" \
        -e "s|@GEOCAM_SYSTEMD_DIR_PLACEHOLDER@|$GEOCAM_SYSTEMD_DIR|g" \
        -e "s|@GEOCAM_LIBEXEC_PLACEHOLDER@|$GEOCAM_LIBEXEC_DIR|g" \
        -e "s|@GEOCAM_OTA_STAGING_PLACEHOLDER@|$GEOCAM_OTA_STAGING_DIR|g" \
        "$SCRIPT_DIR/../systemd/geocam-edge-ota-updater.service.in" > "$SYSTEMD_DIR/geocam-edge-ota-updater.service"
    sed \
        -e "s|@GEOCAM_DATA_DIR_PLACEHOLDER@|$GEOCAM_DATA_DIR|g" \
        "$SCRIPT_DIR/../systemd/geocam-edge-ota-updater.path.in" > "$SYSTEMD_DIR/geocam-edge-ota-updater.path"
    log "wrote privileged OTA updater units to $SYSTEMD_DIR"
fi

# --- 7. Ownership: the daemon owns only its data. Release trees, updater
#        scripts and binaries are root-owned on real Linux targets, so the
#        unprivileged daemon cannot replace the code executed by root OTA. --
if is_real_linux_target; then
    chown -R "$GEOCAM_SERVICE_USER:$GEOCAM_SERVICE_GROUP" "$DATA_DIR"
    chown -R root:root "$PREFIX" "$(root_path "$GEOCAM_LIBEXEC_DIR")"
    chmod 0755 "$PREFIX" "$(root_path "$GEOCAM_LIBEXEC_DIR")"
    chown "root:$GEOCAM_SERVICE_GROUP" "$CONFIG_DIR" "$ENV_FILE"
fi

# --- 8. Enable (never force-start here — 'geocam-edge saas check' / enroll --
#        typically needs to happen first) on real Linux targets only.
if is_real_linux_target && have_cmd systemctl; then
    systemctl daemon-reload
    systemctl enable geocam-edge.service
    if [ -f "$SYSTEMD_DIR/geocam-edge-bootstrap.service" ]; then
        systemctl enable geocam-edge-bootstrap.service
    fi
    if [ -f "$SYSTEMD_DIR/geocam-edge-ota-updater.path" ]; then
        systemctl enable geocam-edge-ota-updater.path
    fi
    log "enabled geocam-edge services (run bootstrap or manual enrollment before start)"
else
    log "skipping systemctl enable (no systemd on this target, or staged/test install)"
fi

if [ "$DATA_DIR_PRE_EXISTING" = "1" ]; then
    log "existing identity/credentials in $DATA_DIR preserved"
fi
log "install complete: $PREFIX/current -> releases/$VERSION"
