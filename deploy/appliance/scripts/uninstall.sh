#!/usr/bin/env bash
# Removes the geocam-edge service, binaries and systemd unit. By default
# GEOCAM_DATA_DIR (identity/credentials/offline buffer) and the config file
# are left completely untouched — this is deliberate: uninstalling should
# never be an accidental way to lose enrollment or the offline buffer.
#
# Usage: uninstall.sh [--purge]
#   --purge   ALSO deletes GEOCAM_DATA_DIR and GEOCAM_CONFIG_DIR. Requires
#             typing the literal word "yes" at a confirmation prompt (skip
#             the prompt only via GEOCAM_UNINSTALL_PURGE_CONFIRM=yes, e.g.
#             for scripted test runs).
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
. "$SCRIPT_DIR/lib.sh"

PURGE=0
[ "${1:-}" = "--purge" ] && PURGE=1

PREFIX="$(root_path "$GEOCAM_PREFIX")"
CONFIG_DIR="$(root_path "$GEOCAM_CONFIG_DIR")"
DATA_DIR="$(root_path "$GEOCAM_DATA_DIR")"
SYSTEMD_DIR="$(root_path "$GEOCAM_SYSTEMD_DIR")"

if is_real_linux_target && have_cmd systemctl; then
    systemctl stop geocam-edge.service geocam-edge-bootstrap.service 2>/dev/null || true
    systemctl disable geocam-edge.service geocam-edge-bootstrap.service 2>/dev/null || true
fi
rm -f "$SYSTEMD_DIR/geocam-edge.service" "$SYSTEMD_DIR/geocam-edge-bootstrap.service"
is_real_linux_target && have_cmd systemctl && systemctl daemon-reload || true

# Binaries/releases only — never the data or config dirs, regardless of
# --purge's outcome below (they are removed explicitly, one at a time, only
# after confirmation).
rm -rf "$PREFIX"
log "removed $PREFIX (binaries/releases) and $SYSTEMD_DIR/geocam-edge.service"
log "preserved: $DATA_DIR (identity/credentials/buffer), $CONFIG_DIR (config)"

if [ "$PURGE" = "1" ]; then
    confirm="${GEOCAM_UNINSTALL_PURGE_CONFIRM:-}"
    if [ "$confirm" != "yes" ]; then
        printf 'Type "yes" to permanently delete %s and %s: ' "$DATA_DIR" "$CONFIG_DIR"
        read -r confirm
    fi
    if [ "$confirm" = "yes" ]; then
        rm -rf "$DATA_DIR" "$CONFIG_DIR"
        log "purged $DATA_DIR and $CONFIG_DIR"
    else
        log "purge not confirmed — $DATA_DIR and $CONFIG_DIR left intact"
    fi
fi
