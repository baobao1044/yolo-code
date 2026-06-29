# Progress Changelog

Lịch sử thay đổi quan trọng của yolo-code, cập nhật theo thời gian.

## 2026-06-28

### Documentation overhaul
- Tạo README.md gốc với badges, features, quickstart, architecture
- Tạo CONTRIBUTING.md, CHANGELOG.md, LICENSE, .env.example
- Mở rộng docs/user/: thêm architecture.md, configuration.md, tools.md, tui-guide.md
- Tạo docs/workflow/: ci-cd.md, development.md
- Tạo docs/rag/: context-engine.md, vector-store.md, memory-lifecycle.md
- Tạo docs/progress/: sprint-status.md, changelog.md

### Multi-turn agent loop
- Fix `HasMore()` trả về `!lastTurn.Final` → agent loop tiếp tục sau tool execution
- Thêm `RecordToolResult(toolName, result)` → conversation history accumulation
- Fix duplicate prompt messages: chỉ init history lần đầu Think()

## 2026-06-27

### Native tool calling API
- Thêm `tools[]` definitions trong OpenAI chat request
- Model emit `delta.tool_calls` thay vì inline tokens
- Rewrite `parseSSE()` với partial tool_calls accumulation (by index)
- Flush trên `finish_reason: "tool_calls"` hoặc `[DONE]`

### 4 Tools tích hợp
- `list_files` — liệt kê repo files (Low risk)
- `read_file` — đọc file nội dung (Low risk)
- `edit_file` — ghi đè file (High risk)
- `bash` — chạy shell command (Medium–Critical risk)

## 2026-06-26

### OpenAI-compatible provider
- Tạo `OpenAICompatProvider` với SSE streaming
- Hỗ trợ Kimi K2.7 qua WandB inference API
- Parse SSE `data: {json}\n\n` format với `[DONE]` terminator

### HITL approval gate
- Risk classification: low/medium/high/critical
- Interactive mode: TUI prompt cho approval
- Headless mode: AutoApprove config (YOLO_AUTO_APPROVE_MEDIUM/HIGH)
- Critical risk: luôn từ chối

## 2026-06-25

### Sandbox hardening
- Wrapper peeling: sudo → peel → classify underlying command
- Path escape detection: `../../etc/passwd` → `ErrPathEscapes`
- Shell escape classification: `eval`, `source`, `$(cmd)` → `RiskCritical`
- Network command classification: `curl`, `wget`, `ssh` → `RiskHigh`
- Red-team test suite: sandbox_redteam_test.go

## 2026-06-20

### Verification Engine
- 7-stage pipeline: AST → Format → Lint → TypeCheck → Build → Test → PolicyCheck
- Fail → auto rollback
- Verdicts: pass / warn / fail

## 2026-06-15

### Patch Engine
- SEARCH/REPLACE primary + unified diff fallback
- Conflict detection
- Git checkpoint trước mỗi edit

## 2026-06-10

### Event Bus
- 16 topic groups
- Fsync-before-fanout
- Per-subscriber FIFO
- At-least-once + idempotent delivery

## 2026-06-05

### Runtime FSM
- 12 states, 20 transitions
- Single-goroutine drive loop
- Context-based cancellation

## 2026-06-01

### Project kickoff
- Khởi tạo project: Go 1.26, bubbletea TUI
- Session Manager: lifecycle, checkpoints, undo stack
- Basic CLI: `yolo` binary, `--headless` flag

---

> Changelog chi tiết kỹ thuật xem [CHANGELOG.md](../../CHANGELOG.md) ở root.
