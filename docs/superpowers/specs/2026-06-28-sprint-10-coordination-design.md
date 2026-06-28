# Sprint 10 — Coordination Layer (L11) Design

**Date:** 2026-06-28
**Sprint:** 10 (roadmap §15.13) — builds `internal/coord` (File 12)
**Predecessor:** Sprint 9 (TUI subscribe-only) — pushed to `master` (`efa607b`)
**Spec status:** approved (2 design questions answered)

This spec fixes the design of Layer 11 — the multi-agent coordination layer —
before any code is written. It records the two confirmed decisions, the
architecture, the 8-ticket breakdown, the TDD discipline, and the spec gaps
File 12 carries vs the real event catalog and the import matrix.

---

## 1. Decisions (confirmed)

1. **Logic + seams, defer real drive** — build the coordination state machine
   (planner/classifier, scheduler/DAG, role registry, orchestrator loop, merge,
   shared cost, board publishing, single-agent fallback) tested with **fake
   agents** via interface seams (`AgentRunner`/`Planner`/`Verifier`/`CostLedger`).
   Real per-agent drive (a cognitive core + exec engine + verifier + patcher per
   agent against a live codebase) is the **integration sprint** — the SAME
   deferral bucket (§15.9.2) as Sprint 9's subscribe-only TUI and the
   exec/verify/patch/restorer composition-root adapters (already documented in
   `cmd/yolo/headless.go:120-130`). Sprint 10 never touches the runtime layer;
   the exit bar is "the orchestration logic is correct with fakes", not "a
   multi-agent run modifies a real repo".

2. **Spec gaps deferred + documented** — File 12's orchestrator code and
   sequence diagram reference `FindingsEvent`, `QuestionEvent`, `review.request`,
   and `test.request`; **none** are in the §5.4.7 event catalog, the §5.4.9 topic
   registry, or `internal/event/catalog.go` (verified — only the five canonical
   `coord.*` events are registered: `coord.task.assign`, `coord.plan.ready`,
   `coord.code.ready`, `coord.review.verdict`, `coord.test.report`). Sprint 10
   builds the **canonical 5-event loop** (Planner→Coder→Reviewer→Tester→merge).
   The Researcher role is defined in the role registry (L11-003) but its
   `QuestionEvent`/`FindingsEvent` delegation is deferred; the orchestrator
   spawns the Reviewer and Tester **directly** via the `AgentRunner` seam (no
   `review.request`/`test.request` events on the bus). Every gap is logged in a
   code comment (Sprint 9 precedent: dollars/loops blank, `env.Str()` missing,
   `TaskStarted` no Kind, `PatchApplied` no hunks, etc.).

---

## 2. Architecture & import boundary

`internal/coord` is the multi-agent orchestrator (File 12): it decomposes a
complex goal into a `Plan` of `Todo`s, dispatches them respecting the `DependsOn`
DAG, drives specialist agents (Coder/Reviewer/Tester/Researcher) through the
canonical `coord.*` event loop, caps rework, merges the diffs, and re-verifies —
or, for a trivial goal, defers to the single-agent runtime.

Import boundary (CI-enforced by L11-001's allowlist lint): the roadmap import
matrix (§15.13, line 492) *allows* `internal/coord` to import
`event, cognitive, exec, verify, patch, memory, infra`. Sprint 10 uses the
**strictest** form — `event` + stdlib only — with every other layer behind a
coord-local interface seam satisfied at the composition root. This mirrors
`runtime/ports.go`, `tui/seam.go`, and `infra/infra.go`, and keeps the layer
substitutable/testable without dragging in real cognitive/exec/patch code this
sprint. The broader matrix is exercised at the integration sprint.

```
                 ┌──────────────── internal/coord ────────────────┐
                 │                                                │
  goal ──► Classify ──► Mode ──► Route                            │
                 │             │ Single ──► DelegateSingle (no plan, no spawn)
                 │             │ Multi ──► Orchestrator.Run       │
                 │                                                │
                 │   Planner seam ──► Plan{Todo[]} ──► publish plan.ready
                 │          │                                     │
                 │   Scheduler ──► DispatchReady (DAG) ──► spawn coder
                 │          │                                     │
                 │   publish task.assign ──► AgentRunner seam ──► (fake) agent
                 │          │                                     │
                 │   agent publishes code.ready / review.verdict / test.report
                 │          │                                     │
                 │   Orchestrator event loop (subscribe coord.>)  │
                 │     code.ready ──► spawn reviewer (direct)      │
                 │     review.verdict ──► approved? spawn tester / rework
                 │     test.report ──► pass? markDone+dispatch / rework
                 │     rework cap (MaxReworkCycles=3) ──► fail+log │
                 │                                                │
                 │   Merge ──► MergedPatch ──► Verifier seam ──► ok│
                 │   CostLedger seam ──► NewTask(plan) + deadline  │
                 └────────────────────────────────────────────────┘
                                  │
                  event.Bus ──► coord.* ──► TUI board (TUI-009, untouched)
```

---

## 3. Seams (TDD-enabling)

`internal/coord/seam.go`:

```go
type Subscribable  interface { Subscribe(...event.Topic) <-chan event.Envelope }
type EventPublisher interface { Publish(context.Context, event.Event) error }

type AgentRunner interface {          // one agent turn (File 12 §12.3.1: agents
    Run(ctx context.Context, role string, task event.TaskAssignEvent) error // publish their own events)
}
type Planner interface {
    Plan(ctx context.Context, goal string) (Plan, Mode, error)
}
type Verifier interface {
    Verify(ctx context.Context, diff string) (bool, error)
}
type CostLedger interface {
    NewTask(id event.TaskID)
    Snapshot(id event.TaskID) (dollars float64, loops int, tokens int, deadline time.Time, ok bool)
    EndTask(id event.TaskID)
}
```

`*event.Bus` satisfies `Subscribable` + `EventPublisher`. Tests pass fakes →
assert exactly which `coord.*` event was published and which agent turn was
spawned, without a real bus or real agents. The orchestrator's `Run` loop and
the scheduler's `DispatchReady` are pure-ish state transitions tested directly
(like TUI's `fold`); the goroutine/pump driver is a thin untested wrapper (like
`infra.Stop` / `tui.Run`).

---

## 4. The Plan and Todo

`internal/coord/plan.go` — typed `Plan`/`Todo` per File 12 §12.2.2:

```go
type Plan struct {
    ID        string
    Goal      string
    Todos     []Todo
    CreatedAt time.Time
}
type Todo struct {
    ID, Title, Acceptance string
    DependsOn  []string
    Status     TodoStatus    // Pending | InProgress | Blocked | Done | Failed
    Assignee   string
    Artifacts  []string
    ReworkCycles int
}
```

`PlanReadyEvent.Plan` stays `json.RawMessage` (event contract unchanged — File 05
is locked). The orchestrator marshals the typed `Plan` to RawMessage on publish;
the TUI already shows `planID` only and fills todos from the subsequent
`coord.task.assign` events (TUI-009 skeleton, unchanged this sprint).

---

## 5. Event protocol (publish/spawn model)

**Spec gap (Decision 2):** File 12 publishes `review.request`/`test.request` on
the bus and switches on `FindingsEvent`/`QuestionEvent`. None exist in the
catalog. Sprint 10's orchestrator:

- publishes `plan.ready` **once** (Plan marshaled to RawMessage);
- publishes `task.assign` **once per todo** (for the Coder);
- spawns the **Reviewer** and **Tester directly** via the `AgentRunner` seam
  (NO `review.request`/`test.request` events on the bus);
- agents publish `code.ready` / `review.verdict` / `test.report` themselves
  (faithful to File 12 §12.3.1 "agents communicate only via events");
- one board row per todo (created by `task.assign`), advancing
  assigned→coded→approved→tested — matches the existing TUI-009 fold exactly;
  **the TUI is not modified** (Sprint 9 is locked).

The full canonical turn: `task.assign(planner)` → (Planner produces Plan) →
`plan.ready` → per ready todo: `task.assign(coder)` → `code.ready` → (Reviewer,
direct) → `review.verdict` → approved? (Tester, direct) → `test.report` → pass?
`markDone`+dispatch dependents / rework.

---

## 6. Ticket breakdown (8 tickets, roadmap order)

| ID | Title | Exit bar | Design notes |
|---|---|---|---|
| L11-001 | Planner → Plan + Todos; auto-vs-single classification | "refactor X, add tests, fix CI" → Multi + heuristic plan ≥3 todos | `plan.go` (Plan/Todo/TodoStatus, AllDone/Todo/StatusOf), `classify.go` (Mode Single/Multi/SingleNamed, Classify — verb count, "and"/comma chains, `/plan`→Multi, `/agent`→SingleNamed, conservative Single default), `seam.go` (all seams), `planner.go` (`heuristicPlanner` deterministic split, implements `Planner`), `lint_test.go` (allowlist {event,cognitive,exec,verify,patch,memory,infra}+stdlib, self-proving). RED: scratch `internal/runtime` import → lint fails |
| L11-002 | Scheduler + Task Queue + DAG ordering | todos run in dependency order | `scheduler.go` (`Scheduler` holds Plan + inflight map, `DispatchReady(spawn func(*Todo))` respects DependsOn + inflight guard + concurrency bound, `depsMet`, `MarkDone/MarkFailed`→re-dispatch dependents). Linear + parallel + blocked + bound tests |
| L11-003 | Agent roles: Coder/Reviewer/Tester/Researcher with scoped tools | each role's tool set is enforced | `role.go` (`Role`, `RoleTools` per §12.2.1, `RoleAllowed(role,tool)`, `ScopedTools(role)`, priority user>planner>coder>reviewer>tester>researcher). Reviewer/Planner read-only (no Write/Patch); Coder only writer; Tester={Bash,Read}; Researcher={Read,Grep,Glob,Browser}. Real exec.Engine-per-role construction is integration (AgentRunner seam); gap logged |
| L11-004 | Orchestrator: spawn agents, collect outputs, rework cap | a rework loop hits the cap and escalates | `orchestrator.go` (`Orchestrator`, `Start(ctx)` subscribes `coord.>`+launches loop, `Run(ctx,goal)`=Plan→publish plan.ready→DispatchReady→event loop switch, `reassignCoder` with `MaxReworkCycles=3`>cap=MarkFailed+log, `cancelAll(ctx)` on ctx.Done, `Stop(ctx)` LIFO/idempotent mirroring infra), `config.go`. Full canonical run with fake runner; rework cap; ctx cancel; idempotent Stop + no-leak |
| L11-005 | Merge: combine diffs, resolve overlaps, re-verify | merged patch passes verification | `merge.go` (`Merge(plan, diffs map[todoID]string)(MergedPatch,error)`, `MergedPatch{CombinedDiff,Summary,Conflicts}`, overlap detection via `Artifacts`/file paths, `Verifier` seam re-verify). Distinct files→combine, no conflict, verifier pass→ok; same file→Conflict; verifier fail→merge fails. Real git-snapshot merge (Patch Engine §10.5) deferred; gap logged |
| L11-006 | Shared cost budget across agents (one ledger per task) | one agent's spend counts against the whole | `cost.go` (orchestrator registers task once `NewTask(planID)` on Start; checks `Snapshot(planID)` deadline before each dispatch→exceeded=abort; all agent events share PlanID so infra.Cost aggregates). NewTask called exactly once (idempotent); deadline exceeded→abort; under-deadline→continues; all agents share one PlanID. Gap: real accrual via infra cost.* subscription (L12-008); orchestrator registers+checks only |
| L11-007 | Board wiring: `coord.*` events populate the TUI board | the board shows live todo status | `cmd/yolo/coordination_test.go` (cross-package: real `event.Bus`, real `tui` fold driven directly (no TTY, mirror Sprint 9 pure-function exit bar), fake `AgentRunner` returning canned outputs; run orchestrator→capture 5 coord.* events→feed through `tui` fold→assert `boardView` shows rows + advancing badges). **No `headless.go` change** (CLI wiring deferred) |
| L11-008 | Single-agent fallback (coordination tax avoided for simple tasks) | "explain this function" stays single-agent | `route.go` (`Decision` DelegateSingle/Orchestrate/SingleNamedAgent, `Route(mode)Decision`, `ShouldOrchestrate(goal)bool`; orchestrator entry returns early for Single — no plan, no spawn). Gap: actual routing to single-agent runtime is composition root (integration) |

---

## 7. Documented spec gaps

- **`FindingsEvent`/`QuestionEvent`/`review.request`/`test.request` not in
  catalog** (Decision 2): only the five canonical `coord.*` events exist. The
  orchestrator spawns Reviewer/Tester directly via the `AgentRunner` seam (no
  bus events for review/test requests); Researcher delegation is deferred.
  Logged in `orchestrator.go` + `role.go`.
- **`PlanReadyEvent.Plan` is `json.RawMessage`**: the typed `Plan`/`Todo` live
  in `internal/coord` (`plan.go`); the orchestrator marshals to RawMessage on
  publish. The TUI shows `planID` only (TUI-009 unchanged). Logged in
  `orchestrator.go`.
- **Merge git-snapshot combined diff (Patch Engine §10.5) deferred**: Sprint 10
  combines in-memory diff strings and detects overlaps via `Todo.Artifacts` /
  file paths. The real three-way merge from Patch Engine snapshots is the
  integration sprint. Logged in `merge.go`.
- **Real agent drive deferred**: per-agent cognitive core + exec engine + scoped
  tool registry + verifier + patcher against a live codebase is the integration
  sprint. Sprint 10 uses the `AgentRunner` seam + a fake that publishes canned
  `code.ready`/`review.verdict`/`test.report`. Logged in `orchestrator.go`.
- **Heuristic planner (deterministic split on "and"/commas) replaces the LLM
  planner this sprint**: the LLM-driven Planner (cognitive core, read-only tools)
  is the integration sprint. Sprint 10's `heuristicPlanner` splits a complex
  goal into ≥3 todos deterministically so tests are reproducible. Logged in
  `planner.go`.
- **dollars/loops accrual via infra's `cost.*` subscription (L12-008)**: the
  orchestrator registers the task (`NewTask(planID)`) once and checks the
  deadline (`Snapshot`) before each dispatch; it does not itself accrue spend
  — that happens in infra.Cost as agent events flow through the bus (all sharing
  `PlanID`). Logged in `cost.go`.
- **`cmd/yolo/headless.go` CLI wiring of coord deferred to integration**:
  Sprint 10 adds no `coord` field to `headlessDeps` and no Start/Stop/drive
  wiring. L11-007 proves the `coord.*`→TUI contract via a cross-package test
  against the real `event.Bus` + real `tui` fold (no TTY). The composition-root
  wiring (mode routing into the runtime loop) is the integration sprint — same
  §15.9.2 bucket as the exec/verify/patch/restorer adapters. Logged in
  `cmd/yolo/coordination_test.go`.

---

## 8. Dependencies (go.mod delta)

**None.** `internal/coord` uses only `event` + stdlib; the existing
`charmbracelet/bubbletea` (Sprint 9) is unaffected. L11-007's cross-package test
imports `internal/tui` (already a dependency-free sibling) + `internal/event`; no
new module deps.

---

## 9. TDD discipline (per ticket)

Every ticket follows strict TDD (established Sprints 0-9):

1. **RED** — write the failing test (often a compile failure: undefined type /
   method / interface). Confirm it fails.
2. **GREEN** — minimal code to pass. Confirm green.
3. **Mutation check** — mutate the implementation (break one invariant),
   confirm the test fails, restore. Each ticket picks ≥1 meaningful mutation.
4. **gofmt -w** + **go vet** + **full suite** `go test ./...` + **3× stability**
   `go test -count=1` on the new tests.
5. **commit + push** to `baobao1044/yolo-code` master.

`-race` is unavailable on this Windows host (no gcc/cgo); 3× `-count=1`
stability is used instead and noted honestly in each commit message.

---

## 10. Sprint exit bar

A complex task ("refactor auth, update callers, add tests") decomposes into a
plan (heuristic, ≥3 todos), the Coder writes, the Reviewer critiques, the Tester
verifies, the Orchestrator merges, and the merged patch passes the `Verifier`
seam. A rework loop hits the cap (`MaxReworkCycles=3`) and escalates
(`MarkFailed` + log) rather than looping forever. A trivial task
("explain this function") stays single-agent (orchestrator not started, no
decomposition). The shared budget holds across agents: one `NewTask(planID)` for
the whole plan, every agent event sharing the `PlanID`. The `coord.*` events
populate the TUI board (L11-007 cross-package test: real bus + real `tui.fold`).
The package is import-clean (L11-001 allowlist lint green).

**Out of Sprint 10 scope** (deferred to the integration sprint, §15.9.2 bucket):
a live multi-agent run that actually modifies a real repo. That requires real
per-agent cognitive cores, exec engines with scoped tool registries, real
verifier/patcher adapters, the git-snapshot merge, the `QuestionEvent`/
`FindingsEvent` Researcher delegation, and the `headless.go` composition-root
wiring (mode routing into the runtime loop) — plus the exec/verify/patch/
restorer adapters still outstanding from Sprint 6. Sprint 10 proves the
orchestration logic + event protocol + board contract are correct with fakes;
the integration sprint wires them end-to-end against real layers.

The Sprint 10 bar is therefore: (a) a replay test runs the full canonical loop
with fake agents + fake Verifier + fake CostLedger + real `event.Bus` and asserts
the plan decomposes, todos advance assigned→coded→approved→tested, the merged
patch passes the Verifier, a rework loop hits the cap and fails, a trivial goal
stays single-agent, and the shared ledger registers once; (b) L11-007 asserts
the `coord.*` events drive the real `tui.fold` to a board with live todo status;
(c) `lint_test` is green; (d) 3× stability green.

---

*End of Sprint 10 design spec.*
