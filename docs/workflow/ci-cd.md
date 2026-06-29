# CI/CD Pipeline

yolo-code có 2 GitHub Actions workflows + Makefile mirror cho local development.

## CI Pipeline (`ci.yml`)

Chạy trên mỗi push đến `master` và mỗi pull request. Concurrency: cancel in-progress khi có push mới.

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

### Jobs chi tiết

| Job | Runner | Mô tả | Lệnh |
|---|---|---|---|
| `lint` | ubuntu-latest | golangci-lint | `golangci-lint run --timeout=5m` |
| `build-and-test` | ubuntu/win/mac | Build + vet + fmt + unit | `go build/vet/test` |
| `race-and-golden` | ubuntu-latest | Race detector + golden tests | `go test -race`, `go test -tags=golden` |
| `cross-compile` | ubuntu-latest | Cross-compile 4 targets | `make cross` |
| `snapshot` | ubuntu-latest | Performance budgets | `make test-snapshot` |
| `docs` | ubuntu-latest | Doc coverage | `make test-docs` |

### Race detector

Race detector yêu cầu CGO + gcc. Chỉ chạy trên Linux runner (GitHub-hosted có gcc). Nếu development trên Windows/Mac, skip race tests locally và rely on CI.

## Release Dry-Run (`release.yml`)

Chạy trên mỗi push đến `master`. Sử dụng GoReleaser snapshot mode — **KHÔNG** tạo tag, release, hay publish artifacts.

```
checkout (fetch-depth: 0)
  → setup Go 1.26
    → GoReleaser snapshot
      → artifacts trong dist/ (bị discard)
```

## Makefile Targets

Makefile mirror CI stages để chạy locally:

| Target | Lệnh | Mô tả |
|---|---|---|
| `make all` | `go build ./...` | Build tất cả |
| `make build` | `go build ./...` | Build |
| `make cross` | `GOOS=linux GOARCH=amd64 go build ...` | Cross-compile 4 targets |
| `make vet` | `go vet ./...` | Static analysis |
| `make fmt` | `gofmt -l .` | Kiểm tra format |
| `make test` | `go test ./...` | Unit tests |
| `make test-race` | `CGO_ENABLED=1 go test -race ./...` | Race detector |
| `make test-golden` | `go test -tags=golden ./...` | Golden-transcript determinism |
| `make test-snapshot` | `go test -tags=snapshot ./internal/tui` | Performance budgets |
| `make test-docs` | `go test -tags=docs ./cmd/yolo` | Doc coverage |
| `make lint` | `golangci-lint run --timeout=5m` | Linter |
| `make ci` | `vet → fmt → build → test → golden` | Chuẩn CI gates |
| `make snapshot` | `goreleaser release --snapshot --clean` | Release dry-run |
| `make clean` | `go clean -testcache` | Xoá test cache |

### Chạy CI locally

```bash
# Tất cả CI gates (race test cần CGO)
make ci

# Thêm race test (chỉ Linux/Mac có gcc)
make test-race

# Thêm lint
make lint

# Full kiểm tra trước push
make ci && make lint && make test-race
```

## Badges

| Badge | Workflow | Ý nghĩa |
|---|---|---|
| ![CI](https://github.com/baobao1044/yolo-code/actions/workflows/ci.yml/badge.svg) | `ci.yml` | Tất cả jobs pass trên master/PR |
| CI fail | `ci.yml` | Ít nhất 1 job fail — phải fix trước merge |

## Build Tags

Một số tests tách biệt bằng build tags để không làm chậm default test loop:

| Tag | Mô tả | Target |
|---|---|---|
| `golden` | Golden-transcript determinism | `make test-golden` |
| `snapshot` | Performance budgets | `make test-snapshot` |
| `docs` | Documentation coverage | `make test-docs` |

## Xem thêm

- [Development Workflow](development.md) — Git workflow, sprint cadence, debugging
- [Configuration](../user/configuration.md) — Cấu hình env vars
- [Makefile](../../Makefile) — Source gốc
