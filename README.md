# yolo-code

> Multi-agent terminal coding agent bằng Go — đọc task, suy nghĩ, chạy tool, viết code.

[![CI](https://github.com/baobao1044/yolo-code/actions/workflows/ci.yml/badge.svg)](https://github.com/baobao1044/yolo-code/actions/workflows/ci.yml)
[![Go](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go)](https://go.dev/)
[![License](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

## Tính năng

- **4 công cụ tích hợp**: `list_files`, `read_file`, `edit_file`, `bash` — đọc repo, sửa file, chạy lệnh
- **Multi-turn agent loop**: Think → Tool Call → Execute → Verify → Think lại cho đến khi hoàn thành
- **HITL approval gate**: Tool phân loại theo risk (low/medium/high/critical) — yêu cầu phê duyệt trước khi chạy lệnh nguy hiểm
- **Sandbox an toàn**: Chặn path escape, wrapper peeling (`sudo`, `env`), shell escape, network commands
- **2 chế độ**: Interactive TUI (terminal đẹp) + Headless (JSON events cho CI/scripts)
- **OpenAI-compatible**: Hoạt động với bất kỳ provider nào hỗ trợ OpenAI API (Kimi K2.7, GPT-4, v.v.)
- **12-layer architecture**: Event Bus backbone, single-goroutine FSM, pure-Go vector store

## Kiến trúc

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

Xem chi tiết tại [docs/user/architecture.md](docs/user/architecture.md).

## Cài đặt

### go install (nhanh nhất)

```bash
go install github.com/yolo-code/yolo/cmd/yolo@latest
```

### Clone và build

```bash
git clone https://github.com/baobao1044/yolo-code.git
cd yolo-code
go build ./...
```

## Quickstart

### 1. Cấu hình LLM

Tạo file `.env` (hoặc set environment variables trực tiếp):

```bash
cp .env.example .env
# Sửa .env: thêm API key và chọn model
```

Ví dụ với Kimi K2.7 qua WandB:

```bash
export OPENAI_API_KEY="wandb_v1_..."
export OPENAI_BASE_URL="https://api.inference.wandb.ai/v1"
export OPENAI_MODEL="moonshotai/Kimi-K2.7-Code"
```

### 2. Chạy headless

```bash
echo "viết hàm fibonacci" | yolo --headless
```

Output: 1 JSON line per event. Phù hợp cho scripts, golden tests, và CI.

### 3. Chạy interactive

```bash
yolo
```

Gõ task ở prompt bên dưới, TUI hiển thị multi-agent board, cost meter, diff viewer.

## Cấu hình

| Biến | Mặc định | Mô tả |
|---|---|---|
| `OPENAI_API_KEY` | — | API key cho LLM provider |
| `OPENAI_BASE_URL` | `https://api.openlight.com/v1` | Base URL của OpenAI-compatible API |
| `OPENAI_MODEL` | `gpt-4` | Tên model |
| `YOLO_LOG` | — | Đường dẫn file log cấu trúc |
| `YOLO_AUTO_APPROVE_MEDIUM` | `false` | Tự approve medium-risk tools |
| `YOLO_AUTO_APPROVE_HIGH` | `false` | Tự approve high-risk tools |

Xem đầy đủ tại [docs/user/configuration.md](docs/user/configuration.md).

## Công cụ (Tools)

| Tool | Args | Risk | Mô tả |
|---|---|---|---|
| `list_files` | — | Low | Liệt kê tất cả files trong repo |
| `read_file` | `file` | Low | Đọc nội dung file |
| `edit_file` | `file`, `content` | High | Ghi đè nội dung file |
| `bash` | `command` | Medium–Critical | Chạy shell command |

Xem chi tiết tại [docs/user/tools.md](docs/user/tools.md).

## Tài liệu

- [Quickstart](docs/user/quickstart.md) — Cài đặt và chạy lần đầu
- [Commands & Flags](docs/user/commands.md) — Tất cả flags, env vars, exit codes
- [Architecture](docs/user/architecture.md) — Kiến trúc 12 layers
- [Configuration](docs/user/configuration.md) — Cấu hình đầy đủ
- [Tools Reference](docs/user/tools.md) — Schema, risk, HITL flow
- [TUI Guide](docs/user/tui-guide.md) — Hướng dẫn dùng TUI
- [CI/CD Workflow](docs/workflow/) — Pipeline và development workflow
- [RAG & Memory](docs/rag/) — Context engine, vector store, memory lifecycle
- [Sprint Progress](docs/progress/) — Theo dõi tiến trình

## Phát triển

```bash
make ci          # chạy tất cả gates (vet, fmt, build, test, golden)
make test-race   # race detector (cần CGO/gcc, chạy trên Linux)
make lint        # golangci-lint
```

Xem [CONTRIBUTING.md](CONTRIBUTING.md) để đóng góp.

## License

[MIT](LICENSE) © 2024–2026
