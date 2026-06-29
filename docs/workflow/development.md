# Development Workflow

## Git Workflow

### Branch naming

```
Main branch: master

Feature branches:  feature/my-feature
Bug fix branches:  fix/my-bug
Doc branches:      docs/my-document
```

### Process

```
1. Create branch from master
   git checkout -b feature/xyz

2. Develop + test locally
   make ci

3. Commit (Conventional Commits)
   git commit -m "feat: add xyz tool"

4. Push to fork
   git push origin feature/xyz

5. Create PR → master

6. CI runs automatically

7. Review + approve

8. Merge (squash or merge commit)
```

### Conventional Commits

```
feat:     New feature
fix:      Bug fix
docs:     Documentation
refactor: Refactor without behaviour change
test:     Add/fix tests
chore:    Build, deps, etc.
perf:     Performance improvement
ci:       CI/CD changes
```

## Sprint Cadence

yolo-code is developed in sprints (see `15-Implementation_Roadmap.md`):

| Sprint | Name | Focus |
|---|---|---|
| S1–S3 | Foundation | Session, Runtime FSM, Event Bus |
| S4–S6 | Cognition | Context Engine, Prompt Compiler, Cognitive Core |
| S7–S9 | Action | Execution Engine, Patch Engine, Verification |
| S10–S11 | Infrastructure + Hardening | TUI, Sandbox hardening |
| S12–S13 | Integration + Superpowers | Multi-agent, RAG |

See [Sprint Progress](../progress/sprint-status.md) for details.

## Testing Strategy

### Pyramid

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

### When to run what

| Activity | Run |
|---|---|
| Every save | `make test` |
| Before commit | `make ci` |
| Before push/PR | `make ci && make lint` |
| Before merge | `make ci && make lint && make test-race` |

### Writing tests

**Unit tests**: place in the same directory as the code
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
    // Compare output against golden file
}
```

**Snapshot tests**: build tag `//go:build snapshot`
```go
//go:build snapshot

func TestSnapshotBudgets(t *testing.T) {
    // Check performance budgets (S1/S2)
}
```

## Debugging

### Run a specific test

```bash
go test -run TestFunctionName ./internal/cognitive/...
```

### Verbose output

```bash
go test -v ./internal/cognitive/...
```

### Race detector

```bash
# Linux/Mac (requires gcc)
CGO_ENABLED=1 go test -race ./...

# Windows — skip, rely on CI
go test ./...
```

### Structured logging

```bash
export YOLO_LOG=/tmp/yolo-debug.log
yolo --headless < task.txt
# View log
cat /tmp/yolo-debug.log
```

### Debug LLM calls

Logs include LLM requests/responses (API keys redacted). Filter:

```bash
grep "tool_call" /tmp/yolo-debug.log
grep "think" /tmp/yolo-debug.log
grep "error" /tmp/yolo-debug.log
```

## Code Review Checklist

Before approving a PR:

- [ ] `make ci` passes
- [ ] `make lint` is clean
- [ ] New tests for new code
- [ ] No data races (`make test-race` passes)
- [ ] Documentation updated (if needed)
- [ ] Import matrix respected (layers only depend on lower layers)
- [ ] Sandbox path for new tool (if adding a tool)
- [ ] HITL risk classification is sensible (if adding a tool)

## See also

- [CI/CD Pipeline](ci-cd.md) — CI job details
- [CONTRIBUTING.md](../../CONTRIBUTING.md) — full contributing guide
- [Sprint Progress](../progress/sprint-status.md) — sprint progress
