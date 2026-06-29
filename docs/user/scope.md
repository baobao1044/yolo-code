# Scope Loop Engineering

yolo-code manages the *scope* of its search through a task across six levels, expanding or contracting based on verification feedback. This is the `internal/scope` package, wired into the runtime via the `ScopeController` port.

## Scope levels

The controller operates at one level at a time. Levels run from the broadest (the whole task) to the narrowest (a single edit), then a verification level:

| Level | Name | Allowed tools (W2) |
|---|---|---|
| 0 | `TASK` | `plan`, `decompose` (no edits) |
| 1 | `REPO` | `list_files`, `grep`, `read_file` (read-only exploration) |
| 2 | `FILE` | `read_file`, `grep` |
| 3 | `FUNCTION` | `read_file`, `view_function`, `call_graph` |
| 4 | `EDIT` | `edit_file`, `write_file` |
| 5 | `VERIFY` | `run_test`, `bash`, `git_diff` |

A tool call the Planner emits that is disallowed at the current level broadens the scope to a level that permits it (the runtime never silently drops a tool call — that would loop forever).

## The verify-driven loop

The runtime consults the controller in the `VERIFY` arm. On a failure it suggests one of:

- **Expand** — widen the search scope (e.g. a `test` failure with a `missing_import` hint → broaden to `REPO` to find the module).
- **Contract** — narrow to re-examine a smaller scope.
- **Stay** — remain in scope and repair (e.g. a `compile` failure is usually a local syntax fix).

On a pass, the controller records a confirmed fact so the scope `Memory` remembers what worked.

## Scope memory

The `Memory` records visited files, tested patches, failed hypotheses, and confirmed facts. Its `LoopGuard` trips after too many patch attempts without progress, forcing a scope transition to avoid spinning on a dead-end fix.

## Scope MCTS

For harder tasks, `ScopeTreeSearch` runs a budget-bounded Monte Carlo Tree Search over scope states (SWE-Search / Moatless style): each node is a scope state, each edge a scope action (expand/contract/stay), UCB1 drives selection, and a rollout function scores leaves. `Search` returns the most-visited (robust) child's action.

## Events

The controller publishes two event topics on the shared bus:

- `scope.enter` — a scope level was entered (carries the level + reason).
- `scope.transition` — a scope transition was suggested (carries from/to level + action).

Both are nil-bus-safe (no controller configured → no publish, no panic).

## See also

- [Architecture](architecture.md) — where scope sits in the layer model.
- [Dynamic Workflow](workflow.md) — per-task workflow routing that complements scope control.
- [Configuration](configuration.md) — how the runtime is wired.
