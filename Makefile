# yolo-code developer Makefile. Mirrors the CI stages (File 15 §15.15.1) so the
# same gates run locally and in CI. Windows note: `race` needs CGO/gcc, which
# is not installed by default on the dev machine; run it on the Linux runner.
.PHONY: all build vet fmt test test-race test-golden lint ci clean

GO ?= go

all: build

build:
	$(GO) build ./...

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

lint:
	golangci-lint run --timeout=5m

ci: vet fmt build test test-golden

clean:
	$(GO) clean -testcache
