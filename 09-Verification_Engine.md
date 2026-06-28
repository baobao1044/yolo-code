# 09 — Verification Engine

> **Goal of this document:** design Layer 8 — the automated check pipeline that
> decides whether a change is safe to keep. It is **not just a linter**: it runs
> a staged pipeline from AST validation through formatting, linting, type
> checking, building, testing, and a final policy check. A failure here drives
> the Reflection step (File 07) and the Patch Engine's rollback (File 10).

This file owns **Layer 8 (`internal/verify`)**. It consumes an `Observation`
(from File 08, which includes patch-applied changes) and returns a `Verdict`
that the runtime acts on (File 04, transitions T11–T14).

---

## Table of Contents

1. [Why Verification Is a Separate Layer](#91-why-verification-is-a-separate-layer)
2. [The Pipeline](#92-the-pipeline)
3. [Per-Stage Contracts](#93-per-stage-contracts)
4. [Verdicts: Pass / Warn / Fail](#94-verdicts-pass--warn--fail)
5. [Scope, Timeouts, and Skips](#95-scope-timeouts-and-skips)
6. [The Engine, consolidated](#96-the-engine-consolidated)

---

## 9.1 Why Verification Is a Separate Layer

In an earlier draft verification lived inside the Patch Engine. Lifting it to
its own layer is deliberate:

- **It verifies *any* change, not just patches.** A `Bash` tool that writes a
  file, an MCP tool that regenerates code, a git operation — all produce changes
  that should be verified the same way. Coupling verification to the patch
  engine would leave non-patch changes unverified.
- **It is the input to Reflection.** A structured `Verdict` (what stage failed,
  what the errors were) is exactly what the Cognitive Core's Reflection step
  needs to diagnose root cause. That contract deserves its own layer.
- **It is policy-driven.** What "done" means varies per task (File 07 §7.5.2):
  a quick explanation needs only AST; a "ship it" task needs tests. The
  Verification Engine consumes the `VerificationPolicy` and runs the configured
  stages.

---

## 9.2 The Pipeline

```mermaid
flowchart TB
    CHG[Change: paths + Observation] --> AST{AST valid?}
    AST -- no --> FAIL[Fail]
    AST -- yes --> FMT{Format check}
    FMT -- no --> AF[Auto-format + re-validate]
    AF --> AST
    FMT -- yes --> LINT{Lint / vet}
    LINT -- errors --> FAIL
    LINT -- warnings only --> KEEP[Pass with warnings]
    LINT -- clean --> TYPE{Type check}
    TYPE -- no --> FAIL
    TYPE -- yes --> BUILD{Build / compile}
    BUILD -- no --> FAIL
    BUILD -- yes --> TEST{Tests}
    TEST -- fail --> FAIL
    TEST -- pass --> POL{Policy check}
    POL -- blocked --> FAIL
    POL -- ok --> DONE[Pass]
    FAIL --> V[Verdict{fail, stage, errors}]
    KEEP --> V2[Verdict{pass, warnings}]
    DONE --> V3[Verdict{pass}]
```

Each stage runs only if the policy requires it (File 07 §7.5.2) and only for
the languages the changed files belong to. A stage that fails short-circuits
the pipeline.

---

## 9.3 Per-Stage Contracts

### 9.3.1 AST validation
Tree-sitter parse of each changed file. An `ERROR` node in the parse means
broken syntax (a half-deleted function, a dangling brace) — caught at the
cheapest cost (a parse, sub-millisecond for typical files). Files of unknown
language or with no grammar **skip** this stage rather than fail (a Markdown
edit is not blocked for lack of a Markdown grammar).

### 9.3.2 Format check
Verifies the file matches the project's formatter. If not and `AutoFormat` is
on, the engine runs the formatter and **re-runs AST validation** (formatting can
expose a syntax issue the model's unformatted version hid). Format mismatches
with `AutoFormat` off are warnings, not failures.

### 9.3.3 Lint / vet
Runs the project's linter scoped to the changed file's package/module —
`go vet` + `golangci-lint` for Go, `ruff` for Python, `clippy` for Rust,
`eslint` for JS/TS. Scoping keeps it fast (`go vet ./pkg/x/`, not `./...`).

### 9.3.4 Type check
Static type checking — `go build` scoped to the package, `mypy` for Python,
`tsc --noEmit` for TS. This catches undefined references and type errors the
AST cannot.

### 9.3.5 Build / compile
A scoped build — `go build ./pkg/x/`, `cargo check`, `npm run build`. Confirms
the change compiles in context, not just in isolation.

### 9.3.6 Tests
Runs the tests for the affected scope — `go test ./pkg/x/`, `pytest pkg/x`,
`cargo test -p x`. Gated by `RequireTests` and the policy's `TestTimeout`. A
test timeout is a **warning** (a slow CI machine shouldn't veto a correct
patch), not a failure.

### 9.3.7 Policy check
A final, configurable gate that applies project rules the linters can't express
— "no new dependencies without a comment", "no edits under `vendor/`", "no
`TODO` without an owner". Implemented as a small rule engine over the diff. This
is where project-specific guardrails from `AGENTS.md` are enforced.

---

## 9.4 Verdicts: Pass / Warn / Fail

```go
package verify

type Verdict struct {
    Pass     bool
    Stage    Stage         // which stage produced this verdict
    Severity Severity      // pass | warn | fail
    Reason   string
    Errors   []Issue
    Warnings []Issue
}

type Stage int
const (
    StageAST Stage = iota; StageFormat; StageLint; StageTypeCheck; StageBuild; StageTest; StagePolicy
)

type Issue struct {
    Path    string
    Line    int
    Code    string         // linter rule id
    Message string
}
```

| Verdict | Runtime action (File 04) |
|---|---|
| `pass` | `VERIFY → PLAN` (more to do) or `VERIFY → DONE` |
| `pass` with warnings | same; warnings surfaced to the model for a follow-up fix |
| `fail` | `VERIFY → PATCH` (reflection proposes a corrective patch) or `VERIFY → PLAN` (reflection decides to replan) |

### 9.4.1 Warnings vs errors
- **Errors** (non-zero hard exit): the change broke something → fail → rollback
  path.
- **Warnings** (configurable): the change is acceptable but imperfect → pass,
  recorded, surfaced. Linters are opinionated; a "unused variable" warning
  should not roll back a correct patch.

### 9.4.2 The verification event
Each completed stage publishes an advisory event so the TUI shows a green
check / red cross per stage:

```go
type VerificationEvent struct {
    Task   TaskID
    Stage  Stage
    Status string   // "pass" | "warn" | "fail"
    Detail string
}
```

---

## 9.5 Scope, Timeouts, and Skips

### 9.5.1 Scoping
Every stage's command is scoped to the changed file's package/module so a
monorepo-wide `go build ./...` doesn't penalize a one-file edit. Scope is
derived from the changed paths.

### 9.5.2 Timeouts
Each stage has a timeout (default 30s, configurable). A timeout is a
**warning**, not a fail — we don't roll back a patch because the build is slow,
only because it fails. This avoids a slow CI machine vetoing correct patches.

### 9.5.3 Skips
A stage is skipped when:
- the policy doesn't require it (`RequireTests == false`),
- the changed language has no tool for it (a `.md` file skips lint/build/test),
- the previous stage already failed (short-circuit).

Skips are recorded in the verdict so the trace shows *why* a stage didn't run,
not just that it didn't.

### 9.5.4 Per-language stage matrix

| Language | Format | Lint | Type check | Build | Tests |
|---|---|---|---|---|---|
| Go | `gofmt -l` | `go vet` / `golangci-lint` | `go build` (pkg) | `go build` (pkg) | `go test` (pkg) |
| Python | `black --check` | `ruff` | `mypy` | — | `pytest` |
| Rust | `rustfmt --check` | `clippy` | `cargo check` | `cargo check` | `cargo test` |
| JS/TS | `prettier --check` | `eslint` | `tsc --noEmit` | `npm run build` | `npm test` |
| Other | skip | skip | skip | skip | skip |

---

## 9.6 The Engine, consolidated

```go
package verify

type Engine struct {
    validator  *ast.Validator          // tree-sitter
    stages     map[Stage]StageRunner
    policy     cognitive.VerificationPolicy   // shared with Core (File 07)
    config     Config
    bus        *event.Bus
    log        *slog.Logger
}

type Config struct {
    AutoFormat   bool
    StageTimeout time.Duration
}

func (e *Engine) Verify(ctx context.Context, obs Observation, task *session.Task) (Verdict, error) {
    paths := obs.Files
    pol := e.policyFor(task)

    stages := e.planStages(paths, pol)   // which stages, scoped, per language
    for _, st := range stages {
        result := st.Run(ctx, paths)
        e.bus.Publish(ctx, VerificationEvent{Task: task.ID, Stage: st.Name, Status: result.Status, Detail: result.Detail})
        switch result.Status {
        case "fail":
            return Verdict{Pass: false, Stage: st.Name, Severity: SevFail,
                Reason: result.Detail, Errors: result.Issues}, nil
        case "warn":
            // continue; record warnings
        }
    }
    return Verdict{Pass: true, Severity: SevPass, Warnings: collectWarnings(stages)}, nil
}

type StageRunner interface {
    Name() Stage
    Run(ctx context.Context, paths []string) StageResult
}
```

### 9.6.1 Stage implementations
Each stage is a `StageRunner` registered at startup; the per-language commands
live in a small table. Adding a language means adding one entry per stage, not
touching the engine — the same extensibility rule as the tool registry.

---

## 9.7 What this file fixes, and what it hands off

**Fixed here:**
- the rationale for verification as its own layer (verifies any change, feeds
  Reflection, policy-driven);
- the staged pipeline (AST → format → lint → type → build → tests → policy)
  with short-circuit and per-stage events;
- the verdict model (pass / warn / fail) and how the runtime maps each to a
  transition;
- scoping, timeouts (timeout = warning, not fail), and the per-language stage
  matrix;
- the policy-driven stage planning.

**Handed off:**
- The `VerificationPolicy` is owned by the Cognitive Core → **File 07 §7.5.2**.
- A `fail` verdict triggers Reflection (which may request a patch) → **File 07
  §7.3** and the Patch Engine → **File 10**.
- The AST validator uses tree-sitter, shared with the chunker in **File 11**.

---

*End of File 09 — Verification Engine.*
