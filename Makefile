BIN := bin/mcpx
CONFIG ?= mcpx.yaml
DOCKER ?= docker
IMAGE  ?= mcpx

VERSION ?= $(shell git describe --tags --abbrev=0 --always 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short=7 HEAD 2>/dev/null || echo unknown)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS := -X mcpx/internal/version.Version=$(VERSION) \
           -X mcpx/internal/version.Commit=$(COMMIT) \
           -X mcpx/internal/version.Date=$(DATE)

.PHONY: all build test vet fmt cover cover-html check run stdio tidy clean \
        docker-build compose-config compose-up compose-down

all: build

build:
	go build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN) ./cmd/mcpx

test:
	go test -race ./...

vet:
	go vet ./...

fmt:
	gofmt -l -w .

cover:
	go test -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -1

cover-html: cover
	go tool cover -html=coverage.out

check: build
	./$(BIN) check -c $(CONFIG)

run: build
	./$(BIN) serve -c $(CONFIG)

stdio: build
	./$(BIN) stdio -c $(CONFIG)

# Image build with the git stamp forwarded. --network=host lets a host-side
# BUILD_PROXY (usually 127.0.0.1:<port>) resolve inside the build container;
# `make compose-up` builds on the bridge network and relies on the Dockerfile's
# no-proxy fallback instead.
docker-build:
	$(DOCKER) build --network=host \
	  --build-arg BUILD_PROXY=$(BUILD_PROXY) \
	  --build-arg VERSION=$(VERSION) \
	  --build-arg COMMIT=$(COMMIT) \
	  --build-arg DATE=$(DATE) \
	  -t $(IMAGE):$(VERSION) -t $(IMAGE):latest .

compose-config:
	VERSION=$(VERSION) COMMIT=$(COMMIT) DATE=$(DATE) \
	$(DOCKER) compose config -q && echo "compose config OK"

compose-up:
	VERSION=$(VERSION) COMMIT=$(COMMIT) DATE=$(DATE) $(DOCKER) compose up -d --build

compose-down:
	$(DOCKER) compose down

tidy:
	go mod tidy

clean:
	rm -rf bin coverage.out
