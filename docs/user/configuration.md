# Cấu hình yolo-code

## Tổng quan

yolo-code cấu hình qua 3 cơ chế (ưu tiên giảm dần):

1. **Command-line flags** — override mọi thứ
2. **Environment variables** — chính cho deployment
3. **File `.env`** — tiện cho local development

## LLM Provider

### Biến bắt buộc

| Biến | Mô tả | Ví dụ |
|---|---|---|
| `OPENAI_API_KEY` | API key cho LLM provider | `sk-...` hoặc `wandb_v1_...` |

### Biến tuỳ chọn

| Biến | Mặc định | Mô tả |
|---|---|---|
| `OPENAI_BASE_URL` | `https://api.openai.com/v1` | Base URL của OpenAI-compatible API |
| `OPENAI_MODEL` | `gpt-4` | Tên model |

### Provider phổ biến

#### OpenAI

```bash
export OPENAI_API_KEY="sk-..."
export OPENAI_BASE_URL="https://api.openai.com/v1"
export OPENAI_MODEL="gpt-4"
```

#### Kimi K2.7 (WandB)

```bash
export OPENAI_API_KEY="wandb_v1_..."
export OPENAI_BASE_URL="https://api.inference.wandb.ai/v1"
export OPENAI_MODEL="moonshotai/Kimi-K2.7-Code"
```

#### Custom provider

Bất kỳ API nào tương thích OpenAI chat completions:

```bash
export OPENAI_API_KEY="your-key"
export OPENAI_BASE_URL="https://your-api.com/v1"
export OPENAI_MODEL="your-model"
```

## Sandbox

| Biến | Mặc định | Mô tả |
|---|---|---|
| `YOLO_REPO_ROOT` | `.` (cwd) | Thư mục gốc repo — sandbox giới hạn file operations trong này |

Sandbox tự động:
- Từ chối path escapes (`../../etc/passwd`)
- Peel wrappers (`sudo`, `env`, `time`) trước khi classify
- Phân loại commands theo risk
- Network default-deny

## HITL Approval

| Biến | Mặc định | Mô tả |
|---|---|---|
| `YOLO_AUTO_APPROVE_MEDIUM` | `false` | Tự approve medium-risk tools (vd: `bash` với lệnh an toàn) |
| `YOLO_AUTO_APPROVE_HIGH` | `false` | Tự approve high-risk tools (vd: `edit_file`, `bash` với lệnh nguy hiểm) |

> **Headless mode**: Nếu không bật auto-approve cho medium/high, agent sẽ deadlock vì không có user để approve. Nên bật khi chạy headless:

```bash
export YOLO_AUTO_APPROVE_MEDIUM=true
export YOLO_AUTO_APPROVE_HIGH=true
```

> **Interactive mode**: TUI hiển thị approval prompt, không cần auto-approve.

### Risk classification

| Risk | Tools | Behaviour |
|---|---|---|
| **Low** | `list_files`, `read_file` | Tự chạy |
| **Medium** | `bash` (lệnh an toàn) | Cần approval (hoặc auto-approve) |
| **High** | `edit_file`, `bash` (lệnh nguy hiểm) | Cần approval (hoặc auto-approve) |
| **Critical** | `bash` (shell escape, rm -rf) | Luôn từ chối |

## Logging

| Biến | Mặc định | Mô tả |
|---|---|---|
| `YOLO_LOG` | (trống) | Đường dẫn file structured log (slog format) |

Khi set, yolo-code ghi structured log ra file. Log bao gồm:
- State transitions
- Tool calls và results
- LLM requests/responses (đã redact secrets)
- Errors và warnings

Ví dụ:

```bash
export YOLO_LOG=/tmp/yolo-debug.log
yolo --headless < task.txt
cat /tmp/yolo-debug.log | grep "tool_call"
```

## File .env

Copy `.env.example` và sửa:

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

yolo-code tự động load `.env` từ thư mục hiện tại khi khởi động.

## Command-line flags

Flags override environment variables:

| Flag | Env tương ứng | Mô tả |
|---|---|---|
| `--headless` | — | Chạy không TUI |
| `--repo <path>` | `YOLO_REPO_ROOT` | Repo root |
| `--open <files>` | — | Files load vào context |
| `--model <name>` | `OPENAI_MODEL` | Override model |
| `--base-url <url>` | `OPENAI_BASE_URL` | Override API URL |
| `--version` | — | In version |

## Ví dụ cấu hình

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
export OPENAI_BASE_URL="https://api.inference.wandb.ai/v1"
export OPENAI_MODEL="moonshotai/Kimi-K2.7-Code"
export YOLO_AUTO_APPROVE_MEDIUM=true
export YOLO_AUTO_APPROVE_HIGH=true

echo "sửa bug #42" | yolo --headless --repo /path/to/repo
```

### Debug mode

```bash
export YOLO_LOG=/tmp/yolo-debug.log
yolo --headless < task.txt 2>&1 | tee /tmp/yolo-output.json
```
