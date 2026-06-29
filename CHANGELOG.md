# Changelog

Tất cả thay đổi đáng chú ý của project này sẽ được ghi lại trong file này.

Format dựa trên [Keep a Changelog](https://keepachangelog.com/vi/1.1.0/),
và project tuân thủ [Semantic Versioning](https://semver.org/lang/vi/).

## [Unreleased]

### Added

- OpenAI-compatible provider (`OpenAICompatProvider`) với SSE streaming
- Hỗ trợ Kimi K2.7 qua WandB inference API
- Native tool calling API — model emit `delta.tool_calls` thay vì inline tokens
- 4 tools tích hợp: `list_files`, `read_file`, `edit_file`, `bash`
- Multi-turn agent loop: Think → Tool Call → Execute → Verify → Think lại
- HITL (Human-in-the-Loop) approval gate với risk classification
- Sandbox an toàn: path confinement, wrapper peeling, shell escape detection, network default-deny
- Interactive TUI mode (bubbletea + lipgloss)
- Headless mode (JSON events cho CI/scripts)
- Event Bus backbone với 16 topic groups
- Single-goroutine Runtime FSM (12 states, 20 transitions)
- Context Engine với relevance scoring (recency, proximity, semantic, centrality, explicit)
- Prompt Compiler: dedup → summarize → budget → order
- Pure-Go vector store cho memory system
- Multi-agent coordination layer (DAG scheduler)
- OpenTelemetry traces + structured logging (slog)
- Cross-compile matrix: linux/amd64, linux/arm64, darwin/amd64, darwin/arm64
- CI pipeline: lint → build → test → race → golden → snapshot → docs
- GoReleaser release dry-run pipeline

### Changed

- Tool `read` đổi tên thành `read_file`, arg `path` → `file`
- Tool `bash` arg `cmd` → `command`
- Headless mode: medium/high risk tools cần AutoApprove config để tránh deadlock
- Conversation history accumulation: `Think()` giữ lịch sử qua nhiều turns

### Fixed

- `parseSSE()` không accumulate partial tool_calls → fix với `partials map[int]*partialCall`
- `HasMore()` trả về `false` sau tool call → fix trả về `!lastTurn.Final`
- Duplicate prompt messages mỗi turn → fix chỉ init history lần đầu
- Headless deadlock khi HITL gate chờ approval → thêm AutoApprove config
