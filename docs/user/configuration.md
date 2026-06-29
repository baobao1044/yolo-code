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
| `OPENAI_API_KEY` | API key for the LLM provider | `sk-...` |

### Optional variables

| Variable | Default | Description |
|---|---|---|
| `OPENAI_BASE_URL` | `https://api.openai.com/v1` | Base URL of the OpenAI-compatible API |
| `OPENAI_MODEL` | `gpt-4` | Model name |

### Popular providers

#### OpenAI

```bash
export OPENAI_API_KEY="sk-..."
export OPENAI_BASE_URL="https://api.openai.com/v1"
export OPENAI_MODEL="gpt-4"
```

#### Custom provider

Any API compatible with OpenAI chat completions:

```bash
export OPENAI_API_KEY="your-key"
export OPENAI_BASE_URL="https://your-api.com/v1"
export OPENAI_MODEL="your-model"
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
| **Low** | `list_files`, `read_file` | Runs automatically |
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
OPENAI_API_KEY=sk-...
OPENAI_BASE_URL=https://api.openai.com/v1
OPENAI_MODEL=gpt-4

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
| `--open <files>` | — | Files to load into context |
| `--model <name>` | `OPENAI_MODEL` | Override model |
| `--base-url <url>` | `OPENAI_BASE_URL` | Override API URL |
| `--version` | — | Print version |

## Configuration examples

### Development (local)

```bash
# .env
OPENAI_API_KEY=sk-abc123
OPENAI_MODEL=gpt-4
YOLO_AUTO_APPROVE_MEDIUM=true
YOLO_AUTO_APPROVE_HIGH=true
```

```bash
yolo  # interactive mode
```

### CI/CD (headless)

```bash
export OPENAI_API_KEY="${{ secrets.API_KEY }}"
export OPENAI_BASE_URL="https://api.openai.com/v1"
export OPENAI_MODEL="gpt-4"
export YOLO_AUTO_APPROVE_MEDIUM=true
export YOLO_AUTO_APPROVE_HIGH=true

echo "fix bug #42" | yolo --headless --repo /path/to/repo
```

### Debug mode

```bash
export YOLO_LOG=/tmp/yolo-debug.log
yolo --headless < task.txt 2>&1 | tee /tmp/yolo-output.json
```
