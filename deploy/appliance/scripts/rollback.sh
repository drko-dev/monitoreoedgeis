#!/usr/bin/env bash
# Reverts `current` to the previously active release (recorded by
# install.sh/update.sh in $PREFIX/.previous every time `current` is
# repointed), restarts the service, and verifies /readyz. Never touches
# GEOCAM_DATA_DIR, credentials, or identity — only the `current` symlink and
# the running service.
#
# Usage: rollback.sh
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
. "$SCRIPT_DIR/lib.sh"

PREFIX="$(root_path "$GEOCAM_PREFIX")"
PREV_FILE="$PREFIX/.previous"

[ -f "$PREV_FILE" ] || die "no previous release recorded at $PREV_FILE — nothing to roll back to"
PREV_TARGET="$(cat "$PREV_FILE")"
[ -n "$PREV_TARGET" ] || die "$PREV_FILE is empty — nothing to roll back to"
[ -d "$PREFIX/$PREV_TARGET" ] || die "recorded previous release $PREV_TARGET no longer exists on disk"

CURRENT_TARGET=""
[ -L "$PREFIX/current" ] && CURRENT_TARGET="$(readlink "$PREFIX/current")"

atomic_symlink_swap "$PREFIX/current" "$PREV_TARGET"
log "rolled back: $PREFIX/current -> $PREV_TARGET"

# The release we just rolled back FROM becomes the new "previous", so a
# rollback is itself reversible by re-running update.sh/rollback.sh — but
# only if we actually changed anything.
if [ -n "$CURRENT_TARGET" ] && [ "$CURRENT_TARGET" != "$PREV_TARGET" ]; then
    echo "$CURRENT_TARGET" > "$PREV_FILE"
fi

if is_real_linux_target && have_cmd systemctl; then
    systemctl restart geocam-edge.service
    log "restarted geocam-edge.service"
    if ! "$SCRIPT_DIR/wait-ready.sh"; then
        die "geocam-edge did not become ready after rollback to $PREV_TARGET — manual intervention required"
    fi
    log "rollback to $PREV_TARGET verified ready"
else
    log "no systemd on this target (or staged/test install): activated $PREV_TARGET, restart/health verification skipped"
fi
