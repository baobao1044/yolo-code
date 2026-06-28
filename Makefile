# yolo-code developer Makefile. Mirrors the CI stages (File 15 §15.15.1) so the
# same gates run locally and in CI. Windows note: `race` needs CGO/gcc, which
# is not installed by default on the dev machine; run it on the Linux runner.
.PHONY: all build vet fmt test test-race test-golden test-snapshot test-docs lint ci clean cross snapshot

GO ?= go

all: build

build:
	$(GO) build ./...

# Cross-compile matrix for release targets (H-001). Pure Go supports these
# without CGO; the Windows dev box has no make, so these commands are also
# exercised directly in CI.
cross:
	GOOS=linux  GOARCH=amd64 $(GO) build ./...
	GOOS=linux  GOARCH=arm64 $(GO) build ./...
	GOOS=darwin GOARCH=amd64 $(GO) build ./...
	GOOS=darwin GOARCH=arm64 $(GO) build ./...

vet:
	$(GO) vet ./...

fmt:
	@gofmt -l . | tee /dev/stderr | grep -q . && exit 1 || true

test:
	$(GO) test ./...

# Race detector (requires CGO + gcc). On the dev Windows box this is skipped
# and run in CI's Linux runner instead.
test-race:
	CGO_ENABLED=1 $(GO) test -race ./...

# Golden-transcript determinism gate (S5). Build tag isolates these tests.
test-golden:
	$(GO) test -tags=golden ./...

# Snapshot performance budgets (S1/S2). Isolated by build tag so micro-
# benchmarks don't slow the default loop.
test-snapshot:
	$(GO) test -tags=snapshot ./internal/tui

# Documentation coverage gate (H-007). Isolated by build tag.
test-docs:
	$(GO) test -tags=docs ./cmd/yolo

# Release dry-run via goreleaser (H-008). Snapshot mode does not publish.
snapshot:
	goreleaser release --snapshot --clean

lint:
	golangci-lint run --timeout=5m

ci: vet fmt build test test-golden

clean:
	$(GO) clean -testcache
