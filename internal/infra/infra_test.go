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
	"runtime"
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

// TestInfraStopTerminatesSubscriberWithUnclosedBus pins the leak fix: Stop must
// end the root subscriber even when the caller never closed the bus. It used to
// wait on `done` alone, so an unclosed bus meant the goroutine ran for the life
// of the process and the flushes were skipped entirely — and the caller cannot
// always close first, since event.Bus.Subscribe after Close hands back a channel
// that is never closed at all. Stop now closes `quit`: the goroutine exits, the
// flushes run, and Stop returns nil well inside the deadline.
func TestInfraStopTerminatesSubscriberWithUnclosedBus(t *testing.T) {
	bus := event.New() // intentionally never closed
	i, _ := Start(context.Background(), bus, testConfig())
	calls := 0
	i.stop = []func(context.Context) error{
		func(context.Context) error { calls++; return nil },
	}
	deadline, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := i.Stop(deadline); err != nil {
		t.Fatalf("Stop with an unclosed bus: %v, want nil (subscriber must exit on quit)", err)
	}
	select {
	case <-i.done:
	default:
		t.Error("done is still open after Stop — the subscriber goroutine leaked")
	}
	if calls != 1 {
		t.Errorf("stop funcs ran %d times, want 1 (shutdown must complete, not be skipped)", calls)
	}
	_ = bus.Close()
}

// TestInfraStopFlushesWhenCtxAlreadyExpired pins the other half of "complete
// Stop": an expired ctx reports ctx.Err() but must NOT skip the shutdown funcs.
// A deadline means hurry, and dropping the flush there discards exactly the
// telemetry an operator needs when shutdown is already going badly.
func TestInfraStopFlushesWhenCtxAlreadyExpired(t *testing.T) {
	bus := event.New() // never closed, so the wait takes the ctx branch
	i, _ := Start(context.Background(), bus, testConfig())
	calls := 0
	i.stop = []func(context.Context) error{
		func(context.Context) error { calls++; return nil },
	}
	expired, cancel := context.WithCancel(context.Background())
	cancel() // already done before Stop is entered
	if err := i.Stop(expired); err == nil {
		t.Log("Stop returned nil — the subscriber beat the expired ctx, which is fine")
	}
	if calls != 1 {
		t.Errorf("stop funcs ran %d times, want 1 (an expired ctx must not skip the flushes)", calls)
	}
	_ = bus.Close()
}

// TestInfraStartStopNoGoroutineLeak pins §13.2.1's "no goroutine leak" with an
// actual count: repeated Start/Stop cycles — the shape a retrying or re-execing
// startup path produces — must return the process to its baseline goroutine
// count. One cycle deliberately skips the bus Close so the quit path is the one
// under measurement.
func TestInfraStartStopNoGoroutineLeak(t *testing.T) {
	base := runtime.NumGoroutine()
	for n := 0; n < 5; n++ {
		bus := event.New()
		i, err := Start(context.Background(), bus, testConfig())
		if err != nil {
			t.Fatalf("Start #%d: %v", n, err)
		}
		if n%2 == 0 {
			if err := bus.Close(); err != nil { // the tidy path
				t.Fatalf("close bus #%d: %v", n, err)
			}
		}
		if err := i.Stop(context.Background()); err != nil {
			t.Fatalf("Stop #%d: %v", n, err)
		}
		_ = bus.Close()
	}
	// The bus's own goroutines unwind asynchronously; give them a bounded
	// window rather than a bare sleep, then assert against the baseline.
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > base && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := runtime.NumGoroutine(); got > base {
		t.Errorf("goroutines = %d after 5 Start/Stop cycles, baseline %d — leak", got, base)
	}
}

// TestInfraStartIsSafeWithMissingConfigAndBus pins "no panic when config or env
// is missing": Start must survive a zero Config (no Version, no HostID, no log
// format, no permissions mode, zero rate limits) and must report a nil bus as
// an error rather than panicking mid-startup, before logging is even up.
func TestInfraStartIsSafeWithMissingConfigAndBus(t *testing.T) {
	if _, err := Start(context.Background(), nil, DefaultConfig()); err == nil {
		t.Error("Start with a nil bus returned nil error, want a wiring error")
	}
	bus := event.New()
	i, err := Start(context.Background(), bus, Config{}) // entirely zero-valued
	if err != nil {
		t.Fatalf("Start with a zero Config: %v", err)
	}
	if i.Perms == nil || i.Limiter == nil || i.Cost == nil || i.Secrets == nil {
		t.Error("Start with a zero Config left a concern nil")
	}
	if err := bus.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := i.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	// A nil ctx must not panic either — the root's ctx plumbing is inconsistent
	// across cmd/yolo's three entry points.
	bus2 := event.New()
	i2, err := Start(nil, bus2, Config{}) //nolint:staticcheck // nil ctx is the case under test
	if err != nil {
		t.Fatalf("Start with a nil ctx: %v", err)
	}
	_ = bus2.Close()
	if err := i2.Stop(nil); err != nil { //nolint:staticcheck // nil ctx is the case under test
		t.Fatalf("Stop with a nil ctx: %v", err)
	}
}
