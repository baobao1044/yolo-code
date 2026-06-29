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
// composition root to inject into layer ports (Secrets → exec, Perms/Limiter →
// exec dispatch, Cost → cognitive) and by tests to assert the observability
// exit bar (Tel spans, Metrics counters, Sentry captures). `log` is unexported:
// only the root subscriber writes to it (os.Stderr in prod), no layer reads it.
type Infra struct {
	// Observers (driven by runRootSubscriber):
	Tel     *Telemetry
	Metrics *Metrics
	log     *logProjector
	Sentry  *SentryHub

	// APIs (injected into layer ports by the composition root):
	Secrets *Secrets
	Perms   *Permissions
	Limiter *RateLimiter
	Cost    *Cost

	cfg  Config
	stop []func(context.Context) error // LIFO: appended in startup-reverse so reverse-iteration runs sentry.flush → metrics → telemetry (§13.11)
	done chan struct{}                 // closes when runRootSubscriber exits (bus Close ends the range)

	stopOnce sync.Once
	stopErr  error
}

// Start wires all eight concerns, subscribes the root topic ">", launches
// runRootSubscriber, and populates the LIFO stop slice. Returns the aggregate;
// the caller owns the bus and must close it (which ends the subscriber range)
// before calling Stop. The single *Secrets registry is wired as the redactor
// for both the log line (§13.5.4) and the Sentry event (§13.6.3) — one registry,
// two boundaries (the L12-005 tie-together). A nil ledger is passed to NewCost
// here; the composition root injects the real cognitive.Cost adapter alongside
// the other ports (Cost is nil-safe per L12-008).
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
func startWithLog(ctx context.Context, bus Subscribable, cfg Config, logW io.Writer) (*Infra, error) {
	i := &Infra{
		Tel:     newTelemetry(cfg),
		Metrics: newMetrics(cfg),
		log:     newLogProjector(cfg, logW),
		Sentry:  newSentry(cfg), // nil if no DSN (opt-in, §13.6.1)
		Secrets: NewSecrets(),
		Perms:   newPermissions(cfg.Permissions),
		Limiter: newRateLimiter(cfg),
		Cost:    NewCost(cfg, nil), // ledger injected by the composition root; nil-safe (L12-008)
		cfg:     cfg,
		done:    make(chan struct{}),
	}
	// One *Secrets registry satisfies both redaction boundaries (L12-005).
	// *Secrets implements logRedactor (RedactAttrs) + sentryRedactor (RedactMap).
	i.log.redactor = i.Secrets
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
func (i *Infra) runRootSubscriber(ctx context.Context, ch <-chan event.Envelope) {
	defer close(i.done)
	for env := range ch {
		i.Tel.Project(ctx, env)
		i.Metrics.Record(env)
		i.log.projectLog(env)
		if i.Sentry != nil && isErrorEvent(env.Evt.Type()) {
			i.Sentry.Report(env)
		}
	}
}

// Stop ends the aggregate: it waits for runRootSubscriber to exit (the caller
// closes the bus, which ends the range → done closes), then runs the shutdown
// funcs in LIFO order (§13.11: sentry.flush → metrics → telemetry), returning
// the first error but running every func (best-effort — one exporter's flush
// failure must not skip another's). Idempotent (sync.Once): a second Stop is a
// no-op. Bounded by ctx: if the bus was never closed, done never closes, so
// Stop returns ctx.Err() WITHOUT flushing (the subscriber is still live; the
// leak is the caller's fault for not closing the bus — the exit bar requires a
// closed bus).
func (i *Infra) Stop(ctx context.Context) error {
	i.stopOnce.Do(func() {
		// Wait for the subscriber goroutine to exit. The caller closes the bus
		// to end the range; without that, done never closes and Stop bounds at
		// ctx's deadline (returns ctx.Err(), skips the flushes).
		select {
		case <-i.done:
		case <-ctx.Done():
			i.stopErr = ctx.Err()
			return
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
