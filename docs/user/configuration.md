# yolo-code Configuration

## Overview

yolo-code is configured via 3 mechanisms (in descending priority):

1. **Command-line flags** — override everything
2. **Environment variables** — primary for deployment
3. **File `.env`** — convenient for local development

## LLM Provider

### Required variables

| Variable | Description | Example |
|---|---|---|
| `YOLO_API_KEY` | API key for the LLM provider (canonical) | `sk-...` |

> `OPENAI_API_KEY` is read as a fallback ONLY if `YOLO_API_KEY` is unset.

### Optional variables

| Variable | Default | Description |
|---|---|---|
| `YOLO_BASE_URL` | `https://api.openai.com/v1` | Base URL of the OpenAI-compatible API |
| `YOLO_MODEL` | `gpt-4o` | Model name |
| `YOLO_WINDOW` | `128000` | Context window size (tokens) |

### Popular providers

#### OpenAI

```bash
export YOLO_API_KEY="sk-..."
export YOLO_BASE_URL="https://api.openai.com/v1"
export YOLO_MODEL="gpt-4o"
```

#### Custom provider

Any API compatible with OpenAI chat completions:

```bash
export YOLO_API_KEY="your-key"
export YOLO_BASE_URL="https://your-api.com/v1"
export YOLO_MODEL="your-model"
```

## Sandbox

| Variable | Default | Description |
|---|---|---|
| `YOLO_REPO_ROOT` | `.` (cwd) | Repo root directory — the sandbox confines file operations within this |

The sandbox automatically:
- Rejects path escapes (`../../etc/passwd`)
- Peels wrappers (`sudo`, `env`, `time`) before classification
- Classifies commands by risk level
- Network default-deny

## HITL Approval

| Variable | Default | Description |
|---|---|---|
| `YOLO_AUTO_APPROVE_MEDIUM` | `false` | Auto-approve medium-risk tools (e.g. `bash` with safe commands) |
| `YOLO_AUTO_APPROVE_HIGH` | `false` | Auto-approve high-risk tools (e.g. `edit_file`, `bash` with dangerous commands) |

> **Headless mode**: If you don't enable auto-approve for medium/high, the agent will deadlock because there's no user to approve. Enable it when running headless:

```bash
export YOLO_AUTO_APPROVE_MEDIUM=true
export YOLO_AUTO_APPROVE_HIGH=true
```

> **Interactive mode**: The TUI displays an approval prompt, no need for auto-approve.

### Risk classification

| Risk | Tools | Behaviour |
|---|---|---|
| **Low** | `list_files`, `read_file`, `grep` | Runs automatically |
| **Medium** | `bash` (safe commands) | Requires approval (or auto-approve) |
| **High** | `edit_file`, `bash` (dangerous commands) | Requires approval (or auto-approve) |
| **Critical** | `bash` (shell escape, rm -rf) | Always rejected |

## Logging

| Variable | Default | Description |
|---|---|---|
| `YOLO_LOG` | (empty) | Structured log file path (slog format) |

When set, yolo-code writes structured logs to the file. Logs include:
- State transitions
- Tool calls and results
- LLM requests/responses (secrets redacted)
- Errors and warnings

Example:

```bash
export YOLO_LOG=/tmp/yolo-debug.log
yolo --headless < task.txt
cat /tmp/yolo-debug.log | grep "tool_call"
```

## File .env

Copy `.env.example` and edit:

```bash
cp .env.example .env
```

```ini
# LLM Provider
YOLO_API_KEY=sk-...
YOLO_BASE_URL=https://api.openai.com/v1
YOLO_MODEL=gpt-4o

# Logging
YOLO_LOG=

# Auto-approve (headless)
YOLO_AUTO_APPROVE_MEDIUM=true
YOLO_AUTO_APPROVE_HIGH=true
```

yolo-code automatically loads `.env` from the current directory on startup.

## Command-line flags

Flags override environment variables:

| Flag | Env equivalent | Description |
|---|---|---|
| `--headless` | — | Run without TUI |
| `--repo <path>` | `YOLO_REPO_ROOT` | Repo root |
| `--model <name>` | `YOLO_MODEL` | Override model |
| `--base-url <url>` | `YOLO_BASE_URL` | Override API URL |
| `--plan <goal>` | — | Multi-agent orchestrator for a complex goal |
| `--version` | — | Print version |

## Configuration examples

### Development (local)

```bash
# .env
YOLO_API_KEY=sk-abc123
YOLO_MODEL=gpt-4o
YOLO_AUTO_APPROVE_MEDIUM=true
YOLO_AUTO_APPROVE_HIGH=true
```

```bash
yolo  # interactive mode
```

### CI/CD (headless)

```bash
export YOLO_API_KEY="${{ secrets.API_KEY }}"
export YOLO_BASE_URL="https://api.openai.com/v1"
export YOLO_MODEL="gpt-4o"
export YOLO_AUTO_APPROVE_MEDIUM=true
export YOLO_AUTO_APPROVE_HIGH=true

echo "fix bug #42" | yolo --headless --repo /path/to/repo
```

### Debug mode

```bash
export YOLO_LOG=/tmp/yolo-debug.log
yolo --headless < task.txt 2>&1 | tee /tmp/yolo-output.json
```
