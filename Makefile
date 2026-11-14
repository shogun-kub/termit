# Convenience targets only. Every target maps to a plain `go` command documented in AGENTS.md §4.2.
#
# Package patterns are explicit (`./cmd/... ./internal/...`) on purpose: `references/` contains
# third-party Go source inside this module's directory tree, and `./...` would compile it.
GO      ?= go
PKGS    := ./cmd/... ./internal/...
VERSION ?= v0.1.0

.PHONY: all build test race vet fmt lint dist lab clean clean-cache push

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
	gofmt -l -w cmd internal deploy

lint:
	golangci-lint run $(PKGS)

# The release directory, built by the same tool CI uses: four binaries, the bootstrap script, the
# management server's unit and SHA256SUMS. Override the version for a real release:
# `make dist VERSION=v0.2.0`. Never build on the target host.
dist:
	$(GO) run ./deploy/release --version $(VERSION) --out dist

# Requires the environment described in AGENTS.md §4.3.
lab:
	$(GO) test -tags=lab ./test/lab -v -timeout 30m

# Everything a build, a test run or a lab session leaves behind, and nothing that costs a download:
# `deploy/clean` keeps the Go toolchain, the caches and the lab notes unless asked otherwise.
clean:
	$(GO) run ./deploy/clean

# The partial cleanup: the compiled output goes, the downloaded modules stay. The next build is a short
# recompile rather than a download.
clean-cache:
	$(GO) run ./deploy/clean --build-cache

# Push the current branch, then clean: the two steps go together, because after a push the artifacts are
# spent and the next thing that happens is a fresh build.
push:
	git push
	$(GO) run ./deploy/clean
