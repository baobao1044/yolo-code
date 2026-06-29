# Đóng góp vào yolo-code

Cảm ơn bạn muốn đóng góp! Dưới đây là hướng dẫn để đảm bảo quy trình trơn tru.

## Fork & Clone

```bash
# 1. Fork repo trên GitHub
# 2. Clone fork của bạn
git clone https://github.com/<username>/yolo-code.git
cd yolo-code

# 3. Thêm upstream remote
git remote add upstream https://github.com/baobao1044/yolo-code.git
```

## Quy trình phát triển

### Tạo branch

```bash
git checkout -b feature/ten-tinh-nang
# hoặc
git checkout -b fix/ten-bug
```

### Chạy CI locally trước khi push

```bash
make ci
```

Lệnh này chạy tất cả CI gates: `vet` → `fmt` → `build` → `test` → `golden`.

Nếu bạn có CGO/gcc (Linux):

```bash
make test-race    # race detector
make lint         # golangci-lint
```

### Commit message

Sử dụng [Conventional Commits](https://www.conventionalcommits.org/):

```
feat: thêm tool xyz
fix: sửa crash khi input rỗng
docs: cập nhật quickstart
refactor: tách module cognitive
test: thêm test cho sandbox
chore: bump Go 1.26
```

### Tạo Pull Request

1. Push branch lên fork:
   ```bash
   git push origin feature/ten-tinh-nang
   ```
2. Tạo PR từ fork → `baobao1044/yolo-code` branch `master`
3. Mô tả rõ: **What** (làm gì), **Why** (tại sao), **How** (cách làm)
4. Đảm bảo CI pass (GitHub Actions chạy tự động)

## Code Style

| Quy tắc | Công cụ |
|---|---|
| Format | `gofmt` (chạy `make fmt`) |
| Static analysis | `go vet` (chạy `make vet`) |
| Linter | `golangci-lint` (chạy `make lint`) |
| Import order | `gofmt` tự xử |

### Quy ước code

- **Mỗi layer chỉ import layer thấp hơn + Event Bus** — không bao giờ phụ thuộc TUI
- **Event-driven**: Memory updates CHỈ qua events, không bao giờ gọi trực tiếp
- **Single-goroutine FSM**: Runtime state machine chạy trên 1 goroutine duy nhất
- **Sandbox-first**: Tất cả filesystem/shell operations phải đi qua sandbox

## Testing

### Loại tests

| Loại | Lệnh | Khi nào dùng |
|---|---|---|
| Unit | `make test` | Mỗi lần thay đổi code |
| Race | `make test-race` | Trước merge (cần CGO) |
| Golden | `make test-golden` | Kiểm tra deterministic output |
| Snapshot | `make test-snapshot` | Performance budgets |
| Docs | `make test-docs` | Documentation coverage |
| Lint | `make lint` | Trước push |

### Viết test mới

- Đặt test file cùng thư mục với code: `foo.go` → `foo_test.go`
- Dùng `t.Parallel()` khi test không phụ thuộc lẫn nhau
- Golden tests: đặt trong `testdata/` với build tag `//go:build golden`
- Snapshot tests: build tag `//go:build snapshot`

## Debugging

```bash
# Chạy 1 test cụ thể
go test -run TestFunctionName ./internal/cognitive/...

# Xem log chi tiết
YOLO_LOG=/tmp/yolo.log go test -v ./...

# Race detector locally (cần CGO)
CGO_ENABLED=1 go test -race ./...
```

## Cấu trúc thư mục

```
cmd/yolo/          # CLI entrypoint
internal/
  cognitive/       # L6 Cognitive Core (provider, core)
  exec/            # L7 Execution Engine (tools, sandbox)
  runtime/          # L2 Runtime FSM
  event/            # L3 Event Bus
  session/          # L1 Session Manager
  context/          # L4 Context Engine
  prompt/           # L5 Prompt Compiler
  memory/           # L10 Memory System
  coord/            # L11 Multi-Agent
  infra/            # L12 Infrastructure
  tui/              # TUI (bubbletea)
  patch/            # L9 Patch Engine
  verify/           # L8 Verification
docs/               # Documentation
00-15*.md           # Design docs (developer-facing)
```

## Cần giúp đỡ?

- Mở issue trên GitHub
- Hỏi trong Discussions
- Xem thêm [docs/workflow/development.md](docs/workflow/development.md)
