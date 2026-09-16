# Multi-stage build. This is an OCI image definition only: the local runtime is
# Rancher Desktop + containerd (nerdctl). Docker Engine is not required.
FROM golang:1.26-alpine AS build

ARG VERSION=0.1.0
ARG COMMIT=unknown
ARG BUILD_DATE=unknown
ARG TARGETOS=linux
ARG TARGETARCH

WORKDIR /src

# No third-party dependencies yet, so go.mod alone is the dependency layer.
COPY go.mod ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal

ENV CGO_ENABLED=0
RUN GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
    -trimpath \
    -ldflags "-s -w \
      -X github.com/drko-dev/monitoreoedgeis/internal/agent.Version=${VERSION} \
      -X github.com/drko-dev/monitoreoedgeis/internal/agent.Commit=${COMMIT} \
      -X github.com/drko-dev/monitoreoedgeis/internal/agent.BuildDate=${BUILD_DATE}" \
    -o /out/geocam-edge ./cmd/geocam-edge

# Static, multi-arch (linux/amd64 + linux/arm64) ffmpeg binary for Hito H's
# video decode pipeline (internal/processing), run as an OS subprocess via
# os/exec — never linked, never cgo. This is a RUNTIME dependency only:
# go.mod gains nothing, CGO_ENABLED stays 0, and build-linux's pure-Go
# cross-compile is unaffected.
#
# LGPL-ONLY, BUILT FROM OFFICIAL SOURCE (not a third-party prebuilt image).
# Configured with --disable-everything, then only the exact components this
# pipeline's one command needs are re-enabled — no --enable-gpl, no
# --enable-nonfree, no libx264/libx265/libxvid, no GPL code at all. Verified
# (both from `./configure`'s own source and by running the built binary)
# to contain none of those. Every enabled component was individually
# confirmed to exist and be necessary against the actual FFmpeg 7.1.5
# source (not assumed from a snippet) for exactly this pipeline command:
#
#   ffmpeg -f h264 -i pipe:0 -f rawvideo -pix_fmt yuv420p -an -sn pipe:1
#
# decoder=h264 (libavcodec/h264dec.c) + parser=h264 (libavcodec/h264_parser.c)
#   decode the raw Annex-B elementary stream and find its frame boundaries
#   (a raw demuxer alone has no container-level frame markers).
# demuxer=h264 (libavformat) matches -f h264 on the input side.
# muxer=rawvideo + encoder=rawvideo (libavcodec/rawenc.c) match -f rawvideo:
#   FFmpeg always runs frames through an "encoder" before muxing, even for
#   nominally-raw output — rawvideo's encoder is a trivial passthrough, but
#   it must be explicitly enabled under --disable-everything or the build
#   has no way to produce -f rawvideo output at all.
# protocol=pipe matches pipe:0/pipe:1.
# avfilter/swscale are left at their default (enabled) — --disable-everything
# only zeroes their FILTER components, not the libraries themselves — since
# modern ffmpeg.c uses the filtergraph path even for the implicit
# format/scale step -pix_fmt can trigger; both are LGPL, no licensing
# reason to strip them, and doing so was not validated to be safe to do.
#
# Source: official https://ffmpeg.org/releases/ffmpeg-7.1.5.tar.xz, fetched
# over HTTPS from the project's own domain and pinned by the SHA256 below
# (computed from that fetch — ffmpeg.org's plain release listing does not
# publish a separate checksum file to cross-verify against).
#
# LGPL COMPLIANCE (factual, not a legal opinion): shipping this binary
# under LGPLv2.1+ requires making the corresponding source (this exact
# version, unmodified) and this build recipe available to recipients, and
# preserving FFmpeg's copyright/license notices. This Dockerfile stage IS
# that build recipe; docs/ARCHITECTURE.md links back to it and to the
# pinned upstream source. This is not a substitute for legal review — it
# documents what the build actually does, not a guarantee of compliance.
FROM alpine:3.20 AS ffmpeg-build

ARG FFMPEG_VERSION=7.1.5
ARG FFMPEG_SHA256=de668509caf9e35e3cd162473441fdb29538c6d96ed080292b3cf9e6fc5d558f

RUN apk add --no-cache build-base yasm nasm pkgconfig diffutils curl

WORKDIR /build
RUN curl -fsSL -o ffmpeg.tar.xz "https://ffmpeg.org/releases/ffmpeg-${FFMPEG_VERSION}.tar.xz" \
 && echo "${FFMPEG_SHA256}  ffmpeg.tar.xz" | sha256sum -c - \
 && tar xf ffmpeg.tar.xz && mv "ffmpeg-${FFMPEG_VERSION}" src

WORKDIR /build/src
RUN ./configure \
    --disable-everything \
    --disable-doc \
    --disable-shared \
    --enable-static \
    --disable-network \
    --disable-avdevice \
    --disable-postproc \
    --disable-ffplay \
    --disable-ffprobe \
    --enable-decoder=h264 \
    --enable-parser=h264 \
    --enable-demuxer=h264 \
    --enable-muxer=rawvideo \
    --enable-encoder=rawvideo \
    --enable-protocol=pipe \
    --extra-ldflags="-static" \
    --pkg-config-flags="--static" \
 && make -j"$(nproc)" \
 && make install DESTDIR=/out

# Final image: static geocam-edge binary + static ffmpeg binary. No Go
# toolchain, no Python, no YOLO.
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/geocam-edge /usr/local/bin/geocam-edge
COPY --from=ffmpeg-build /out/usr/local/bin/ffmpeg /usr/local/bin/ffmpeg

USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/geocam-edge"]
