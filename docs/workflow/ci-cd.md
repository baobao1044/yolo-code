# CI/CD Pipeline

yolo-code has 2 GitHub Actions workflows + a Makefile mirror for local development.

## CI Pipeline (`ci.yml`)

Runs on every push to `master` and every pull request. Concurrency: cancel in-progress when a new push arrives.

### Pipeline stages

```
lint ─────────────────────────────────────────────────┐
                                                      │
build-and-test (matrix: ubuntu/win/mac)               │
  ├─ Build all platforms                              │
  ├─ go vet                                           │
  ├─ gofmt check                                      │
  └─ Unit tests                                       ├──► Pass/Fail
                                                      │
race-and-golden (ubuntu, CGO enabled)                 │
  ├─ Race tests (-race)                               │
  └─ Golden-transcript determinism                    │
                                                      │
cross-compile (ubuntu)                                │
  └─ make cross (linux/darwin, amd64/arm64)           │
                                                      │
snapshot (ubuntu)                                     │
  └─ Performance budgets (S1/S2)                      │
                                                      │
docs (ubuntu)                                         │
  └─ Documentation coverage gate                      │
                                                      ┘
```

### Job details

| Job | Runner | Description | Command |
|---|---|---|---|
| `lint` | ubuntu-latest | golangci-lint | `golangci-lint run --timeout=5m` |
| `build-and-test` | ubuntu/win/mac | Build + vet + fmt + unit | `go build/vet/test` |
| `race-and-golden` | ubuntu-latest | Race detector + golden tests | `go test -race`, `go test -tags=golden` |
| `cross-compile` | ubuntu-latest | Cross-compile 4 targets | `make cross` |
| `snapshot` | ubuntu-latest | Performance budgets | `make test-snapshot` |
| `docs` | ubuntu-latest | Doc coverage | `make test-docs` |

### Race detector

The race detector requires CGO + gcc. It only runs on the Linux runner (GitHub-hosted has gcc). If developing on Windows/Mac, skip race tests locally and rely on CI.

## Release Dry-Run (`release.yml`)

Runs on every push to `master`. Uses GoReleaser snapshot mode — does NOT create tags, releases, or publish artifacts.

```
checkout (fetch-depth: 0)
  → setup Go 1.26
    → GoReleaser snapshot
      → artifacts in dist/ (discarded)
```

## Makefile Targets

The Makefile mirrors CI stages for local running:

| Target | Command | Description |
|---|---|---|
| `make all` | `go build ./...` | Build everything |
| `make build` | `go build ./...` | Build |
| `make cross` | `GOOS=linux GOARCH=amd64 go build ...` | Cross-compile 4 targets |
| `make vet` | `go vet ./...` | Static analysis |
| `make fmt` | `gofmt -l .` | Check formatting |
| `make test` | `go test ./...` | Unit tests |
| `make test-race` | `CGO_ENABLED=1 go test -race ./...` | Race detector |
| `make test-golden` | `go test -tags=golden ./...` | Golden-transcript determinism |
| `make test-snapshot` | `go test -tags=snapshot ./internal/tui` | Performance budgets |
| `make test-docs` | `go test -tags=docs ./cmd/yolo` | Doc coverage |
| `make lint` | `golangci-lint run --timeout=5m` | Linter |
| `make ci` | `vet → fmt → build → test → golden` | Standard CI gates |
| `make snapshot` | `goreleaser release --snapshot --clean` | Release dry-run |
| `make clean` | `go clean -testcache` | Clear test cache |

### Running CI locally

```bash
# All CI gates (race test requires CGO)
make ci

# Add race test (only Linux/Mac with gcc)
make test-race

# Add lint
make lint

# Full check before push
make ci && make lint && make test-race
```

## Badges

| Badge | Workflow | Meaning |
|---|---|---|
| ![CI](https://github.com/baobao1044/yolo-code/actions/workflows/ci.yml/badge.svg) | `ci.yml` | All jobs pass on master/PR |
| CI fail | `ci.yml` | At least 1 job failed — must fix before merge |

## Build Tags

Some tests are isolated with build tags so they don't slow down the default test loop:

| Tag | Description | Target |
|---|---|---|
| `golden` | Golden-transcript determinism | `make test-golden` |
| `snapshot` | Performance budgets | `make test-snapshot` |
| `docs` | Documentation coverage | `make test-docs` |

## See also

- [Development Workflow](development.md) — Git workflow, sprint cadence, debugging
- [Configuration](../user/configuration.md) — Env vars configuration
- [Makefile](../../Makefile) — Source of truth
