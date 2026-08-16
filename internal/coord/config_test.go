// Tests for defaultConfig's zero guards (§4.19 FIX 6).
//
// Both guards were correct and both were completely undefended: the reviewer
// deleted them and the whole suite stayed GREEN. With MaxReworkCycles = 0 every
// todo fails on its FIRST rework (1 > 0 — an unset cap read as already
// exhausted); with Concurrency = 0 the scheduler bound is unbounded, which walks
// straight into the wide-fan-out deadlock. A zero-value Config{} is a completely
// natural call, so these are the values the package actually ships with.

package coord

import (
	"context"
	"testing"
	"time"

	"github.com/baobao1044/yolo-code/internal/event"
)

// TestDefaultConfigFillsZeroFields pins Config{} → the documented defaults.
// This is the table test that makes removing either guard go red.
func TestDefaultConfigFillsZeroFields(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   Config
		want Config
	}{
		{
			name: "zero value gets every default",
			in:   Config{},
			want: Config{MaxReworkCycles: 3, Concurrency: 1, AgentTimeout: DefaultAgentTimeout},
		},
		{
			name: "negative rework cap is still unset, not a cap of -2",
			in:   Config{MaxReworkCycles: -2, Concurrency: -5},
			want: Config{MaxReworkCycles: 3, Concurrency: 1, AgentTimeout: DefaultAgentTimeout},
		},
		{
			name: "explicit values survive",
			in:   Config{MaxReworkCycles: 7, Concurrency: 4, AgentTimeout: time.Second},
			want: Config{MaxReworkCycles: 7, Concurrency: 4, AgentTimeout: time.Second},
		},
		{
			name: "a negative agent timeout is the explicit no-cap opt-out",
			in:   Config{MaxReworkCycles: 1, Concurrency: 1, AgentTimeout: -1},
			want: Config{MaxReworkCycles: 1, Concurrency: 1, AgentTimeout: -1},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := defaultConfig(tc.in)
			if got.MaxReworkCycles != tc.want.MaxReworkCycles {
				t.Errorf("MaxReworkCycles = %d, want %d", got.MaxReworkCycles, tc.want.MaxReworkCycles)
			}
			if got.Concurrency != tc.want.Concurrency {
				t.Errorf("Concurrency = %d, want %d", got.Concurrency, tc.want.Concurrency)
			}
			if got.AgentTimeout != tc.want.AgentTimeout {
				t.Errorf("AgentTimeout = %v, want %v", got.AgentTimeout, tc.want.AgentTimeout)
			}
		})
	}
}

// TestZeroConfigSurvivesOneRework is the behavioural half: the guard's job is
// that a Config{} caller gets three rework cycles, not zero. Without it the
// FIRST rejection fails the todo (ReworkCycles 1 > cap 0) and a plan that would
// have succeeded on its second attempt reports as given up.
func TestZeroConfigSurvivesOneRework(t *testing.T) {
	bus := event.New()
	defer func() { _ = bus.Close() }()
	runner := &fakeRunner{
		bus:       bus,
		codeReady: map[string]string{"todo_A": "diff-A"},
		verdict:   true,
		verdictN:  1, // reject once, then approve
		testPass:  true,
	}
	plan := Plan{ID: "p", Goal: "one todo", Todos: []Todo{{ID: "todo_A", Title: "work", Status: Pending}}}

	// Config{} — every field zero, the natural call.
	o := NewOrchestrator(Config{}, fakePlanner{plan: plan, mode: Multi}, bus, bus, runner)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := runWithWatchdog(t, o, ctx, plan.Goal, 5*time.Second); err != nil {
		t.Fatalf("Run = %v, want nil — with Config{} a single rework must be allowed, not read as an exhausted cap", err)
	}
	if got := o.plan.StatusOf("todo_A"); got != Done {
		t.Errorf("todo_A = %v, want Done after one rework cycle", got)
	}
}

// TestTurnTimeoutResolution pins the per-role override rules, including the two
// sign conventions the orchestrator depends on.
func TestTurnTimeoutResolution(t *testing.T) {
	c := defaultConfig(Config{
		AgentTimeout: 30 * time.Second,
		RoleTimeouts: map[Role]time.Duration{
			RoleTester:   2 * time.Minute,
			RoleReviewer: -1, // no cap for this role
		},
	})
	for _, tc := range []struct {
		role Role
		want time.Duration
	}{
		{RoleCoder, 30 * time.Second},   // no entry → the plan-wide cap
		{RoleTester, 2 * time.Minute},   // entry wins
		{RoleReviewer, -1},              // entry wins, and ≤ 0 is "no cap"
		{RolePlanner, 30 * time.Second}, // no entry
		{Role("unknown"), 30 * time.Second},
	} {
		if got := c.turnTimeout(tc.role); got != tc.want {
			t.Errorf("turnTimeout(%q) = %v, want %v", tc.role, got, tc.want)
		}
	}
}

// TestZeroConcurrencyIsBoundedNotUnbounded is the Concurrency guard's
// behavioural half. NewScheduler treats bound ≤ 0 as UNBOUNDED, so a Config{}
// whose guard was removed hands the scheduler 0 and dispatches the whole plan at
// once — the shape the wide-fan-out deadlock lives in. The guard is the only
// thing between a zero-value Config and that.
func TestZeroConcurrencyIsBoundedNotUnbounded(t *testing.T) {
	if got := defaultConfig(Config{}).Concurrency; got <= 0 {
		t.Fatalf("defaultConfig(Config{}).Concurrency = %d — NewScheduler reads ≤ 0 as unbounded", got)
	}
	bus := event.New()
	defer func() { _ = bus.Close() }()
	runner := &countingRunner{bus: bus, hold: 5 * time.Millisecond}
	plan := independentPlan(4)
	o := NewOrchestrator(Config{}, fakePlanner{plan: plan, mode: Multi}, bus, bus, runner)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := runWithWatchdog(t, o, ctx, plan.Goal, 5*time.Second); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if peak := runner.peak(); peak != 1 {
		t.Errorf("max simultaneous coders = %d with Config{}, want 1 (the default bound)", peak)
	}
}
