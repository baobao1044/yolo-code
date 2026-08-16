// Tests for the cost publisher's honesty contract and its bus manners.
//
// The env-var names are spelled as literals here on purpose: these tests are
// the external contract an operator relies on, so they should break if the
// knob is renamed.

package main

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/baobao1044/yolo-code/internal/event"
)

// costCollector drains a subscription into a slice under a mutex, so a test can
// read what arrived without racing the fan-out.
type costCollector struct {
	mu   sync.Mutex
	got  []event.Event
	seen chan struct{} // buffered; one token per event
}

func newCostCollector(bus *event.Bus, topics ...event.Topic) (*costCollector, func()) {
	c := &costCollector{seen: make(chan struct{}, 4096)}
	ch := bus.Subscribe(topics...)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for env := range ch {
			c.mu.Lock()
			c.got = append(c.got, env.Evt)
			c.mu.Unlock()
			select {
			case c.seen <- struct{}{}:
			default:
			}
		}
	}()
	return c, func() { <-done }
}

// waitFor blocks until n events have arrived or the deadline passes.
func (c *costCollector) waitFor(t *testing.T, n int, d time.Duration) []event.Event {
	t.Helper()
	deadline := time.After(d)
	for {
		c.mu.Lock()
		have := len(c.got)
		c.mu.Unlock()
		if have >= n {
			c.mu.Lock()
			out := append([]event.Event(nil), c.got...)
			c.mu.Unlock()
			return out
		}
		select {
		case <-c.seen:
		case <-deadline:
			c.mu.Lock()
			out := append([]event.Event(nil), c.got...)
			c.mu.Unlock()
			t.Fatalf("timed out after %s waiting for %d events; got %d", d, n, len(out))
			return out
		}
	}
}

func onlyCostEvents(evts []event.Event) []*event.CostIncurredEvent {
	var out []*event.CostIncurredEvent
	for _, e := range evts {
		if ci, ok := e.(*event.CostIncurredEvent); ok {
			out = append(out, ci)
		}
	}
	return out
}

// TestCostPublisherDoesNotFabricateDollars pins the honesty contract: with no
// operator-supplied rates there is nothing this publisher can see from which a
// price can be derived, so cost.incurred must carry $0 and say plainly that the
// call was not priced. The old default table (bash=$0.005, patch=$0.010, …) was
// sourced from nothing and rendered as dollars.
//
// "Nothing this publisher can see" is now the precise claim. Providers that
// honour stream_options.include_usage do report token counts, and the drive
// loop bills them to the ledger — but they arrive there by return value, never
// on the bus, and tool.result carries none of them. So the reason string may
// say this event is unpriced; it may not say the build reports no token usage.
func TestCostPublisherDoesNotFabricateDollars(t *testing.T) {
	t.Setenv("YOLO_COST_RATES", "")

	bus := event.New()
	c, wait := newCostCollector(bus, event.Topic("cost.incurred"))

	pub := newCostPublisher(bus)
	ctx := context.Background()
	pub.Start(ctx)

	if err := bus.Publish(ctx, &event.ToolResultEvent{Task: "t_1", Tool: "bash"}); err != nil {
		t.Fatalf("publish tool.result: %v", err)
	}

	got := onlyCostEvents(c.waitFor(t, 1, 2*time.Second))
	if got[0].Dollars != 0 {
		t.Errorf("Dollars = %v, want 0 — no rates are configured, so any figure here is invented", got[0].Dollars)
	}
	if !strings.Contains(got[0].Reason, "unpriced") {
		t.Errorf("Reason = %q, want it to say the call is unpriced", got[0].Reason)
	}
	// The string an operator reads must not deny a capability the build has.
	for _, stale := range []string{"no provider", "reports token usage"} {
		if strings.Contains(got[0].Reason, stale) {
			t.Errorf("Reason = %q, want it not to claim %q — providers that honour stream_options.include_usage do report usage; it is billed by the ledger, not here", got[0].Reason, stale)
		}
	}

	_ = bus.Close()
	pub.Stop()
	wait()
}

// TestCostPublisherPricesOnlyConfiguredRates pins the other half: when the
// operator does supply rates, those are the numbers reported, "*" is the
// fallback, and the reason marks the figure an estimate rather than a bill.
func TestCostPublisherPricesOnlyConfiguredRates(t *testing.T) {
	t.Setenv("YOLO_COST_RATES", "bash=0.25, *=0.01")

	bus := event.New()
	c, wait := newCostCollector(bus, event.Topic("cost.incurred"))

	pub := newCostPublisher(bus)
	ctx := context.Background()
	pub.Start(ctx)

	for _, tool := range []string{"bash", "read_file"} {
		if err := bus.Publish(ctx, &event.ToolResultEvent{Task: "t_1", Tool: tool}); err != nil {
			t.Fatalf("publish tool.result: %v", err)
		}
	}

	got := onlyCostEvents(c.waitFor(t, 2, 2*time.Second))
	if got[0].Dollars != 0.25 {
		t.Errorf("bash Dollars = %v, want 0.25 (the configured rate)", got[0].Dollars)
	}
	if got[1].Dollars != 0.01 {
		t.Errorf("read_file Dollars = %v, want 0.01 (the configured \"*\" rate)", got[1].Dollars)
	}
	for _, ci := range got {
		if !strings.Contains(ci.Reason, "estimate") {
			t.Errorf("Reason = %q, want it to mark the figure an estimate", ci.Reason)
		}
	}

	_ = bus.Close()
	pub.Stop()
	wait()
}

// TestCostPublisherDoesNotBlockOnItsOwnEmissions is the [4.8b] regression.
// Subscribing to ">" put the publisher's own cost.incurred back into the very
// channel its drain loop reads: each tool.result consumed re-filled the slot it
// freed, so under a sustained producer the 64-slot buffer saturates, the
// producer parks inside the subscriber's gate holding it, and the drain loop
// then blocks forever inside its own Publish waiting for a turn only it can
// grant. Subscribing narrowly to tool.result makes that structurally
// impossible.
func TestCostPublisherDoesNotBlockOnItsOwnEmissions(t *testing.T) {
	t.Setenv("YOLO_COST_RATES", "")

	const n = 300

	bus := event.New()
	c, wait := newCostCollector(bus, event.Topic("cost.incurred"))

	pub := newCostPublisher(bus)
	ctx := context.Background()
	pub.Start(ctx)

	pumped := make(chan error, 1)
	go func() {
		for i := 0; i < n; i++ {
			if err := bus.Publish(ctx, &event.ToolResultEvent{Task: "t_1", Tool: "bash"}); err != nil {
				pumped <- err
				return
			}
		}
		pumped <- nil
	}()

	got := onlyCostEvents(c.waitFor(t, n, 10*time.Second))
	if len(got) != n {
		t.Errorf("cost.incurred count = %d, want %d", len(got), n)
	}

	select {
	case err := <-pumped:
		if err != nil {
			t.Errorf("pump: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Error("producer never finished publishing tool.result — the publisher is wedged")
	}

	if d := bus.Stats().Dropped; d != 0 {
		t.Errorf("bus dropped %d deliveries, want 0", d)
	}

	_ = bus.Close()
	pub.Stop()
	wait()
}

// TestCostPublisherToolCallCeilingWarns pins the one cap this file can honestly
// own: a ceiling on the measured tool-call count. It is off unless the operator
// sets it, it warns exactly once, and it says out loud that nothing was halted
// — the publisher observes the bus, it cannot stop a run.
func TestCostPublisherToolCallCeilingWarns(t *testing.T) {
	t.Setenv("YOLO_COST_RATES", "")
	t.Setenv("YOLO_MAX_TOOL_CALLS", "3")

	bus := event.New()
	c, wait := newCostCollector(bus, event.Topic("error"))

	pub := newCostPublisher(bus)
	ctx := context.Background()
	pub.Start(ctx)

	for i := 0; i < 6; i++ {
		if err := bus.Publish(ctx, &event.ToolResultEvent{Task: "t_1", Tool: "bash"}); err != nil {
			t.Fatalf("publish tool.result: %v", err)
		}
	}

	evts := c.waitFor(t, 1, 2*time.Second)
	warn, ok := evts[0].(*event.ErrorEvent)
	if !ok {
		t.Fatalf("event type = %T, want *ErrorEvent", evts[0])
	}
	if warn.Code != "cost_ceiling_tool_calls" {
		t.Errorf("Code = %q, want cost_ceiling_tool_calls", warn.Code)
	}
	if warn.Retry {
		t.Error("Retry = true, want false — this is a warning, not a retryable failure")
	}
	if !strings.Contains(warn.Msg, "not halted") {
		t.Errorf("Msg = %q, want it to say nothing was halted", warn.Msg)
	}

	// Three more calls crossed the ceiling; only the first may warn.
	_ = bus.Close()
	pub.Stop()
	wait()

	c.mu.Lock()
	n := len(c.got)
	c.mu.Unlock()
	if n != 1 {
		t.Errorf("error events = %d, want exactly 1 (warn once, not per call)", n)
	}
}

// TestCostPublisherCeilingsOffByDefault: an unconfigured run must never warn,
// however many tools it calls. A threshold nobody asked for is as dishonest as
// a price nobody quoted.
func TestCostPublisherCeilingsOffByDefault(t *testing.T) {
	t.Setenv("YOLO_COST_RATES", "")
	t.Setenv("YOLO_MAX_TOOL_CALLS", "")
	t.Setenv("YOLO_MAX_COST", "")

	bus := event.New()
	c, wait := newCostCollector(bus, event.Topic("error"))

	pub := newCostPublisher(bus)
	ctx := context.Background()
	pub.Start(ctx)

	cost, costWait := newCostCollector(bus, event.Topic("cost.incurred"))
	for i := 0; i < 50; i++ {
		if err := bus.Publish(ctx, &event.ToolResultEvent{Task: "t_1", Tool: "bash"}); err != nil {
			t.Fatalf("publish tool.result: %v", err)
		}
	}
	cost.waitFor(t, 50, 5*time.Second)

	_ = bus.Close()
	pub.Stop()
	wait()
	costWait()

	c.mu.Lock()
	n := len(c.got)
	c.mu.Unlock()
	if n != 0 {
		t.Errorf("error events = %d, want 0 — no ceiling was configured", n)
	}
}

// TestCostPublisherStopIsSafe: Stop must not panic on a second call and must
// not hang when Start was never reached (an early error return on a wiring
// path is exactly when Stop runs).
func TestCostPublisherStopIsSafe(t *testing.T) {
	bus := event.New()
	defer func() { _ = bus.Close() }()

	done := make(chan struct{})
	go func() {
		defer close(done)
		neverStarted := newCostPublisher(bus)
		neverStarted.Stop()

		started := newCostPublisher(bus)
		started.Start(context.Background())
		started.Stop()
		started.Stop()
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop hung")
	}
}

func TestCostRatesFromEnvParsing(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want map[string]float64
	}{
		{"empty", "", nil},
		{"single", "bash=0.005", map[string]float64{"bash": 0.005}},
		{"spaces and wildcard", " bash = 0.25 , * = 0.01 ", map[string]float64{"bash": 0.25, "*": 0.01}},
		{"malformed pairs skipped", "bash=nope,grep=0.5,=0.1,oops", map[string]float64{"grep": 0.5}},
		{"negative skipped", "bash=-1,grep=0.5", map[string]float64{"grep": 0.5}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := parseCostRates(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("parseCostRates(%q) = %v, want %v", tc.in, got, tc.want)
			}
			for k, v := range tc.want {
				if got[k] != v {
					t.Errorf("rate[%q] = %v, want %v", k, got[k], v)
				}
			}
		})
	}
}
