# Commands and Flags

## Global flags

| Flag | Default | Description |
|---|---|---|
| `--headless` | `false` | Run without TUI; emit JSON events to stdout |
| `--repo` | cwd | Repo root for the context engine |
| `--model` | from env | Override model name |
| `--base-url` | from env | Override API base URL |
| `--plan <goal>` | — | Multi-agent orchestrator for a complex goal |
| `--version` | n/a | Print version and exit |

## Environment variables

| Variable | Purpose |
|---|---|
| `YOLO_API_KEY` | API key for the LLM provider (canonical) |
| `OPENAI_API_KEY` | Fallback API key, used only if `YOLO_API_KEY` is unset |
| `YOLO_PROVIDER` | Provider preset name (openai, groq, ollama, etc.) — see `/provider` in TUI |
| `YOLO_BASE_URL` | API base URL (default `https://api.openai.com/v1`) |
| `YOLO_MODEL` | Model name (default `gpt-4o`) |
| `YOLO_LOG` | Structured log file path — **not read by the current code** |
| `YOLO_AUTO_APPROVE_MEDIUM` | `"true"` = auto-approve medium-risk tools |
| `YOLO_AUTO_APPROVE_HIGH` | `"true"` = auto-approve high-risk tools |

> **Neither auto-approve variable is read.** Medium- and high-risk tools are
> hardcoded to auto-approve in every mode; see
> [Configuration → HITL Approval](configuration.md#hitl-approval).
| `YOLO_REPO_ROOT` | Repo root (default = cwd) |

## Slash commands (TUI)

| Command | Action |
|---|---|
| `/help` | Toggle help overlay |
| `/clear` | Wipe chat + reset scroll |
| `/theme` | List themes + current |
| `/theme <name>` | Switch palette (dark/light/contrast/mono) |
| `/model <name>` | Swap model (rebuilds provider) |
| `/provider` | List all 29 provider presets |
| `/provider <name>` | Switch provider preset |
| `/status` | Show model, provider, theme |

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
