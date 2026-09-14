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

# Final image: static binary only. No Go toolchain, no Python, no YOLO.
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/geocam-edge /usr/local/bin/geocam-edge

USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/geocam-edge"]
