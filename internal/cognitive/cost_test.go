package cognitive

import (
	"testing"
	"time"

	"github.com/baobao1044/yolo-code/internal/event"
	"github.com/baobao1044/yolo-code/internal/session"
)

// newCost builds a Cost Controller over a real bus + a pricer, returning both
// so the test can inspect published cost.* events.
func newCost(t *testing.T, cfg CostConfig, perToken float64) (*Cost, *event.Bus) {
	t.Helper()
	bus := event.New()
	t.Cleanup(func() { _ = bus.Close() })
	c := NewCost(cfg, FixedPricer{PerToken: perToken}, bus)
	c.RegisterTask("t_c")
	return c, bus
}

// TestLoopCapDisablesReflection is the L6-006 exit criterion (loop side): after
// MaxLoops reflection loops, the Cost Controller publishes cost.degraded
// {stage: reflection_disabled} and ReflectionAllowed returns false (only-
// verify mode, File 07 §7.6.2).
func TestLoopCapDisablesReflection(t *testing.T) {
	cfg := CostConfig{MaxLoops: 3, MaxReflections: 10, MaxDollars: 1.0, MaxTime: 10 * time.Minute}
	cost, bus := newCost(t, cfg, 0)
	ch := bus.Subscribe("cost.degraded")

	for i := 0; i < cfg.MaxLoops; i++ {
		cost.IncLoop("t_c")
	}

	// The degrade fires exactly when loops reach MaxLoops (the 3rd call).
	env := drain(t, ch)
	de, ok := env.Evt.(*event.CostDegradedEvent)
	if !ok {
		t.Fatalf("event type = %T, want *CostDegradedEvent", env.Evt)
	}
	if de.Stage != "reflection_disabled" {
		t.Errorf("degrade stage = %q, want reflection_disabled", de.Stage)
	}
	if de.Task != event.TaskID("t_c") {
		t.Errorf("degrade task = %q, want %q", de.Task, "t_c")
	}
	// Reflection is now disallowed (only-verify mode).
	if cost.ReflectionAllowed("t_c") {
		t.Error("ReflectionAllowed = true after MaxLoops, want false (only-verify mode)")
	}
}

// TestReflectionCapAutosubmits pins the hard cap on reflection calls (§7.6.4):
// when reflections reach MaxReflections, cost.degraded {stage: autosubmit}
// fires so the runtime submits the best state for manual review.
func TestReflectionCapAutosubmits(t *testing.T) {
	cfg := CostConfig{MaxLoops: 100, MaxReflections: 5, MaxDollars: 1.0, MaxTime: 10 * time.Minute}
	cost, bus := newCost(t, cfg, 0)
	ch := bus.Subscribe("cost.degraded")

	for i := 0; i < cfg.MaxReflections; i++ {
		cost.IncReflection("t_c")
	}

	env := drain(t, ch)
	de, _ := env.Evt.(*event.CostDegradedEvent)
	if de.Stage != "autosubmit" {
		t.Errorf("degrade stage = %q, want autosubmit", de.Stage)
	}
}

// TestSpendCapAborts is the L6-006 exit criterion (spend side): when accrued
// dollars cross MaxDollars, the Cost Controller publishes cost.abort
// {reason: spend cap} (File 07 §7.6.2 hard abort).
func TestSpendCapAborts(t *testing.T) {
	cfg := CostConfig{MaxLoops: 6, MaxReflections: 10, MaxDollars: 0.50, MaxTime: 10 * time.Minute}
	cost, bus := newCost(t, cfg, 0.001) // $0.001/token → 500 tokens = $0.50 cap
	ch := bus.Subscribe("cost.abort")

	// Accrue tokens until the spend cap is crossed.
	cost.AddTokens("t_c", 600, 0) // $0.60 > $0.50

	env := drain(t, ch)
	ce, ok := env.Evt.(*event.CostAbortEvent)
	if !ok {
		t.Fatalf("event type = %T, want *CostAbortEvent", env.Evt)
	}
	if ce.Reason != "spend cap" {
		t.Errorf("abort reason = %q, want 'spend cap'", ce.Reason)
	}
	// After the spend cap, reflection is disallowed (the hard cap).
	if cost.ReflectionAllowed("t_c") {
		t.Error("ReflectionAllowed = true after spend cap, want false")
	}
}

// TestSpendCapFiresOnce pins that crossing the spend cap publishes the abort
// once, not per subsequent AddTokens call (no event storm).
func TestSpendCapFiresOnce(t *testing.T) {
	cfg := CostConfig{MaxDollars: 0.10, MaxTime: 10 * time.Minute}
	cost, bus := newCost(t, cfg, 0.01) // 10 tokens = $0.10 cap
	ch := bus.Subscribe("cost.abort")

	cost.AddTokens("t_c", 20, 0) // cross
	cost.AddTokens("t_c", 30, 0) // more — should NOT re-publish

	count := 0
	for {
		select {
		case <-ch:
			count++
		default:
			goto done
		}
	}
done:
	if count != 1 {
		t.Errorf("cost.abort published %d times, want 1 (no event storm)", count)
	}
}

// TestReflectionAllowedBeforeCaps pins that reflection is allowed while under
// all caps — the common path during normal execution.
func TestReflectionAllowedBeforeCaps(t *testing.T) {
	cfg := CostConfig{MaxLoops: 6, MaxReflections: 10, MaxDollars: 1.0, MaxTime: 10 * time.Minute}
	cost, _ := newCost(t, cfg, 0.0001)
	cost.AddTokens("t_c", 100, 0) // $0.01, well under cap
	if !cost.ReflectionAllowed("t_c") {
		t.Error("ReflectionAllowed = false under all caps, want true (normal path)")
	}
}

// TestTimeCapDisablesReflection pins the wall-clock hard cap (§7.6.2): once the
// deadline passes, ReflectionAllowed returns false.
func TestTimeCapDisablesReflection(t *testing.T) {
	bus := event.New()
	t.Cleanup(func() { _ = bus.Close() })
	cost := NewCost(CostConfig{MaxLoops: 100, MaxReflections: 10, MaxDollars: 1.0, MaxTime: 1 * time.Nanosecond}, nil, bus)
	cost.RegisterTask("t_t")
	time.Sleep(2 * time.Millisecond) // past the 1ns deadline

	if cost.ReflectionAllowed("t_t") {
		t.Error("ReflectionAllowed = true past the deadline, want false (time cap)")
	}
}

// TestIncToolCallRecordsLedger pins the per-task tool-call tally (§7.6.1).
func TestIncToolCallRecordsLedger(t *testing.T) {
	cost, _ := newCost(t, DefaultCostConfig(), 0)
	cost.IncToolCall("t_c")
	cost.IncToolCall("t_c")
	cost.mu.Lock()
	tc := cost.perTask["t_c"]
	n := tc.toolCalls
	cost.mu.Unlock()
	if n != 2 {
		t.Errorf("toolCalls = %d, want 2", n)
	}
}

// TestNilPricerNoAbort pins that a nil pricer (default) accrues no dollars and
// never fires the spend cap — safe for tests that don't model spend.
func TestNilPricerNoAbort(t *testing.T) {
	bus := event.New()
	t.Cleanup(func() { _ = bus.Close() })
	cost := NewCost(CostConfig{MaxDollars: 0.001, MaxTime: 10 * time.Minute}, nil, bus)
	cost.RegisterTask("t_n")
	ch := bus.Subscribe("cost.abort")
	cost.AddTokens("t_n", 1_000_000, 0) // zero pricer → $0
	select {
	case env := <-ch:
		t.Errorf("zero pricer fired cost.abort: %+v", env)
	case <-time.After(20 * time.Millisecond):
	}
}

// TestAddTokensAccruesAndPrices pins the ledger's token + dollar accounting.
func TestAddTokensAccruesAndPrices(t *testing.T) {
	cost, _ := newCost(t, CostConfig{MaxDollars: 100, MaxTime: 10 * time.Minute}, 0.001)
	cost.AddTokens("t_c", 1000, 500) // $1.50
	if d := cost.Dollars("t_c"); d != 1.5 {
		t.Errorf("Dollars = %v, want 1.5", d)
	}
}

// Ensure the session import is used (RegisterTask takes a session.TaskID).
var _ = session.TaskID("")
