# Quickstart

## Cài đặt

### Cách 1: go install (nhanh nhất)

```bash
go install github.com/yolo-code/yolo/cmd/yolo@latest
```

Binary sẽ ở `$GOPATH/bin/yolo` (hoặc `$HOME/go/bin/yolo`).

### Cách 2: Clone và build

```bash
git clone https://github.com/baobao1044/yolo-code.git
cd yolo-code
go build -o yolo ./cmd/yolo
```

## Cấu hình LLM Provider

yolo-code cần 1 LLM provider hỗ trợ OpenAI-compatible API. Cấu hình qua environment variables hoặc file `.env`.

### Option A: OpenAI

```bash
export OPENAI_API_KEY="sk-..."
export OPENAI_BASE_URL="https://api.openai.com/v1"
export OPENAI_MODEL="gpt-4"
```

### Option B: Kimi K2.7 qua WandB

```bash
export OPENAI_API_KEY="wandb_v1_..."
export OPENAI_BASE_URL="https://api.inference.wandb.ai/v1"
export OPENAI_MODEL="moonshotai/Kimi-K2.7-Code"
```

### Option C: File .env

```bash
cp .env.example .env
# Sửa .env với API key và model của bạn
```

> yolo-code tự động load `.env` nếu có file trong thư mục hiện tại.

## Chạy lần đầu

### Headless mode

```bash
echo "giải thích hàm main" | yolo --headless
```

Output: 1 JSON line per event. Ví dụ:

```json
{"type":"state.change","state":"think"}
{"type":"state.change","state":"exec"}
{"type":"observation","tool":"read_file","stdout":"package main..."}
{"type":"state.change","state":"think"}
{"type":"task.completed"}
```

Headless mode phù hợp cho:
- Scripts và automation
- Golden tests (output deterministic cho cùng input)
- CI pipelines

### Interactive mode

```bash
yolo
```

TUI hiển thị:
- **Board**: multi-agent progress khi coordination layer phân tách task
- **Cost meter**: token usage và chi phí
- **Diff viewer**: xem thay đổi file real-time
- **Status bar**: trạng thái FSM hiện tại

Gõ task ở prompt bên dưới và nhấn Enter.

## Ví dụ: Tạo project nhỏ

```bash
# Chạy agent tạo 1 project Fibonacci CLI
echo "tạo 1 CLI tool tính fibonacci với tests" | yolo --headless
```

Agent sẽ:
1. `list_files` — xem repo hiện tại
2. `edit_file` — tạo `main.go` với fibonacci function
3. `edit_file` — tạo `main_test.go` với tests
4. `bash` — chạy `go test`
5. `bash` — chạy `go build`
6. Verify và hoàn thành

## Kiểm tra version

```bash
yolo version
```

## Bước tiếp theo

- [Commands & Flags](commands.md) — tất cả flags và env vars
- [Configuration](configuration.md) — cấu hình đầy đủ
- [Tools Reference](tools.md) — 4 tools và cách hoạt động
- [TUI Guide](tui-guide.md) — sử dụng TUI
