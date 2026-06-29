# Dynamic Workflow

yolo-code selects a per-task *workflow* and drives it with conditional branching, rather than running a single fixed pipeline for every task. This is the `internal/workflow` package, wired into the runtime via the `WorkflowEngine` port.

## Why dynamic

A bug fix, a feature, and a refactor need different phases. Instead of one FSM for all tasks, the workflow engine classifies the task goal and routes it to one of three built-in workflows, each with its own phase state machine.

## Built-in workflows

| Workflow | Phases | Notes |
|---|---|---|
| `bugfix` | LOCALIZE → REPAIR → VALIDATE | On a logic-error verify failure → multi-hypothesis; on a compile failure → repair loop; on timeout → degrade model. |
| `feature` | DESIGN → DECOMPOSE → IMPLEMENT → VERIFY | On context-needed → localize; on verify failure → scope contract then IMPLEMENT. |
| `refactor` | ANALYZE → TRANSFORM → VERIFY | Behavior-preserving: on a non-behavior-preserving failure → scope contract; on pass → submit. |

Each workflow's `Next(state, event)` returns an `Action` (`localize`, `generate_patch`, `multi_hypothesis`, `verify`, `repair_loop`, `scope_contract`, `submit`, `degrade_model`) and advances the phase.

## Classification

A heuristic `Classifier` maps the lowercased goal to a workflow type, first match wins:

1. `bugfix` — "fix", "bug", "error", "fail", "crash", "broken"
2. `refactor` — "refactor", "rename", "restructure", "clean up", "cleanup"
3. `feature` — "add", "implement", "feature", "support", "create", "build"
4. default — `bugfix` (a misclassified bugfix is cheaper to recover from than a misclassified feature).

## Integration with the runtime

The runtime calls `WorkflowEngine.Next` in the `PLAN` arm, threading the last verify result as a workflow event. The action is **advisory** — it does not override the FSM; it primes the workflow state for routing. When no engine is wired, the noop engine returns `submit` and the legacy fixed FSM flow applies unchanged.

## Events

The engine publishes one event topic:

- `workflow.selected` — a workflow was chosen for a goal (carries the goal + workflow name).

It is published by the explicit `Select` entry, not on every `Next` call, to avoid a publish storm inside the drive loop. Nil-bus-safe.

## See also

- [Architecture](architecture.md) — where workflow sits in the layer model.
- [Scope Loop Engineering](scope.md) — scope control that complements workflow routing.
- [Configuration](configuration.md) — how the runtime is wired.
