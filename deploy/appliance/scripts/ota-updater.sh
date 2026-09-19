#!/usr/bin/env bash
# Root-only OTA apply boundary. The Edge daemon never executes this script.
# It accepts exactly one fixed-path request and revalidates the complete
# pending release before invoking the existing versioned update path.
set -euo pipefail
: "${GEOCAM_INSTALL_ROOT:=}"
: "${GEOCAM_PREFIX:=/opt/geocam-edge}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
. "${GEOCAM_INSTALL_ROOT}${GEOCAM_PREFIX}/current/scripts/lib.sh"

OTA_DIR="$(root_path "$GEOCAM_DATA_DIR")/ota"
REQUEST="$OTA_DIR/apply.request"
PENDING="$OTA_DIR/pending"
PREFIX="$(root_path "$GEOCAM_PREFIX")"
STATE="$OTA_DIR/state"

atomic_state() {
    local value="$1" tmp
    tmp="$OTA_DIR/.state.tmp.$$"
    printf '%s\n' "$value" > "$tmp"
    chmod 0600 "$tmp"
    mv "$tmp" "$STATE"
}

[ -d "$OTA_DIR" ] || exit 0
[ -f "$REQUEST" ] || exit 0
[ ! -L "$REQUEST" ] || { atomic_state "failed:request-symlink"; rm -f "$REQUEST"; exit 1; }

request="$(cat "$REQUEST")"
case "$request" in
    *$'\n'*|*\r*|""|*[!A-Za-z0-9._-]*)
        atomic_state "failed:invalid-request"
        rm -f "$REQUEST"
        exit 1
        ;;
esac
release_id="$request"
case "$release_id" in
    [A-Za-z0-9]*|[A-Za-z0-9]*[A-Za-z0-9._-]) ;;
    *) atomic_state "failed:invalid-release-id"; rm -f "$REQUEST"; exit 1 ;;
esac
[ "${#release_id}" -le 64 ] || { atomic_state "failed:release-id-too-long"; rm -f "$REQUEST"; exit 1; }

release_dir="$PENDING/$release_id"
pending_real="$(realpath "$PENDING" 2>/dev/null || true)"
release_real="$(realpath "$release_dir" 2>/dev/null || true)"
if [ -z "$pending_real" ] || [ -z "$release_real" ] || [ "$release_real" = "$pending_real" ]; then
    atomic_state "failed:path-validation"
    rm -f "$REQUEST"
    exit 1
fi
case "$release_real" in
    "$pending_real"/*) ;;
    *) atomic_state "failed:path-escape"; rm -f "$REQUEST"; exit 1 ;;
esac

artifact="$release_real/artifact.tar.gz"
for required in "$artifact" "$release_real/SHA256SUMS" "$release_real/SHA256SUMS.sig" "$release_real/metadata.json"; do
    [ -f "$required" ] && [ ! -L "$required" ] || { atomic_state "failed:missing-or-symlinked-input"; rm -f "$REQUEST"; exit 1; }
done

# The current release is root-owned and is the only verifier accepted here.
# It owns the Ed25519 policy and checksum implementation; this boundary does
# not create a second cryptographic protocol.
verifier="$PREFIX/current/geocam-edge"
[ -x "$verifier" ] || { atomic_state "failed:missing-verifier"; rm -f "$REQUEST"; exit 1; }
if ! "$verifier" ota verify --artifact-dir "$release_real"; then
    atomic_state "failed:verification"
    mv "$REQUEST" "$OTA_DIR/apply.request.processed"
    exit 1
fi

atomic_state "applying:$release_id"
if ! GEOCAM_VERSION="$release_id" \
    GEOCAM_DATA_DIR="$GEOCAM_DATA_DIR" \
    GEOCAM_PREFIX="$GEOCAM_PREFIX" \
    GEOCAM_CONFIG_DIR="$GEOCAM_CONFIG_DIR" \
    GEOCAM_SYSTEMD_DIR="$GEOCAM_SYSTEMD_DIR" \
    "$PREFIX/current/scripts/update.sh" "$artifact"; then
    atomic_state "failed:apply"
    mv "$REQUEST" "$OTA_DIR/apply.request.processed"
    exit 1
fi

atomic_state "succeeded:$release_id"
mv "$REQUEST" "$OTA_DIR/apply.request.processed"
