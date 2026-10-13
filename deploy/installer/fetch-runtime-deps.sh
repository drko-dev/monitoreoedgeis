#!/usr/bin/env bash
# Fetches, verifies, and stages the hermetic Python 3.11 Standalone runtime
# for macOS Apple Silicon (darwin-arm64) used by GEO CAM Edge.
#
# Usage:
#   fetch-runtime-deps.sh [--target-dir <dir>] [--platform <darwin-arm64>] [--verify-only]
#                         [--fault-inject <stage>] [--force]
#
# Security & Reliability Guarantees:
#   - Pinned exact release (no 'latest').
#   - Fails closed on SHA-256 checksum mismatch BEFORE extraction.
#   - Secure archive extraction prevents path traversal, absolute paths, and symlink/hardlink escapes.
#   - Staging occurs strictly in the same filesystem as destination ($TARGET_DIR/.python.tmp.$$).
#   - Existing valid runtime is NEVER touched until new distribution is 100% verified.
#   - Explicit state machine manages preparation, backup, activation, verification, and commit.
#   - Rollback restores previous valid runtime if activation or post-activation verification fails.
#   - Previous valid backup is NEVER deleted until post-activation verification succeeds.
#   - If rollback fails, the backup directory is safely preserved and its location logged.
#   - Signal handlers prevent destructive double cleanup or deletion of recoverable backups.
#   - Deterministic fault injection support for automated reliability testing.
set -euo pipefail

TARGET_DIR="runtime"
PLATFORM="darwin-arm64"
VERIFY_ONLY="false"
FAULT_INJECT=""
FORCE="false"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --target-dir)
      TARGET_DIR="$2"
      shift 2
      ;;
    --platform)
      PLATFORM="$2"
      shift 2
      ;;
    --verify-only)
      VERIFY_ONLY="true"
      shift
      ;;
    --fault-inject)
      FAULT_INJECT="$2"
      shift 2
      ;;
    --force)
      FORCE="true"
      shift
      ;;
    -h|--help)
      echo "usage: $0 [--target-dir <dir>] [--platform <darwin-arm64>] [--verify-only] [--fault-inject <stage>] [--force]"
      exit 0
      ;;
    *)
      echo "::error::Unknown argument: $1" >&2
      exit 1
      ;;
  esac
done

fail() { echo "::error::$*" >&2; exit 1; }

# Platform checks
if [ "$PLATFORM" != "darwin-arm64" ]; then
  fail "Platform '$PLATFORM' is not supported in H2 (only darwin-arm64 is implemented)."
fi

# Pinned metadata for Python 3.11.9 Standalone (astral-sh/python-build-standalone)
PYTHON_VERSION="3.11.9"
RELEASE_TAG="20240415"
ARCHIVE_NAME="cpython-3.11.9+20240415-aarch64-apple-darwin-install_only.tar.gz"
DOWNLOAD_URL="https://github.com/astral-sh/python-build-standalone/releases/download/${RELEASE_TAG}/${ARCHIVE_NAME}"
EXPECTED_ARCHIVE_SHA256="7af7058f7c268b4d87ed7e08c2c7844ef8460863b3e679db3afdce8bb1eedfae"
EXPECTED_ARCHIVE_SIZE=17915401

EXPECTED_BINARY_REL="python/bin/python3.11"
EXPECTED_BINARY_SHA256="906779570632e91fc62730f51b2b3ef0327d4992d989d6c9a2b6e7118836c170"
EXPECTED_BINARY_SIZE=50168

calc_sha256() {
  local file="$1"
  if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$file" | awk '{print $1}'
  elif command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$file" | awk '{print $1}'
  else
    fail "Neither shasum nor sha256sum found on host"
  fi
}

# Verifies physical existence and cryptographic integrity of the entire distribution
verify_target_distribution() {
  local root="$1"
  local bin="$root/$EXPECTED_BINARY_REL"
  local symlink="$root/python/bin/python3"
  local dylib="$root/python/lib/libpython3.11.dylib"
  local stdlib="$root/python/lib/python3.11"
  local os_mod="$root/python/lib/python3.11/os.py"
  local dynload="$root/python/lib/python3.11/lib-dynload"

  # 1. Main executable binary
  [ -f "$bin" ] || return 1
  [ -x "$bin" ] || return 1

  local actual_size
  actual_size=$(wc -c < "$bin" | tr -d ' ')
  [ "$actual_size" -eq "$EXPECTED_BINARY_SIZE" ] || return 1

  local actual_hash
  actual_hash=$(calc_sha256 "$bin")
  [ "$actual_hash" = "$EXPECTED_BINARY_SHA256" ] || return 1

  # 2. Symlink python3 pointing to python3.11
  [ -e "$symlink" ] || return 1

  # 3. Mach-O architecture on macOS
  if [ "$(uname -s)" = "Darwin" ]; then
    file -b "$bin" | grep -q 'Mach-O 64-bit.*arm64' || return 1
    # 4. Dynamic library
    [ -f "$dylib" ] || return 1
    file -b "$dylib" | grep -q 'Mach-O 64-bit.*arm64' || return 1
  fi

  # 5. Standard library completeness
  [ -d "$stdlib" ] || return 1
  [ -f "$os_mod" ] || return 1
  [ -d "$dynload" ] || return 1

  return 0
}

# If verify-only requested
if [ "$VERIFY_ONLY" = "true" ]; then
  if verify_target_distribution "$TARGET_DIR"; then
    echo "ok: target distribution $TARGET_DIR is valid"
    exit 0
  else
    fail "verify-only failed: $TARGET_DIR does not contain valid Python standalone distribution"
  fi
fi

# Check if target is already valid and not forced
if [ "$FORCE" != "true" ] && verify_target_distribution "$TARGET_DIR"; then
  echo "ok: $TARGET_DIR already contains verified Python $PYTHON_VERSION standalone"
  exit 0
fi

# Ensure TARGET_DIR exists so staging and backups occur on the exact SAME filesystem
mkdir -p "$TARGET_DIR"

# State machine variables
STATE="INIT"
IN_CLEANUP=""

# Staging, backup, and target paths on the exact SAME filesystem
TARGET_PYTHON="$TARGET_DIR/python"
staging_tmp="$TARGET_DIR/.python.tmp.$$"
backup_old="$TARGET_DIR/.python.old.$$"
broken_tmp="$TARGET_DIR/.python.broken.$$"
work=""

perform_rollback() {
  local reason="$1"
  echo "::warning::Initiating rollback due to: $reason (prior state: $STATE)" >&2
  STATE="ROLLING_BACK"

  # Move aside target if it was created during activation
  if [ -e "$TARGET_PYTHON" ] || [ -L "$TARGET_PYTHON" ]; then
    rm -rf "$broken_tmp" 2>/dev/null || true
    mv "$TARGET_PYTHON" "$broken_tmp" 2>/dev/null || rm -rf "$TARGET_PYTHON" 2>/dev/null || true
  fi

  # Fault injection for simulated restore failure
  if [ "${FAULT_INJECT}" = "restore_fail" ]; then
    STATE="ROLLBACK_FAILED"
    echo "::error::FAULT_INJECTION: simulated failure during backup restoration" >&2
    echo "::error::CRITICAL: Backup restoration failed. Previous valid runtime is SAFE and PRESERVED at: $backup_old" >&2
    echo "::error::Manual recovery command: mv \"$backup_old\" \"$TARGET_PYTHON\"" >&2
    return 1
  fi

  if [ -d "$backup_old" ]; then
    if mv "$backup_old" "$TARGET_PYTHON"; then
      STATE="ROLLED_BACK"
      echo "::notice::Rollback successful: previous valid runtime restored to $TARGET_PYTHON" >&2
      [ -d "$broken_tmp" ] && rm -rf "$broken_tmp" 2>/dev/null || true
      return 0
    else
      STATE="ROLLBACK_FAILED"
      echo "::error::CRITICAL: Failed to restore backup from $backup_old to $TARGET_PYTHON" >&2
      echo "::error::The previous valid runtime is SAFE and PRESERVED at: $backup_old" >&2
      echo "::error::Manual recovery command: mv \"$backup_old\" \"$TARGET_PYTHON\"" >&2
      return 1
    fi
  else
    echo "::notice::No backup found at $backup_old (fresh installation); nothing to rollback" >&2
    return 0
  fi
}

cleanup() {
  local sig="${1:-EXIT}"
  local exit_code="${2:-$?}"
  # Disable traps immediately to prevent re-entrant calls
  trap - EXIT INT TERM HUP
  [ -z "$IN_CLEANUP" ] || return $exit_code
  IN_CLEANUP=1

  # If triggered by a signal, ensure exit_code reflects signal termination
  if [ "$sig" != "EXIT" ] && [ "$exit_code" -eq 0 ]; then
    case "$sig" in
      INT)  exit_code=130 ;;
      TERM) exit_code=143 ;;
      HUP)  exit_code=129 ;;
      *)    exit_code=1 ;;
    esac
  fi

  if [ "${FAULT_INJECT}" = "sigint_in_cleanup" ]; then
    echo "::notice::Fault injection: simulated signal during cleanup" >&2
  fi

  # Filesystem evidence recovery:
  # Regardless of the memory STATE variable, if an uncommitted backup physically exists on disk,
  # it indicates that a previous valid runtime was moved and must be restored!
  if [ -d "$backup_old" ] && [ "$STATE" != "COMMITTED" ]; then
    echo "::warning::Abnormal termination with active backup found on filesystem at $backup_old (state: $STATE, sig: $sig, exit code: $exit_code). Attempting rollback..." >&2
    if perform_rollback "filesystem_evidence_${STATE}_${sig}_${exit_code}"; then
      rm -rf "$staging_tmp" "$broken_tmp" 2>/dev/null || true
    else
      echo "::error::CRITICAL: Backup preserved at: $backup_old" >&2
    fi
    [ -n "${work:-}" ] && [ -d "$work" ] && rm -rf "$work" 2>/dev/null || true
    exit $exit_code
  fi

  case "$STATE" in
    COMMITTED)
      # Normal successful exit: remove temporary staging, backup, and work directories
      rm -rf "$staging_tmp" 2>/dev/null || true
      [ -d "$backup_old" ] && rm -rf "$backup_old" 2>/dev/null || true
      [ -n "${work:-}" ] && [ -d "$work" ] && rm -rf "$work" 2>/dev/null || true
      ;;
    ROLLED_BACK)
      # Successful rollback: backup was restored to TARGET_PYTHON.
      # Clean up failed staging and broken dirs
      rm -rf "$staging_tmp" "$broken_tmp" 2>/dev/null || true
      [ -n "${work:-}" ] && [ -d "$work" ] && rm -rf "$work" 2>/dev/null || true
      ;;
    ROLLBACK_FAILED)
      # CRITICAL: DO NOT DELETE BACKUP!
      echo "::error::EMERGENCY: Rollback could not be completed automatically." >&2
      echo "::error::The previous valid runtime is SAFE and PRESERVED at: $backup_old" >&2
      echo "::error::Manual recovery command: mv \"$backup_old\" \"$TARGET_PYTHON\"" >&2
      rm -rf "$staging_tmp" 2>/dev/null || true
      [ -n "${work:-}" ] && [ -d "$work" ] && rm -rf "$work" 2>/dev/null || true
      ;;
    BACKED_UP|ACTIVATING|ACTIVATED|VERIFYING|ROLLING_BACK)
      # Signal or abnormal exit caught during the transition window!
      # Attempt rollback to preserve the valid runtime!
      echo "::warning::Abnormal termination in state '$STATE' (sig: $sig, exit code: $exit_code). Attempting rollback..." >&2
      if perform_rollback "trap_${sig}_${exit_code}"; then
        rm -rf "$staging_tmp" "$broken_tmp" 2>/dev/null || true
      else
        echo "::error::CRITICAL: Backup preserved at: $backup_old" >&2
      fi
      [ -n "${work:-}" ] && [ -d "$work" ] && rm -rf "$work" 2>/dev/null || true
      ;;
    *)
      # INIT, PREPARING, STAGED: target was NEVER modified!
      rm -rf "$staging_tmp" 2>/dev/null || true
      [ -n "${work:-}" ] && [ -d "$work" ] && rm -rf "$work" 2>/dev/null || true
      ;;
  esac

  exit $exit_code
}

trap 'cleanup INT 130' INT
trap 'cleanup TERM 143' TERM
trap 'cleanup HUP 129' HUP
trap 'cleanup EXIT $?' EXIT

STATE="PREPARING"
work=$(mktemp -d)
archive_path="$work/$ARCHIVE_NAME"

if [ -n "${GEOCAM_RUNTIME_CACHE_DIR:-}" ] && [ -f "${GEOCAM_RUNTIME_CACHE_DIR}/${ARCHIVE_NAME}" ]; then
  echo "Using cached archive from ${GEOCAM_RUNTIME_CACHE_DIR}/${ARCHIVE_NAME}..."
  cp "${GEOCAM_RUNTIME_CACHE_DIR}/${ARCHIVE_NAME}" "$archive_path"
else
  echo "Downloading Python $PYTHON_VERSION Standalone for $PLATFORM..."
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL -o "$archive_path" "$DOWNLOAD_URL"
  elif command -v wget >/dev/null 2>&1; then
    wget -q -O "$archive_path" "$DOWNLOAD_URL"
  else
    fail "Neither curl nor wget available to fetch runtime dependency"
  fi
  if [ -n "${GEOCAM_RUNTIME_CACHE_DIR:-}" ]; then
    mkdir -p "${GEOCAM_RUNTIME_CACHE_DIR}"
    cp "$archive_path" "${GEOCAM_RUNTIME_CACHE_DIR}/${ARCHIVE_NAME}"
  fi
fi

# 1. Pre-extraction cryptographic verification
echo "Verifying archive cryptographic integrity..."
archive_size=$(wc -c < "$archive_path" | tr -d ' ')
[ "$archive_size" -eq "$EXPECTED_ARCHIVE_SIZE" ] || fail "Archive size mismatch: got $archive_size, expected $EXPECTED_ARCHIVE_SIZE"

archive_hash=$(calc_sha256 "$archive_path")
[ "$archive_hash" = "$EXPECTED_ARCHIVE_SHA256" ] || fail "Archive SHA-256 mismatch: got $archive_hash, expected $EXPECTED_ARCHIVE_SHA256"

# Fault injection: error before activation
if [ "${FAULT_INJECT}" = "before_activate" ]; then
  fail "FAULT_INJECTION: simulated error before activation"
fi

# 2. Secure extraction directly into isolated staging directory on the same filesystem
mkdir -p "$staging_tmp"
echo "Extracting runtime archive securely..."

if command -v go >/dev/null 2>&1; then
  go run "$REPO_ROOT/cmd/runtime-extractor" \
    --archive "$archive_path" \
    --target "$staging_tmp" \
    --expected-sha256 "$EXPECTED_ARCHIVE_SHA256" \
    --stage=false
else
  fail "Go compiler required to execute secure runtime-extractor"
fi

# Ensure execution permissions on bin
staged_python="$staging_tmp/python"
[ -d "$staged_python" ] || fail "Extracted archive does not contain 'python' directory"
chmod -R +x "$staged_python/bin"

# 3. Full distribution verification on staged directory BEFORE touching target
echo "Verifying complete staged Python distribution..."
if ! verify_target_distribution "$staging_tmp"; then
  fail "Verification of staged Python distribution failed; target runtime was NOT modified"
fi
STATE="STAGED"

# 4. Safe backup and atomic activation sequence with rollback protection
#
# Architectural Note on Atomicity:
# In POSIX filesystems without atomic exchange syscalls (e.g. Darwin), replacing an
# existing directory with another directory requires a two-step sequence:
#   1. Move existing target to backup (.python.old.$$)
#   2. Move staging to target (python)
#
# Between step 1 and step 2, target is momentarily absent.
# To guarantee 100% reliability against interruption:
#   - Staging is verified BEFORE step 1.
#   - Signals (INT, TERM, HUP) are trapped to immediately restore backup.
#   - Backup is NEVER deleted until step 5 (final post-activation verification) passes.
#   - If step 2 or verification fails, rollback automatically restores the backup.
#   - If rollback itself encounters an unrecoverable failure, the backup is SAFELY PRESERVED.

if [ -e "$TARGET_PYTHON" ] || [ -L "$TARGET_PYTHON" ]; then
  STATE="BACKING_UP"
  if [ "${FAULT_INJECT}" = "move_old_fail" ]; then
    fail "FAULT_INJECTION: simulated error moving old runtime to backup"
  fi

  echo "Backing up previous runtime to $backup_old..."
  if ! mv "$TARGET_PYTHON" "$backup_old"; then
    fail "Failed to backup previous runtime from $TARGET_PYTHON to $backup_old"
  fi

  # Confirm backup was physically created on filesystem before progressing
  if [ ! -d "$backup_old" ]; then
    fail "Failed to verify backup creation: $backup_old does not exist on filesystem"
  fi

  # Fault injection: simulate interruption right after rename and BEFORE updating STATE to BACKED_UP
  if [ "${FAULT_INJECT}" = "sigint_during_backing_up" ] || [ "${FAULT_INJECT}" = "interrupt_during_backing_up" ]; then
    echo "::notice::FAULT_INJECTION: simulating SIGINT after rename but before STATE=BACKED_UP" >&2
    kill -INT $$
  fi

  STATE="BACKED_UP"

  if [ "${FAULT_INJECT}" = "sigint_between_moves" ]; then
    echo "::notice::FAULT_INJECTION: simulating SIGINT between renames" >&2
    kill -INT $$
  fi
fi

STATE="ACTIVATING"
if [ "${FAULT_INJECT}" = "activate_fail" ] || [ "${FAULT_INJECT}" = "restore_fail" ]; then
  echo "::error::FAULT_INJECTION: simulated failure triggering rollback (stage: ${FAULT_INJECT})" >&2
  perform_rollback "fault_inject_${FAULT_INJECT}" || true
  fail "Activation failed (fault injected: ${FAULT_INJECT}); rollback initiated"
fi

echo "Activating new runtime into $TARGET_PYTHON..."
if ! mv "$staged_python" "$TARGET_PYTHON"; then
  echo "::error::Failed to move staged runtime into $TARGET_PYTHON" >&2
  perform_rollback "mv_staging_failed"
  fail "Activation rename failed; rollback performed"
fi
STATE="ACTIVATED"

# 5. Final post-activation verification of distribution in target position
STATE="VERIFYING"
echo "Performing post-activation verification in target location..."

if [ "${FAULT_INJECT}" = "verify_fail" ]; then
  echo "::error::FAULT_INJECTION: simulated post-activation verification failure" >&2
  perform_rollback "fault_inject_verify_fail"
  fail "Final verification failed (fault injected); rollback performed"
fi

if ! verify_target_distribution "$TARGET_DIR"; then
  echo "::error::Final verification failed on activated runtime; initiating rollback" >&2
  perform_rollback "final_verification_failed"
  fail "Final verification failed in $TARGET_DIR/python; rollback performed"
fi

# 6. Commit: only delete backup after verification has 100% succeeded
STATE="COMMITTED"
if [ -d "$backup_old" ]; then
  echo "Cleaning up verified previous backup..."
  rm -rf "$backup_old"
fi
rm -rf "$staging_tmp"
echo "SUCCESS: Hermetic Python $PYTHON_VERSION Standalone safely activated in $TARGET_PYTHON"
