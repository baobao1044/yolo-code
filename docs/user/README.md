# yolo-code — Tài liệu Người dùng

`yolo-code` là multi-agent terminal coding agent bằng Go. Nó đọc task, điều khiển cognitive core qua tool stack, và hoặc sửa repo hoặc báo cáo kết quả.

## Chế độ chạy

- **Headless** (`yolo --headless`) — không TTY; emit 1 JSON line per event. Phù hợp cho scripts, golden tests, CI.
- **Interactive** (`yolo`) — khởi động TUI với multi-agent board, cost meter, diff viewer, và status bar.

## Kiến trúc tổng quan

```
┌──────────── Người dùng ────────────────┐
│  Headless (JSON)   │   TUI (terminal)   │
└────────┬───────────┴──────────┬─────────┘
         │                      │
    ┌────▼──────────────────────▼────┐
    │        Event Bus (L3)         │
    └──┬─────┬──────┬──────┬───────┘
       │     │      │      │
  Session  Runtime  Context  Cognitive
   (L1)     (L2)    (L4)     (L6)
                    │         │
                 Prompt    Execution
                (L5) ──►  (L7) ──► Patch (L9)
                                      │
                                 Verify (L8)
                                      │
                                 Memory (L10)
                                      │
                            Multi-Agent (L11)
```

**Data flow**: User Task → Context Engine → Prompt Compiler → Cognitive Core → Tool Execution → Verification → Loop hoặc Done

**Quy tắc vàng**:
- Mỗi layer chỉ phụ thuộc layer thấp hơn + Event Bus
- TUI subscribe-only, không bao giờ chứa logic
- Memory updates CHỈ qua events
- Runtime FSM chạy trên 1 goroutine duy nhất

Xem chi tiết tại [Architecture](architecture.md).

## An toàn

Tất cả filesystem writes và shell commands đều đi qua `exec` sandbox. Sandbox:
- Từ chối path escapes (`../../etc/passwd` → `ErrPathEscapes`)
- Phân loại network/disk/shell-escape commands là high/critical risk
- Peel wrappers như `sudo` trước khi quyết định

Xem `docs/security/sandbox-redteam.md` cho red-team checklist.

## Tài liệu

| File | Nội dung |
|---|---|
| [Quickstart](quickstart.md) | Cài đặt và chạy lần đầu |
| [Commands & Flags](commands.md) | Flags, env vars, exit codes |
| [Architecture](architecture.md) | Kiến trúc 12 layers |
| [Configuration](configuration.md) | Cấu hình đầy đủ |
| [Tools Reference](tools.md) | 4 tools, schema, risk, HITL |
| [TUI Guide](tui-guide.md) | Hướng dẫn dùng TUI |
