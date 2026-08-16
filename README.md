# yolo-code

> Multi-agent terminal coding agent in Go — reads a task, thinks, runs tools, writes code.

[![CI](https://github.com/baobao1044/yolo-code/actions/workflows/ci.yml/badge.svg)](https://github.com/baobao1044/yolo-code/actions/workflows/ci.yml)
[![Go](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go)](https://go.dev/)
[![License](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

## Features

- **5 built-in tools**: `list_files`, `read_file`, `edit_file`, `bash`, `grep` — read repos, search code, edit files, run commands
- **Multi-turn agent loop**: Think → Tool Call → Execute → Verify → Think again until done
- **HITL approval gate**: Tools classified by risk (low/medium/high/critical); critical is denied outright. The gate is implemented, but the composition roots currently auto-approve medium and high risk, so no prompt fires — see the note under [Configuration](#configuration) below
- **Safe sandbox**: Blocks path escapes, wrapper peeling (`sudo`, `env`), shell escapes, network commands
- **2 modes**: Interactive TUI (beautiful terminal) + Headless (JSON events for CI/scripts)
- **OpenAI-compatible**: Works with any provider that supports the OpenAI API (GPT-4, etc.)
- **12-layer architecture**: Event Bus backbone, single-goroutine FSM, pure-Go retrieval index
- **RAG & memory (S13)**: Cold-start repo indexing (per-function chunking), lexical retrieval wired into the prompt under a `<rag>` tag, 6 event-driven memory types with cross-session persistence, knowledge insight store fed by verify/task events, rolling-window exec history, LRU eviction
  - Retrieval is a pure-Go lexical index — hashed term-frequency similarity, so a query matches chunks that share its literal tokens. There is no embedding model; the `Embedder` interface is the seam where one plugs in. See [docs/rag/vector-store.md](docs/rag/vector-store.md).

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
| `YOLO_LOG` | — | Structured log file path (not read by the current code) |
| `YOLO_AUTO_APPROVE_MEDIUM` | `false` | Auto-approve medium-risk tools (`bash` on safe commands) without prompting |
| `YOLO_AUTO_APPROVE_HIGH` | `false` | Auto-approve high-risk tools (`edit_file`, dangerous `bash`) without prompting |
| `YOLO_PROVIDER` | — | Provider preset name (openai, groq, ollama, etc.) — use `/provider` in TUI to list/switch |
| `YOLO_THEME` | `dark` | TUI color theme: `dark`/`light`/`contrast`/`mono` |
| `NO_COLOR` | — | Any non-empty value forces the mono theme (per [NO_COLOR](https://no-color.org/)) |
| `YOLO_NO_MOTION` | — | Any non-empty value disables spinner + cursor blink (reduced motion) |
| `YOLO_COST_PER_TOKEN` | `0` | Dollars per token, charged on (in + out) for turns whose provider reported usage |
| `YOLO_MAX_COST` | — | **Hard cap**: dollar spend. Reaching it cancels the task. Needs `YOLO_COST_PER_TOKEN` to ever fire |
| `YOLO_MAX_TIME` | — | **Hard cap**: wall clock as a Go duration (`10m`, `90s`). A unitless `600` is rejected and caps nothing |
| `YOLO_COST_RATES` | — | `tool=dollars` pairs you price yourself, e.g. `bash=0.005,*=0.001`. Unpriced runs report tool calls, not money |
| `YOLO_MAX_TOOL_CALLS` | — | Warn once at this many tool invocations. A warning only — **not** a cap; the run continues |
| `YOLO_WINDOW` | `128000` | Context window in tokens reported to the provider. A non-numeric or non-positive value is ignored |
| `YOLO_REPO_ROOT` | working dir | Workspace root (`--repo`) |
| `YOLO_MEMORY_DIR` | `<user config dir>/yolo-code/memory` | Durable memory root |
| `YOLO_SESSION_DIR` | `<user config dir>/yolo-code/sessions` | Durable session/transcript root |
| `YOLO_EVENT_LOG` | — | Append every event to a durable, fsynced log (`--event-log`), making a run replayable after a crash |
| `YOLO_STUB` | — | Use the deterministic offline stub provider (`--stub`) — **not** a real model. Stub replies are labelled as such |

> **Approval defaults to on.** Both `YOLO_AUTO_APPROVE_*` variables are read, in
> `newExecEngine` — the single point where headless, TUI and coord all build
> their engine — and both default to `false`, so medium- and high-risk tools
> prompt unless you opt out. `--auto-approve` is shorthand for setting both.
> Critical-risk tools are denied outright and no setting overrides that.
>
> This was previously broken and previously documented as broken: the variables
> were read nowhere and the map was hardcoded to `{medium: true, high: true}`,
> so no shipped path ever asked a human anything. Both the defect and the note
> warning about it are gone.

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
- [RAG & Memory](docs/rag/) — Context engine, lexical retrieval index, memory lifecycle
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
