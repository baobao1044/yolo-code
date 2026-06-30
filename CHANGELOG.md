# Changelog

All notable changes to this project will be documented in this file.

Format based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added

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
