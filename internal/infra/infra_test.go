// Tests for L12-009 — Infra.Start/Stop lifecycle + LIFO shutdown + root
// subscriber (File 13 §13.2.1, §13.11). This is the Sprint 8 exit bar: one
// event stream fans out to span / metric / log / sentry, all from a single
// root-topic subscription, with a clean LIFO shutdown and no goroutine leak.
//
// The tests use a REAL event.Bus (it satisfies Subscribable) and publish a
// realistic event sequence — the same lifecycle an agent run emits — so the
// fan-out is proven against the actual Envelope the bus stamps, not a stub.
// Log capture goes through a package-internal startForTest helper (Start itself
// writes to os.Stderr, the production path); the helper is the only difference.

package infra

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/baobao1044/yolo-code/internal/event"
)

// drainBus publishes all events then closes the bus so the subscriber range
// ends and `done` closes. Returns after the close (the caller waits on done via
// Stop). Used by the fan-out test to stage a realistic agent-run event stream.
func drainBus(t *testing.T, bus *event.Bus, events []event.Event) {
	t.Helper()
	ctx := context.Background()
	for _, e := range events {
		if err := bus.Publish(ctx, e); err != nil {
			t.Fatalf("publish %T: %v", e, err)
		}
	}
	if err := bus.Close(); err != nil {
		t.Fatalf("close bus: %v", err)
	}
}

// TestInfraStartWiresAllConcerns pins §13.2.1: Start constructs the aggregate
// with all eight concerns non-nil and subscribes the root topic so a published
// event is projected. Closing the bus ends the subscriber and Stop returns nil
// (no leak).
func TestInfraStartWiresAllConcerns(t *testing.T) {
	bus := event.New()
	i, err := Start(context.Background(), bus, testConfig())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	for _, c := range []struct {
		name string
		got  any
	}{
		{"Tel", i.Tel}, {"Metrics", i.Metrics}, {"Sentry", i.Sentry},
		{"Secrets", i.Secrets}, {"Perms", i.Perms}, {"Limiter", i.Limiter},
		{"Cost", i.Cost},
	} {
		if c.got == nil {
			t.Errorf("Start left %s nil", c.name)
		}
	}
	// The log projector is unexported (only the subscriber writes to it); assert
	// it was wired by publishing one event and checking a span landed.
	if err := bus.Publish(context.Background(), &event.TaskStartedEvent{Task: "t_1", Session: "s_1", Goal: "g"}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if err := bus.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := i.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if got := len(i.Tel.Spans()); got == 0 {
		t.Error("Tel.Spans is empty — Start didn't subscribe the root topic, or the subscriber didn't fan out")
	}
}

// TestInfraRootSubscriberFansOutToAllConcerns is the Sprint 8 exit bar at the
// infra level: a realistic event sequence (task.started → state.change →
// tool.result → error → cost.abort) fans out to ALL four observers from one
// stream — a span per event (Tel), events.total per topic (Metrics), one DEBUG
// log line per event (Log, redacted), and Sentry captures for error + cost.abort
// only. Zero agent-logic changes: the events are exactly what the runtime
// already publishes.
func TestInfraRootSubscriberFansOutToAllConcerns(t *testing.T) {
	bus := event.New()
	cfg := testConfig()
	cfg.Sentry.DSN = "https://fake@stub/s" // opt Sentry in so Report captures
	var logBuf bytes.Buffer
	i := startForTest(context.Background(), bus, cfg, &logBuf)

	events := []event.Event{
		&event.TaskStartedEvent{Task: "t_1", Session: "s_1", Goal: "demo"},
		&event.StateChangeEvent{Task: "t_1", From: "INIT", To: "PLAN", Why: "go"},
		&event.ToolResultEvent{Task: "t_1", Tool: "ls", Obs: []byte(`{}`)},
		&event.ErrorEvent{Task: "t_1", Layer: "exec", Code: "boom", Msg: "kaboom", Retry: true},
		&event.CostAbortEvent{Task: "t_1", Reason: "spend cap"},
	}
	drainBus(t, bus, events)
	if err := i.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// Span tree: one span per event (5 events → 5 spans), queryable as a slice.
	if got, want := len(i.Tel.Spans()), len(events); got != want {
		t.Errorf("Tel.Spans = %d, want %d (one span per event)", got, want)
	}

	// Metrics: events.total per topic, summing to the event count.
	wantTotal := int64(len(events))
	var gotTotal int64
	for _, e := range events {
		gotTotal += i.Metrics.Counter("events.total", labels{"topic": string(e.Type())})
	}
	if gotTotal != wantTotal {
		t.Errorf("events.total across topics = %d, want %d", gotTotal, wantTotal)
	}
	// tool.calls.total{tool=ls} == 1 (one tool.result).
	if got := i.Metrics.Counter("tool.calls.total", labels{"tool": "ls"}); got != 1 {
		t.Errorf("tool.calls.total{tool=ls} = %d, want 1", got)
	}

	// Log: one DEBUG line per event (5 events → ≥5 lines), each naming its topic.
	logOut := logBuf.String()
	if got, want := strings.Count(logOut, "level=DEBUG"), len(events); got != want {
		t.Errorf("DEBUG log lines = %d, want %d (one per event)\nlog:\n%s", got, want, logOut)
	}
	for _, e := range events {
		if !strings.Contains(logOut, "topic="+string(e.Type())) {
			t.Errorf("log missing topic=%s line\nlog:\n%s", e.Type(), logOut)
		}
	}

	// Sentry: captures error + cost.abort only (the isErrorEvent filter), not
	// the three lifecycle events.
	if got, want := len(i.Sentry.Captured()), 2; got != want {
		t.Errorf("Sentry.Captured = %d, want %d (error + cost.abort only)", got, want)
	}
}

// TestInfraStopRunsLIFO pins §13.11: Stop runs the shutdown funcs in LIFO
// execution order sentry.flush → metrics → telemetry (the buffered exporters
// flush before the tracer drains). A custom stop slice of recording funcs lets
// the test observe the exact order.
func TestInfraStopRunsLIFO(t *testing.T) {
	cfg := testConfig()
	bus := event.New()
	i := startForTest(context.Background(), bus, cfg, nil)

	// Replace the stop slice with recording funcs so the order is observable.
	var order []string
	var mu sync.Mutex
	rec := func(name string) func(context.Context) error {
		return func(context.Context) error {
			mu.Lock()
			order = append(order, name)
			mu.Unlock()
			return nil
		}
	}
	// Append order mirrors Start's: [telemetry, metrics, sentry] so LIFO
	// (reverse) execution is sentry → metrics → telemetry.
	i.stop = []func(context.Context) error{
		rec("telemetry"),
		rec("metrics"),
		rec("sentry.flush"),
	}
	// done is already closed? No — the bus isn't closed, so done is open. Close
	// the bus so Stop's wait-on-done returns promptly.
	if err := bus.Close(); err != nil {
		t.Fatalf("close bus: %v", err)
	}
	if err := i.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	want := []string{"sentry.flush", "metrics", "telemetry"}
	if len(order) != len(want) {
		t.Fatalf("stop order = %v, want %v", order, want)
	}
	for k, name := range want {
		if order[k] != name {
			t.Errorf("stop order[%d] = %q, want %q (full: %v)", k, order[k], name, order)
		}
	}
}

// TestInfraStopIsIdempotent pins the §13.11 idempotency: a second Stop is a
// no-op (the shutdown funcs run exactly once). Uses recording stop funcs.
func TestInfraStopIsIdempotent(t *testing.T) {
	cfg := testConfig()
	bus := event.New()
	i := startForTest(context.Background(), bus, cfg, nil)

	calls := 0
	i.stop = []func(context.Context) error{
		func(context.Context) error { calls++; return nil },
	}
	if err := bus.Close(); err != nil {
		t.Fatalf("close bus: %v", err)
	}
	if err := i.Stop(context.Background()); err != nil {
		t.Fatalf("first Stop: %v", err)
	}
	if err := i.Stop(context.Background()); err != nil {
		t.Fatalf("second Stop: %v", err)
	}
	if calls != 1 {
		t.Errorf("stop func ran %d times, want 1 (Stop must be idempotent)", calls)
	}
}

// TestInfraStopNoLeakAfterBusClose pins the exit-bar's "no goroutine leak":
// after the bus is closed, the subscriber range ends and `done` closes, so Stop
// returns nil promptly. The goroutine has exited before Stop's flushes run.
func TestInfraStopNoLeakAfterBusClose(t *testing.T) {
	bus := event.New()
	i, _ := Start(context.Background(), bus, testConfig())
	if err := bus.Close(); err != nil {
		t.Fatalf("close bus: %v", err)
	}
	// Give the subscriber a moment to observe the close + drain + close done.
	deadline, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := i.Stop(deadline); err != nil {
		t.Fatalf("Stop after bus close: %v (goroutine did not exit — leak)", err)
	}
	// done must be closed (select returns immediately).
	select {
	case <-i.done:
	default:
		t.Error("done is still open after Stop — subscriber goroutine leaked")
	}
}

// TestInfraStopBoundsWaitAtCtxDeadline pins the exit-bar's "≤ a deadline": if
// the bus is NEVER closed, done never closes, so Stop bounds its wait at ctx's
// deadline and returns ctx.Err() WITHOUT running the flushes (the subscriber is
// still live — flushing under it is safe for the stubs, but the leak is the
// caller's fault for not closing the bus).
func TestInfraStopBoundsWaitAtCtxDeadline(t *testing.T) {
	bus := event.New() // intentionally never closed
	i, _ := Start(context.Background(), bus, testConfig())
	calls := 0
	i.stop = []func(context.Context) error{
		func(context.Context) error { calls++; return nil },
	}
	deadline, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	err := i.Stop(deadline)
	if err == nil {
		t.Fatal("Stop returned nil with an unclosed bus, want ctx deadline error")
	}
	if calls != 0 {
		t.Errorf("stop funcs ran %d times, want 0 (must not flush when the subscriber is still live)", calls)
	}
	// Clean up the leaked goroutine so the test process exits cleanly.
	_ = bus.Close()
	_ = i.Stop(context.Background())
}
