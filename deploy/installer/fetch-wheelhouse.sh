#!/usr/bin/env bash
# Fetches, verifies, and stages the hermetic offline wheelhouse dependencies
# for macOS Apple Silicon (darwin-arm64) used by GEO CAM Edge.
#
# Usage:
#   fetch-wheelhouse.sh [--target-dir <dir>] [--lock <lockfile>] [--verify-only] [--force]
#
# Security & Reliability Guarantees:
#   - Pinned exact release (36 wheels, no 'latest').
#   - Strict offline client provisioning: downloads occur ONLY during packaging.
#   - Fails closed on SHA-256 checksum or size mismatch.
#   - Stages in hidden temporary directory on same filesystem before activation.
#   - Preserves existing valid wheelhouse on error.
set -euo pipefail

TARGET_DIR="runtime/wheelhouse"
LOCK_FILE="internal/installer/assets/wheelhouse-lock.json"
VERIFY_ONLY="false"
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
    --force)
      FORCE="true"
      shift
      ;;
    -h|--help)
      echo "usage: $0 [--target-dir <dir>] [--lock <lockfile>] [--verify-only] [--force]"
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

echo "Synchronizing wheelhouse to $TARGET_DIR using lock $LOCK_FILE..."
mkdir -p "$TARGET_DIR"

staging_dir="${TARGET_DIR}.tmp.$$"
mkdir -p "$staging_dir"

cleanup() {
  rm -rf "$staging_dir" 2>/dev/null || true
}
trap cleanup EXIT INT TERM

CACHE_DIR="${GEOCAM_RUNTIME_CACHE_DIR:-$HOME/.cache/geocam-edge/wheelhouse}"
mkdir -p "$CACHE_DIR"

# Download / copy each wheel
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
        # PyPI files URL pattern: https://files.pythonhosted.org/packages/...
        # Alternatively query PyPI JSON API for exact filename download url
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
" "$LOCK_FILE" "$staging_dir" "$CACHE_DIR"

# Verify staging directory
if ! verify_wheelhouse "$staging_dir"; then
  fail "Staged wheelhouse validation failed"
fi

# Move staged files into target directory
mv "$staging_dir"/* "$TARGET_DIR"/
rmdir "$staging_dir" 2>/dev/null || true

echo "SUCCESS: Wheelhouse synchronized and verified in $TARGET_DIR"
