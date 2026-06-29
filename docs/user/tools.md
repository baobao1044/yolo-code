# Tools Reference

yolo-code có 4 tools tích hợp. Cognitive Core (LLM) gọi tools qua **OpenAI native function calling API**.

## Tổng quan

| Tool | Args | Risk | Sandbox | Mô tả |
|---|---|---|---|---|
| `list_files` | — | Low | Có | Liệt kê tất cả files trong repo |
| `read_file` | `file` | Low | Có | Đọc nội dung file |
| `edit_file` | `file`, `content` | High | Có | Ghi đè nội dung file (tạo mới nếu chưa có) |
| `bash` | `command` | Medium–Critical | Có | Chạy shell command |

## list_files

Liệt kê tất cả files trong repo root recursively.

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
- Tự skip: `.git/`, `node_modules/`, `vendor/`, `__pycache__/`, `.cache/`, `dist/`
- Trả về relative paths với forward slashes
- **Risk**: Low — chỉ đọc, không ghi
- **Cost**: Cheap — chỉ filesystem walk

### Kết quả ví dụ

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

Đọc nội dung file.

### Schema

```json
{
  "name": "read_file",
  "parameters": {
    "type": "object",
    "properties": {
      "file": {
        "type": "string",
        "description": "Đường dẫn file relative to repo root"
      }
    },
    "required": ["file"]
  }
}
```

### Behaviour

- Giải quyết path qua sandbox (path confinement)
- Từ chối path escapes (`../../etc/passwd` → `ErrPathEscapes`)
- **Risk**: Low — chỉ đọc
- **Cost**: Cheap — 1 file read

### Ví dụ call

```json
{
  "name": "read_file",
  "arguments": { "file": "internal/cognitive/core.go" }
}
```

## edit_file

Ghi đè nội dung file. Tạo file mới nếu chưa tồn tại (tạo parent dirs tự động).

### Schema

```json
{
  "name": "edit_file",
  "parameters": {
    "type": "object",
    "properties": {
      "file": {
        "type": "string",
        "description": "Đường dẫn file relative to repo root"
      },
      "content": {
        "type": "string",
        "description": "Nội dung đầy đủ của file"
      }
    },
    "required": ["file", "content"]
  }
}
```

### Behaviour

- Giải quyết path qua sandbox
- Tạo parent directories nếu chưa có
- Ghi đè toàn bộ file (không phải partial edit)
- **Risk**: High — sửa/xoá nội dung file
- **Cost**: Expensive — cần HITL approval

### Ví dụ call

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

Chạy shell command.

### Schema

```json
{
  "name": "bash",
  "parameters": {
    "type": "object",
    "properties": {
      "command": {
        "type": "string",
        "description": "Shell command để chạy"
      }
    },
    "required": ["command"]
  }
}
```

### Behaviour

- Sandbox phân loại risk dựa trên command:
  - **Low**: `ls`, `go test`, `go build`, `git status`, v.v.
  - **Medium**: Unknown commands (default)
  - **High**: `curl`, `wget`, `ssh`, `scp`, `rsync`, `nc`, network commands
  - **Critical**: `eval`, `source`, shell escapes (`$(...)`, backticks), `bash -c 'rm -rf /'`, v.v.
- Wrapper peeling: `sudo rm -rf /` → peel `sudo` → `rm -rf /` → Critical
- **Risk**: Medium–Critical (phụ thuộc command)
- **Cost**: Expensive — cần HITL approval (trừ Low risk)

### Ví dụ call

```json
{
  "name": "bash",
  "arguments": { "command": "go test ./..." }
}
```

## HITL Approval Flow

```
Tool call từ LLM
      │
      ▼
  Sandbox classify risk
      │
      ├── Low ──────► Tự chạy ✅
      │
      ├── Medium ──► Cần approval
      │                  │
      │                  ├── Interactive: TUI hiển thị prompt
      │                  │   User approve/reject
      │                  │
      │                  └── Headless: AutoApprove nếu configured
      │                      Hoặc deadlock ❌ nếu không configured
      │
      ├── High ────► Cần approval (như Medium)
      │
      └── Critical ─► Luôn từ chối ❌
```

### Auto-approve config

```bash
# Headless mode: bật auto-approve để tránh deadlock
export YOLO_AUTO_APPROVE_MEDIUM=true   # bash (lệnh an toàn)
export YOLO_AUTO_APPROVE_HIGH=true     # edit_file, bash (lệnh nguy hiểm)
```

> **Lưu ý**: Critical-risk tools LUÔN bị từ chối, kể cả auto-approve bật.

## Tool Calling API

yolo-code dùng **OpenAI native function calling** (không phải inline token format). Khi tạo provider, 4 tool definitions được gửi trong request:

```json
{
  "model": "moonshotai/Kimi-K2.7-Code",
  "messages": [...],
  "tools": [
    { "type": "function", "function": { "name": "list_files", ... } },
    { "type": "function", "function": { "name": "read_file", ... } },
    { "type": "function", "function": { "name": "edit_file", ... } },
    { "type": "function", "function": { "name": "bash", ... } }
  ]
}
```

Model trả về `delta.tool_calls` trong SSE stream thay vì inline `<|tool_calls|>` tokens. Runtime accumulate partial tool_calls qua nhiều SSE chunks và flush khi nhận `finish_reason: "tool_calls"` hoặc `[DONE]`.

## Multi-turn Agent Loop

```
1. Think() → LLM trả về tool_calls
2. Dispatch tool calls → Execution Engine
3. Sandbox check → HITL approval (nếu cần)
4. Tool thực thi → Observation
5. RecordToolResult() → thêm vào conversation history
6. HasMore() = true (vì lastTurn.Final = false)
7. Loop lại Think() với history mới
8. LLM trả về final answer (không tool_calls)
9. HasMore() = false → DONE
```

## Xem thêm

- [Configuration](configuration.md) — cấu hình HITL, sandbox
- [Architecture](architecture.md) — Execution Engine trong kiến trúc
- [Sandbox Red-Team](../security/sandbox-redteam.md) — adversarial test checklist
