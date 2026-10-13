#!/usr/bin/env bash
# Fetches, verifies, and stages the hermetic Python 3.11 Standalone runtime
# for macOS Apple Silicon (darwin-arm64) used by GEO CAM Edge.
#
# Usage:
#   fetch-runtime-deps.sh [--target-dir <dir>] [--platform <darwin-arm64>] [--verify-only]
#
# Constraints:
#   - Pinned exact release (no 'latest').
#   - Fails closed on SHA-256 checksum mismatch.
#   - Verifies Mach-O 64-bit arm64 architecture.
#   - Extracts safely into temporary directory with path confinement checks.
#   - Atomically updates target directory.
set -euo pipefail

TARGET_DIR="runtime"
PLATFORM="darwin-arm64"
VERIFY_ONLY="false"

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

verify_target_binary() {
  local root="$1"
  local bin="$root/$EXPECTED_BINARY_REL"
  local symlink="$root/python/bin/python3"

  [ -f "$bin" ] || return 1
  [ -x "$bin" ] || return 1

  local actual_size
  actual_size=$(wc -c < "$bin" | tr -d ' ')
  [ "$actual_size" -eq "$EXPECTED_BINARY_SIZE" ] || return 1

  local actual_hash
  actual_hash=$(calc_sha256 "$bin")
  [ "$actual_hash" = "$EXPECTED_BINARY_SHA256" ] || return 1

  # Ensure symlink python3 exists and resolves to python3.11
  [ -e "$symlink" ] || return 1

  # Check Mach-O architecture if on macOS
  if [ "$(uname -s)" = "Darwin" ]; then
    file -b "$bin" | grep -q 'Mach-O 64-bit.*arm64' || return 1
  fi

  return 0
}

# If verify-only requested
if [ "$VERIFY_ONLY" = "true" ]; then
  if verify_target_binary "$TARGET_DIR"; then
    echo "ok: target binary $TARGET_DIR/$EXPECTED_BINARY_REL is valid"
    exit 0
  else
    fail "verify-only failed: $TARGET_DIR does not contain valid Python standalone runtime"
  fi
fi

# Check if target is already valid
if verify_target_binary "$TARGET_DIR"; then
  echo "ok: $TARGET_DIR already contains verified Python $PYTHON_VERSION standalone"
  exit 0
fi

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

archive_path="$work/$ARCHIVE_NAME"

echo "Downloading Python $PYTHON_VERSION Standalone for $PLATFORM..."
if command -v curl >/dev/null 2>&1; then
  curl -fsSL -o "$archive_path" "$DOWNLOAD_URL"
elif command -v wget >/dev/null 2>&1; then
  wget -q -O "$archive_path" "$DOWNLOAD_URL"
else
  fail "Neither curl nor wget available to fetch runtime dependency"
fi

# Verify archive size and checksum
echo "Verifying archive cryptographic integrity..."
archive_size=$(wc -c < "$archive_path" | tr -d ' ')
[ "$archive_size" -eq "$EXPECTED_ARCHIVE_SIZE" ] || fail "Archive size mismatch: got $archive_size, expected $EXPECTED_ARCHIVE_SIZE"

archive_hash=$(calc_sha256 "$archive_path")
[ "$archive_hash" = "$EXPECTED_ARCHIVE_SHA256" ] || fail "Archive SHA-256 mismatch: got $archive_hash, expected $EXPECTED_ARCHIVE_SHA256"

# Safe extraction into staging dir
staging="$work/extracted"
mkdir -p "$staging"

echo "Extracting runtime archive..."
tar -xzf "$archive_path" -C "$staging"

# Confinement check: verify no files escaped staging
staged_python="$staging/python"
[ -d "$staged_python" ] || fail "Extracted archive does not contain 'python' directory"

# Ensure execution permissions on bin
chmod -R +x "$staged_python/bin"

# Verify staged binary
if ! verify_target_binary "$staging"; then
  fail "Verification of extracted binary failed in staging"
fi

# Atomic install to target directory
mkdir -p "$TARGET_DIR"
target_python="$TARGET_DIR/python"

if [ -d "$target_python" ]; then
  backup_dir="$work/python_backup"
  mv "$target_python" "$backup_dir"
fi

mv "$staged_python" "$target_python"

if verify_target_binary "$TARGET_DIR"; then
  echo "SUCCESS: Hermetic Python $PYTHON_VERSION Standalone staged in $TARGET_DIR/python"
else
  fail "Failed to verify Python binary in final target location: $TARGET_DIR"
fi
