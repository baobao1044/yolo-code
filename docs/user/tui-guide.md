# Hướng dẫn TUI

yolo-code TUI xây dựng trên [bubbletea](https://github.com/charmbracelet/bubbletea) + [lipgloss](https://github.com/charmbracelet/lipgloss) + [bubbles](https://github.com/charmbracelet/bubbles).

## Khởi động

```bash
yolo
```

TUI chiếm toàn bộ terminal. Nhập task ở prompt bên dưới.

## Layout

```
┌─────────────────────────────────────────────┐
│  Status Bar — FSM state + cost meter        │
├─────────────────────────────────────────────┤
│                                             │
│  Board — Multi-agent progress               │
│                                             │
│  Hiển thị:                                  │
│  • Agent hiện tại đang làm gì               │
│  • Tool calls và results                    │
│  • State transitions                        │
│  • Diff viewer (khi edit_file)              │
│                                             │
├─────────────────────────────────────────────┤
│  Cost Meter — Token usage + chi phí          │
├─────────────────────────────────────────────┤
│  Input Prompt — Gõ task ở đây               │
└─────────────────────────────────────────────┘
```

### Status Bar

Hiển thị FSM state hiện tại:

| State | Hiển thị | Ý nghĩa |
|---|---|---|
| IDLE | `●` | Chờ task |
| PLAN | `◆` | Lập kế hoạch |
| THINK | `◉` | LLM đang suy nghĩ |
| EXEC | `▶` | Đang chạy tool |
| WAIT_TOOL | `◷` | Chờ tool hoàn thành |
| VERIFY | `✓` | Đang verify kết quả |
| DONE | `✔` | Hoàn thành |

### Board

Hiển thị real-time:
- **Tool calls**: `read_file("main.go")` → kết quả
- **State transitions**: THINK → EXEC → WAIT_TOOL
- **Diff**: khi `edit_file` chạy, hiển thị diff view
- **Multi-agent**: khi coordination layer phân tách task, board hiển thị nhiều agents

### Cost Meter

- Token usage (input + output)
- Estimated cost (USD)
- Cập nhật mỗi LLM call

## Tương tác

### Nhập task

1. Gõ task ở input prompt
2. Nhấn **Enter** để gửi
3. Agent bắt đầu xử lý

### HITL Approval

Khi tool cần approval (medium/high risk), TUI hiển thị prompt:

```
⚠ bash: "go test ./..."  [Medium Risk]
Approve? [y/n]
```

- **y** → chạy tool
- **n** → từ chối, agent nhận kết quả "denied by user"

### Interrupt

- **Ctrl+C** — Huỷ task hiện tại, thoát TUI (exit code 130)
- Agent dừng ở state hiện tại, session không tự save

## Headless vs Interactive

| | Interactive (TUI) | Headless |
|---|---|---|
| Khởi động | `yolo` | `yolo --headless` |
| Output | TUI đẹp | JSON lines (stdout) |
| HITL | Prompt trong TUI | Auto-approve config |
| Dùng cho | Development hàng ngày | Scripts, CI, tests |
| Input | TTY prompt | Stdin |

### Khi nào dùng TUI

- Development hàng ngày — xem agent làm gì real-time
- Debug — theo dõi tool calls và state transitions
- Học cách agent hoạt động

### Khi nào dùng Headless

- CI/CD pipelines
- Batch automation
- Golden tests — output deterministic cho regression
- Scripts — pipe task vào stdin, xử lý JSON output

## Ví dụ session

```bash
$ yolo
```

```
┌─────────────────────────────────────────────┐
│ ● IDLE                                      │
├─────────────────────────────────────────────┤
│                                             │
│ > tạo 1 CLI tool tính fibonacci             │
│                                             │
├─────────────────────────────────────────────┤
│ ◉ THINK — LLM đang suy nghĩ...             │
├─────────────────────────────────────────────┤
│ ▶ EXEC — list_files()                       │
│   → 15 files found                          │
│                                             │
│ ▶ EXEC — edit_file("fibonacci/main.go")    │
│   ⚠ High Risk — Approve? [y/n] y           │
│   → File written successfully               │
│                                             │
│ ▶ EXEC — edit_file("fibonacci/main_test.go")│
│   → File written successfully               │
│                                             │
│ ▶ EXEC — bash("go test ./...")             │
│   → PASS                                    │
│                                             │
│ ✓ VERIFY — all checks passed                │
│                                             │
│ ✔ DONE — Task completed                     │
├─────────────────────────────────────────────┤
│ Tokens: 2,847 | Cost: $0.04                 │
└─────────────────────────────────────────────┘
```

## Xem thêm

- [Quickstart](quickstart.md) — chạy lần đầu
- [Commands & Flags](commands.md) — tất cả flags
- [Tools Reference](tools.md) — chi tiết tools
- [Configuration](configuration.md) — cấu hình HITL approval
