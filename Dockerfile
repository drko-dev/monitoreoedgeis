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

# Static, multi-arch (linux/amd64 + linux/arm64, verified) ffmpeg binary for
# Hito H's video decode pipeline (internal/processing), run as an OS
# subprocess via os/exec — never linked, never cgo. This is a RUNTIME
# dependency only: go.mod gains nothing, CGO_ENABLED stays 0, and
# build-linux's pure-Go cross-compile is unaffected.
#
# LICENSE NOTE (do not silently change without re-checking): this image's
# ffmpeg is built with libx264/libx265 enabled (confirmed against
# github.com/wader/static-ffmpeg's Dockerfile), which requires ffmpeg's
# --enable-gpl — i.e. this is a GPL-licensed ffmpeg binary, not an
# LGPL-only one, even though this project only uses it for H.264 decode
# (ffmpeg's native decoder, not libx264/libx265, which are encoders).
# Bundling it into this image means the image contains GPL-licensed code.
# See docs/ARCHITECTURE.md for the full tradeoff writeup and the
# LGPL-only-rebuild alternative if GPL distribution is unacceptable.
FROM mwader/static-ffmpeg@sha256:415a41fa3167b890b9703d20bd0f00bf1e9dab8a4b6c27fef1445b2bf5f1ab4a AS ffmpeg

# Final image: static geocam-edge binary + static ffmpeg binary. No Go
# toolchain, no Python, no YOLO.
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/geocam-edge /usr/local/bin/geocam-edge
COPY --from=ffmpeg /ffmpeg /usr/local/bin/ffmpeg

USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/geocam-edge"]
