# Common development tasks. On Windows without make, the equivalent
# commands are "go build ./cmd/kubectl-tuggy" and "go test ./...".

BINARY  := kubectl-tuggy
PKG     := github.com/tuggy-dev/kubectl-tuggy
VERSION ?= $(shell git describe --tags --dirty 2>/dev/null || echo v0.0.0-dev)
COMMIT  ?= $(shell git rev-parse HEAD 2>/dev/null)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS := -s -w \
	-X $(PKG)/internal/version.Version=$(VERSION) \
	-X $(PKG)/internal/version.Commit=$(COMMIT) \
	-X $(PKG)/internal/version.Date=$(DATE)

.PHONY: all build install test lint fmt tidy clean

all: lint test build

## build: compile bin/kubectl-tuggy for this machine
build:
	go build -trimpath -ldflags '$(LDFLAGS)' -o bin/$(BINARY) ./cmd/kubectl-tuggy

## install: install kubectl-tuggy into GOBIN (or GOPATH/bin) so kubectl can find it
install:
	go install -trimpath -ldflags '$(LDFLAGS)' ./cmd/kubectl-tuggy

## test: run unit tests with the race detector
test:
	go test -race ./...

## lint: run golangci-lint
lint:
	golangci-lint run

## fmt: format code
fmt:
	golangci-lint fmt

## tidy: tidy go.mod and go.sum
tidy:
	go mod tidy

## clean: remove build output
clean:
	rm -rf bin dist

help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/^## //'
