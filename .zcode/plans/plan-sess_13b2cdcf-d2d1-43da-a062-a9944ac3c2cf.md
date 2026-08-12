# Plan — Fix Respond Glitch + UI Alignment (Codex/OpenCode/Claude Code)

## Bối cảnh (đã khảo sát)

### Bug "respond bị gì"
- System prompt (`internal/context/engine.go:110-138`): "You are yolo, a terminal coding agent" + chỉ list coding tools + coding-only STRATEGY. Không có rule: (a) trả lời thẳng, (b) không tự giới thiệu, (c) dùng bash cho non-coding tasks.
- Khi user hỏi "search giá vàng hôm nay" → model không biết trả lời sao → fallback self-introduction ("Chào bạn! 👋 Tôi là yolo...").
- Prose tool list thiếu `grep` (native tools array có 5, prose chỉ list 4).
- **Fix**: rewrite system prompt → directive rõ ràng + bổ sung grep.

### UI khó dùng (so sánh Codex/OpenCode/Claude Code)
- **Color palette**: yolo dùng blue (state) + cyan (assistant) → Codex style guide: avoid blue/yellow, assistant=magenta, user=default bold, state=cyan, dim=secondary.
- **Footer**: yolo hiện tất cả hints 1 dòng không collapse → Codex có width-aware collapse (drop hints khi terminal hẹp).
- **Message rendering**: yolo prefix thô ("assistant: ", "tool: ") → Codex dùng style rõ (magenta assistant, dim tool, no verbose prefix).
- **Empty state**: yolo có static examples (OK, giữ).

Quyết định best-judgment: **Prompt + UI full** (user muốn cả 2).

## Phase 1 — Fix system prompt (`internal/context/engine.go`, sửa lines 110-138)
Rewrite system prompt thành:
```
You are yolo, an AI assistant in the terminal. Answer the user's question directly and concisely.

RULES:
- Answer the user's actual question. Do NOT introduce yourself, greet, or ask what they want.
- For coding tasks: use tools (read_file, edit_file, bash, grep) to operate on the local repo.
- For general questions (non-coding): answer directly using your knowledge, or use bash to run commands (e.g. curl for web queries).
- Be concise. No filler. No "Sure, I'll help with that." Just answer.

AVAILABLE TOOLS:
- list_files: list files in the repo. Args: {}
- read_file: read a file's contents. Args: {"file": "<path>"}
- edit_file: edit a file. Args: {"file": "<path>", "content": "<full new file content>"}
- bash: run a shell command. Args: {"command": "<cmd>"}
- grep: search file contents. Args: {"pattern": "<regex>", "path": "<dir or file>"}

TOOL CALL FORMAT:
(same fenced ```tool block as before)

You may include prose before/after tool blocks. A single response can contain
multiple tool blocks. If no tool blocks are present, the response is treated as
a direct answer and the task ends.

CODING STRATEGY:
1. Read relevant files first (read_file or grep) to understand the codebase.
2. Plan your changes.
3. Apply edits (edit_file).
4. Verify with bash (go build, go test, etc.).

Always output the FULL file content when using edit_file — never partial diffs.
```

Thay đổi: identity "coding agent" → "AI assistant" (broader), thêm RULES (answer directly, don't introduce), thêm grep tool, cho phép bash cho general queries, "Be concise" rule.

## Phase 2 — Color palette Codex-style (`internal/tui/theme.go`, sửa darkTheme)
Theo Codex `styles.md`:
- cyan = interactive/status → header, state, focus
- magenta = assistant → assistant (đổi từ cyan hiện tại)
- green = success/additions → success (giữ)
- red = errors/deletions → error (giữ)
- dim = secondary → thinking, tool, observation, muted
- default (no color, bold) = primary text → user (đổi từ white)
- amber = warning (giữ, OK)

Sửa darkTheme:
- `assistant`: cyan → magenta (`ac("#9d4edd", "#c77dff")`)
- `state`: blue → cyan (đổi sang cyan, align với "status indicators = cyan")
- `user`: white → bold no-color (`lipgloss.NewStyle().Bold(true)`, bỏ Foreground — primary text = default)
- `observation`: cyan → dim gray (secondary)
- `header`: giữ cyan (OK — interactive)
- `prompt`: giữ grayBright (OK)
- `tool`: giữ gray (OK — dim)
- `thinking`: giữ gray (OK — dim)

Light theme tương tự (assistant magenta dark variant, state cyan dark variant).

## Phase 3 — Message rendering gọn (`internal/tui/view.go`, sửa chatView)
Hiện tại prefix: "assistant: ", "tool: ", "obs: ", "reflection: " → verbose.
Codex pattern: style riêng, prefix ngắn hoặc không prefix.

Sửa chatView switch:
- `user`: không prefix, chỉ bold (đã có style) + text. Bỏ "> " prefix (Codex không dùng).
- `assistant`: prefix "│ " (magenta bar) thay "assistant: " — gọn + visually clear.
- `tool`: prefix "  ▸ " (dim) thay "tool: " — compact.
- `observation`: prefix "  ← " (dim) thay "obs: ".
- `reflection`: prefix "  ⟳ " (amber) thay "reflection: ".
- `error`: prefix "  ✗ " (red) thay "error: ".
- `thinking`: giữ "thinking: " (dim, italic-style via dim).
- `system`: prefix "  · " (dim) thay "system: ".
- `verification`: giữ (đã dim).
- `review`: prefix "  ⊙ " (dim).

## Phase 4 — Footer width-aware collapse (`internal/tui/view.go`, sửa statusView)
Hiện tại statusView nối tất cả hints: `[chat] · [diff] · approval: y/n · q quit · esc cancel · ctrl+p pause · ? help · type goal + Enter · ↑3 lines`

Codex pattern: width-aware — drop hints từ phải sang trái khi terminal hẹp.

Sửa statusView:
- Build hints theo priority (cao → thấp): focus tags > approval hint > pause/resume > quit hint > help hint > type goal > scroll offset.
- Tính total width; nếu > m.width, drop hints từ priority thấp nhất cho đến vừa.
- Render remaining hints.

## Phase 5 — Tests
- `internal/context/engine_test.go` (nếu có): assert system prompt mới chứa "Answer directly" + "Do NOT introduce" + grep.
- `internal/tui/theme_test.go`: assert assistant=magenta, user=bold no-color.
- `internal/tui/view_test.go`: assert footer collapse khi width hẹp.

## Phase 6 — Docs
- CHANGELOG: Added/Fixed section.
- `docs/user/tui-guide.md`: cập nhật message rendering description.

## Phase 7 — Build + vet + test + race
- `go build ./...`, `go vet ./...`, `go test ./...`, `go test -race -count=1 ./...`.
- Golden transcript: system prompt thay đổi → headless golden hash sẽ đổi (cần regenerate golden). Kiểm tra: headless stub provider không quan tâm prompt content (chỉ echo), nên golden có thể ổn OR cần update fixture.

## Thứ tự thực thi
1 (prompt) → 2 (palette) → 3 (messages) → 4 (footer) → 5 (tests) → 6 (docs) → 7 (build).

## Rủi ro
- **Golden transcript**: system prompt text thay đổi → `TestHeadlessSingleTurnPrintsDeterministicTranscript` có thể fail (hash đổi). Nếu fail → regenerate golden fixture (`go test -update` hoặc manually). Stub provider echo text user, không echo system prompt → golden có thể ổn.
- **Color change**: assistant cyan→magenta có thể ảnh suất test nào assert màu cụ thể. Kiểm tra theme_test.go.
- **Prefix change**: view_test.go có thể assert "assistant: " prefix → cần update.