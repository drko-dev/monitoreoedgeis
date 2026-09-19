#!/usr/bin/env bash
# First-boot bootstrap and zero-touch enrollment for the geocam-edge appliance.
#
# Idempotent and safe:
# 1. Checks if the edge is already enrolled:
#    - If enrolled ($DATA_DIR/identity.json and credentials.json exist and are valid),
#      skips enrollment immediately.
# 2. If not enrolled, looks for an ephemeral one-time enrollment token in:
#    - GEOCAM_ENROLLMENT_TOKEN environment variable
#    - /boot/geocam-enroll.token (or /boot/firmware/geocam-enroll.token on Raspberry Pi OS)
#    - /etc/geocam-edge/enroll.token
#    - Removable media (/media/*/geocam-enroll.token, /mnt/*/geocam-enroll.token)
# 3. If a token is found:
#    - Claims it against GEOCAM_SAAS_URL using existing `geocam-edge enroll`
#      (token piped via stdin, never printed or saved to plaintext logs/config)
#    - Deletes the ephemeral seed file best-effort (flash/wear-leveling media prevents
#      cryptographically guaranteed physical erasure; treat seed file as secret while present)
#    - Ensures $DATA_DIR ownership by GEOCAM_SERVICE_USER on real Linux
# 4. Service lifecycle:
#    - When running under systemd service context (INVOCATION_ID set), finishes cleanly
#      and defers geocam-edge.service startup to systemd (Before=geocam-edge.service).
#    - When executed manually outside systemd, starts geocam-edge.service and verifies readiness.
# 5. Optionally executes a local ONVIF discovery scan (--scan) via existing CLI.
#
# Usage:
#   bootstrap.sh [--saas-url <url>] [--scan]

set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
. "$SCRIPT_DIR/lib.sh"

SAAS_URL_ARG=""
DO_SCAN=0

while [ $# -gt 0 ]; do
    case "$1" in
        --saas-url)
            [ $# -gt 1 ] || die "--saas-url requires an argument"
            SAAS_URL_ARG="$2"
            shift 2
            ;;
        --scan)
            DO_SCAN=1
            shift
            ;;
        --help|-h)
            cat <<EOF
Usage: bootstrap.sh [options]

Options:
  --saas-url <url>     SaaS base URL override (default: from /etc/geocam-edge/geocam-edge.env)
  --scan               Run immediate ONVIF discovery scan after bootstrap
  --help, -h           Show this help message
EOF
            exit 0
            ;;
        *)
            die "unknown argument: $1 (see --help)"
            ;;
    esac
done

PREFIX="$(root_path "$GEOCAM_PREFIX")"
CONFIG_DIR="$(root_path "$GEOCAM_CONFIG_DIR")"
DATA_DIR="$(root_path "$GEOCAM_DATA_DIR")"
if [ -n "${GEOCAM_BOOT_DIR:-}" ]; then
    BOOT_DIR="$GEOCAM_BOOT_DIR"
else
    BOOT_DIR="$(root_path "/boot")"
fi
if [ -n "${GEOCAM_BOOT_FIRMWARE_DIR:-}" ]; then
    BOOT_FIRMWARE_DIR="$GEOCAM_BOOT_FIRMWARE_DIR"
else
    BOOT_FIRMWARE_DIR="$(root_path "/boot/firmware")"
fi
ENV_FILE="$CONFIG_DIR/geocam-edge.env"
BINARY="$PREFIX/current/geocam-edge"

# If current symlink is missing, check if invoked next to binary or in release
if [ ! -f "$BINARY" ] && [ -f "$SCRIPT_DIR/../geocam-edge" ]; then
    BINARY="$SCRIPT_DIR/../geocam-edge"
fi

# is_systemd_service_context detects when bootstrap.sh is invoked as a systemd unit
# (systemd sets INVOCATION_ID for every service execution).
is_systemd_service_context() {
    [ -n "${INVOCATION_ID:-}" ]
}

# --- 1. Load non-secret config defaults from env file if available ---------
if [ -f "$ENV_FILE" ]; then
    while IFS='=' read -r key val || [ -n "$key" ]; do
        case "$key" in
            \#*|"") continue ;;
            GEOCAM_*)
                # strip potential enclosing quotes
                val="${val%\"}"
                val="${val#\"}"
                val="${val%\'}"
                val="${val#\'}"
                if [ -z "${!key:-}" ]; then
                    export "$key=$val"
                fi
                ;;
        esac
    done < "$ENV_FILE"
fi

if [ -n "$SAAS_URL_ARG" ]; then
    export GEOCAM_SAAS_URL="$SAAS_URL_ARG"
fi

# --- 2. Check if already enrolled -----------------------------------------
is_enrolled() {
    local cred_file="$DATA_DIR/credentials.json"
    local ident_file="$DATA_DIR/identity.json"
    if [ -s "$cred_file" ] && [ -s "$ident_file" ]; then
        if grep -q '"credential"' "$cred_file" 2>/dev/null; then
            return 0
        fi
    fi
    return 1
}

if is_enrolled; then
    log "appliance is already enrolled in $DATA_DIR; zero-touch enrollment skipped"
else
    # --- 3. Resolve ephemeral enrollment token ----------------------------
    TOKEN=""
    TOKEN_FILE=""

    if [ -n "${GEOCAM_ENROLLMENT_TOKEN:-}" ]; then
        TOKEN="$GEOCAM_ENROLLMENT_TOKEN"
    else
        # Search candidate seed locations
        CANDIDATES=(
            "$BOOT_DIR/geocam-enroll.token"
            "$BOOT_FIRMWARE_DIR/geocam-enroll.token"
            "$CONFIG_DIR/enroll.token"
        )
        for cand in "${CANDIDATES[@]}"; do
            if [ -f "$cand" ] && [ -s "$cand" ]; then
                TOKEN_FILE="$cand"
                break
            fi
        done

        # Check removable drives on live systems if not found in boot
        if [ -z "$TOKEN_FILE" ] && [ -z "${GEOCAM_INSTALL_ROOT:-}" ]; then
            for cand in /media/*/geocam-enroll.token /mnt/*/geocam-enroll.token; do
                if [ -f "$cand" ] && [ -s "$cand" ]; then
                    TOKEN_FILE="$cand"
                    break
                fi
            done
        fi

        if [ -n "$TOKEN_FILE" ]; then
            TOKEN="$(head -n 1 "$TOKEN_FILE" | tr -d '\r\n')"
            log "found ephemeral enrollment token file at $TOKEN_FILE"
        fi
    fi

    # --- 4. Execute enrollment if token present ---------------------------
    if [ -n "$TOKEN" ]; then
        [ -n "${GEOCAM_SAAS_URL:-}" ] || die "GEOCAM_SAAS_URL must be configured in environment or $ENV_FILE to enroll"
        [ -f "$BINARY" ] || die "geocam-edge binary not found at $BINARY"

        log "claiming zero-touch enrollment against $GEOCAM_SAAS_URL"

        # Pipe token to existing `geocam-edge enroll` CLI command
        if printf '%s\n' "$TOKEN" | GEOCAM_DATA_DIR="$DATA_DIR" GEOCAM_CONFIG_DIR="$CONFIG_DIR" "$BINARY" enroll; then
            log "zero-touch enrollment completed successfully"
        else
            die "zero-touch enrollment failed"
        fi

        # Ensure correct ownership of newly written identity and credentials
        if is_real_linux_target && [ "$(id -u)" = "0" ]; then
            chown -R "$GEOCAM_SERVICE_USER:$GEOCAM_SERVICE_GROUP" "$DATA_DIR"
            chmod 0700 "$DATA_DIR"
            chmod 0600 "$DATA_DIR/identity.json" "$DATA_DIR/credentials.json" 2>/dev/null || true
        fi

        # Best-effort deletion of ephemeral token seed file (never persist in plaintext).
        # Note: flash storage wear-leveling prevents cryptographically guaranteed physical
        # erasure, so the seed file must be treated as secret while it exists.
        if [ -n "$TOKEN_FILE" ] && [ -f "$TOKEN_FILE" ]; then
            if have_cmd shred; then
                shred -u "$TOKEN_FILE" 2>/dev/null || rm -f "$TOKEN_FILE"
            else
                rm -f "$TOKEN_FILE"
            fi
            log "removed ephemeral enrollment token seed file"
        fi

        # Clear token variable from memory
        unset TOKEN
        unset GEOCAM_ENROLLMENT_TOKEN
    else
        log "no enrollment token detected; appliance awaiting zero-touch token or manual enrollment"
    fi
fi

# --- 5. Service startup (manual execution only; deferred under systemd) ----
if is_systemd_service_context; then
    log "running under systemd service context ($INVOCATION_ID): deferring geocam-edge.service startup to systemd (Before=geocam-edge.service)"
elif is_real_linux_target && have_cmd systemctl; then
    if is_enrolled; then
        if ! systemctl is-active --quiet geocam-edge.service; then
            systemctl start geocam-edge.service
            log "started geocam-edge.service"
        fi

        if [ -f "$SCRIPT_DIR/wait-ready.sh" ]; then
            if "$SCRIPT_DIR/wait-ready.sh"; then
                log "appliance verified READY"
            else
                log "warning: geocam-edge service did not report ready within timeout"
            fi
        fi
    else
        log "geocam-edge.service not started: awaiting enrollment"
    fi
else
    log "no systemd on this target (or staged/test run): service management skipped"
fi

# --- 6. Optional post-enrollment discovery scan ---------------------------
if [ "$DO_SCAN" = "1" ]; then
    if [ -f "$BINARY" ]; then
        log "running local ONVIF discovery scan..."
        "$BINARY" discovery scan || log "discovery scan finished with non-zero status"
    fi
fi

log "bootstrap completed"
exit 0
