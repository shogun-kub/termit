# Convenience targets only. Every target maps to a plain `go` command documented in AGENTS.md §4.2.
#
# Package patterns are explicit (`./cmd/... ./internal/...`) on purpose: `references/` contains
# third-party Go source inside this module's directory tree, and `./...` would compile it.
GO       ?= go
PKGS     := ./cmd/... ./internal/...
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT   ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE     ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
VERPKG   := github.com/shogun-kub/termit/internal/version
LDFLAGS  := -s -w \
	-X $(VERPKG).Version=$(VERSION) \
	-X $(VERPKG).Commit=$(COMMIT) \
	-X $(VERPKG).Date=$(DATE)

.PHONY: all build test race vet fmt lint dist lab clean

all: build

build:
	$(GO) build $(PKGS)

test:
	$(GO) test $(PKGS)

race:
	$(GO) test -race ./internal/...

vet:
	$(GO) vet $(PKGS)

fmt:
	gofmt -l -w cmd internal

lint:
	golangci-lint run $(PKGS)

# Cross-compiled release binaries. Never build on the target host.
dist:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o dist/linux-amd64/termit-uv ./cmd/uv
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o dist/linux-amd64/termit-mn ./cmd/mn
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o dist/linux-arm64/termit-uv ./cmd/uv
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o dist/linux-arm64/termit-mn ./cmd/mn

# Requires the environment described in AGENTS.md §4.3.
lab:
	$(GO) test -tags=lab ./test/lab -v -timeout 30m

clean:
	rm -rf dist
