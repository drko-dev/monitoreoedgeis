#!/usr/bin/env bash
# Shared helpers for the geocam-edge appliance install/update/rollback
# scripts. POSIX-friendly bash (no bash4+ features: arrays are avoided).
#
# All scripts source this file, then honor GEOCAM_INSTALL_ROOT (default:
# empty / real filesystem root) as a path prefix, the same way `DESTDIR`
# works for a staged install. This is what lets the test suite exercise the
# exact same code path against a throwaway directory instead of the real
# system, and is NOT a documented end-user knob for a production appliance.

set -euo pipefail

: "${GEOCAM_INSTALL_ROOT:=}"
: "${GEOCAM_PREFIX:=/opt/geocam-edge}"
: "${GEOCAM_CONFIG_DIR:=/etc/geocam-edge}"
: "${GEOCAM_DATA_DIR:=/var/lib/geocam-edge}"
: "${GEOCAM_SYSTEMD_DIR:=/etc/systemd/system}"
: "${GEOCAM_SERVICE_USER:=geocam-edge}"
: "${GEOCAM_SERVICE_GROUP:=geocam-edge}"

root_path() {
    # Joins GEOCAM_INSTALL_ROOT with an absolute path, e.g.
    # root_path /opt/geocam-edge -> $GEOCAM_INSTALL_ROOT/opt/geocam-edge
    printf '%s%s\n' "$GEOCAM_INSTALL_ROOT" "$1"
}

log() { printf '[geocam-edge] %s\n' "$*"; }
err() { printf '[geocam-edge] error: %s\n' "$*" >&2; }
die() { err "$*"; exit 1; }

# host_arch prints amd64/arm64, or dies on anything else — the appliance
# only ships those two architectures (see Makefile build-linux).
host_arch() {
    case "$(uname -m)" in
        x86_64|amd64) echo "amd64" ;;
        aarch64|arm64) echo "arm64" ;;
        *) die "unsupported architecture: $(uname -m) (geocam-edge only ships amd64/arm64)" ;;
    esac
}

# is_real_linux_target reports whether this run should perform actual
# privileged system changes (useradd, systemctl, chown to a system user).
# It is false whenever we're staged under GEOCAM_INSTALL_ROOT (tests) or not
# running on Linux (this repo's dev/CI sandbox has no systemd) — in both
# cases the script still creates the full directory/file layout so that
# behavior is fully exercised, it just skips steps that require a real
# Linux init system or root-owned users.
is_real_linux_target() {
    [ -z "$GEOCAM_INSTALL_ROOT" ] && [ "$(uname -s)" = "Linux" ]
}

have_cmd() { command -v "$1" >/dev/null 2>&1; }

# atomic_symlink_swap points $1 (an existing or new symlink path) at $2
# (target, relative to the symlink's directory).
#
# ponytail: this is `ln -sfn` directly, not a create-temp-then-rename dance.
# The obvious "safer" version — `ln -sfn target link.tmp.$$ && mv link.tmp.$$
# link` — is actually WRONG here and was the first version of this function:
# when `link` already exists and resolves (through the symlink) to a
# directory, both GNU and BSD `mv` treat an existing symlink-to-directory
# destination as "move source INTO that directory", not "replace this
# entry" — so the swap silently no-ops and `current` keeps pointing at the
# old release. `ln`'s own `-n` flag exists precisely to avoid that
# dereference: `-f` overwrites the existing entry, `-n` means "as a file,
# not through the directory it points to". `ln -sfn` is unlink+link
# internally on both platforms, so there's a sub-millisecond window with no
# symlink present — acceptable for a single-writer install/update/rollback
# script; upgrade to renameat2/copy_file_range-based swap if concurrent
# swaps or true crash-atomicity ever matter here.
atomic_symlink_swap() {
    ln -sfn "$2" "$1"
}
