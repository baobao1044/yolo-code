# Sprint 13 Design — Multi-Agent End-to-End & Cost Accrual

## 1. Scope

Sprint 12 wired every individual seam into the runtime. Sprint 13 runs the
whole machine end-to-end: a `coord.Planner` decomposes a complex goal into
multiple `Todo`s, a multi-role `AgentRunner` dispatches real `runtime.Core`
agents, and the orchestrator merges their diffs, re-verifies, and reports
done. Cost accrual is also wired so every agent run contributes to the
shared plan budget.

**Out of scope (deferred):**
- Full TUI interactive wiring to runtime (`WAIT_USER`, `PAUSED` arms, §15.10).
- External LLM providers; all agents use scripted/canned providers.
- Actual GitHub release artifact publishing (was Sprint 12 dry-run).

## 2. Background

- `internal/coord/planner.go` has a heuristic `Planner`.
- `internal/coord/merge.go` can combine per-todo `diff` strings and detect
  file overlaps.
- `cmd/yolo/coord_runner.go` has a `runtimeAgentRunner` that handles only
  `RoleCoder` through a real runtime.Core; reviewer/tester still publish
  canned events.
- `cmd/yolo/headless.go` wires real adapters when no `headlessDeps` are
  injected.
- `internal/infra/cost.go` exists; `internal/coord/cost.go` wraps it as a
  `Budget` but real accrual is not yet wired.

## 3. Acceptance criteria

1. `runtimeAgentRunner` supports `RoleReviewer` and `RoleTester` with real,
    deterministic work fed by scripted providers.
2. A `coord.NewOrchestrator` with the real planner and runner can drive a
    3-todo plan to completion: coder patches pass, reviewer returns a
    `ReviewVerdictEvent`, tester returns a `TestReportEvent`, and
    `coord.Merge` produces an approved `MergedPatch`.
3. The orchestrator emits `cost.incurred` events at least once.
4. CLI `yolo --plan "goal"` uses the orchestrator path.
5. Existing gates stay green:
    - `go test ./...`
    - `go test -tags golden ./cmd/yolo`
    - `go vet ./cmd/yolo ./internal/runtime ./internal/coord`
    - `gofmt`

## 4. Tickets

### S13-001 — AgentRunner supports reviewer + tester

- Extend `cmd/yolo/coord_runner.go`.
- `RoleReviewer`: read the todo artifact file(s), run a lightweight analysis
  with the scripted provider, publish `coord.review.verdict`.
- `RoleTester`: run `go test` or equivalent via the real exec adapter,
  publish `coord.test.report`.
- Keep deterministic by injecting a scripted `Provider` and using only
  low-risk `go version` / test commands that pass RiskLow classification.

### S13-002 — Multi-agent end-to-end regression

- `cmd/yolo/coordination_test.go` or new `cmd/yolo/multiagent_test.go`.
- Use `heuristicPlanner`, real `runtimeAgentRunner`, and fake/seeded
  providers per role.
- Assert:
  - `coord.plan.ready`
  - per-todo `task.assign`
  - `coord.code.ready` for each coder todo
  - `coord.review.verdict` with `Approved: true`
  - `coord.test.report` with `Passed: true`
  - `coord.merge.ready` (or merge event)
  - final plan done (`Done` status on all todos)

### S13-003 — Cost accrual wiring

- In `cmd/yolo/headless.go` and `cmd/yolo/coord_runner.go`, create a real
  `infra.Cost`, register it on the event bus, and inject it into
  `coord.NewBudget`.
- Ensure `cost.incurred` events appear in the multi-agent regression.
- Add a unit test in `cmd/yolo` that asserts `cost.incurred` is emitted
  after a simple agent run.

### S13-004 — CLI `--plan` flag

- `cmd/yolo/main.go` supports `yolo --plan <goal>`.
- Uses the real planner, runner, and orchestrator.
- If `<goal>` is Single mode, fall back to the existing headless path.
- Prints the transcript/bus events as JSONL (mirroring headless output).

### S13-005 — Document & exit bar

- Update this spec checklist.
- Add short `README.md` note or inline `cmd/yolo/main.go` help text.
- Run all gates and push to `master`.

## 5. Specitative design notes

### Scripted multi-role providers

Each agent run receives a `prompt.Message` list containing the todo title
and prior context. The provider returns deterministic deltas/tool calls:

- **Coder**: emits a `patch` tool call for its assigned file, then a
  `verify` tool call; finally a direct answer.
- **Reviewer**: emits a direct answer containing `DECISION: approve`.
- **Tester**: emits a `bash go test ./...` tool call, then a direct answer
  with `PASS`.

Because `runtimeAgentRunner` creates a fresh `runtime.Core` per todo, the
runner must pass the todo ID as the task/session ID so events stay
attributable.

### Cost wiring

`infra.NewCost(bus)` registers for tool-call and token events and publishes
`cost.incurred`. The composition root must:

1. Create `infra.Cost`.
2. `Start` it on the shared bus.
3. Pass it as the `CostLedger` to `coord.NewBudget(planID, cost)`.
4. Stop it after `orchestrator.Run` returns.

## 6. Exit bar checklist

- [ ] `S13-001` implemented + tests pass
- [ ] `S13-002` multi-agent regression passes
- [ ] `S13-003` cost events wired
- [ ] `S13-004` `--plan` CLI works
- [ ] `go test ./...` green
- [ ] `go test -tags golden ./cmd/yolo` green
- [ ] `go vet ./cmd/yolo ./internal/runtime ./internal/coord` green
- [ ] `gofmt` clean
- [ ] Spec committed to `docs/superpowers/specs/2026-06-30-sprint-13-design.md`
- [ ] All work pushed to `master`
