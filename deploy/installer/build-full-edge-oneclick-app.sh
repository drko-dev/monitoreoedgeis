#!/bin/bash
# Builds a self-contained macOS arm64 GEO CAM Edge installer whose
# "Preparar Full Edge" button installs the bundled, signed Runtime Pack
# offline with one click (H5). Owner/lab builds only: the publisher key is a
# local Ed25519 seed. The app is ad-hoc signed unless SIGN_IDENTITY names a
# codesigning identity (e.g. "Developer ID Application: ..."); then every
# Mach-O, including the Runtime Pack's, gets a hardened-runtime signature
# BEFORE the manifest is signed, so the pinned hashes cover the signed bytes.
# NOTARY_PROFILE (a `notarytool store-credentials` profile) also notarizes and
# staples the app.
#
# Usage: build-full-edge-oneclick-app.sh <runtime-pack-dir> <models-dir> <out-dir> <key-file> <version> <person-fixture.h264>
# The fixture is the known clip the H8 inference certification must detect.
set -euo pipefail
if [[ "$(uname -s)" != Darwin || "$(uname -m)" != arm64 || $# -ne 6 ]]; then
  echo "Usage on macOS arm64: $0 <runtime-pack-dir> <models-dir> <out-dir> <key-file> <version> <person-fixture.h264>" >&2
  exit 2
fi
src_pack="$1"; models="$2"; out="$3"; key="$4"; version="$5"; fixture="$6"
repo="$(cd "$(dirname "$0")/../.." && pwd -P)"
commit="$(git -C "$repo" rev-parse --short HEAD)"
work="$out/work"
rm -rf "$work"
mkdir -p "$work/release"

echo "==> staging Runtime Pack (APFS clone, symlinks materialized)"
cp -cR "$src_pack" "$work/RuntimePack"
rm -f "$work/RuntimePack/lab-manifest.json"
# The provisioner rejects symlinks; replace each with a copy of its target.
find "$work/RuntimePack" -type l | while read -r link; do
  target="$(readlink -f "$link")"
  rm "$link"
  cp -c "$target" "$link"
done

identity="${SIGN_IDENTITY:--}"
if [[ "$identity" != - ]]; then
  echo "==> hardened-runtime signing Runtime Pack Mach-O files ($identity)"
  entitlements="$repo/deploy/installer/full-edge-python.entitlements"
  find "$work/RuntimePack" -type f -print0 | python3 -I -c '
import sys
for p in sys.stdin.buffer.read().split(b"\0"):
    if not p: continue
    with open(p, "rb") as f: magic = f.read(8)
    # 64-bit Mach-O, or a fat header with a small arch count (not a Java class).
    if magic[:4] == b"\xcf\xfa\xed\xfe" or (magic[:4] == b"\xca\xfe\xba\xbe" and int.from_bytes(magic[4:8], "big") < 8):
        sys.stdout.buffer.write(p + b"\0")
' | xargs -0 codesign --force --timestamp --options runtime --sign "$identity"
  # The interpreter loads extension modules and torch uses executable memory.
  codesign --force --timestamp --options runtime --entitlements "$entitlements" \
    --sign "$identity" "$work/RuntimePack/python/bin/python3.11"
fi

echo "==> signing runtime manifest"
pins="$(cd "$repo" && go run ./cmd/runtime-pack-sign -pack "$work/RuntimePack" -out "$work/release" \
  -key "$key" -runtime-version "$version" -ffmpeg-version "$("$work/RuntimePack/ffmpeg/ffmpeg" -version | awk 'NR==1{print $3}')")"

echo "==> building installer"
agent=github.com/drko-dev/monitoreoedgeis/internal/agent
ldflags="-X $agent.Version=$version -X $agent.Commit=$commit -X $agent.BuildDate=$(date -u +%Y-%m-%dT%H:%M:%SZ) $pins -X main.releaseOfflineProvisioning=1"
(cd "$repo/cmd/geocam-edge-ui" && go run github.com/wailsapp/wails/v2/cmd/wails@v2.16.0 build -clean -platform darwin/arm64 -ldflags "$ldflags")
app="$out/GEO-CAM-Edge-Full-Edge-$version.app"
rm -rf "$app"
ditto "$repo/cmd/geocam-edge-ui/build/bin/geocam-edge-ui.app" "$app"
(cd "$repo" && go build -ldflags "-X $agent.Version=$version -X $agent.Commit=$commit" -o "$app/Contents/MacOS/geocam-edge" ./cmd/geocam-edge)

echo "==> bundling pack, signed manifest, trust anchor and models"
res="$app/Contents/Resources"
mv "$work/RuntimePack" "$res/RuntimePack"
cp "$work/release/runtime-manifest.signed.json" "$work/release/trust-anchors.json" "$res/"
cp -cR "$models" "$res/Models"
mkdir -p "$res/Fixtures"
cp -c "$fixture" "$res/Fixtures/person-annexb.h264"

echo "==> signing app (outer bundle only: pack bytes are hash-pinned, never --deep)"
sign=(codesign --force --sign "$identity")
[[ "$identity" != - ]] && sign+=(--timestamp --options runtime)
"${sign[@]}" "$app/Contents/MacOS/geocam-edge"
"${sign[@]}" "$app"
codesign --verify --strict "$app"
rm -rf "$work"
if [[ -n "${NOTARY_PROFILE:-}" ]]; then
  echo "==> notarizing"
  ditto -c -k --keepParent "$app" "$out/notarize.zip"
  xcrun notarytool submit "$out/notarize.zip" --keychain-profile "$NOTARY_PROFILE" --wait
  rm -f "$out/notarize.zip"
  xcrun stapler staple "$app"
fi
echo "$app"
