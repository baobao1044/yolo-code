# Sprint 8 — Infrastructure (L12) Design

**Spec source:** File 13 (`13-Infrastructure.md`), roadmap §15.11.
**Precedent:** Sprint 7 committed pure-Go stdlib-only (zero external deps in `go.mod`). Sprint 8 honors the same constraint — the OpenTelemetry SDK, the Sentry Go SDK, and the OTel metrics SDK are replaced by stdlib stubs that implement the same seams behind interfaces, exactly as Sprint 7 replaced SQLite with JSON-file backing and the hosted embedder with a deterministic hash embedder. The real SDKs swap in behind those interfaces in a later hardening sprint.

## Confirmed decisions

1. **Pure-Go stdlib stubs** for traces/metrics/sentry (no `go.opentelemetry.io`, no `getsentry/sentry-go`). Same precedent as Sprint 7.
2. **Infra wraps existing** — `infra.Cost` delegates to the existing `cognitive.Cost` (the ledger + ladder live in L6, §13.10 splits accounting here from policy there); `infra.Secrets` becomes the single registry that `exec/normalizer.go`'s `redact()` delegates to. No logic duplication, no Sprint-3/4 regression.
3. **All 9 tickets end-to-end** (L12-001…009).
4. **Composition-root inject** — `cmd/yolo` injects `infra.Permissions` / `infra.RateLimiter` / `infra.Secrets` / `infra.Cost` into the runtime/exec/cognitive deps via adapters, exactly as L10-006 injected memory. `Infra.Start` runs the root subscriber; the layers receive the slices they need, never the whole aggregate.

## Architecture

`internal/infra` is a **pure observer** (§13.1.2): it imports only `event` + stdlib, subscribes to the root topic `>`, and projects each envelope into span / metric / log / sentry. It never drives the agent except via `cost.*` events (already published by `cognitive.Cost`, which the `infra.Cost` wrapper surfaces unchanged). The export paths are synchronous in-memory (no network) — the stdlib stubs record into in-process buffers, so the "off the hot path" rule (§13.1.2 rule 2) reduces to "record is a map append, not a syscall."

```
event.Bus
   │ subscribe ">"
   ▼
Infra.runRootSubscriber ──► Telemetry.Project (in-memory span store)
                        ──► Metrics.Record    (in-memory counters/histograms)
                        ──► projectLog       (slog DEBUG, redacted)
                        ──► Sentry.Report    (nil stub unless DSN)
```

The other four concerns (Secrets, Permissions, RateLimiter, Cost) are **not** event-driven — they're APIs the layers call directly, wired through the composition root:

```
cmd/yolo (composition root)
   ├─ infra.Start(bus, cfg) → *Infra
   ├─ runtime.Deps.Memory  ← memoryStoreAdapter (Sprint 7)
   ├─ exec.New(... infraSecretsAdapter{i.Secrets} ...)        ← L12-005
   ├─ cognitive.NewCost(... , infraCostAdapter{i.Cost}, ...)  ← L12-008
   ├─ exec dispatch: perms.Check + limiter.Allow before Run   ← L12-006/007 (composition-root adapters)
   └─ infra.Stop(ctx) (LIFO: sentry.flush → metrics → telemetry)
```

## Package layout (File 13 §13.2, stdlib-only)

```
internal/infra/
  doc.go           // package doc + invariant (imports: event + stdlib only)
  config.go        // Config + sub-configs + defaults + env wiring
  telemetry.go     // Telemetry: in-memory span recorder (tracer + span store)
  metrics.go       // Metrics: in-memory counters/histograms (no OTel SDK)
  logger.go        // slog wrapper + event→line projection (DEBUG, redacted)
  sentry.go        // SentryHub: nil-stub unless DSN; CaptureEvent no-op
  secrets.go       // Secrets registry + Redact/RedactAttrs/RedactMap/WouldLeak
  permissions.go   // Permissions: modes + policy table + Check + scoped elevation
  ratelimit.go     // RateLimiter: per-key token bucket, Allow(ctx, key)
  cost.go          // Cost: wraps cognitive.Cost (delegate, no duplicate ledger)
  infra.go         // Infra aggregate + Start/Stop lifecycle + runRootSubscriber
  *_test.go        // per-concern TDD tests
```

## Ticket breakdown (9 tickets, sequential TDD)

### L12-001 — Telemetry: span-per-event projector + drain
- `Telemetry` type: `tracer` (no-op interface impl), `roots sync.Map` (taskID→span), in-memory span store (slice of recorded spans for test assertions).
- `StartRoot(ctx, taskID)` / `EndRoot(taskID, err)` — record task root span (L2 calls these; Infra only stores them).
- `Project(ctx, env)` — one span per event, parented to its task root via `WithLinks`, attributes from event fields (marshal event → `map[string]any` → attributes), status Error if event is error-class.
- `shutdown(ctx)` — drain (no-op for in-memory; returns nil).
- **Exit bar:** a run produces a recorded span tree (root + child per event), queryable by taskID, attributes carry event fields.

### L12-002 — Metrics: counters/histograms (unsampled, §13.4.1)
- `Metrics` type: in-memory counters (`map[name]map[labels]int64`) + histograms (`map[name]map[labels][]int64`); `sync.Mutex`.
- `Record(env)` — increment `events.total{topic}` for every event; topic-specific increments (`tool.calls.total{tool,outcome}`, `verify.verdicts.total{stage,verdict}`, `patch.files.total`, etc. per §13.4.1 table).
- Cardinality discipline (§13.4.3): task IDs / file paths NEVER labels.
- `Snapshot(name, labels) int64` / `Histogram(name, labels) []int64` — test accessors.
- **Exit bar:** after a synthetic event stream, the counters reflect the §13.4.1 table exactly (events.total == N, tool.calls.total{...} == tool calls, etc.).

### L12-003 — Structured `log/slog` logger + event→line projection
- `newLogger(cfg)` — `*slog.Logger`, text or JSON handler (cfg.Log.Format), `AddSource: true`, level from cfg, base attrs `host.id` + `version`.
- `Infra.projectLog(env)` — one `DEBUG` line per event: `attrs = [topic, task, ...env.Fields]`, passed through `Secrets.RedactAttrs` (§13.5.4 second boundary).
- **Exit bar:** a captured log handler sees exactly one DEBUG line per event, with topic+task attrs, and a secret in an event field is masked in the log line.

### L12-004 — Sentry opt-in hub + `error`/`cost.abort` forwarding
- `SentryHub` type: nil when `cfg.Sentry.DSN == ""` (opt-out); every method guarded by nil-receiver.
- `newSentry(cfg, log)` — nil-stub unless DSN; if DSN set, a no-op stub that records captured events in an in-memory slice (no network; the real SDK swaps behind this). Fail-silent if "init" fails.
- `Report(env)` — record `error`/`cost.abort` events as captured events (level, message, tags from env, extra redacted via `Secrets.RedactMap`).
- `Flush(ctx)` — no-op (in-memory); returns nil.
- **Exit bar:** a forced `error` event produces one captured record in the hub's in-memory store; with no DSN the hub is nil and `Report` is a no-op.

### L12-005 — Secrets redaction registry + 3 boundaries
- `Secrets` type: `patterns []SecretPattern` + `sync.RWMutex`; `defaultSecretPatterns()` mirrors §13.7.1 (AWS key, AWS secret, GitHub PAT, GitHub token, PEM, generic kv, JWT).
- `Register(SecretPattern)` — append (runtime pattern, e.g. repo-local config).
- `Redact(string) string`, `RedactAttrs([]any) []any`, `RedactMap(map[string]any) map[string]any`, `WouldLeak(string) bool`.
- **Boundary 1 (exec):** `exec/normalizer.go` `redact()` delegates to an injected `*infra.Secrets` (composition-root adapter `infraSecretsAdapter`); the local regex vars become the default registry's patterns.
- **Boundary 2 (log):** `Infra.projectLog` uses `Secrets.RedactAttrs` (L12-003).
- **Boundary 3 (sentry):** `SentryHub.Report` uses `Secrets.RedactMap` (L12-004).
- **Exit bar:** a secret in tool output is masked in all three sinks (exec output, log line, captured sentry record).

### L12-006 — Permissions: modes + policy table + scoped elevation
- `Permissions` type: `mode PermMode` + `policy []policyRule` (ordered, first match wins).
- Modes: `PermYolo` / `PermAuto` / `PermAsk` / `PermReadOnly` (§13.8.2).
- Actions: `file.read`/`file.write`/`file.delete`/`cmd.exec`/`net.request`/`mcp.tool`.
- `Check(action, resource) (Verdict, reason)` — mode switch + policy lookup; default auto-mode policy table per §13.8.3.
- `Elevate(rule)` — append an allow rule (scoped elevation persists to policy; §13.8.4).
- `isWrite(action)`, `globMatch(pattern, resource)` helpers.
- **Exit bar:** a denied action is blocked; elevating it persists an allow rule so the next identical request hits the fast path.

### L12-007 — Rate limiter: per-key token buckets
- `RateLimiter` type: `buckets map[string]*bucket` + `sync.Mutex`.
- `bucket{tokens, last, rate, burst}` — token-bucket refill.
- `Allow(ctx, key) (wait time.Duration, ok bool)` — consume one token; block up to ctx deadline if empty; first call per key seeds the bucket with `burst`.
- Bucket keys: `llm:<provider>`, `tool:<name>`, `mcp:<server>` (§13.9.2).
- **Exit bar:** a fast loop calling `Allow(ctx, "tool:foo")` past the bucket's burst is throttled (returns a nonzero wait + eventually false on ctx cancel), not errored.

### L12-008 — Cost ledger wrap: snapshot API (delegates to cognitive.Cost)
- `infra.Cost` type: wraps `*cognitive.Cost` (the ledger + ladder stay in L6; §13.10.1 split: accounting in infra, policy in cognitive — here "infra.Cost" is the read/snapshot API the spec wants L2/L6 to call, delegating to the existing controller so there's one ledger, not two).
- `Snapshot(taskID) (dollars float64, loops, tokens int, deadline time.Time, ok bool)` — delegates to `cognitive.Cost` accessors (`Dollars`, `Loops`) + the pricer's token sum.
- `NewTask` / `EndTask` — delegate to `cognitive.Cost.RegisterTask` / nothing (controller manages its own map; `EndTask` is a documented no-op since the controller doesn't expose deletion — spec gap noted in code).
- `AddTokens` / `IncLoop` — the controller already does these (L6-006); `infra.Cost` does NOT re-implement — it surfaces them so a caller holding `infra.Cost` reaches the same ledger.
- **Exit bar:** `infra.Cost.Snapshot` returns the same dollars/loops the cognitive controller accrues (single source of truth); a spend-cap abort flows through the controller's `cost.abort` event unchanged.

### L12-009 — `Infra.Start`/`Stop` lifecycle + LIFO shutdown + root subscriber
- `Infra` aggregate (§13.2.1): `Tel`, `Metrics`, `Log`, `Sentry`, `Secrets`, `Perms`, `Limiter`, `Cost`, `cfg`, `stop []func(ctx) error`.
- `Start(ctx, bus, cfg) (*Infra, error)` — wire all eight concerns, subscribe root topic `>`, launch `runRootSubscriber` goroutine, populate `stop` LIFO slice (sentry.flush → metrics → telemetry, §13.11).
- `runRootSubscriber(ctx, <-chan Envelope)` — `for env := range ch`: `Tel.Project` + `Metrics.Record` + `projectLog` + (error/cost.abort) `Sentry.Report`.
- `Stop(ctx) error` — LIFO run `stop` funcs, return first error (best-effort).
- `Subscribable` interface (bus seam, §13.2.1) — `event.Bus` satisfies it.
- Composition-root wiring: `cmd/yolo` builds `infra.Infra` (or injects its slices into `headlessDeps` for the integration test), wires `infraSecretsAdapter` into exec, `infraCostAdapter` into cognitive deps.
- **Exit bar (Sprint 8):** a headless run with Infra wired exports a recorded trace tree (queryable by taskID), counters matching §13.4.1, one redacted DEBUG log line per event, and captures error/cost.abort events — all from the same event stream with zero agent-logic changes; `Stop` flushes in ≤ a deadline with no goroutine leak.

## TDD discipline (per ticket)

Each ticket follows the established Sprint 0-7 cycle: RED (failing test) → GREEN (minimal code) → mutation check (mutate impl, confirm test fails, restore) → `gofmt -w` + `go vet` + full suite `go test ./... -race` + 3× stability (`-count=1`) → commit + push to `baobao1044/yolo-code` master.

## Spec gaps documented in code

- **OTel/Sentry SDKs replaced by stdlib stubs** — same precedent as Sprint 7's SQLite/JSON and hosted/hash embedder. Real SDKs swap behind `Telemetry`/`SentryHub`/`Metrics` in a hardening sprint; the seam interfaces are the swap point.
- **`infra.Cost` wraps `cognitive.Cost`** — the ledger + degradation ladder live in L6 (Sprint 3); `infra.Cost` is the snapshot/read API the spec wants, delegating to avoid a second ledger (§13.10.1 accounting/policy split honored). `EndTask` is a no-op (controller doesn't expose deletion).
- **`exec/normalizer.go` redaction delegates to `infra.Secrets`** — the local regex patterns move into the registry's defaults; the `redact()` function becomes a thin delegator so there's one pattern set, not two (boundary 1 of 3).
- **Cardinality discipline** — task IDs / file paths never metric labels (§13.4.3); enforced by the `labelAttrs` helper rejecting unbounded keys.
- **Import matrix** — `internal/infra` imports only `event` + stdlib (§13.1.2 lint gate); the `Subscribable` interface keeps the bus seam substitutable.
