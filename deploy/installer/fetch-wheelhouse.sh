#!/usr/bin/env bash
# Fetches, verifies, and stages the hermetic offline wheelhouse dependencies
# for macOS Apple Silicon (darwin-arm64) used by GEO CAM Edge.
#
# Usage:
#   fetch-wheelhouse.sh [--target-dir <dir>] [--lock <lockfile>] [--verify-only]
#                       [--fault-inject <stage>] [--force]
#
# Security & Reliability Guarantees:
#   - Pinned exact release (36 wheels, no 'latest').
#   - Strict offline client provisioning: downloads occur ONLY during packaging.
#   - Fails closed on SHA-256 checksum or size mismatch.
#   - Stages in hidden temporary directory on same filesystem before activation.
#   - Preserves existing valid wheelhouse on error.
#   - Explicit transactional state machine manages backup, activation, verification, and commit.
#   - Automatic rollback restores previous valid wheelhouse if activation or post-verification fails.
#   - Filesystem-evidence recovery recovers active uncommitted backups on abnormal exit.
#   - Unrecovered backups are never deleted.
set -euo pipefail

TARGET_DIR="runtime/wheelhouse"
LOCK_FILE="internal/installer/assets/wheelhouse-lock.json"
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
    --lock)
      LOCK_FILE="$2"
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
      echo "usage: $0 [--target-dir <dir>] [--lock <lockfile>] [--verify-only] [--fault-inject <stage>] [--force]"
      exit 0
      ;;
    *)
      echo "::error::Unknown argument: $1" >&2
      exit 1
      ;;
  esac
done

fail() { echo "::error::$*" >&2; exit 1; }

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

[ -f "$LOCK_FILE" ] || fail "Lockfile not found at: $LOCK_FILE"

# Python interpreter to parse JSON lock and drive download/verification
PY_BIN=""
if [ -x "$REPO_ROOT/runtime/python/bin/python3" ]; then
  PY_BIN="$REPO_ROOT/runtime/python/bin/python3"
elif command -v python3 >/dev/null 2>&1; then
  PY_BIN="python3"
else
  fail "Python interpreter required to parse wheelhouse lock"
fi

verify_wheelhouse() {
  local dir="$1"
  [ -d "$dir" ] || return 1

  "$PY_BIN" -c "
import json, os, hashlib, sys

lock_path = sys.argv[1]
wheel_dir = sys.argv[2]

with open(lock_path) as fp:
    lock = json.load(fp)

for dep in lock.get('dependencies', []):
    fname = dep['filename']
    expected_hash = dep['sha256']
    expected_size = dep['size_bytes']
    
    path = os.path.join(wheel_dir, fname)
    if not os.path.isfile(path):
        sys.exit(1)
    if os.path.getsize(path) != expected_size:
        sys.exit(1)
    with open(path, 'rb') as fp:
        actual_hash = hashlib.sha256(fp.read()).hexdigest()
    if actual_hash.lower() != expected_hash.lower():
        sys.exit(1)

sys.exit(0)
" "$LOCK_FILE" "$dir"
}

if [ "$VERIFY_ONLY" = "true" ]; then
  if verify_wheelhouse "$TARGET_DIR"; then
    echo "ok: wheelhouse $TARGET_DIR is valid according to $LOCK_FILE"
    exit 0
  else
    fail "verify-only failed: $TARGET_DIR does not contain valid wheelhouse matching $LOCK_FILE"
  fi
fi

if [ "$FORCE" != "true" ] && verify_wheelhouse "$TARGET_DIR"; then
  echo "ok: $TARGET_DIR already contains verified wheelhouse matching $LOCK_FILE"
  exit 0
fi

# Ensure parent of TARGET_DIR exists so staging and backups occur on the exact SAME filesystem
mkdir -p "$(dirname "$TARGET_DIR")"

# State machine variables
STATE="INIT"
IN_CLEANUP=""

staging_tmp="${TARGET_DIR}.tmp.$$"
backup_old="${TARGET_DIR}.old.$$"
broken_tmp="${TARGET_DIR}.broken.$$"

perform_rollback() {
  local reason="$1"
  echo "::warning::Initiating wheelhouse rollback due to: $reason (prior state: $STATE)" >&2
  STATE="ROLLING_BACK"

  # Move aside target if it was created during activation
  if [ -e "$TARGET_DIR" ] || [ -L "$TARGET_DIR" ]; then
    rm -rf "$broken_tmp" 2>/dev/null || true
    mv "$TARGET_DIR" "$broken_tmp" 2>/dev/null || rm -rf "$TARGET_DIR" 2>/dev/null || true
  fi

  # Fault injection for simulated restore failure
  if [ "${FAULT_INJECT}" = "restore_fail" ]; then
    STATE="ROLLBACK_FAILED"
    echo "::error::FAULT_INJECTION: simulated failure during wheelhouse backup restoration" >&2
    echo "::error::CRITICAL: Backup restoration failed. Previous valid wheelhouse is SAFE and PRESERVED at: $backup_old" >&2
    echo "::error::Manual recovery command: mv \"$backup_old\" \"$TARGET_DIR\"" >&2
    return 1
  fi

  if [ -d "$backup_old" ]; then
    if mv "$backup_old" "$TARGET_DIR"; then
      STATE="ROLLED_BACK"
      echo "::notice::Rollback successful: previous valid wheelhouse restored to $TARGET_DIR" >&2
      [ -d "$broken_tmp" ] && rm -rf "$broken_tmp" 2>/dev/null || true
      return 0
    else
      STATE="ROLLBACK_FAILED"
      echo "::error::CRITICAL: Failed to restore backup from $backup_old to $TARGET_DIR" >&2
      echo "::error::The previous valid wheelhouse is SAFE and PRESERVED at: $backup_old" >&2
      echo "::error::Manual recovery command: mv \"$backup_old\" \"$TARGET_DIR\"" >&2
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
  # it indicates that a previous valid wheelhouse was moved and must be restored!
  if [ -d "$backup_old" ] && [ "$STATE" != "COMMITTED" ]; then
    echo "::warning::Abnormal termination with active backup found on filesystem at $backup_old (state: $STATE, sig: $sig, exit code: $exit_code). Attempting rollback..." >&2
    if perform_rollback "filesystem_evidence_${STATE}_${sig}_${exit_code}"; then
      rm -rf "$staging_tmp" "$broken_tmp" 2>/dev/null || true
    else
      echo "::error::CRITICAL: Backup preserved at: $backup_old" >&2
    fi
    exit $exit_code
  fi

  case "$STATE" in
    COMMITTED)
      rm -rf "$staging_tmp" 2>/dev/null || true
      [ -d "$backup_old" ] && rm -rf "$backup_old" 2>/dev/null || true
      ;;
    ROLLED_BACK)
      rm -rf "$staging_tmp" "$broken_tmp" 2>/dev/null || true
      ;;
    ROLLBACK_FAILED)
      echo "::error::EMERGENCY: Rollback could not be completed automatically." >&2
      echo "::error::The previous valid wheelhouse is SAFE and PRESERVED at: $backup_old" >&2
      echo "::error::Manual recovery command: mv \"$backup_old\" \"$TARGET_DIR\"" >&2
      rm -rf "$staging_tmp" 2>/dev/null || true
      ;;
    BACKED_UP|ACTIVATING|ACTIVATED|VERIFYING|ROLLING_BACK)
      echo "::warning::Abnormal termination in state '$STATE' (sig: $sig, exit code: $exit_code). Attempting rollback..." >&2
      if perform_rollback "trap_${sig}_${exit_code}"; then
        rm -rf "$staging_tmp" "$broken_tmp" 2>/dev/null || true
      else
        echo "::error::CRITICAL: Backup preserved at: $backup_old" >&2
      fi
      ;;
    *)
      # INIT, PREPARING, STAGED: target was NEVER modified!
      rm -rf "$staging_tmp" 2>/dev/null || true
      ;;
  esac

  exit $exit_code
}

trap 'cleanup INT 130' INT
trap 'cleanup TERM 143' TERM
trap 'cleanup HUP 129' HUP
trap 'cleanup EXIT $?' EXIT

STATE="PREPARING"
echo "Synchronizing wheelhouse to $TARGET_DIR using lock $LOCK_FILE..."
mkdir -p "$staging_tmp"

CACHE_DIR="${GEOCAM_RUNTIME_CACHE_DIR:-$HOME/.cache/geocam-edge/wheelhouse}"
mkdir -p "$CACHE_DIR"

# Download / copy each wheel into staging directory
"$PY_BIN" -c "
import json, os, urllib.request, hashlib, sys

lock_path = sys.argv[1]
staging_dir = sys.argv[2]
cache_dir = sys.argv[3]

with open(lock_path) as fp:
    lock = json.load(fp)

total = len(lock.get('dependencies', []))
for idx, dep in enumerate(lock.get('dependencies', [])):
    fname = dep['filename']
    expected_hash = dep['sha256']
    expected_size = dep['size_bytes']
    
    cached_path = os.path.join(cache_dir, fname)
    target_path = os.path.join(staging_dir, fname)
    
    # Check cache
    valid_cache = False
    if os.path.isfile(cached_path) and os.path.getsize(cached_path) == expected_size:
        with open(cached_path, 'rb') as fp:
            if hashlib.sha256(fp.read()).hexdigest().lower() == expected_hash.lower():
                valid_cache = True
                
    if valid_cache:
        with open(cached_path, 'rb') as src, open(target_path, 'wb') as dst:
            dst.write(src.read())
        print(f'[{idx+1}/{total}] Cached: {fname}')
    else:
        # Pinned PyPI download
        import urllib.request, json as jmod
        pkg_name = dep['name']
        ver = dep['version']
        api_url = f'https://pypi.org/pypi/{pkg_name}/{ver}/json'
        
        req = urllib.request.Request(api_url, headers={'User-Agent': 'geocam-wheelhouse-fetcher/1.0'})
        with urllib.request.urlopen(req) as resp:
            data = jmod.load(resp)
            
        found_url = None
        for release_file in data.get('urls', []):
            if release_file.get('filename') == fname:
                found_url = release_file.get('url')
                break
                
        if not found_url:
            print(f'Error: could not find download URL for {fname}', file=sys.stderr)
            sys.exit(1)
            
        print(f'[{idx+1}/{total}] Downloading: {fname}...')
        req = urllib.request.Request(found_url, headers={'User-Agent': 'geocam-wheelhouse-fetcher/1.0'})
        with urllib.request.urlopen(req) as resp, open(target_path, 'wb') as out_f:
            out_f.write(resp.read())
            
        # Verify downloaded file
        if os.path.getsize(target_path) != expected_size:
            print(f'Size mismatch for {fname}', file=sys.stderr)
            sys.exit(1)
        with open(target_path, 'rb') as fp:
            if hashlib.sha256(fp.read()).hexdigest().lower() != expected_hash.lower():
                print(f'Hash mismatch for {fname}', file=sys.stderr)
                sys.exit(1)
                
        # Cache file
        with open(target_path, 'rb') as src, open(cached_path, 'wb') as dst:
            dst.write(src.read())
" "$LOCK_FILE" "$staging_tmp" "$CACHE_DIR"

# Verify entire staged wheelhouse BEFORE touching target
echo "Verifying complete staged wheelhouse..."
if ! verify_wheelhouse "$staging_tmp"; then
  fail "Staged wheelhouse validation failed; target wheelhouse was NOT modified"
fi
STATE="STAGED"

# Fault injection: error before activation
if [ "${FAULT_INJECT}" = "before_activate" ]; then
  fail "FAULT_INJECTION: simulated error before activation"
fi

# Directory swap with rollback protection
if [ -e "$TARGET_DIR" ] || [ -L "$TARGET_DIR" ]; then
  STATE="BACKING_UP"
  if [ "${FAULT_INJECT}" = "move_old_fail" ]; then
    fail "FAULT_INJECTION: simulated error moving old wheelhouse to backup"
  fi

  echo "Backing up previous wheelhouse to $backup_old..."
  if ! mv "$TARGET_DIR" "$backup_old"; then
    fail "Failed to backup previous wheelhouse from $TARGET_DIR to $backup_old"
  fi

  if [ ! -d "$backup_old" ]; then
    fail "Failed to verify backup creation: $backup_old does not exist on filesystem"
  fi

  STATE="BACKED_UP"
fi

# Activate new wheelhouse
STATE="ACTIVATING"
if [ "${FAULT_INJECT}" = "activate_fail" ] || [ "${FAULT_INJECT}" = "restore_fail" ]; then
  echo "::error::FAULT_INJECTION: simulated failure triggering rollback (stage: ${FAULT_INJECT})" >&2
  perform_rollback "fault_inject_${FAULT_INJECT}" || true
  fail "Activation failed (fault injected: ${FAULT_INJECT}); rollback initiated"
fi

echo "Activating new wheelhouse to $TARGET_DIR..."
if ! mv "$staging_tmp" "$TARGET_DIR"; then
  echo "::error::Activation rename failed; initiating rollback" >&2
  perform_rollback "activation_rename_failed"
  fail "Activation failed: could not rename $staging_tmp to $TARGET_DIR"
fi
STATE="ACTIVATED"

# Post-activation verification on active TARGET_DIR
STATE="VERIFYING"
if [ "${FAULT_INJECT}" = "verify_fail" ]; then
  echo "::error::FAULT_INJECTION: simulated verification failure on active target" >&2
  perform_rollback "fault_inject_verify_fail"
  fail "Post-activation verification failed"
fi

echo "Verifying activated wheelhouse in $TARGET_DIR..."
if ! verify_wheelhouse "$TARGET_DIR"; then
  echo "::error::Target wheelhouse verification failed post-activation; rolling back!" >&2
  perform_rollback "post_activation_verify_failed"
  fail "Active wheelhouse verification failed"
fi

# Commit
STATE="COMMITTED"
if [ -d "$backup_old" ]; then
  rm -rf "$backup_old" 2>/dev/null || true
fi

echo "SUCCESS: Wheelhouse synchronized and verified in $TARGET_DIR"
