# Development Workflow

## Git Workflow

### Branch naming

```
main branch: master

Feature branches:  feature/ten-tinh-nang
Bug fix branches:  fix/ten-bug
Doc branches:      docs/ten-tai-lieu
```

### Quy trình

```
1. Tạo branch từ master
   git checkout -b feature/xyz

2. Phát triển + test locally
   make ci

3. Commit (Conventional Commits)
   git commit -m "feat: thêm tool xyz"

4. Push lên fork
   git push origin feature/xyz

5. Tạo PR → master

6. CI chạy tự động

7. Review + approve

8. Merge (squash or merge commit)
```

### Conventional Commits

```
feat:     Tính năng mới
fix:      Sửa bug
docs:     Tài liệu
refactor: Refactor không đổi behaviour
test:     Thêm/sửa tests
chore:    Build, deps, v.v.
perf:     Performance improvement
ci:       CI/CD changes
```

## Sprint Cadence

yolo-code phát triển theo sprints (xem `15-Implementation_Roadmap.md`):

| Sprint | Tên | Focus |
|---|---|---|
| S1–S3 | Foundation | Session, Runtime FSM, Event Bus |
| S4–S6 | Cognition | Context Engine, Prompt Compiler, Cognitive Core |
| S7–S9 | Action | Execution Engine, Patch Engine, Verification |
| S10–S11 | Infrastructure + Hardening | TUI, Sandbox hardening |
| S12–S13 | Integration + Superpowers | Multi-agent, RAG |

Xem chi tiết tại [Sprint Progress](../progress/sprint-status.md).

## Testing Strategy

### Pyramids

```
        ┌──────────┐
        │  Golden   │  ← Deterministic output
        │  Snapshot │  ← Performance budgets
        ├──────────┤
        │  Race     │  ← Data race detection
        │  Docs     │  ← Documentation coverage
        ├──────────┤
        │  Unit     │  ← Logic correctness
        │  Tests    │
        └──────────┘
```

### Khi nào chạy gì

| Hoạt động | Chạy |
|---|---|
| Mỗi lần save | `make test` |
| Trước commit | `make ci` |
| Trước push/PR | `make ci && make lint` |
| Trước merge | `make ci && make lint && make test-race` |

### Viết tests

**Unit tests**: đặt cùng thư mục với code
```go
// foo.go
func Foo() int { return 42 }

// foo_test.go
func TestFoo(t *testing.T) {
    if got := Foo(); got != 42 {
        t.Errorf("Foo() = %d, want 42", got)
    }
}
```

**Golden tests**: build tag `//go:build golden`
```go
//go:build golden

func TestGoldenTranscript(t *testing.T) {
    // So sánh output với golden file
}
```

**Snapshot tests**: build tag `//go:build snapshot`
```go
//go:build snapshot

func TestSnapshotBudgets(t *testing.T) {
    // Kiểm tra performance budgets (S1/S2)
}
```

## Debugging

### Chạy 1 test cụ thể

```bash
go test -run TestFunctionName ./internal/cognitive/...
```

### Verbose output

```bash
go test -v ./internal/cognitive/...
```

### Race detector

```bash
# Linux/Mac (cần gcc)
CGO_ENABLED=1 go test -race ./...

# Windows — skip, rely on CI
go test ./...
```

### Structured logging

```bash
export YOLO_LOG=/tmp/yolo-debug.log
yolo --headless < task.txt
# Xem log
cat /tmp/yolo-debug.log
```

### Debug LLM calls

Log bao gồm LLM requests/responses (đã redact API keys). Filter:

```bash
grep "tool_call" /tmp/yolo-debug.log
grep "think" /tmp/yolo-debug.log
grep "error" /tmp/yolo-debug.log
```

## Code Review Checklist

Trước khi approve PR:

- [ ] `make ci` pass
- [ ] `make lint` clean
- [ ] Tests mới cho code mới
- [ ] No data races (`make test-race` pass)
- [ ] Documentation cập nhật (nếu cần)
- [ ] Import matrix tuân thủ (layer chỉ phụ thuộc layer thấp hơn)
- [ ] Sandbox path cho tool mới (nếu thêm tool)
- [ ] HITL risk classification hợp lý (nếu thêm tool)

## Xem thêm

- [CI/CD Pipeline](ci-cd.md) — chi tiết CI jobs
- [CONTRIBUTING.md](../../CONTRIBUTING.md) — hướng dẫn đóng góp đầy đủ
- [Sprint Progress](../progress/sprint-status.md) — tiến trình sprints
