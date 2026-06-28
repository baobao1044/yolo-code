# Sprint 9 — TUI (subscribe-only) Design

**Date:** 2026-06-28
**Sprint:** 9 (roadmap §15.12) — builds `internal/tui` (File 14)
**Predecessor:** Sprint 8 (Infrastructure, L12) — pushed to `master`
**Spec status:** approved (3 design questions answered)

This spec fixes the design of the terminal UI before any code is written. It
records the three confirmed decisions, the architecture, the 9-ticket
breakdown, the TDD discipline, and the spec gaps File 14 carries vs the real
event catalog and bus API.

---

## 1. Decisions (confirmed)

1. **Real bubbletea deps** — add `charmbracelet/bubbletea` + `lipgloss` + `bubbles`
   to `go.mod` (fetchable via module proxy, verified: bubbletea v1.3.10 latest).
   This is the **first external dependency** after 8 sprints of stdlib-only
   (Sprint 7 precedent). File 14 explicitly calls for bubbletea; the production
   renderer is real. Tests target the pure `fold`/`handleInput`/`tick`/`View`
   functions + IO seams, never `tea.Program.Run` (untestable without a TTY).

2. **Cost meter = degraded+abort only, gap logged** — the event catalog
   (verified) has `CostDegradedEvent` + `CostAbortEvent` but **no**
   `CostSpendEvent`/`CostLoopEvent`. TUI-005 shows the degradation level (from
   `cost.degraded`) + the abort banner (from `cost.abort`); dollars/loops stay
   blank and are documented as an integration-sprint gap (the events don't
   exist yet — L6 cognitive doesn't publish them). Keeps TUI subscribe-only +
   import-matrix clean (no `tui→infra` import). Matches the L12-008 gap.

3. **Pure functions + interface seams** — `fold`/`handleInput`/`tick`/`View`
   are pure `(Model, Cmd)` transitions tested directly with fake envelopes /
   inputs (no TTY). `busWatcher` tested by feeding its subscription channel.
   `Run()` (`tea.NewProgram().Run()`) is a thin untested driver (accepted,
   like `infra.Stop`). Bus + publisher are small interfaces defined in `tui`
   (`Subscribable{Subscribe}`, `EventPublisher{Publish}`) that `*event.Bus`
   satisfies; tests pass fakes to assert **exactly** which `user.*` event was
   published (no real-bus read, no timing flakiness).

---

## 2. Architecture & import boundary

`internal/tui` is a **pure projection** (File 14 §14.1): it subscribes to
rendering topics, projects event state onto the screen with bubbletea, and
publishes `user.*` events when the user types. It holds no state machine of
its own — `state` is a string copied from the last `state.change`, the TUI
does not model the FSM.

Import boundary (CI-enforced by TUI-008): `internal/tui` may import ONLY
`event` + `bubbletea`/`lipgloss`/`bubbles` + stdlib. This is the seam that
lets the same agent run headless or behind a future web UI without changing
a line of runtime code.

```
        ┌──────────── internal/tui ────────────┐
        │                                       │
 stdin ─► handleInput ──publish(user.*)──► EventPublisher seam ──► event.Bus
        │      ▲                                │
        │      │ keymap: Enter/Esc/y/n/Ctrl+P/R/C│
        │                                       │
 bus ──► busWatcher Cmd ──busMsg──► Update ──fold──► Model ──View──► screen
        │  (long-lived, re-launches)            │   (type-switch on
        └── Subscribe(task.>, state.change, …) ─┘    Evt, not env.Str())
```

---

## 3. Seams (TDD-enabling)

`internal/tui/seam.go`:

```go
type Subscribable interface { Subscribe(...event.Topic) <-chan event.Envelope }
type EventPublisher interface { Publish(context.Context, event.Event) error }
```

`*event.Bus` satisfies both. Tests pass fakes → assert which `user.*` was
published without a real bus. Pure `fold`/`handleInput`/`tick`/`View` take a
`Model` (carrying the seams); `tea.Program.Run()` is a thin untested driver.

---

## 4. The Model (render state)

`Model` holds render state — enough to paint, nothing more: `width,height`,
`focus pane`, `taskID`, `taskKind`, `state`, `messages []messageView`,
`thinking`, `streaming`, `activeTool`, `toolStart`, `approval *approvalView`,
`diff *diffView`, `cost costView`, `board *boardView`, `banner`, `input
textinput.Model`, `sub`, `cancel`, `publisher`. Every field is derived from
events.

---

## 5. `fold()` — type-switch, NOT `env.Str()`

**Spec gap (File 14 §14.4.2):** the doc uses `env.Str("task_id")` /
`env.Float("dollars")` — these accessors **do not exist**. `Envelope.Evt` is a
typed `event.Event`, so `fold` performs a **type switch** on `env.Evt` and
reads typed fields. This is type-safe and compile-checked. Event fields map
1:1 to the doc (e.g. `*TaskStartedEvent{Task, Session, Goal}` → `m.taskID = e.Task`).
`fold` returns the re-launched `busWatcher` Cmd so the bridge keeps pumping;
it never calls the runtime.

---

## 6. Ticket breakdown (9 tickets, roadmap order)

| ID | Title | Exit bar | Design notes |
|---|---|---|---|
| TUI-001 | bubbletea program + busWatcher bridge + Model + seams | events render to the screen | `seam.go` (Subscribable/EventPublisher), `model.go`, `bus.go` (busWatcher Cmd), `run.go` (tea.NewProgram, untested driver), `subscribe()` passes `xxx.>` prefixes. RED: fold produces a header from TaskStartedEvent |
| TUI-002 | Chat pane | a streamed message renders incrementally | `messageView{role,text,outcome}`, fold on `llm.thinking`/`assistant.message`/`tool.call`/`tool.result`/`observation.received`. Roles: user/assistant/thinking/tool |
| TUI-003 | Status bar | state.change updates the bar | `task.*`/`state.change`/`context.built`/`memory.update` |
| TUI-004 | Diff viewer (viewport) on patch.applied/verification.failed | a diff scrolls, hunk-colored | `bubbles/viewport`, lipgloss green `-`/red `+`/cyan hunk headers (hunk-coloring, not full language syntax highlighting). Sets `m.diff` |
| TUI-005 | Cost meter rail | degradation level + abort banner shown | **degraded+abort only** (gap logged — no cost.spend/cost.loop events in catalog; dollars/loops blank) |
| TUI-006 | Input + keymap → user.* events | keystrokes drive the runtime via the bus | `handleInput`: Enter→submit, Esc→cancel, y/n→approve/reject, Ctrl+P/R→pause/resume, Ctrl+C→quit. Publishes via fake pub in tests |
| TUI-007 | High-freq coalescing (token/thinking → 60 Hz) | a fast stream doesn't peg the render thread | `tickMsg` Cmd + `tick()`. fold accumulates; View shows accumulated state; tick repaints at 60 Hz |
| TUI-008 | Import-allowlist lint (no layer except event) | a forbidden import fails CI | a Go test in `internal/tui` lists the package's own files + asserts the allowlist (self-proving, no CI tooling). RED: add a tui→cognitive import → test fails |
| TUI-009 | Multi-agent board on coord.* (skeleton) | board renders when coord.plan.ready arrives | `boardView`, fold on PlanReady/TaskAssign/CodeReady/ReviewVerdict/TestReport. Skeletons only (filled in Sprint 10) |

---

## 7. Documented spec gaps

- **TUI-005 dollars/loops**: `cost.spend`/`cost.loop` events don't exist in the
  catalog (only `CostDegraded`/`CostAbort`). TUI shows degraded level + abort
  banner; dollars/loops blank. Deferred to the integration sprint (adds events
  in L6 + wires TUI). Mirrors the L12-008 gap we already documented.
- **`env.Str()` dynamic accessors**: don't exist; `fold` uses a type switch.
  Documented in code.
- **`SubscribeMulti`**: doesn't exist; `Subscribe` is variadic + supports
  `prefix.>` wildcards (verified in bus.go `matches`). Pass N topic prefixes.
- **`tea.Program.Run()` untested**: needs a TTY; accepted as a thin driver,
  like `infra.Stop`. Each ticket's pure logic is unit-tested; the Sprint exit
  bar is additionally asserted via the composition-root integration (cmd/yolo
  wires the TUI like headless).

---

## 8. Dependencies (go.mod delta)

Added in Sprint 9: `github.com/charmbracelet/bubbletea@v1.3.10`,
`github.com/charmbracelet/lipgloss`, `github.com/charmbracelet/bubbles` (latest
resolvable; `go mod tidy` resolves transitives). First break of the
stdlib-only precedent; File 14 explicitly calls for it.

---

## 9. TDD discipline (per ticket)

Every ticket follows strict TDD (established Sprints 0-8):

1. **RED** — write the failing test (often a compile failure: undefined type /
   method). Confirm it fails.
2. **GREEN** — minimal code to pass. Confirm green.
3. **Mutation check** — mutate the implementation (break one invariant),
   confirm the test fails, restore. Each ticket picks ≥1 meaningful mutation.
4. **gofmt -w** + **go vet** + **full suite** `go test ./...` + **3× stability**
   `go test -count=1` on the new tests.
5. **commit + push** to `baobao1044/yolo-code` master.

---

## 10. Sprint exit bar

The interactive agent is usable: a user types a prompt, watches the thinking
stream, sees a tool run, reviews the applied diff, and the task completes —
all rendered purely from events. **S1** (<200 ms cold start to first paint),
**S2** (<50 ms token→screen), **S6** (<1 keypress to "what is it doing") are
measured and green. The TUI imports no layer except `event` — verified by CI
(TUI-008). Because the TUI can't be exercised end-to-end without a TTY, the
exit bar is additionally asserted via the **composition-root integration**
(cmd/yolo wires the TUI like headless) + the **unit coverage of every pure
component**.

---

*End of Sprint 9 design spec.*
