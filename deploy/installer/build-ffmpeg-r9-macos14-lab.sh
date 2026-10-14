#!/bin/bash
# H6D-R9 two clean, same-host builds. LAB ONLY: no release/provisioning.
set -euo pipefail
umask 077
export PATH=/usr/bin:/bin:/usr/sbin:/sbin
export LC_ALL=C TZ=UTC MACOSX_DEPLOYMENT_TARGET=14.0 ZERO_AR_DATE=1
unset CPATH C_INCLUDE_PATH CPLUS_INCLUDE_PATH LIBRARY_PATH DYLD_LIBRARY_PATH DYLD_INSERT_LIBRARIES PKG_CONFIG_PATH PKG_CONFIG_LIBDIR
if [[ "$(uname -s)" != Darwin || "$(uname -m)" != arm64 || $# != 2 ]]; then
  echo "NO-GO: requires macOS arm64 + <existing archive> <new lab output>" >&2; exit 2
fi
source_archive="$1"
out="$2"
if [[ ! -f "$source_archive" || -L "$source_archive" || "$(basename "$source_archive")" != "ffmpeg-8.1.3.tar.xz" ]]; then
  echo "NO-GO: invalid source archive" >&2; exit 2
fi
expected="7138d28c96d9d3e3af4ee3d8cad72741f8ffb40da90c1112235dea3ecd3178a3"
actual="$(/usr/bin/shasum -a 256 "$source_archive" | /usr/bin/awk '{print $1}')"
if [[ "$actual" != "$expected" ]]; then echo "NO-GO: unpinned FFmpeg source" >&2; exit 2; fi
if [[ -e "$out" || -L "$out" || ! -d "$(dirname "$out")" || -L "$(dirname "$out")" ]]; then
  echo "NO-GO: output must be new, and parent an existing real directory" >&2; exit 2
fi
work="$(/usr/bin/mktemp -d /tmp/geocam-h6d-r9-ffmpeg.XXXXXXXX)"
cleanup() {
  if [[ -n "$work" && "$work" == /tmp/geocam-h6d-r9-ffmpeg.* && -d "$work" ]]; then
    /bin/rm -rf -- "$work"
  fi
}
trap cleanup EXIT
/bin/mkdir "$work/staging"
sdk="$(/usr/bin/xcrun --sdk macosx --show-sdk-version)"
toolchain="$(/usr/bin/xcrun --find clang)"
{
  echo "LAB_ONLY=true"
  echo "SOURCE_ARCHIVE_SHA256=$actual"
  echo "MACOSX_DEPLOYMENT_TARGET=14.0"
  echo "SDK_VERSION=$sdk"
  echo "COMPILER=$toolchain"
  /usr/bin/xcrun clang --version | /usr/bin/head -3
} > "$work/staging/toolchain.txt"
set -- \
  --prefix=/opt/geocam-lab-ffmpeg \
  --disable-everything --disable-autodetect --disable-programs \
  --enable-ffmpeg --enable-ffprobe --enable-static --disable-shared \
  --disable-network --disable-doc --disable-debug \
  --enable-protocol=file,pipe --enable-demuxer=h264,mov,matroska \
  --enable-muxer=null,image2,rawvideo --enable-parser=h264 \
  --enable-decoder=h264 --enable-encoder=rawvideo,wrapped_avframe \
  --enable-filter=scale,format --enable-swscale --enable-avformat \
  --enable-avcodec --enable-avutil \
  --extra-cflags=-mmacosx-version-min=14.0 \
  --extra-ldflags=-mmacosx-version-min=14.0
printf '%s\n' "$@" > "$work/staging/configure-flags.txt"
for i in 1 2; do
  echo "R9_BUILD_$i: extract, configure, compile from pinned source" >&2
  /bin/mkdir "$work/build-$i"
  /usr/bin/tar -xJf "$source_archive" -C "$work/build-$i"
  cd "$work/build-$i/ffmpeg-8.1.3"
  ./configure "$@" > "$work/staging/configure-$i.log" 2>&1
  /usr/bin/make -j4 > "$work/staging/build-$i.log" 2>&1
  for name in ffmpeg ffprobe; do
    if [[ ! -x "$name" ]]; then echo "NO-GO: missing binary $name" >&2; exit 1; fi
    /bin/cp -p "$name" "$work/staging/$name-$i"
    /usr/bin/shasum -a 256 "$name" | /usr/bin/awk -v n="$name" -v i="$i" '{print n "_build" i "_sha256=" $1}' >> "$work/staging/digests.txt"
    /usr/bin/xcrun vtool -show-build "$name" > "$work/staging/$name-$i-buildversion.txt"
    /usr/bin/otool -L "$name" > "$work/staging/$name-$i-linked-libs.txt"
    /usr/bin/lipo -archs "$name" > "$work/staging/$name-$i-archs.txt"
  done
done
for name in ffmpeg ffprobe; do
  a="$(/usr/bin/shasum -a 256 "$work/staging/$name-1" | /usr/bin/awk '{print $1}')"
  b="$(/usr/bin/shasum -a 256 "$work/staging/$name-2" | /usr/bin/awk '{print $1}')"
  if [[ "$a" == "$b" ]]; then
    echo "$name SAME_HOST_TWO_BUILD_SHA256_MATCH=$a" | /usr/bin/tee -a "$work/staging/two-build-comparison.txt"
  else
    echo "$name SAME_HOST_TWO_BUILD_SHA256_MISMATCH A=$a B=$b" | /usr/bin/tee -a "$work/staging/two-build-comparison.txt"
  fi
done
echo "R9_FINAL: lab-only outputs into new directory: $out" >&2
/bin/mv "$work/staging" "$out"
echo "R9_BUILDS_FINISHED_LAB_ONLY; no model, release or Wails activation" >&2
