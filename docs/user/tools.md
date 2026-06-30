# Tools Reference

yolo-code has 5 built-in tools. The Cognitive Core (LLM) calls tools via the **OpenAI native function calling API**.

## Overview

| Tool | Args | Risk | Sandbox | Description |
|---|---|---|---|---|
| `list_files` | — | Low | Yes | List all files in the repo |
| `read_file` | `file` | Low | Yes | Read file contents |
| `grep` | `pattern`, `path?` | Low | Yes | Search file contents for a regex pattern |
| `edit_file` | `file`, `content` | High | Yes | Overwrite file contents (creates if not exists) |
| `bash` | `command` | Medium–Critical | Yes | Run a shell command |

## list_files

Lists all files in the repo root recursively.

### Schema

```json
{
  "name": "list_files",
  "parameters": {
    "type": "object",
    "properties": {},
    "required": []
  }
}
```

### Behaviour

- Walk repo root recursively
- Auto-skip: `.git/`, `node_modules/`, `vendor/`, `__pycache__/`, `.cache/`, `dist/`
- Returns relative paths with forward slashes
- **Risk**: Low — read-only, no writes
- **Cost**: Cheap — only a filesystem walk

### Example result

```
cmd/yolo/main.go
internal/cognitive/core.go
internal/cognitive/openai_compat.go
internal/exec/bash.go
internal/exec/edit_file.go
internal/exec/list_files.go
internal/exec/read.go
go.mod
Makefile
```

## read_file

Reads file contents.

### Schema

```json
{
  "name": "read_file",
  "parameters": {
    "type": "object",
    "properties": {
      "file": {
        "type": "string",
        "description": "File path relative to repo root"
      }
    },
    "required": ["file"]
  }
}
```

### Behaviour

- Resolves path through sandbox (path confinement)
- Rejects path escapes (`../../etc/passwd` → `ErrPathEscapes`)
- **Risk**: Low — read-only
- **Cost**: Cheap — 1 file read

### Example call

```json
{
  "name": "read_file",
  "arguments": { "file": "internal/cognitive/core.go" }
}
```

## grep

Searches file contents for a regex pattern across the repo. Essential for locating code patterns.

### Schema

```json
{
  "name": "grep",
  "parameters": {
    "type": "object",
    "properties": {
      "pattern": {
        "type": "string",
        "description": "regex pattern to search for"
      },
      "path": {
        "type": "string",
        "description": "optional directory or file to search in (default: repo root)"
      }
    },
    "required": ["pattern"]
  }
}
```

### Behaviour

- Uses `ripgrep` (`rg`) when available, falls back to POSIX `grep`
- Returns matching lines with file paths and line numbers (`-n`), two lines of context
- Caps results (max 50 matches) and truncates long lines (`--max-columns 200`) to keep output bounded
- No matches is not an error — returns an empty result with summary `"no matches"`
- **Risk**: Low — read-only
- **Cost**: Cheap — a single search invocation

### Example call

```json
{
  "name": "grep",
  "arguments": { "pattern": "func.*Core", "path": "internal" }
}
```

## edit_file

Overwrites file contents. Creates the file if it doesn't exist (auto-creates parent directories).

### Schema

```json
{
  "name": "edit_file",
  "parameters": {
    "type": "object",
    "properties": {
      "file": {
        "type": "string",
        "description": "File path relative to repo root"
      },
      "content": {
        "type": "string",
        "description": "Full file contents"
      }
    },
    "required": ["file", "content"]
  }
}
```

### Behaviour

- Resolves path through sandbox
- Creates parent directories if needed
- Overwrites the entire file (not a partial edit)
- **Risk**: High — modifies/deletes file contents
- **Cost**: Expensive — requires HITL approval

### Example call

```json
{
  "name": "edit_file",
  "arguments": {
    "file": "cmd/fibonacci/main.go",
    "content": "package main\n\nimport \"fmt\"\n\nfunc main() {\n    fmt.Println(\"hello\")\n}\n"
  }
}
```

## bash

Runs a shell command.

### Schema

```json
{
  "name": "bash",
  "parameters": {
    "type": "object",
    "properties": {
      "command": {
        "type": "string",
        "description": "Shell command to run"
      }
    },
    "required": ["command"]
  }
}
```

### Behaviour

- Sandbox classifies risk based on the command:
  - **Low**: `ls`, `go test`, `go build`, `git status`, etc.
  - **Medium**: Unknown commands (default)
  - **High**: `curl`, `wget`, `ssh`, `scp`, `rsync`, `nc`, network commands
  - **Critical**: `eval`, `source`, shell escapes (`$(...)`, backticks), `bash -c 'rm -rf /'`, etc.
- Wrapper peeling: `sudo rm -rf /` → peel `sudo` → `rm -rf /` → Critical
- **Risk**: Medium–Critical (depends on command)
- **Cost**: Expensive — requires HITL approval (except Low risk)

### Example call

```json
{
  "name": "bash",
  "arguments": { "command": "go test ./..." }
}
```

## HITL Approval Flow

```
Tool call from LLM
      │
      ▼
  Sandbox classify risk
      │
      ├── Low ──────► Runs automatically ✅
      │
      ├── Medium ──► Needs approval
      │                  │
      │                  ├── Interactive: TUI displays prompt
      │                  │   User approves/rejects
      │                  │
      │                  └── Headless: AutoApprove if configured
      │                      Otherwise deadlock ❌
      │
      ├── High ────► Needs approval (same as Medium)
      │
      └── Critical ─► Always rejected ❌
```

### Auto-approve config

```bash
# Headless mode: enable auto-approve to avoid deadlock
export YOLO_AUTO_APPROVE_MEDIUM=true   # bash (safe commands)
export YOLO_AUTO_APPROVE_HIGH=true     # edit_file, bash (dangerous commands)
```

> **Note**: Critical-risk tools are ALWAYS rejected, even with auto-approve enabled.

## Tool Calling API

yolo-code uses **OpenAI native function calling** (not inline token format). When creating a provider request, 5 tool definitions are included:

```json
{
  "model": "gpt-4o",
  "messages": [...],
  "tools": [
    { "type": "function", "function": { "name": "list_files", ... } },
    { "type": "function", "function": { "name": "read_file", ... } },
    { "type": "function", "function": { "name": "grep", ... } },
    { "type": "function", "function": { "name": "edit_file", ... } },
    { "type": "function", "function": { "name": "bash", ... } }
  ]
}
```

The model returns `delta.tool_calls` in the SSE stream instead of inline `<|tool_calls|>` tokens. The runtime accumulates partial tool_calls across SSE chunks and flushes on `finish_reason: "tool_calls"` or `[DONE]`.

## Multi-turn Agent Loop

```
1. Think() → LLM returns tool_calls
2. Dispatch tool calls → Execution Engine
3. Sandbox check → HITL approval (if needed)
4. Tool executes → Observation
5. RecordToolResult() → added to conversation history
6. HasMore() = true (because lastTurn.Final = false)
7. Loop back to Think() with new history
8. LLM returns final answer (no tool_calls)
9. HasMore() = false → DONE
```

## See also

- [Configuration](configuration.md) — configuring HITL, sandbox
- [Architecture](architecture.md) — Execution Engine in the architecture
- [Sandbox Red-Team](../security/sandbox-redteam.md) — adversarial test checklist
