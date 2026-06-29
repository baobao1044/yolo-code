# Quickstart

## Installation

### Option 1: go install (fastest)

```bash
go install github.com/yolo-code/yolo/cmd/yolo@latest
```

The binary will be at `$GOPATH/bin/yolo` (or `$HOME/go/bin/yolo`).

### Option 2: Clone and build

```bash
git clone https://github.com/baobao1044/yolo-code.git
cd yolo-code
go build -o yolo ./cmd/yolo
```

## Configure LLM Provider

yolo-code requires an LLM provider with an OpenAI-compatible API. Configure via environment variables or a `.env` file.

### Option A: OpenAI

```bash
export OPENAI_API_KEY="sk-..."
export OPENAI_BASE_URL="https://api.openai.com/v1"
export OPENAI_MODEL="gpt-4"
```

### Option B: .env file

```bash
cp .env.example .env
# Edit .env with your API key and model
```

> yolo-code automatically loads `.env` if the file exists in the current directory.

## First Run

### Headless mode

```bash
echo "explain the main function" | yolo --headless
```

Output: 1 JSON line per event. Example:

```json
{"type":"state.change","state":"think"}
{"type":"state.change","state":"exec"}
{"type":"observation","tool":"read_file","stdout":"package main..."}
{"type":"state.change","state":"think"}
{"type":"task.completed"}
```

Headless mode is ideal for:
- Scripts and automation
- Golden tests (deterministic output for same input)
- CI pipelines

### Interactive mode

```bash
yolo
```

The TUI displays:
- **Board**: multi-agent progress when the coordination layer splits a task
- **Cost meter**: token usage and cost
- **Diff viewer**: see file changes in real-time
- **Status bar**: current FSM state

Type a task at the prompt and press Enter.

## Example: Create a small project

```bash
# Run the agent to create a Fibonacci CLI
echo "create a CLI tool that computes fibonacci with tests" | yolo --headless
```

The agent will:
1. `list_files` — see the current repo
2. `edit_file` — create `main.go` with a fibonacci function
3. `edit_file` — create `main_test.go` with tests
4. `bash` — run `go test`
5. `bash` — run `go build`
6. Verify and complete

## Check version

```bash
yolo version
```

## Next steps

- [Commands & Flags](commands.md) — all flags and env vars
- [Configuration](configuration.md) — full configuration
- [Tools Reference](tools.md) — 4 tools and how they work
- [TUI Guide](tui-guide.md) — using the TUI
