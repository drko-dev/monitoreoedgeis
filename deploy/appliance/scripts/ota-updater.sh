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
STAGING_ROOT="$(root_path "$GEOCAM_OTA_STAGING_DIR")"

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

mkdir -p "$STAGING_ROOT"
chmod 0700 "$STAGING_ROOT"
SNAPSHOT="$(mktemp -d "$STAGING_ROOT/release.XXXXXX")"
SNAPSHOT="$(realpath "$SNAPSHOT")"
trap 'rm -rf "$SNAPSHOT"' EXIT
# Snapshot first. The daemon owns the source tree, but cannot modify this
# private root-owned staging directory after the copy completes.
cp -a "$release_real/." "$SNAPSHOT/"
chmod 0700 "$SNAPSHOT"
if is_real_linux_target; then
    chown -R root:root "$SNAPSHOT"
fi

for required in "$SNAPSHOT/SHA256SUMS" "$SNAPSHOT/SHA256SUMS.sig" "$SNAPSHOT/metadata.json"; do
    [ -f "$required" ] && [ ! -L "$required" ] || { atomic_state "failed:missing-or-symlinked-snapshot-input"; mv "$REQUEST" "$OTA_DIR/apply.request.processed"; exit 1; }
done

# The current release is root-owned and is the only verifier accepted here.
# It owns the Ed25519 policy and returns the concrete authenticated artifact
# as a single `artifact=<relative-path>` line. No second crypto protocol is
# created here.
verifier="$PREFIX/current/geocam-edge"
[ -x "$verifier" ] || { atomic_state "failed:missing-verifier"; rm -f "$REQUEST"; exit 1; }
verify_output="$("$verifier" ota verify --artifact-dir "$SNAPSHOT")" || {
    atomic_state "failed:verification"
    mv "$REQUEST" "$OTA_DIR/apply.request.processed"
    exit 1
}
artifact_rel="$(printf '%s\n' "$verify_output" | awk -F= '$1 == "artifact" {print substr($0, 10)}')"
artifact_count="$(printf '%s\n' "$verify_output" | awk -F= '$1 == "artifact" {n++} END {print n+0}')"
case "$artifact_count" in
    1) ;;
    *) atomic_state "failed:verifier-artifact-contract"; mv "$REQUEST" "$OTA_DIR/apply.request.processed"; exit 1 ;;
esac
if ! printf '%s\n' "$artifact_rel" | grep -Eq '^[A-Za-z0-9][A-Za-z0-9._-]*\.tar\.gz$'; then
    atomic_state "failed:invalid-verified-artifact"
    mv "$REQUEST" "$OTA_DIR/apply.request.processed"
    exit 1
fi
artifact="$SNAPSHOT/$artifact_rel"
artifact_real="$(realpath "$artifact" 2>/dev/null || true)"
case "$artifact_real" in
    "$SNAPSHOT"/*) ;;
    *) atomic_state "failed:artifact-path-escape"; mv "$REQUEST" "$OTA_DIR/apply.request.processed"; exit 1 ;;
esac
[ -f "$artifact_real" ] && [ ! -L "$artifact_real" ] || { atomic_state "failed:invalid-verified-artifact"; mv "$REQUEST" "$OTA_DIR/apply.request.processed"; exit 1; }

# update.sh keeps the historical local `<artifact>.sha256` path. Generate
# that handoff from the already verified immutable snapshot, rather than
# interpreting every line of a multi-artifact signed manifest.
if have_cmd sha256sum; then
    ( cd "$(dirname "$artifact_real")" && sha256sum "$(basename "$artifact_real")" > "$artifact_real.sha256" )
elif have_cmd shasum; then
    ( cd "$(dirname "$artifact_real")" && shasum -a 256 "$(basename "$artifact_real")" > "$artifact_real.sha256" )
else
    atomic_state "failed:no-sha256-tool"
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
