# Contributing to yolo-code

Thanks for wanting to contribute! Here's how to ensure a smooth process.

## Fork & Clone

```bash
# 1. Fork the repo on GitHub
# 2. Clone your fork
git clone https://github.com/<username>/yolo-code.git
cd yolo-code

# 3. Add upstream remote
git remote add upstream https://github.com/baobao1044/yolo-code.git
```

## Development Workflow

### Create a branch

```bash
git checkout -b feature/my-feature
# or
git checkout -b fix/my-bug
```

### Run CI locally before pushing

```bash
make ci
```

This runs all CI gates: `vet` → `fmt` → `build` → `test` → `golden`.

If you have CGO/gcc (Linux):

```bash
make test-race    # race detector
make lint         # golangci-lint
```

### Commit messages

Use [Conventional Commits](https://www.conventionalcommits.org/):

```
feat: add xyz tool
fix: fix crash on empty input
docs: update quickstart
refactor: extract cognitive module
test: add sandbox tests
chore: bump Go 1.26
```

### Create a Pull Request

1. Push branch to your fork:
   ```bash
   git push origin feature/my-feature
   ```
2. Create a PR from your fork → `baobao1044/yolo-code` branch `master`
3. Describe clearly: **What** (what you did), **Why** (why), **How** (how)
4. Ensure CI passes (GitHub Actions runs automatically)

## Code Style

| Rule | Tool |
|---|---|
| Format | `gofmt` (run `make fmt`) |
| Static analysis | `go vet` (run `make vet`) |
| Linter | `golangci-lint` (run `make lint`) |
| Import order | `gofmt` handles it |

### Code conventions

- **Each layer may only import lower layers + Event Bus** — never depend on TUI
- **Event-driven**: Memory updates ONLY via events, never direct calls
- **Single-goroutine FSM**: Runtime state machine runs on exactly 1 goroutine
- **Sandbox-first**: All filesystem/shell operations must go through the sandbox

## Testing

### Test types

| Type | Command | When to use |
|---|---|---|
| Unit | `make test` | Every code change |
| Race | `make test-race` | Before merge (requires CGO) |
| Golden | `make test-golden` | Check deterministic output |
| Snapshot | `make test-snapshot` | Performance budgets |
| Docs | `make test-docs` | Documentation coverage |
| Lint | `make lint` | Before push |

### Writing new tests

- Place test files in the same directory as the code: `foo.go` → `foo_test.go`
- Use `t.Parallel()` when tests don't depend on each other
- Golden tests: place in `testdata/` with build tag `//go:build golden`
- Snapshot tests: build tag `//go:build snapshot`

## Debugging

```bash
# Run a specific test
go test -run TestFunctionName ./internal/cognitive/...

# Verbose logging
YOLO_LOG=/tmp/yolo.log go test -v ./...

# Race detector locally (requires CGO)
CGO_ENABLED=1 go test -race ./...
```

## Directory structure

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

## Need help?

- Open an issue on GitHub
- Ask in Discussions
- See [docs/workflow/development.md](docs/workflow/development.md)
