# Commands and Flags

## Global flags

| Flag | Default | Description |
|---|---|---|
| `--headless` | `false` | Run without TUI; emit JSON events to stdout |
| `--repo` | cwd | Repo root for the context engine |
| `--open` | `""` | Comma-separated list of files to load into context |
| `--model` | from env | Override model name |
| `--base-url` | from env | Override API base URL |
| `--version` | n/a | Print version and exit |

## Environment variables

| Variable | Purpose |
|---|---|
| `OPENAI_API_KEY` | API key for the LLM provider (required) |
| `OPENAI_BASE_URL` | Base URL of the OpenAI-compatible API |
| `OPENAI_MODEL` | Model name (e.g. `gpt-4`) |
| `YOLO_LOG` | Structured log file path |
| `YOLO_AUTO_APPROVE_MEDIUM` | `"true"` = auto-approve medium-risk tools |
| `YOLO_AUTO_APPROVE_HIGH` | `"true"` = auto-approve high-risk tools |
| `YOLO_REPO_ROOT` | Repo root (default = cwd) |

## Exit codes

| Code | Meaning |
|---|---|
| `0` | Task completed successfully |
| `1` | Task failed, was cancelled, or unexpected error |
| `130` | Interrupted by user (Ctrl+C) |

Interactive mode returns `0` only when the task reaches `task.completed`. Headless mode returns `0` if the transcript ends in a completed state.

## Examples

```bash
# Headless — task from stdin
echo "refactor the main function" | yolo --headless

# Headless — specify repo
echo "fix the bug" | yolo --headless --repo /path/to/project

# Headless — load specific files into context
echo "explain the code" | yolo --headless --open main.go,internal/cognitive/core.go

# Interactive
yolo

# Interactive — specify model
yolo --model gpt-4o --base-url https://api.openai.com/v1

# Version
yolo version
```

## Headless JSON format

Each event is a JSON line. Main event types:

| Event | Description |
|---|---|
| `state.change` | FSM state transition |
| `observation` | Tool execution result |
| `task.completed` | Task completed |
| `task.failed` | Task failed |

Example:

```json
{"type":"state.change","state":"think"}
{"type":"state.change","state":"exec"}
{"type":"observation","tool":"bash","stdout":"ok"}
{"type":"task.completed"}
```
