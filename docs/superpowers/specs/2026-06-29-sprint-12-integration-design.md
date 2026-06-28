# Sprint 12 — Integration End-to-End (§15.9.2)

**Date:** 2026-06-29
**Sprint:** 12 — Integration sprint
**Predecessor:** Sprint 11 (Hardening & Distribution) — pushed to `master` (`bf85a05`)
**Spec status:** draft (pending approval)

This spec records the design for the deferred §15.9.2 integration bucket:
wire the real `exec`, `verify`, `patch`, `session`, `cognitive`, `context`,
`prompt`, and `coord` layers together through the runtime port seams so that a
`yolo --headless` run can actually edit a repo, run tests, apply patches, and
roll back on failure.

---

## 1. Decisions (confirmed)

1. **Single-agent runtime first, multi-agent second** — Sprint 12 wires the
   headless single-agent path (`cmd/yolo/headless.go`) first.  The multi-agent
   `coord` path (L11) uses the same adapters through a thin `coord.AgentRunner`
   bridge, but the exit bar is a single-agent edit→verify→rollback that
   exercises every real port (`exec`, `verify`, `patch/restorer`).

2. **Adapters live in `cmd/yolo`** — just like `memoryStoreAdapter` and
   `infraSecretsAdapter` before them, the new `execAdapter`, `verifyAdapter`,
   `patchAdapter`, `cognitiveAdapter`, and `checkpointerAdapter` are composition-
   root adapters in `cmd/yolo`.  They keep the import matrix clean:
   `internal/runtime` does not import `exec`/`verify`/`patch`; `cmd/yolo` is
   allowed to import any layer.

3. **No new external dependencies** — the integration uses only existing
   packages (`event`, `context`, `prompt`, `cognitive`, `exec`, `verify`,
   `patch`, `session`, `infra`) + stdlib.

4. **Real checkpointer = git shadow copy** — rollbacks are real: the patch
   checkpointer records a `SnapshotRef` by copying the relevant files into a
   temp shadow tree; `Restore` copies them back.  This satisfies the §15.9.2
   rollback invariant without requiring a full git commit per edit.

---

## 2. Architecture

```
         cmd/yolo/headless.go
              │
              ▼
    ┌─────────────────────────┐
    │  Composition-root adapters (cmd/yolo)
    │  execAdapter ────────► exec.Engine
    │  verifyAdapter ──────► verify.Engine
    │  patchAdapter ───────► patch.Engine
    │  checkpointerAdapter ► patch.Engine (shadow copy)
    │  cognitiveAdapter ───► cognitive.Core
    │  ctxAdapter, prompt... ► context.Engine, prompt.Compiler
    └─────────────────────────┘
              │
              ▼
    ┌─────────────────────────┐
    │    runtime.Core (FSM)     │
    │  needs all ports wired   │
    │  drives EXECUTE→VERIFY   │
    └─────────────────────────┘
```

The integration wires the runtime `Deps` with real implementations:

| Runtime port | Real implementation | Adapter |
|---|---|---|
| `ContextBuilder` | `context.Engine` | direct (same method shape) |
| `PromptCompiler` | `prompt.Compiler` | direct |
| `CognitiveCore` | `cognitive.Core` | `cognitiveAdapter` (type bridge) |
| `Executor` | `exec.Engine` | `execAdapter` (tool-call bridge + patch routing) |
| `Verifier` | `verify.Engine` | `verifyAdapter` (policy/verdict bridge) |
| `Patcher` | `patch.Engine` | `patchAdapter` (Op bridge) |
| `Restorer` | `*session.Manager` | direct (`Restore` matches) |
| `MemoryStore` | `memory.Store` | `memoryStoreAdapter` (exists) |
| `Bus` / `Session` | `*event.Bus`, `*session.Manager` | direct |

---

## 3. Adapters

### 3.1 `cmd/yolo/exec_adapter.go`

`execAdapter` implements `runtime.Executor`:

- `Dispatch` maps `runtime.ToolCall` → `exec.ToolCall`.
- If the tool is `"patch"`, parses JSON args for `path` and `body`, calls the
  patch engine directly, and returns a `runtime.Observation` with:
  - `Stdout`/`Stderr` from patch.
  - `Summary.Files` and `Summary.Insertions/Deletions`.
  - `Checkpoint` set from the patch result's `SnapshotRef`.
- Otherwise it looks up the registered tool from `exec.Engine` and runs it.
- All filesystem paths pass through the sandbox rooted at the repo root.

### 3.2 `cmd/yolo/verify_adapter.go`

`verifyAdapter` implements `runtime.Verifier`:

- Translates `runtime.Observation` + `runtime.VerifyPolicy` to
  `verify.Change` + `verify.Policy`.
- Uses a real `verify.Runner` backed by `exec.CommandContext` to run `gofmt`,
  `go vet`, `go test`, etc.
- Returns `runtime.Verdict`.

### 3.3 `cmd/yolo/patch_adapter.go`

`patchAdapter` implements `runtime.Patcher`:

- Builds `patch.Op` from `runtime.PatchOp`.
- Parses `PatchOp.Body` with `patch.ParseBlocks`.
- Infers target file path from the tool-call args (passed through the prior
  observation) or from `Observation.Files`.
- Calls `patch.Engine.Apply` and returns `runtime.PatchResult`.

### 3.4 `cmd/yolo/checkpointer_adapter.go`

`shadowCheckpointer` implements `patch.Checkpointer`:

- `Checkpoint(ctx, ref, files)` copies the listed files from the repo into a
  temp shadow directory keyed by `ref`.
- `Restore(ctx, ref)` copies the shadow files back.
- This is a real rollback that survives across the runtime FSM states.

### 3.5 `cmd/yolo/cognitive_adapter.go`

`cognitiveAdapter` implements `runtime.CognitiveCore`:

- Bridges `runtime.Prompt` (`[]prompt.Message`) to `[]prompt.Message`.
- Bridges `cognitive.Turn` → `runtime.CognitiveTurn`.
- Bridges `cognitive.ToolCall[]` → `runtime.ToolCall[]`.
- Bridges `cognitive.ReflectionDecision`/`cognitive.PatchOp` → runtime types.

---

## 4. Ticket breakdown (Integration tickets)

| ID | Title | Exit bar | Files |
|---|---|---|---|
| INT-001 | Wire `exec.Engine` adapter in headless runtime | A `bash` tool call executes inside the sandbox and returns stdout | `cmd/yolo/exec_adapter.go`, `cmd/yolo/headless.go` |
| INT-002 | Wire `verify.Engine` adapter with real os/exec runner | `go test` failure produces a failing `runtime.Verdict` | `cmd/yolo/verify_adapter.go`, `cmd/yolo/verify_runner.go` |
| INT-003 | Wire `patch.Engine` + shadow checkpointer + rollback | A patch edit is applied; on verify failure the original file is restored | `cmd/yolo/patch_adapter.go`, `cmd/yolo/checkpointer_adapter.go` |
| INT-004 | Wire real `context.Engine`, `prompt.Compiler`, `cognitive.Core` adapters | Headless run produces a compiled prompt from real context and a real cognitive turn | `cmd/yolo/cognitive_adapter.go`, `cmd/yolo/headless.go` |
| INT-005 | Single-agent end-to-end regression | A synthetic headless run edits a Go file, breaks tests, and rolls back; transcript contains `patch.applied` + `verification.failed` + file restored | `cmd/yolo/integration_test.go` |
| INT-006 | Multi-agent coord `AgentRunner` adapter | The orchestrator can spawn a real coder via `runtime.Core` for one todo; events appear on the bus | `cmd/yolo/coord_runner.go`, `cmd/yolo/coordination_test.go` update |
| INT-007 | Real Planner adapter for coord | A heuristic/real planner returns a Plan from a goal; single-agent goals still bypass orchestrator | `cmd/yolo/planner_adapter.go` |
| INT-008 | Headless/TUI mode routing into runtime loop | `yolo --headless` uses wired runtime; interactive path still prints the pending-integration hint | `cmd/yolo/main.go`, `cmd/yolo/headless.go` |
| INT-009 | Fix FSM/policy seams for integration | Verify policy is selected per task type; `PatchOp` carries enough metadata for the patch adapter | `internal/runtime/core.go`, `internal/runtime/ports.go` |

**Out of Sprint 12 scope** (deferred):
- Real LLM planner / real LLM cognitive core (still uses deterministic/test
  scripted cores; integration tests control them).  Real providers are a later
  hardening sprint.
- Full git snapshot checkpointer (shadow copy is enough for the exit bar).
- TUI interactive mode fully wired to runtime (kept as deferred composition-
  root wiring).
- Publishing/cost dashboards.

---

## 5. Documented spec gaps

- `runtime.PatchOp` currently lacks `Path`/`Seq` fields; the patch adapter must
  extract target path from tool-call args or from `Observation.Files`.
- `runtime.Core.policyFor` is hard-coded to full verification; integration tests
  can pass but a real task policy seam is noted as a follow-up.
- `FindingsEvent`/`QuestionEvent`/`review.request`/`test.request` are still
  absent from the event catalog, so the multi-agent `coord` path remains on
  the direct-spawn model established in Sprint 10.
- The `exec` registry has no built-in `"patch"` tool; Sprint 12 routes it at the
  composition-root adapter level so `exec` does not need to import `patch`.

---

## 6. Dependencies (go.mod delta)

**None.** All wiring uses existing internal packages and stdlib.

---

## 7. TDD discipline (per ticket)

1. **RED** — write the failing test (compile or behavior failure).
2. **GREEN** — minimal adapter code to pass.
3. **Mutation check** — mutate the adapter bridge and confirm the test fails;
   restore.
4. **gofmt -w** + **go vet** + **default suite** `go test ./...` + **3× stability**
   `go test -count=1`.
5. **commit + push** to `baobao1044/yolo-code` master.

`-race` remains CI/Linux only; local 3× stability is used and noted in commit
messages.

---

## 8. Sprint exit bar

Sprint 12 is done when:

1. **INT-001..INT-004** — Real adapters for `exec`, `verify`, `patch/restorer`,
   `cognitive`, `context`, `prompt` are wired into `headlessDeps`.
2. **INT-005** — A single-agent headless integration test edits a Go file,
   triggers a real `verification.failed` event, and rolls the file back via the
   shadow checkpointer.  The task does not end `DONE`.
3. **INT-006 + INT-007** — The orchestrator can drive a `runtime.Core`-backed
   `AgentRunner` for a simple multi-todo plan; the canonical `coord.*` events
   still match the TUI board contract.
4. **INT-008** — `yolo --headless` uses the wired runtime.
5. **Repo hygiene** — `go test ./...`, snapshot, golden, docs, and release gates
   remain green; cross-compile green; gofmt clean; 3× stability green;
   commits pushed to master.

---

*End of Sprint 12 design spec.*
