#!/bin/bash
# Builds a self-contained macOS arm64 GEO CAM Edge installer whose
# "Preparar Full Edge" button installs the bundled, signed Runtime Pack
# offline with one click (H5). Owner/lab builds only: the publisher key is a
# local Ed25519 seed, the app is ad-hoc signed and not notarized.
#
# Usage: build-full-edge-oneclick-app.sh <runtime-pack-dir> <models-dir> <out-dir> <key-file> <version>
set -euo pipefail
if [[ "$(uname -s)" != Darwin || "$(uname -m)" != arm64 || $# -ne 5 ]]; then
  echo "Usage on macOS arm64: $0 <runtime-pack-dir> <models-dir> <out-dir> <key-file> <version>" >&2
  exit 2
fi
src_pack="$1"; models="$2"; out="$3"; key="$4"; version="$5"
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

echo "==> ad-hoc signing (outer bundle only: pack bytes are hash-pinned)"
codesign --force --sign - "$app/Contents/MacOS/geocam-edge"
codesign --force --sign - "$app"
codesign --verify --strict "$app"
rm -rf "$work"
echo "$app"
