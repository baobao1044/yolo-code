# Changelog

All notable changes to this project will be documented in this file.

Format based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Changed

- **`SemanticStore` renamed to `LexicalStore`** (`internal/memory`) — the type never did semantic retrieval. The default `Embedder` is a hashing term-frequency vectorizer (FNV-1a, dim 384), so retrieval matches literal shared tokens; a synonym-only paraphrase scores zero, now pinned by `TestDefaultEmbedderIsLexicalNotSemantic`. Constructors are `NewLexicalStore`, `NewLexicalStoreWith`, `NewLexicalStoreWithFS`. The accessor `Store.Semantic()` is unchanged — it keeps the spec's File 11 §11.6 name and has live call sites.
- **Documentation corrected to match the implementation.** Earlier entries in this file (and the docs they described) called the retrieval layer a "pure-Go vector store" with "semantic search" and a "local embedding model". No embedding model has ever shipped, hosted or local, and there is no HNSW/ANN index — `Retrieve` is a linear scan scored by cosine over hashed term-frequency vectors. `docs/rag/vector-store.md`, `docs/rag/memory-lifecycle.md`, `docs/rag/context-engine.md`, `docs/user/architecture.md`, `README.md`, and File 11 now say lexical retrieval index. The `Embedder` interface remains the substitution seam: injecting a real model via `memory.Deps.Embedder` upgrades retrieval with no other change.
- **Context Engine relevance blend documented honestly** — the "semantic" signal is token overlap, not vector cosine, and `centrality` is stubbed to `0` pending the repo dependency graph. Both are now stated in `docs/rag/context-engine.md`.

### Fixed

- **Model responds with self-introduction instead of answering** — the system prompt (`internal/context/engine.go`) defined yolo as a "terminal coding agent" with coding-only tools/strategy, so non-coding questions (e.g. "search giá vàng hôm nay") made the model fall back to greeting ("Chào bạn! 👋 Tôi là yolo..."). Rewritten: identity is now "AI assistant", with explicit RULES to answer directly, not self-introduce, be concise; `grep` tool added to the prose tool list (it was in the native tool array but missing from the prompt); `bash` is now sanctioned for general queries (e.g. `curl` for web search).
- **Color palette** — dark/light themes aligned with the Codex TUI style guide: assistant is now **magenta** (not cyan), state is **cyan** (not blue — blue has no guaranteed contrast), user is **bold default** (primary text, no explicit color). Observations/tools render dim. Avoids blue/yellow/black/white foreground per Codex `styles.md`.
- **Message prefixes** — verbose "assistant: ", "tool: ", "obs: " replaced with compact glyphs: `│` (assistant, magenta), `▸` (tool, dim), `←` (observation, dim), `⟳` (reflection, amber), `✗` (error, red), `·` (system, dim). User messages render bold without a `>` prefix (Codex pattern).
- **Status line width-aware collapse** — the footer hints (focus tags, approval, quit/help, "type goal + Enter", scroll offset) now drop from the right on narrow terminals (Codex-style width-aware footer), so the status line never overflows.

### Added

- **Slash commands + Provider registry (kiểu AI SDK / Models.dev)**:
  - **Slash commands** in TUI: `/model <name>`, `/provider <name>`, `/provider` (list), `/status`, `/help`, `/clear`, `/theme <name>`. Local commands (help/clear/theme) handle in TUI; runtime commands (model/provider/status) go through the event bus (`UserCommandEvent` → driver → `CommandResponseEvent`).
  - **Provider registry** (`internal/cognitive/providers.go`): 29 built-in presets (OpenAI, Anthropic-compat, Together, Groq, Mistral, DeepSeek, OpenRouter, Ollama, LM Studio, Fireworks, Perplexity, Reka, AI21, Cohere, NVIDIA, Cloudflare, AI/ML API, HuggingFace, SiliconFlow, NovitaAI, SambaNova, Lepton, Volcano, Qwen, ZhipuAI, 01.AI, Moonshot, MiniMax, W&B). Each carries base URL + default model + key env var. Local providers (Ollama, LM Studio) need no API key.
  - `YOLO_PROVIDER` env var selects a preset at startup; `/provider <name>` switches at runtime. `cog.Core.SetProvider()` swaps the provider live (keeps conversation history).
  - New events: `UserCommandEvent`, `CommandResponseEvent`.
- **TUI Overhaul — 4 fixes (theme, input/approval, onboarding, diff/cost)**:
  - **Theme system** (`internal/tui/theme.go`): 4 palettes (dark/light/contrast/mono) selected via `YOLO_THEME`; `NO_COLOR` forces mono; `YOLO_NO_MOTION` disables spinner + cursor blink. Replaces 14 hardcoded bright 256-color styles with adaptive, accessible palettes.
  - **Input widget** (`bubbles/textinput`): real blinking cursor, ←/→/Home/End/Ctrl-A/E/Ctrl-W cursor movement, full UTF-8 support (non-ASCII no longer dropped). Replaces the hand-rolled char append.
  - **Approval non-trapping**: when an approval is pending, scroll/help/quit/cancel still work — only typing is suppressed until you answer y/n.
  - **Onboarding empty-state**: before the first task, the chat pane shows a welcome panel with three example prompts + the `/help` hint.
  - **Help overlay grouped** into three labeled sections (Navigation, Task control, Approval) in a bordered box.
  - **Color-blind status tags**: board status uses glyph + text (`[~]` in progress, `[+]` done, `[!]` rework) so it's readable without color.
  - **Diff viewer real hunks** (`internal/patch/diff_render.go`): `UnifiedDiff(original, next)` produces unified-diff-style hunks (LCS line diff); `PatchAppliedEvent` widened with a `Diff string` field; the TUI renders `+`/`-`/context lines colored + numbered.
  - **Cost meter accumulate**: `CostIncurredEvent.Dollars` (real per-tool-call rate) + rough token estimate (`len(llm.token delta)/4`) accumulate and render as `cost: $X.XX · ~N tok`.
- **S13 Superpowers — RAG & Memory wired into the agent path**:
  - Memory Store wired into all composition-root sites (headless, coord, TUI) with a sandbox-confined `memory.FS`; production no longer runs with `noopMemory`.
  - Context Engine: `KindRAG` group + `Memory.Retrieve` port; gathered RAG chunks feed the prompt under a new `<rag>` wire tag with an 8% budget slot.
  - Cold-start `IndexRepo` walks the repo deterministically (skips vendored/cache/oversized files), chunks per-function, and `BulkInsert`s in one locked pass; `patch.applied` reindexes via the FS.
  - Memory lifecycle complete: Working memory task/state + clear-on-completed; Exec history rolling window (last 50) with monotonic seq; SemanticStore `Delete`/`Size`/`Evict` (LRU)/`SetThreshold`; Knowledge insight store (distinct from code-chunk RAG) with cross-session persistence.
  - Listener widened to 9 topics (`task.started`/`state.change`/`verification.*`/`user.preference` added); slow I/O offloaded to background goroutines tracked by `Store.bg`.
  - New `user.preference` event.
- **Scope Loop Engineering** — new `internal/scope` package: a scope-level state machine (`Level`: Task/Repo/File/Function/Edit/Verify), scope-gated tool permissions (W2 table), scope expansion/contraction on verify feedback (W3), scope `Memory` to avoid infinite loops, and a budget-bounded **Scope MCTS** (SWE-Search/Moatless style) over scope states.
- **Dynamic Workflow** — new `internal/workflow` package: per-task workflow selection (`bugfix`/`feature`/`refactor`) via a heuristic `Classifier`, a `Workflow` interface with conditional branching (multi-hypothesis, repair loop, scope contraction, model degrade), and an `Engine` that publishes a `workflow.selected` event.
- **Multi-candidate reflection** — `cognitive.ReflectMulti` generates and reranks multiple corrective patch candidates (AlphaCode-style); `RerankCandidates` scores by index and penalizes repeats of failed patch bodies.
- **Reflection memory** — `cognitive.ReflectionMemory` accumulates lessons and facts across iterations and exposes a `PromptPrefix` to prime the next reflection turn.
- **Cost-degrade ladder** — `Cost.MultiCandidateAllowed()` disables multi-candidate generation one rung before disabling reflection entirely (only-verify → single-forced-candidate → abort).
- `.env` auto-load on startup (stdlib-only `LoadDotEnv`); shell env and CLI flags take precedence over the file.
- CLI flags `--model`, `--base-url`, `--repo` (override env config).
- `grep` tool added to the OpenAI native tool-calling schema (`toolDefs`) — the model can now invoke `grep` via structured tool calls.
- New event topics: `scope.enter`, `scope.transition`, `workflow.selected`.

### Changed

- **Module path**: `go.mod` corrected from `github.com/yolo-code/yolo` to `github.com/baobao1044/yolo-code` to match the git remote; all internal imports rewritten accordingly. The documented `go install` command now works.
- LLM provider env vars canonicalized to `YOLO_API_KEY` / `YOLO_BASE_URL` / `YOLO_MODEL` (default model `gpt-4o`). `OPENAI_API_KEY` remains as a key-only fallback.
- `.env.example` and docs updated to the canonical `YOLO_*` variable names.
- Runtime FSM extended to 21 transitions (T1–T21): added the missing `EXECUTE → PLAN` edge (`SigTurnDone`) so a multi-turn tool-using loop continues to the next Planner turn instead of terminating.

### Fixed

- **Module path mismatch** — `go.mod` (`github.com/yolo-code/yolo`) disagreed with the git remote + README install path (`github.com/baobao1044/yolo-code`), making the documented `go install` command fail. Now consistent.
- **`grep` missing from LLM tool schema** — `grep` was registered as a runtime tool and advertised in the README but absent from `toolDefs`, so tool-calling models could never invoke it. Now included.
- **Missing `EXECUTE → PLAN` FSM edge** — when a Planner turn's tool calls were all dispatched, the drive loop fired `SigPlannerAnswer` (which only has a `PLAN → DONE` edge), causing `ErrNoTransition` and premature task termination instead of looping back to `PLAN`. Fixed with `SigTurnDone` (T21).
- **Env var documentation drift** — docs/`.env.example` documented `OPENAI_BASE_URL` / `OPENAI_MODEL`, which the code never read; documented `--open`/`--model`/`--base-url`/`--repo` flags that did not exist; claimed `.env` auto-load that was not implemented. All corrected.

### Prior Added (below this line are older entries)
- 4 built-in tools: `list_files`, `read_file`, `edit_file`, `bash`
- Multi-turn agent loop: Think → Tool Call → Execute → Verify → Think again
- HITL (Human-in-the-Loop) approval gate with risk classification
- Safe sandbox: path confinement, wrapper peeling, shell escape detection, network default-deny
- Interactive TUI mode (bubbletea + lipgloss)
- Headless mode (JSON events for CI/scripts)
- Event Bus backbone with 16 topic groups
- Single-goroutine Runtime FSM (12 states, 20 transitions)
- Context Engine with relevance scoring (recency, proximity, semantic, centrality, explicit)
- Prompt Compiler: dedup → summarize → budget → order
- Pure-Go vector store for memory system
- Multi-agent coordination layer (DAG scheduler)
- OpenTelemetry traces + structured logging (slog)
- Cross-compile matrix: linux/amd64, linux/arm64, darwin/amd64, darwin/arm64
- CI pipeline: lint → build → test → race → golden → snapshot → docs
- GoReleaser release dry-run pipeline

### Changed

- Tool `read` renamed to `read_file`, arg `path` → `file`
- Tool `bash` arg `cmd` → `command`
- Headless mode: medium/high risk tools need AutoApprove config to avoid deadlock
- Conversation history accumulation: `Think()` retains history across turns

### Fixed

- `parseSSE()` did not accumulate partial tool_calls → fixed with `partials map[int]*partialCall`
- `HasMore()` returned `false` after tool call → fixed to return `!lastTurn.Final`
- Duplicate prompt messages each turn → fixed to init history only once
- Headless deadlock when HITL gate waits for approval → added AutoApprove config
