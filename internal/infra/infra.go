// L12-009 — Infra.Start/Stop lifecycle + LIFO shutdown + root subscriber
// (File 13 §13.2.1, §13.11). This is the Sprint 8 exit bar: one event stream
// (the root topic ">") fans out to span / metric / log / sentry, all from a
// single subscriber goroutine, with a clean LIFO shutdown and no goroutine
// leak. Zero agent-logic changes — the runtime already publishes the events;
// Infra only observes them.
//
// The aggregate owns the eight L12 concerns. The four observers (Tel, Metrics,
// Log, Sentry) are driven by the root subscriber; the four APIs (Secrets,
// Perms, Limiter, Cost) are wired into the layer ports by the composition root
// (cmd/yolo, L12-009 wiring) — the layers receive only the slice they need,
// never the whole aggregate (§13.1.2). Infra imports only event + stdlib
// (import matrix, §13.1.2 lint gate); the Subscribable interface keeps the bus
// seam substitutable so a test or a hardening sprint can swap the bus.

package infra

import (
	"context"
	"errors"
	"io"
	"sync"

	"github.com/baobao1044/yolo-code/internal/event"
)

// Subscribable is the bus seam (§13.2.1): the minimal surface Start needs —
// just Subscribe. event.Bus satisfies it. Kept an interface so infra never
// depends on the concrete *event.Bus; a test can substitute a fake, and a
// hardening sprint can swap the transport behind it. Mirrors the seam-first
// discipline of logRedactor / sentryRedactor / CostLedger.
type Subscribable interface {
	Subscribe(topics ...event.Topic) <-chan event.Envelope
}

// Infra is the L12 aggregate (§13.2.1). The exported concerns are read by the
// composition root to inject into layer ports (Secrets → exec, Cost →
// cognitive) and by tests to assert the observability exit bar (Tel spans,
// Metrics counters, Sentry captures). `log` is unexported: only the root
// subscriber writes to it (os.Stderr in prod), no layer reads it.
//
// This comment used to also say "Perms/Limiter → exec dispatch". It does not
// happen. Both are constructed on every run by Start below and neither is read
// by any production line in the module; see the DEAD SEAM notes on the fields.
// The sentence is worth remembering as a specimen: a doc comment describing the
// wiring the design called for rather than the wiring that exists is exactly as
// misleading as a stub that returns success.
type Infra struct {
	// Observers (driven by runRootSubscriber):
	Tel     *Telemetry
	Metrics *Metrics
	log     *logProjector
	Sentry  *SentryHub

	// APIs (injected into layer ports by the composition root):
	Secrets *Secrets
	// DEAD SEAM. Permissions.Check has no caller outside permissions_test.go.
	// The gate is built, and headless.go deliberately sets Permissions.Root
	// ("absolute; empty would deny every real write"), but nothing in the module
	// ever asks it whether a write is allowed. The permission model actually in
	// force is exec.Engine.NeedsApproval plus the sandbox path resolver, neither
	// of which consults this. Two overlapping models, one live. Pinned in
	// cmd/yolo/deadseam_test.go.
	Perms *Permissions
	// DEAD SEAM. RateLimiter.Allow has no caller outside ratelimit_test.go: no
	// tool call and no LLM request is rate limited. Pinned in
	// cmd/yolo/deadseam_test.go.
	Limiter *RateLimiter
	Cost    *Cost

	cfg  Config
	stop []func(context.Context) error // LIFO: appended in startup-reverse so reverse-iteration runs sentry.flush → metrics → telemetry (§13.11)
	done chan struct{}                 // closes when runRootSubscriber exits (bus Close ends the range)
	quit chan struct{}                 // closed by Stop so the subscriber exits even if the caller never closed the bus

	stopOnce sync.Once
	stopErr  error
}

// errNoBus is returned by Start when bus is nil. Start is called from the
// composition root during process startup, where a nil dependency is a wiring
// mistake to surface as an error, not a panic that takes the process down
// before logging is up.
var errNoBus = errors.New("infra.Start: nil bus")

// Start wires all eight concerns, subscribes the root topic ">", launches
// runRootSubscriber, and populates the LIFO stop slice. Returns the aggregate.
// The caller owns the bus and should close it before Stop so the subscriber
// drains every queued event; Stop no longer depends on that to terminate.
//
// Start is safe to call from a real startup path: a nil bus is an error rather
// than a panic, a nil ctx is treated as Background, and a zero Config yields a
// working aggregate. Each call returns an independent aggregate with its own
// subscriber goroutine, so repeated calls neither share nor corrupt state —
// but each one must be Stopped, and the composition root should call Start
// exactly once. The shared *Secrets registry (DefaultRedactor) is the single
// deliberate exception: one registry backs the log line (§13.5.4), the Sentry
// event (§13.6.3) and — via Infra.Secrets — the exec output boundary (§8.4.5).
// A nil ledger is passed to NewCost here; the composition root injects the real
// cognitive.Cost adapter alongside the other ports (Cost is nil-safe per
// L12-008).
func Start(ctx context.Context, bus Subscribable, cfg Config) (*Infra, error) {
	return startWithLog(ctx, bus, cfg, nil /* os.Stderr */)
}

// startForTest is the package-internal Start variant that writes log lines to
// `logW` (a buffer in tests) so the fan-out test can assert the DEBUG lines.
// Production Start passes nil → os.Stderr (logger.go's default). Identical
// wiring otherwise; this is the ONLY divergence.
func startForTest(ctx context.Context, bus Subscribable, cfg Config, logW io.Writer) *Infra {
	i, _ := startWithLog(ctx, bus, cfg, logW)
	return i
}

// startWithLog is the shared core of Start / startForTest. logW may be nil
// (→ os.Stderr). The indirection keeps Start's public signature clean (no
// writer parameter) while letting tests capture log output.
//
// Every concern below is constructed from cfg's zero value without panicking,
// so a caller that has not built a Config yet (or whose env lookups came back
// empty) still gets a working aggregate: DefaultConfig is a convenience, not a
// precondition.
func startWithLog(ctx context.Context, bus Subscribable, cfg Config, logW io.Writer) (*Infra, error) {
	if bus == nil {
		return nil, errNoBus
	}
	if ctx == nil {
		ctx = context.Background()
	}
	i := &Infra{
		Tel:     newTelemetry(cfg),
		Metrics: newMetrics(cfg),
		log:     newLogProjector(cfg, logW),
		Sentry:  newSentry(cfg),    // nil if no DSN (opt-in, §13.6.1)
		Secrets: DefaultRedactor(), // the one registry every boundary shares (L12-005)
		Perms:   newPermissions(cfg.Permissions),
		Limiter: newRateLimiter(cfg),
		Cost:    NewCost(cfg, nil), // ledger injected by the composition root; nil-safe (L12-008)
		cfg:     cfg,
		done:    make(chan struct{}),
		quit:    make(chan struct{}),
	}
	// One *Secrets registry satisfies every redaction boundary (L12-005).
	// *Secrets implements logRedactor (RedactAttrs), sentryRedactor (RedactMap)
	// and spanRedactor (both, plus Redact). Telemetry was the one observer in
	// this fan-out with nothing wired, so a Register from the composition root
	// covered the log and Sentry while the trace kept the raw value.
	i.log.redactor = i.Secrets
	i.Tel.redactor = i.Secrets
	if i.Sentry != nil {
		i.Sentry.redactor = i.Secrets
	}
	// LIFO stop slice: append in startup-reverse so reverse-iteration runs the
	// §13.11 order sentry.flush → metrics → telemetry (the buffered exporters
	// flush before the tracer drains). Each is nil-safe (Sentry may be nil).
	i.stop = []func(context.Context) error{
		i.Tel.shutdown,
		i.Metrics.shutdown,
		i.Sentry.Flush, // nil-hub Flush is a no-op (sentry.go)
	}

	// Subscribe the root wildcard BEFORE any publisher can miss an event, then
	// launch the single fan-out goroutine. The bus's Close ends the range → done
	// closes → Stop's wait returns.
	ch := bus.Subscribe(event.Topic(">"))
	go i.runRootSubscriber(ctx, ch)
	return i, nil
}

// runRootSubscriber is the single event-stream fan-out: every envelope projects
// into a span (Tel.Project), a metric (Metrics.Record), a redacted DEBUG log
// line (log.projectLog), and — for error/cost.abort only (isErrorEvent) — a
// Sentry capture. Exits when the bus closes the subscriber channel (Close →
// range ends), then closes done so Stop can wait for the goroutine's exit.
// Each concern is safe for concurrent use (mutex-guarded), so a slow concern
// can't corrupt another's read.
//
// It also exits on `quit` (closed by Stop). Ranging on the channel alone was a
// guaranteed leak whenever the caller did not close the bus first — and one
// case where the caller cannot: event.Bus.Subscribe after Close returns a
// channel that is never closed, so the goroutine would have hung for the life
// of the process. Stop must be able to end this goroutine on its own.
func (i *Infra) runRootSubscriber(ctx context.Context, ch <-chan event.Envelope) {
	defer close(i.done)
	for {
		// Queued events take priority over the quit signal. A plain two-case
		// select picks uniformly among ready cases, and the ordinary shutdown
		// (close the bus, then Stop) leaves both ready at once — so an unbiased
		// select would drop the tail of the run's telemetry at random.
		select {
		case env, ok := <-ch:
			if !ok {
				return // bus closed and drained: the normal path
			}
			i.project(ctx, env)
			continue
		default:
		}
		select {
		case env, ok := <-ch:
			if !ok {
				return
			}
			i.project(ctx, env)
		case <-i.quit:
			return // Stop with an unclosed, idle bus: nothing left to drain
		}
	}
}

// project fans one envelope out to the four observers. Split out of
// runRootSubscriber so the drain-priority select doesn't duplicate it.
func (i *Infra) project(ctx context.Context, env event.Envelope) {
	i.Tel.Project(ctx, env)
	i.Metrics.Record(env)
	i.log.projectLog(env)
	if i.Sentry != nil && isErrorEvent(env.Evt.Type()) {
		i.Sentry.Report(env)
	}
}

// Stop ends the aggregate: it signals the root subscriber to exit, waits for
// it, then runs the shutdown funcs in LIFO order (§13.11: sentry.flush →
// metrics → telemetry), returning the first error but running every func
// (best-effort — one exporter's flush failure must not skip another's).
// Idempotent (sync.Once): a second Stop is a no-op.
//
// Closing the bus first is still the right thing to do — it lets the subscriber
// drain every queued event before exiting. But it is no longer required for
// termination: Stop closes `quit`, so the goroutine ends either way and the
// aggregate never outlives its Stop. If ctx expires while waiting, Stop records
// ctx.Err() and STILL runs the flushes — a deadline means "hurry up", and
// skipping the flush there drops exactly the telemetry an operator wants when
// shutdown is going badly.
func (i *Infra) Stop(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	i.stopOnce.Do(func() {
		// Tell the subscriber to exit even if the caller never closed the bus.
		close(i.quit)
		select {
		case <-i.done:
		case <-ctx.Done():
			i.stopErr = ctx.Err()
		}
		// LIFO: iterate the stop slice in reverse so the last-appended runs
		// first. The slice is appended [telemetry, metrics, sentry], so reverse
		// execution is sentry.flush → metrics → telemetry (§13.11).
		for k := len(i.stop) - 1; k >= 0; k-- {
			if err := i.stop[k](ctx); err != nil && i.stopErr == nil {
				i.stopErr = err
			}
		}
	})
	return i.stopErr
}
