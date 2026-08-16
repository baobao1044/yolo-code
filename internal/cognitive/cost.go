// The Cost Controller (File 07 §7.6). It tracks per-task loops, reflections,
// tool calls, tokens, dollars, and wall-clock time, and runs the
// auto-degradation ladder: after MaxLoops reflection loops the Core disables
// reflection (only-verify mode) and further failure autosubmits; hard caps
// (MaxDollars, MaxTime) abort the task and surface to the user. The retry cap
// (Task.RetryMax) is sourced from CostConfig.MaxReflections at task start
// (§7.6.4), so the Session Manager's retry counter and the Cost Controller's
// reflection cap are the same number seen from two layers.
//
// Sprint 3 (L6-006) implements the ledger + ladder against a deterministic
// pricer (the real provider's pricing is wired in Sprint 4). The dollars truth
// comes from AddTokens' pricer; the spend cap fires when it crosses MaxDollars.

package cognitive

import (
	"context"
	"sync"
	"time"

	"github.com/baobao1044/yolo-code/internal/event"
	"github.com/baobao1044/yolo-code/internal/session"
)

// Pricer turns a token count into dollars. The real one (per-model pricing,
// Sprint 4) is injected; Sprint 3 uses a fixed deterministic rate so the
// ledger is testable without a real bill.
type Pricer interface {
	Price(tokensIn, tokensOut int) float64
}

// FixedPricer prices both in and out tokens at the same fixed rate per token.
// It is deterministic (S5): same tokens → same dollars, every run.
type FixedPricer struct {
	PerToken float64
}

// Price returns the dollar cost of a token pair.
func (p FixedPricer) Price(in, out int) float64 {
	return float64(in+out) * p.PerToken
}

// CostConfig bounds a task's spend, loops, and time (File 07 §7.6.1). MaxLoops
// is the reflection-loop threshold before degradation; MaxReflections the hard
// cap on reflection calls (sourced into Task.RetryMax); MaxDollars the spend
// cap; MaxTime the wall-clock cap.
type CostConfig struct {
	MaxLoops       int
	MaxReflections int
	MaxDollars     float64
	MaxTime        time.Duration
}

// DefaultCostConfig is the §7.6 default: 6 reflection loops → disable
// reflection, 10 reflection calls hard cap, a generous spend cap (the real
// default is tuned per provider in Sprint 4), and a 10-minute wall-clock cap.
func DefaultCostConfig() CostConfig {
	return CostConfig{
		MaxLoops:       6,
		MaxReflections: 10,
		MaxDollars:     1.0,
		MaxTime:        10 * time.Minute,
	}
}

// taskCost is the per-task ledger entry (File 07 §7.6.1).
type taskCost struct {
	loops       int
	reflections int
	toolCalls   int
	tokensIn    int
	tokensOut   int
	dollars     float64
	deadline    time.Time
	aborted     bool // spend cap fired once; guards against re-publishing
	loopDegrade bool // reflection_disabled published once
	reflDegrade bool // autosubmit published once
}

// Cost is the Cost Controller (File 07 §7.6). It is safe for concurrent use —
// the runtime drives a task on one goroutine (I1) but the bus publish and
// token accounting can overlap with TUI subscriber reads, so the ledger is
// mutex-guarded.
type Cost struct {
	mu      sync.Mutex
	perTask map[session.TaskID]*taskCost
	config  CostConfig
	pricer  Pricer
	bus     *event.Bus
}

// NewCost constructs a Cost Controller. A nil bus makes degradation/abort
// publishes no-ops so unit tests can inspect the ledger directly. A nil
// pricer uses a zero FixedPricer (no dollars accrue — tests that need spend
// pass their own).
func NewCost(config CostConfig, pricer Pricer, bus *event.Bus) *Cost {
	c := &Cost{perTask: map[session.TaskID]*taskCost{}, config: config, pricer: pricer, bus: bus}
	if c.pricer == nil {
		c.pricer = FixedPricer{PerToken: 0}
	}
	return c
}

// RegisterTask starts the ledger for a task and sets its deadline from MaxTime
// (§7.6.1). Call at task start so the spend/time caps are measured from when
// the task began, not first token.
// A MaxTime of zero means "no wall-clock cap" and leaves the deadline zero.
// Adding it blindly used to stamp a deadline of exactly now, so every reader
// that compares against it saw the task as already expired the instant it
// registered — a zero CostConfig killed tasks on their first turn.
//
// Idempotent: a second call for an id that already has an entry keeps that
// entry. Replacing it zeroed the task's dollars and its loop/reflection counts
// and re-stamped the deadline, so both hard caps started over on a task that
// had already spent; and spend still accruing through a *taskCost pointer a
// caller took from an earlier task() call landed on the orphaned entry, where
// no cap would ever see it. infra.Cost.NewTask guards its own single
// registration (internal/infra/cost.go), which kept this a trap rather than a
// live abort — but the ledger is where the guarantee belongs.
func (c *Cost) RegisterTask(id session.TaskID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.perTask[id]; ok {
		return
	}
	c.perTask[id] = c.newTaskCost()
}

// newTaskCost builds a fresh ledger entry with the deadline rule applied once,
// so the two registration paths cannot stamp different deadlines. Both callers
// reach it only for an id with no entry yet — RegisterTask returns early on a
// live id, task() builds one only on a miss — so no accrued ledger is ever
// replaced by a fresh one. Caller holds the mutex; c.config is read-only after
// New.
func (c *Cost) newTaskCost() *taskCost {
	tc := &taskCost{}
	if c.config.MaxTime > 0 {
		tc.deadline = time.Now().Add(c.config.MaxTime)
	}
	return tc
}

// task returns the ledger entry for a task, registering one lazily if missing
// (a task whose Start wasn't called). Caller must NOT hold the mutex.
func (c *Cost) task(id session.TaskID) *taskCost {
	c.mu.Lock()
	defer c.mu.Unlock()
	tc, ok := c.perTask[id]
	if !ok {
		tc = c.newTaskCost()
		c.perTask[id] = tc
	}
	return tc
}

// IncLoop records a reflection loop and runs the degradation ladder's first
// rung (File 07 §7.6.2): when loops reach MaxLoops, publish cost.degraded
// {stage: reflection_disabled} once (the loopDegrade flag guards subsequent
// calls). Subsequent verify failures go to only-verify mode (ReflectionAllowed
// returns false).
func (c *Cost) IncLoop(id session.TaskID) {
	tc := c.task(id)
	c.mu.Lock()
	tc.loops++
	crossed := !tc.loopDegrade && tc.loops == c.config.MaxLoops && c.config.MaxLoops > 0
	if crossed {
		tc.loopDegrade = true
	}
	c.mu.Unlock()
	if crossed {
		c.publish(context.Background(), &event.CostDegradedEvent{Task: event.TaskID(id), Stage: "reflection_disabled"})
	}
}

// IncReflection records a reflection call. When reflections reach
// MaxReflections, the hard cap fires (§7.6.4): publish cost.degraded
// {stage: autosubmit} once (the reflDegrade flag guards re-publishes) so the
// runtime submits the best state for manual review.
func (c *Cost) IncReflection(id session.TaskID) {
	tc := c.task(id)
	c.mu.Lock()
	tc.reflections++
	crossed := !tc.reflDegrade && tc.reflections == c.config.MaxReflections && c.config.MaxReflections > 0
	if crossed {
		tc.reflDegrade = true
	}
	c.mu.Unlock()
	if crossed {
		c.publish(context.Background(), &event.CostDegradedEvent{Task: event.TaskID(id), Stage: "autosubmit"})
	}
}

// IncToolCall records a tool call (File 07 §7.6.1).
func (c *Cost) IncToolCall(id session.TaskID) {
	tc := c.task(id)
	c.mu.Lock()
	tc.toolCalls++
	c.mu.Unlock()
}

// AddTokens records token spend and dollars, firing the spend-cap abort when
// dollars cross MaxDollars (File 07 §7.6.2). The hard-abort is published once
// (the first time the cap is crossed); the `aborted` flag guards subsequent
// calls so a runaway task doesn't storm the bus.
func (c *Cost) AddTokens(id session.TaskID, in, out int) {
	tc := c.task(id)
	c.mu.Lock()
	tc.tokensIn += in
	tc.tokensOut += out
	cost := c.pricer.Price(in, out)
	tc.dollars += cost
	crossed := !tc.aborted && tc.dollars >= c.config.MaxDollars && cost > 0 && c.config.MaxDollars > 0
	if crossed {
		tc.aborted = true
	}
	c.mu.Unlock()
	if crossed {
		c.publish(context.Background(), &event.CostAbortEvent{Task: event.TaskID(id), Reason: "spend cap"})
	}
}

// ReflectionAllowed reports whether reflection may run for a task (File 07
// §7.6.2): false once loops reach MaxLoops (only-verify mode), or when a hard
// cap (spend, time) is crossed.
//
// A MaxLoops of zero means "no loop threshold", not "threshold already
// reached". Every limit here is compared with >=, which is satisfied at zero,
// so an unset MaxLoops used to make this predicate false from turn zero:
// reflection was silently off for the whole task, with no cap configured and
// nothing logged. Same rule as the deadline above and as MaxDollars below —
// unset means off.
func (c *Cost) ReflectionAllowed(id session.TaskID) bool {
	tc := c.task(id)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.config.MaxLoops > 0 && tc.loops >= c.config.MaxLoops {
		return false // only-verify mode
	}
	if tc.dollars >= c.config.MaxDollars && c.config.MaxDollars > 0 {
		return false
	}
	if !tc.deadline.IsZero() && time.Now().After(tc.deadline) {
		return false
	}
	return true
}

// HardCapExceeded reports whether a task has crossed a cap that means stop, and
// names the one that fired. The two hard caps are spend (MaxDollars) and wall
// clock (MaxTime); a zero value for either disables that cap rather than
// tripping it immediately.
//
// "A zero CostConfig caps nothing" is a claim about the whole controller, not
// just this predicate, and it was false while every other limit here was
// compared with a bare >=: HardCapExceeded answered false under CostConfig{}
// while ReflectionAllowed and MultiCandidateAllowed both answered false from
// turn zero, because `loops >= 0` and `reflections >= 0` hold before anything
// has happened. Every rung in this file now guards its limit with `> 0`, so
// the claim holds; pinned by TestZeroCostConfigCapsNothing, which asserts all
// four predicates rather than this one.
//
// It exists because ReflectionAllowed cannot answer this question. That
// predicate goes false for three different reasons, and one of them —
// loops >= MaxLoops — is a *degradation* rung: §7.6.2 says the agent should
// carry on with reflection disabled, not halt. A caller that reads
// ReflectionAllowed as "stop" silently promotes MaxLoops (default 6) into a
// hard iteration cap and kills tasks that were working correctly.
//
// The loop count is deliberately absent here: MaxLoops was tuned as a
// degradation threshold, and a runaway drive loop is bounded by MaxTime, which
// is a real limit somebody chose rather than an iteration count invented to
// look like one.
func (c *Cost) HardCapExceeded(id session.TaskID) (bool, string) {
	tc := c.task(id)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.config.MaxDollars > 0 && tc.dollars >= c.config.MaxDollars {
		return true, "spend cap"
	}
	if c.config.MaxTime > 0 && !tc.deadline.IsZero() && time.Now().After(tc.deadline) {
		return true, "time cap"
	}
	return false, ""
}

// MultiCandidateAllowed reports whether the cost budget still permits
// multi-candidate patch generation (File 07 §7.6.2). After MaxLoops is reached
// the agent degrades to single-candidate (only-verify-adjacent) mode, one rung
// before reflection is disabled entirely; after MaxReflections it degrades
// further to a single forced candidate (autosubmit). It mirrors
// ReflectionAllowed's loop threshold + the spend/time hard caps (multi-candidate
// is a strict subset of reflection — if reflection is hard-aborted, so is
// multi-candidate) and reuses the same internal counters, adding the
// MaxReflections rung.
//
// Zero on either rung means that rung is off, for the reason spelled out on
// ReflectionAllowed: `>= 0` is true at zero, so an unset cap would read as an
// exhausted one and multi-candidate would never run under a zero CostConfig.
func (c *Cost) MultiCandidateAllowed(id session.TaskID) bool {
	tc := c.task(id)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.config.MaxLoops > 0 && tc.loops >= c.config.MaxLoops {
		return false // single-candidate (only-verify-adjacent) mode
	}
	if tc.dollars >= c.config.MaxDollars && c.config.MaxDollars > 0 {
		return false // spend cap — reflection itself is off
	}
	if !tc.deadline.IsZero() && time.Now().After(tc.deadline) {
		return false // time cap — reflection itself is off
	}
	if c.config.MaxReflections > 0 && tc.reflections >= c.config.MaxReflections {
		return false // single forced candidate (autosubmit, §7.6.4)
	}
	return true
}

// Dollars returns the accrued dollar spend for a task (for the TUI meter).
func (c *Cost) Dollars(id session.TaskID) float64 {
	tc := c.task(id)
	c.mu.Lock()
	defer c.mu.Unlock()
	return tc.dollars
}

// Loops returns a task's reflection-loop count.
func (c *Cost) Loops(id session.TaskID) int {
	tc := c.task(id)
	c.mu.Lock()
	defer c.mu.Unlock()
	return tc.loops
}

// publish sends an event iff a bus is present.
func (c *Cost) publish(ctx context.Context, evt event.Event) {
	if c.bus == nil {
		return
	}
	_ = c.bus.Publish(ctx, evt)
}
