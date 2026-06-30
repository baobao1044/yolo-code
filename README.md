# yolo-code

> Multi-agent terminal coding agent in Go — reads a task, thinks, runs tools, writes code.

[![CI](https://github.com/baobao1044/yolo-code/actions/workflows/ci.yml/badge.svg)](https://github.com/baobao1044/yolo-code/actions/workflows/ci.yml)
[![Go](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go)](https://go.dev/)
[![License](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

## Features

- **5 built-in tools**: `list_files`, `read_file`, `edit_file`, `bash`, `grep` — read repos, search code, edit files, run commands
- **Multi-turn agent loop**: Think → Tool Call → Execute → Verify → Think again until done
- **HITL approval gate**: Tools classified by risk (low/medium/high/critical) — require approval before running dangerous operations
- **Safe sandbox**: Blocks path escapes, wrapper peeling (`sudo`, `env`), shell escapes, network commands
- **2 modes**: Interactive TUI (beautiful terminal) + Headless (JSON events for CI/scripts)
- **OpenAI-compatible**: Works with any provider that supports the OpenAI API (GPT-4, etc.)
- **12-layer architecture**: Event Bus backbone, single-goroutine FSM, pure-Go vector store

## Architecture

```
┌──────────── TUI / Headless ────────────┐
│                                        │
│  L11 Multi-Agent ── L12 Infrastructure │
│         │                  │           │
│  L6 Cognitive ← L5 Prompt ← L4 Context │
│         │                              │
│  L7 Execution → L9 Patch → L8 Verify   │
│         │                  │           │
│  L10 Memory ←────────────────          │
│                                        │
│  L2 Runtime FSM ← L1 Session ← L3 Bus  │
└────────────────────────────────────────┘
```

See [docs/user/architecture.md](docs/user/architecture.md) for details.

## Installation

### go install (fastest)

```bash
go install github.com/baobao1044/yolo-code/cmd/yolo@latest
```

### Clone and build

```bash
git clone https://github.com/baobao1044/yolo-code.git
cd yolo-code
go build ./...
```

## Quickstart

### 1. Configure LLM

Create a `.env` file (or set environment variables directly):

```bash
cp .env.example .env
# Edit .env: add API key and choose model
```

Example with OpenAI:

```bash
export YOLO_API_KEY="sk-..."
export YOLO_BASE_URL="https://api.openai.com/v1"
export YOLO_MODEL="gpt-4o"
```

### 2. Run headless

```bash
echo "write a fibonacci function" | yolo --headless
```

Output: 1 JSON line per event. Ideal for scripts, golden tests, and CI.

### 3. Run interactive

```bash
yolo
```

Type a task at the prompt. The TUI displays a multi-agent board, cost meter, diff viewer.

## Configuration

| Variable | Default | Description |
|---|---|---|
| `YOLO_API_KEY` | — | API key for the LLM provider (canonical) |
| `OPENAI_API_KEY` | — | Fallback API key, used only if `YOLO_API_KEY` is unset |
| `YOLO_BASE_URL` | `https://api.openai.com/v1` | Base URL of the OpenAI-compatible API |
| `YOLO_MODEL` | `gpt-4o` | Model name |
| `YOLO_LOG` | — | Structured log file path |
| `YOLO_AUTO_APPROVE_MEDIUM` | `false` | Auto-approve medium-risk tools |
| `YOLO_AUTO_APPROVE_HIGH` | `false` | Auto-approve high-risk tools |

See [docs/user/configuration.md](docs/user/configuration.md) for full details.

## Tools

| Tool | Args | Risk | Description |
|---|---|---|---|
| `list_files` | — | Low | List all files in the repo |
| `read_file` | `file` | Low | Read file contents |
| `edit_file` | `file`, `content` | High | Overwrite file contents |
| `grep` | `pattern`, `path?` | Low | Search file contents for a regex pattern |
| `bash` | `command` | Medium–Critical | Run a shell command |

See [docs/user/tools.md](docs/user/tools.md) for details.

## Documentation

- [Quickstart](docs/user/quickstart.md) — Install and run for the first time
- [Commands & Flags](docs/user/commands.md) — All flags, env vars, exit codes
- [Architecture](docs/user/architecture.md) — 12-layer architecture
- [Configuration](docs/user/configuration.md) — Full configuration
- [Tools Reference](docs/user/tools.md) — Schema, risk, HITL flow
- [TUI Guide](docs/user/tui-guide.md) — How to use the TUI
- [CI/CD Workflow](docs/workflow/) — Pipeline and development workflow
- [RAG & Memory](docs/rag/) — Context engine, vector store, memory lifecycle
- [Sprint Progress](docs/progress/) — Progress tracking

## Development

```bash
make ci          # run all gates (vet, fmt, build, test, golden)
make test-race   # race detector (requires CGO/gcc, run on Linux)
make lint        # golangci-lint
```

See [CONTRIBUTING.md](CONTRIBUTING.md) to contribute.

## License

[MIT](LICENSE) © 2024–2026
