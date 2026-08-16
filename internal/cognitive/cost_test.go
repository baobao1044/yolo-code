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

// TestZeroCostConfigCapsNothing is the footgun regression guard, and it checks
// what its name promises: a Cost Controller wired with a zero CostConfig must
// not consider a freshly registered task to be over budget on ANY predicate,
// not just the hard-cap one.
//
// Two separate bugs live here, both the same mistake — a limit of zero compared
// with >= is satisfied at zero, so an unset cap reads as an exhausted one.
// RegisterTask used to stamp `now.Add(MaxTime)` unconditionally, so a zero
// MaxTime produced a deadline of exactly now; and ReflectionAllowed /
// MultiCandidateAllowed used to compare `loops >= MaxLoops` and
// `reflections >= MaxReflections` with no `> 0` guard, so under CostConfig{}
// both answered false from turn zero — reflection and multi-candidate silently
// off for the whole task, nothing logged, while HardCapExceeded cheerfully
// reported that nothing was capped.
func TestZeroCostConfigCapsNothing(t *testing.T) {
	cost := NewCost(CostConfig{}, nil, nil)
	cost.RegisterTask("t_z")

	if over, cap := cost.HardCapExceeded("t_z"); over {
		t.Errorf("HardCapExceeded = true (%q) on a zero config, want false — a zero config caps nothing", cap)
	}
	if !cost.ReflectionAllowed("t_z") {
		t.Error("ReflectionAllowed = false on a zero config, want true — MaxLoops 0 is no loop threshold, not one already reached")
	}
	if !cost.MultiCandidateAllowed("t_z") {
		t.Error("MultiCandidateAllowed = false on a zero config, want true — MaxLoops/MaxReflections 0 are off, not exhausted")
	}
	// No MaxTime means no wall-clock cap, which is a zero deadline rather than
	// a deadline of now. Every reader treats a zero deadline as "unbounded".
	cost.mu.Lock()
	deadline := cost.perTask["t_z"].deadline
	cost.mu.Unlock()
	if !deadline.IsZero() {
		t.Errorf("deadline = %v with MaxTime 0, want the zero time (no wall-clock cap)", deadline)
	}
}

// TestZeroLoopCapsAreOffNotExhausted is the loop half of "unset means off, not
// exhausted", driven rather than asserted at rest: with MaxLoops and
// MaxReflections both zero, no number of loops or reflections may turn the two
// degradation predicates off, because there is no threshold to reach.
func TestZeroLoopCapsAreOffNotExhausted(t *testing.T) {
	cost := NewCost(CostConfig{MaxDollars: 1.0, MaxTime: time.Minute}, nil, nil)
	cost.RegisterTask("t_zl")

	for i := 0; i < 50; i++ {
		cost.IncLoop("t_zl")
		cost.IncReflection("t_zl")
	}
	if !cost.ReflectionAllowed("t_zl") {
		t.Error("ReflectionAllowed = false after 50 loops with MaxLoops 0, want true — an unset threshold never trips")
	}
	if !cost.MultiCandidateAllowed("t_zl") {
		t.Error("MultiCandidateAllowed = false after 50 loops/reflections with both caps 0, want true")
	}
}

// TestRegisterTaskWithoutMaxTimeDoesNotExpireImmediately pins the same fix from
// the reader's side, on a config that caps spend and loops but not wall clock.
// This is the case a zero deadline actually protects: with the old
// unconditional `now.Add(0)` the task registers already past its deadline, so
// ReflectionAllowed and MultiCandidateAllowed both go false before the task has
// done a single thing.
func TestRegisterTaskWithoutMaxTimeDoesNotExpireImmediately(t *testing.T) {
	cost := NewCost(CostConfig{MaxLoops: 6, MaxReflections: 10, MaxDollars: 1.0}, nil, nil)
	cost.RegisterTask("t_nt")

	if !cost.ReflectionAllowed("t_nt") {
		t.Error("ReflectionAllowed = false on a freshly registered task with no MaxTime, want true")
	}
	if !cost.MultiCandidateAllowed("t_nt") {
		t.Error("MultiCandidateAllowed = false on a freshly registered task with no MaxTime, want true")
	}
	if over, cap := cost.HardCapExceeded("t_nt"); over {
		t.Errorf("HardCapExceeded = true (%q) with no MaxTime, want false", cap)
	}
}

// TestLoopCapIsNotAHardCap pins that the two predicates disagree ON PURPOSE
// once loops reach MaxLoops: ReflectionAllowed goes false (§7.6.2's
// reflection_disabled rung — "carry on with less") while HardCapExceeded stays
// false, because a degradation rung is not a stop condition.
//
// Do not "fix" this test by making the two agree. The runtime's PLAN arm calls
// HardCapExceeded on every cycle; wiring it to ReflectionAllowed instead would
// turn MaxLoops (default 6) into a hard six-turn kill switch on every task.
func TestLoopCapIsNotAHardCap(t *testing.T) {
	cfg := CostConfig{MaxLoops: 2, MaxReflections: 10, MaxDollars: 1.0, MaxTime: 10 * time.Minute}
	cost, _ := newCost(t, cfg, 0)

	if !cost.ReflectionAllowed("t_c") {
		t.Fatal("ReflectionAllowed = false before any loop, want true")
	}

	// Drive well past MaxLoops — the point is that no loop count, however
	// large, is a hard cap.
	for i := 0; i < cfg.MaxLoops*3; i++ {
		cost.IncLoop("t_c")
		if over, cap := cost.HardCapExceeded("t_c"); over {
			t.Fatalf("HardCapExceeded = true (%q) after %d loops (MaxLoops %d), want false — MaxLoops is a degradation rung, not a stop condition", cap, i+1, cfg.MaxLoops)
		}
	}

	if cost.ReflectionAllowed("t_c") {
		t.Error("ReflectionAllowed = true past MaxLoops, want false (only-verify mode)")
	}
}

// TestHardCapExceededNamesSpendCap pins the spend hard cap and the reason
// string the runtime surfaces to the user when it aborts.
func TestHardCapExceededNamesSpendCap(t *testing.T) {
	cost, _ := newCost(t, CostConfig{MaxLoops: 6, MaxReflections: 10, MaxDollars: 0.50}, 0.001)

	cost.AddTokens("t_c", 100, 0) // $0.10 — under the cap
	if over, cap := cost.HardCapExceeded("t_c"); over {
		t.Fatalf("HardCapExceeded = true (%q) at $0.10 of a $0.50 cap, want false", cap)
	}

	cost.AddTokens("t_c", 500, 0) // $0.60 total — over
	over, cap := cost.HardCapExceeded("t_c")
	if !over {
		t.Fatalf("HardCapExceeded = false at $%v of a $0.50 cap, want true", cost.Dollars("t_c"))
	}
	if cap != "spend cap" {
		t.Errorf("cap name = %q, want 'spend cap'", cap)
	}
}

// TestHardCapExceededNamesTimeCap pins the wall-clock hard cap. MaxTime is a
// single millisecond so the wait is bounded and the test stays fast; sleeping
// longer than the deadline can only ever overshoot, never undershoot, so this
// does not race.
func TestHardCapExceededNamesTimeCap(t *testing.T) {
	cost := NewCost(CostConfig{MaxLoops: 100, MaxReflections: 10, MaxDollars: 1.0, MaxTime: 1 * time.Millisecond}, nil, nil)
	cost.RegisterTask("t_tc")

	time.Sleep(5 * time.Millisecond) // comfortably past the 1ms deadline

	over, cap := cost.HardCapExceeded("t_tc")
	if !over {
		t.Fatal("HardCapExceeded = false past the deadline, want true")
	}
	if cap != "time cap" {
		t.Errorf("cap name = %q, want 'time cap'", cap)
	}
}

// TestZeroSpendCapIsOffNotTripped pins that MaxDollars 0 means "no spend cap".
// Without the `config.MaxDollars > 0` guard the `dollars >= MaxDollars`
// comparison is true at zero spend, so an unset cap would read as a tripped one.
func TestZeroSpendCapIsOffNotTripped(t *testing.T) {
	cost, _ := newCost(t, CostConfig{MaxLoops: 6, MaxReflections: 10, MaxTime: 10 * time.Minute}, 0.001)

	if over, cap := cost.HardCapExceeded("t_c"); over {
		t.Errorf("HardCapExceeded = true (%q) at zero spend with MaxDollars 0, want false", cap)
	}
	cost.AddTokens("t_c", 1_000_000, 1_000_000) // $2000 — no cap to cross
	if over, cap := cost.HardCapExceeded("t_c"); over {
		t.Errorf("HardCapExceeded = true (%q) at $%v with MaxDollars 0, want false — an unset cap is off, not tripped", cap, cost.Dollars("t_c"))
	}
}

// TestZeroTimeCapIsOffNotTripped is the MaxTime half of the same rule: an unset
// wall-clock cap never expires, however long the task runs.
func TestZeroTimeCapIsOffNotTripped(t *testing.T) {
	cost := NewCost(CostConfig{MaxLoops: 6, MaxReflections: 10, MaxDollars: 1.0}, nil, nil)
	cost.RegisterTask("t_zt")

	time.Sleep(5 * time.Millisecond) // long enough to pass a `now` deadline

	if over, cap := cost.HardCapExceeded("t_zt"); over {
		t.Errorf("HardCapExceeded = true (%q) with MaxTime 0, want false — an unset cap is off, not tripped", cap)
	}
}

// TestHardCapExceededSurvivesLazyRegistration covers the path where the runtime
// never called RegisterTask and task() registers the entry itself. Both paths
// now build the entry through newTaskCost, so the deadline rule is applied once
// and cannot drift; HardCapExceeded is additionally guarded by
// `config.MaxTime > 0`, so this passes belt and braces.
func TestHardCapExceededSurvivesLazyRegistration(t *testing.T) {
	cost := NewCost(CostConfig{}, nil, nil)

	// No RegisterTask — go straight to the predicate.
	if over, cap := cost.HardCapExceeded("t_lazy"); over {
		t.Errorf("HardCapExceeded = true (%q) on a lazily registered task with a zero config, want false", cap)
	}
}

// TestLazyRegistrationDoesNotExpireImmediately is the assertion the belt-and-
// braces guard above cannot make. task() used to stamp `now.Add(MaxTime)`
// unconditionally while RegisterTask had already been fixed to skip it, so the
// two registration paths disagreed: a task that reached the ledger without
// RegisterTask got a deadline of exactly now, and every reader comparing
// against it — ReflectionAllowed, MultiCandidateAllowed — saw it as expired a
// nanosecond later. Reflection and multi-candidate would be silently off for
// the whole task, with no cap configured and nothing logged. Those two readers
// have no `config.MaxTime > 0` guard of their own, so they are the ones that
// actually prove the paths agree.
func TestLazyRegistrationDoesNotExpireImmediately(t *testing.T) {
	// Both rungs above the deadline check are armed with real limits so this
	// test exercises the deadline branch specifically rather than passing
	// because the rungs above it were never consulted. (Zero limits would also
	// pass now that every rung guards on `> 0` — see
	// TestZeroLoopCapsAreOffNotExhausted — but a config with the loop caps
	// actually set is the honest shape for a test about the deadline.)
	// Deliberately no MaxTime — that is the condition under test.
	cost := NewCost(CostConfig{MaxLoops: 3, MaxReflections: 2}, nil, nil)

	// No RegisterTask — task() has to build the entry.
	if !cost.ReflectionAllowed("t_lazy") {
		t.Error("ReflectionAllowed = false on a lazily registered task with no MaxTime, want true — the lazy path stamped a deadline of now")
	}
	if !cost.MultiCandidateAllowed("t_lazy") {
		t.Error("MultiCandidateAllowed = false on a lazily registered task with no MaxTime, want true")
	}
}

// TestRegisterTaskIsIdempotent pins that re-registering a live task preserves
// its ledger. RegisterTask used to assign a fresh entry unconditionally, so a
// second call — from a retry, a resumed task, or any caller that cannot cheaply
// know whether the id is new — zeroed the accrued dollars and the loop and
// reflection counts, and re-stamped the wall-clock deadline. Both hard caps
// started over on a task that had already spent, which is the failure mode caps
// exist to prevent.
func TestRegisterTaskIsIdempotent(t *testing.T) {
	cost, _ := newCost(t, CostConfig{MaxLoops: 6, MaxReflections: 10, MaxDollars: 1.0, MaxTime: 10 * time.Minute}, 0.001)

	cost.AddTokens("t_c", 100, 0) // $0.10
	cost.IncLoop("t_c")
	cost.mu.Lock()
	firstDeadline := cost.perTask["t_c"].deadline
	cost.mu.Unlock()

	cost.RegisterTask("t_c") // second registration for a live id

	if d := cost.Dollars("t_c"); d != 0.10 {
		t.Errorf("Dollars after re-registration = %v, want 0.1 — the ledger was destroyed", d)
	}
	if n := cost.Loops("t_c"); n != 1 {
		t.Errorf("Loops after re-registration = %d, want 1 — the ledger was destroyed", n)
	}
	cost.mu.Lock()
	secondDeadline := cost.perTask["t_c"].deadline
	cost.mu.Unlock()
	if !secondDeadline.Equal(firstDeadline) {
		t.Errorf("deadline moved from %v to %v on re-registration — the wall-clock cap restarted", firstDeadline, secondDeadline)
	}
}

// TestRegisterTaskDoesNotOrphanTaskPointers is the other consequence of the
// destructive re-registration, and the one no ledger read can show. Every
// accessor resolves the entry through task(), so a caller holding a *taskCost
// from before the second RegisterTask keeps writing to the replaced entry while
// every cap reads the fresh one: spend accrues where nothing will ever see it.
func TestRegisterTaskDoesNotOrphanTaskPointers(t *testing.T) {
	cost, _ := newCost(t, CostConfig{MaxDollars: 1.0, MaxTime: 10 * time.Minute}, 0.001)

	before := cost.task("t_c")
	cost.RegisterTask("t_c")
	after := cost.task("t_c")

	if before != after {
		t.Error("task() returned a different *taskCost after re-registration — spend through the old pointer lands on an orphaned entry no cap reads")
	}
}

// TestRegisterTaskStillStartsANewTask pins the half that must not change: a
// first registration for an unknown id still creates the entry and stamps its
// deadline, so the idempotence guard cannot be satisfied by doing nothing.
func TestRegisterTaskStillStartsANewTask(t *testing.T) {
	cost := NewCost(CostConfig{MaxTime: 10 * time.Minute}, nil, nil)
	cost.RegisterTask("t_new")

	cost.mu.Lock()
	tc, ok := cost.perTask["t_new"]
	cost.mu.Unlock()
	if !ok {
		t.Fatal("RegisterTask created no entry for a new id")
	}
	if tc.deadline.IsZero() {
		t.Error("deadline is zero with MaxTime 10m, want a stamped deadline")
	}
}

// assertNoAbort fails if a cost.abort event shows up on ch. The window is short
// but real: the bus delivers asynchronously, and AddTokens publishes inline, so
// anything that was going to be published has been by the time this returns.
func assertNoAbort(t *testing.T, ch <-chan event.Envelope, context string) {
	t.Helper()
	select {
	case env := <-ch:
		t.Errorf("%s published %+v, want no cost.abort — an unset cap is off, not tripped", context, env)
	case <-time.After(50 * time.Millisecond):
	}
}

// TestZeroSpendCapPublishesNoAbortEvent is the assertion
// TestZeroSpendCapIsOffNotTripped does not make. That test reads
// HardCapExceeded, which has its own `config.MaxDollars > 0` guard, so it stays
// green even with AddTokens' guard deleted — the suite had no way to see the
// missing term. What AddTokens does when the cap is unset is publish
// CostAbortEvent{"spend cap"} on the very first priced token, aborting a task
// nobody capped. Assert on the bus, where that is visible.
func TestZeroSpendCapPublishesNoAbortEvent(t *testing.T) {
	cost, bus := newCost(t, CostConfig{MaxLoops: 6, MaxReflections: 10, MaxTime: 10 * time.Minute}, 0.001)
	ch := bus.Subscribe("cost.abort")

	cost.AddTokens("t_c", 1, 0) // the first priced token — $0.001 >= $0 is true
	assertNoAbort(t, ch, "the first priced token with MaxDollars 0")

	cost.AddTokens("t_c", 1_000_000, 1_000_000) // $2000, still no cap to cross
	assertNoAbort(t, ch, "$2000 of spend with MaxDollars 0")
}

// TestPricedSpendCapStillPublishesAbort is the control for the test above: the
// guard must switch the event off only when the cap is unset. Without this, the
// zero-cap assertion could be satisfied by never publishing at all.
func TestPricedSpendCapStillPublishesAbort(t *testing.T) {
	cost, bus := newCost(t, CostConfig{MaxDollars: 0.10, MaxTime: 10 * time.Minute}, 0.001)
	ch := bus.Subscribe("cost.abort")

	cost.AddTokens("t_c", 200, 0) // $0.20 > $0.10
	env := drain(t, ch)
	ce, ok := env.Evt.(*event.CostAbortEvent)
	if !ok {
		t.Fatalf("event type = %T, want *CostAbortEvent", env.Evt)
	}
	if ce.Reason != "spend cap" {
		t.Errorf("abort reason = %q, want 'spend cap'", ce.Reason)
	}
}

// Ensure the session import is used (RegisterTask takes a session.TaskID).
var _ = session.TaskID("")
