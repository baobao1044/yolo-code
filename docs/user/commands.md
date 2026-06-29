# Commands and Flags

## Global flags

| Flag | Mặc định | Mô tả |
|---|---|---|
| `--headless` | `false` | Chạy không TUI; emit JSON events ra stdout |
| `--repo` | cwd | Repo root cho context engine |
| `--open` | `""` | Danh sách files (phẩy) load vào context ban đầu |
| `--model` | từ env | Override model name |
| `--base-url` | từ env | Override API base URL |
| `--version` | n/a | In version và thoát |

## Environment variables

| Biến | Mục đích |
|---|---|
| `OPENAI_API_KEY` | API key cho LLM provider (bắt buộc) |
| `OPENAI_BASE_URL` | Base URL của OpenAI-compatible API |
| `OPENAI_MODEL` | Tên model (vd: `gpt-4`, `moonshotai/Kimi-K2.7-Code`) |
| `YOLO_LOG` | Đường dẫn file structured log |
| `YOLO_AUTO_APPROVE_MEDIUM` | `"true"` = tự approve medium-risk tools |
| `YOLO_AUTO_APPROVE_HIGH` | `"true"` = tự approve high-risk tools |
| `YOLO_REPO_ROOT` | Repo root (mặc định = cwd) |

## Exit codes

| Code | Ý nghĩa |
|---|---|
| `0` | Task hoàn thành thành công |
| `1` | Task thất bại, bị huỷ, hoặc lỗi không mong đợi |
| `130` | Bị interrupt bởi user (Ctrl+C) |

Interactive mode trả về `0` chỉ khi task đạt `task.completed`. Headless mode trả về `0` nếu transcript kết thúc ở completed state.

## Ví dụ

```bash
# Headless — task từ stdin
echo "refactor hàm main" | yolo --headless

# Headless — chỉ định repo
echo "sửa bug" | yolo --headless --repo /path/to/project

# Headless — load files cụ thể vào context
echo "giải thích code" | yolo --headless --open main.go,internal/cognitive/core.go

# Interactive
yolo

# Interactive — chỉ định model
yolo --model gpt-4o --base-url https://api.openai.com/v1

# Version
yolo version
```

## Headless JSON format

Mỗi event là 1 JSON line. Các loại event chính:

| Event | Mô tả |
|---|---|
| `state.change` | FSM chuyển state |
| `observation` | Tool execution result |
| `task.completed` | Task hoàn thành |
| `task.failed` | Task thất bại |

Ví dụ:

```json
{"type":"state.change","state":"think"}
{"type":"state.change","state":"exec"}
{"type":"observation","tool":"bash","stdout":"ok"}
{"type":"task.completed"}
```
