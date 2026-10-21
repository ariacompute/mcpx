BIN := bin/mcpx
CONFIG ?= mcpx.yaml

VERSION ?= $(shell git describe --tags --abbrev=0 --always 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short=7 HEAD 2>/dev/null || echo unknown)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS := -X mcpx/internal/version.Version=$(VERSION) \
           -X mcpx/internal/version.Commit=$(COMMIT) \
           -X mcpx/internal/version.Date=$(DATE)

.PHONY: all build test vet fmt cover cover-html check run stdio tidy clean

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

tidy:
	go mod tidy

clean:
	rm -rf bin coverage.out
