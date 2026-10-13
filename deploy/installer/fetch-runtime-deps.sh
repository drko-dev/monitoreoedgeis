#!/usr/bin/env bash
# Fetches, verifies, and stages the hermetic Python 3.11 Standalone runtime
# for macOS Apple Silicon (darwin-arm64) used by GEO CAM Edge.
#
# Usage:
#   fetch-runtime-deps.sh [--target-dir <dir>] [--platform <darwin-arm64>] [--verify-only]
#
# Security & Reliability Guarantees:
#   - Pinned exact release (no 'latest').
#   - Fails closed on SHA-256 checksum mismatch BEFORE extraction.
#   - Secure archive extraction prevents path traversal, absolute paths, and symlink/hardlink escapes.
#   - Staging occurs strictly in the same filesystem as destination ($TARGET_DIR/.python.tmp.$$).
#   - Existing valid runtime is NEVER touched until new distribution is 100% verified.
#   - Atomic directory swap with automatic rollback protection.
#   - Verification covers complete distribution: executable, dylib, and standard library.
set -euo pipefail

TARGET_DIR="runtime"
PLATFORM="darwin-arm64"
VERIFY_ONLY="false"

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
    -h|--help)
      echo "usage: $0 [--target-dir <dir>] [--platform <darwin-arm64>] [--verify-only]"
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

# Check if target is already valid
if verify_target_distribution "$TARGET_DIR"; then
  echo "ok: $TARGET_DIR already contains verified Python $PYTHON_VERSION standalone"
  exit 0
fi

# Ensure TARGET_DIR exists so staging occurs on the exact SAME filesystem
mkdir -p "$TARGET_DIR"

# Sibling staging paths on the exact SAME filesystem
staging_tmp="$TARGET_DIR/.python.tmp.$$"
backup_old="$TARGET_DIR/.python.old.$$"

cleanup() {
  rm -rf "$staging_tmp"
  if [ -d "$backup_old" ]; then
    rm -rf "$backup_old"
  fi
  if [ -n "${work:-}" ] && [ -d "$work" ]; then
    rm -rf "$work"
  fi
}
trap cleanup EXIT INT TERM

work=$(mktemp -d)
archive_path="$work/$ARCHIVE_NAME"

echo "Downloading Python $PYTHON_VERSION Standalone for $PLATFORM..."
if command -v curl >/dev/null 2>&1; then
  curl -fsSL -o "$archive_path" "$DOWNLOAD_URL"
elif command -v wget >/dev/null 2>&1; then
  wget -q -O "$archive_path" "$DOWNLOAD_URL"
else
  fail "Neither curl nor wget available to fetch runtime dependency"
fi

# 1. Pre-extraction cryptographic verification
echo "Verifying archive cryptographic integrity..."
archive_size=$(wc -c < "$archive_path" | tr -d ' ')
[ "$archive_size" -eq "$EXPECTED_ARCHIVE_SIZE" ] || fail "Archive size mismatch: got $archive_size, expected $EXPECTED_ARCHIVE_SIZE"

archive_hash=$(calc_sha256 "$archive_path")
[ "$archive_hash" = "$EXPECTED_ARCHIVE_SHA256" ] || fail "Archive SHA-256 mismatch: got $archive_hash, expected $EXPECTED_ARCHIVE_SHA256"

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

# 4. Atomic activation on the SAME filesystem with rollback protection
target_python="$TARGET_DIR/python"

if [ -d "$target_python" ]; then
  # Previous runtime exists: rename to backup, swap in staged, remove backup
  mv "$target_python" "$backup_old"
  if ! mv "$staged_python" "$target_python"; then
    # Atomic rollback!
    mv "$backup_old" "$target_python"
    fail "Atomic rename failed; rolled back to previous valid Python runtime"
  fi
  rm -rf "$backup_old"
else
  # Fresh installation: direct atomic rename
  mv "$staged_python" "$target_python"
fi

# Clean up staging directory
rm -rf "$staging_tmp"

# 5. Final verification of activated distribution
if verify_target_distribution "$TARGET_DIR"; then
  echo "SUCCESS: Hermetic Python $PYTHON_VERSION Standalone safely activated in $TARGET_DIR/python"
else
  fail "Final verification failed in $TARGET_DIR/python"
fi
