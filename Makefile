BINARY      := geocam-edge
PKG         := ./cmd/geocam-edge
MODULE      := github.com/drko-dev/monitoreoedgeis
VERSION     ?= 0.1.0
COMMIT      ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_DATE  ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
IMAGE       ?= geocam-edge:dev
NAMESPACE   ?= geocam-edge-dev
RELEASE     ?= geocam-edge
CHART       := deploy/helm/geocam-edge

LDFLAGS := -s -w \
  -X $(MODULE)/internal/agent.Version=$(VERSION) \
  -X $(MODULE)/internal/agent.Commit=$(COMMIT) \
  -X $(MODULE)/internal/agent.BuildDate=$(BUILD_DATE)

.PHONY: all test vet fmt build build-linux image helm-lint helm-template deploy status logs uninstall clean

all: vet test build

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(BINARY) $(PKG)

## Deployment targets: linux/amd64 and linux/arm64, static (CGO off).
build-linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(BINARY)-linux-amd64 $(PKG)
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(BINARY)-linux-arm64 $(PKG)

## Builds the OCI image into the k8s.io containerd namespace so K3s can use it
## directly. Uses nerdctl (Rancher Desktop) — Docker Engine is NOT required.
image:
	nerdctl --namespace k8s.io build \
	  --build-arg VERSION=$(VERSION) \
	  --build-arg COMMIT=$(COMMIT) \
	  --build-arg BUILD_DATE=$(BUILD_DATE) \
	  -t $(IMAGE) .

helm-lint:
	helm lint $(CHART)

helm-template:
	helm template $(RELEASE) $(CHART) --namespace $(NAMESPACE)

deploy:
	helm upgrade --install $(RELEASE) $(CHART) \
	  --namespace $(NAMESPACE) --create-namespace --wait

status:
	kubectl get pods -n $(NAMESPACE)

logs:
	kubectl logs -n $(NAMESPACE) -l app.kubernetes.io/name=geocam-edge --tail=50

## Removes ONLY the Edge release. Never touches the `geocam` SaaS namespace.
uninstall:
	helm uninstall $(RELEASE) --namespace $(NAMESPACE)

clean:
	rm -rf bin/
