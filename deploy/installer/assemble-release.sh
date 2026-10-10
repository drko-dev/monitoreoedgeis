#!/usr/bin/env bash
# Assembles the GEO CAM Edge installer release from the four platform build
# artifacts (see .github/workflows/installer-build.yml).
#
# Usage: assemble-release.sh <version> <commit> <artifacts-dir> <out-dir>
#
# Fails closed: exits non-zero unless every expected package is present, was
# built from <version> and <commit>, still matches the SHA-256 its build job
# recorded, and has the expected binary format/architecture. On success
# <out-dir> holds exactly the four packages plus SHA256SUMS.txt.
set -euo pipefail

if [ "$#" -ne 4 ]; then
  echo "usage: $0 <version> <commit> <artifacts-dir> <out-dir>" >&2
  exit 2
fi
version=$1 commit=$2 in_dir=$3 out_dir=$4

fail() { echo "::error::$*" >&2; exit 1; }

[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || fail "version '$version' is not MAJOR.MINOR.PATCH"
mkdir -p "$out_dir"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

check_package() {
  local platform=$1 pkg=$2 x="$work/$1"
  mkdir -p "$x"
  case "$platform" in
    windows-amd64)
      file -b "$pkg" | grep -q 'PE32+ executable.*x86-64' || fail "$platform: not a Windows x86-64 executable: $(file -b "$pkg")"
      ;;
    macos-arm64)
      unzip -q "$pkg" -d "$x"
      local app="$x/geocam-edge-ui.app"
      file -b "$app/Contents/MacOS/geocam-edge-ui" | grep -q 'Mach-O 64-bit.*arm64' || fail "$platform: app binary is not Mach-O arm64"
      grep -A1 'CFBundleShortVersionString' "$app/Contents/Info.plist" | grep -q "<string>$version</string>" \
        || fail "$platform: Info.plist CFBundleShortVersionString is not $version"
      ;;
    linux-amd64 | linux-arm64)
      local arch_re='x86-64'
      [ "$platform" = linux-arm64 ] && arch_re='ARM aarch64'
      tar -xzf "$pkg" -C "$x"
      local dir="$x/geocam-edge-installer-$version-$platform"
      [ -x "$dir/geocam-edge-ui" ] || fail "$platform: $dir/geocam-edge-ui missing or not executable"
      file -b "$dir/geocam-edge-ui" | grep -q "ELF 64-bit.*$arch_re" || fail "$platform: binary is not ELF $arch_re"
      [ "$(cat "$dir/VERSION")" = "$version" ] || fail "$platform: VERSION file is not $version"
      ;;
  esac
}

for platform in windows-amd64 macos-arm64 linux-amd64 linux-arm64; do
  case "$platform" in
    windows-*) expected="geocam-edge-installer-$platform.exe" ;;
    macos-*) expected="geocam-edge-installer-$platform.zip" ;;
    *) expected="geocam-edge-installer-$platform.tar.gz" ;;
  esac

  infos=$(find "$in_dir" -type f -name "BUILD_INFO-$platform.txt")
  [ -n "$infos" ] || fail "$platform: no build artifact (BUILD_INFO-$platform.txt missing)"
  [ "$(printf '%s\n' "$infos" | wc -l)" -eq 1 ] || fail "$platform: more than one BUILD_INFO-$platform.txt"
  info=$infos
  field() { sed -n "s/^$1=//p" "$info"; }

  [ "$(field version)" = "$version" ] || fail "$platform: built as version '$(field version)', expected '$version'"
  [ "$(field commit)" = "$commit" ] || fail "$platform: built from commit '$(field commit)', expected '$commit'"
  [ "$(field asset)" = "$expected" ] || fail "$platform: asset '$(field asset)', expected '$expected'"

  pkg="$(dirname "$info")/$expected"
  [ -f "$pkg" ] || fail "$platform: $expected missing from the build artifact"
  echo "$(field sha256)  $pkg" | sha256sum -c --quiet - || fail "$platform: $expected does not match the SHA-256 recorded at build time"

  check_package "$platform" "$pkg"
  cp "$pkg" "$out_dir/$expected"
  echo "ok  $platform  $expected"
done

cd "$out_dir"
sha256sum geocam-edge-installer-* > SHA256SUMS.txt
sha256sum -c --quiet SHA256SUMS.txt
cat SHA256SUMS.txt
