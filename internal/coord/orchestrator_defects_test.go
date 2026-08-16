// Regression tests for the L11 orchestration defects found in the §4.19
// adversarial review. Each test below pins one of them:
//
//	plan.done announced a green, merged plan whose failed todo's work was
//	  never merged (TestPlanDoneNamesFailedTodos);
//	plan.done was published only on the success path, so a consumer could not
//	  tell total failure — or cancellation, or a closed bus — from a run still
//	  in progress (TestPlanDonePublishedWhen*);
//	Concurrency was inert (agents ran strictly serially) and deadlocked the
//	  process at 32 (TestConcurrencyRunsTodosInParallel, TestWideFanOut*);
//	a panicking sub-agent killed the process (TestAgentPanicFailsTodoOnly);
//	an agent that returned nil and published nothing stalled the plan until
//	  the parent context died (TestAgentTimeoutFailsStalledTodo);
//	the bus-close failOutstanding call was undefended (TestBusCloseFails*).
//
// Every test that can hang carries a watchdog: this package already shipped one
// test (TestOrchestratorCancel) that would have blocked the suite rather than
// reported, and a hang is strictly worse than a failure.

package coord

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/baobao1044/yolo-code/internal/event"
)

// planDoneRecorder is an EventPublisher that captures plan.done and forwards
// everything to the bus. plan.done is the event under test in half this file:
// it is the entire content of the TUI board's final row and of the --plan JSONL
// transcript's last line.
type planDoneRecorder struct {
	mu       sync.Mutex
	done     []event.PlanDoneEvent
	delegate EventPublisher
}

func newPlanDoneRecorder(bus EventPublisher) *planDoneRecorder {
	return &planDoneRecorder{delegate: bus}
}

func (p *planDoneRecorder) Publish(ctx context.Context, e event.Event) error {
	if d, ok := e.(*event.PlanDoneEvent); ok {
		p.mu.Lock()
		p.done = append(p.done, *d)
		p.mu.Unlock()
	}
	return p.delegate.Publish(ctx, e)
}

// only returns the single plan.done the run must have published. "<NEVER
// PUBLISHED>" is what the probe for this bug printed, so the failure says so.
func (p *planDoneRecorder) only(t *testing.T) event.PlanDoneEvent {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.done) == 0 {
		t.Fatalf("plan.done: <NEVER PUBLISHED> — a consumer waiting on it cannot tell this run from one still in progress")
	}
	if len(p.done) != 1 {
		t.Fatalf("plan.done published %d times, want exactly 1", len(p.done))
	}
	return p.done[0]
}

// fakeVerifierPass accepts every diff. (merge_test.go's fakeVerifier is keyed
// by a pass flag; this is the same thing named for what it does here.)
type alwaysVerifier struct{}

func (alwaysVerifier) Verify(context.Context, string) (bool, error) { return true, nil }

// perTodoRunner scripts the canonical loop per todo: the reviewer's verdict and
// the tester's result are looked up by todo ID, so one plan can hold a todo that
// succeeds and a todo that burns its rework cap.
type perTodoRunner struct {
	bus      EventPublisher
	diffs    map[string]string
	approve  map[string]bool // todoID -> reviewer approves (default false)
	testPass map[string]bool // todoID -> tester passes (default false)
}

func (r *perTodoRunner) Run(ctx context.Context, role Role, task event.TaskAssignEvent) error {
	switch role {
	case RoleCoder:
		return r.bus.Publish(ctx, &event.CodeReadyEvent{
			PlanID: task.PlanID, TodoID: task.TodoID, Diff: r.diffs[task.TodoID], SelfReport: "done",
		})
	case RoleReviewer:
		return r.bus.Publish(ctx, &event.ReviewVerdictEvent{
			PlanID: task.PlanID, TodoID: task.TodoID, Approved: r.approve[task.TodoID],
		})
	case RoleTester:
		return r.bus.Publish(ctx, &event.TestReportEvent{
			PlanID: task.PlanID, TodoID: task.TodoID, Passed: r.testPass[task.TodoID], Output: "ok",
		})
	}
	return nil
}

// runWithWatchdog runs the orchestrator on its own goroutine and fails the test
// if it has not returned within limit, instead of blocking the suite.
func runWithWatchdog(t *testing.T, o *Orchestrator, ctx context.Context, goal string, limit time.Duration) error {
	t.Helper()
	type result struct{ err error }
	ch := make(chan result, 1)
	go func() { ch <- result{o.Run(ctx, goal)} }()
	select {
	case r := <-ch:
		return r.err
	case <-time.After(limit):
		t.Fatalf("Orchestrator.Run did not return within %s — it is wedged", limit)
		return nil
	}
}

// TestPlanDoneNamesFailedTodos is FIX 1: a plan where one todo burned its
// rework cap and another finished LAST used to merge cleanly and publish
// Done:true Merged:true Summary:"merged". Merge deliberately skips a Failed
// todo's diff, so that green report covered a plan with half its work missing.
// plan.done must say Done:false and name the todo that failed.
func TestPlanDoneNamesFailedTodos(t *testing.T) {
	bus := event.New()
	defer func() { _ = bus.Close() }()
	rec := newPlanDoneRecorder(bus)
	runner := &perTodoRunner{
		bus:      rec,
		diffs:    map[string]string{"todo_A": "diff-A", "todo_B": "diff-B"},
		approve:  map[string]bool{"todo_A": false, "todo_B": true}, // A is rejected forever
		testPass: map[string]bool{"todo_B": true},
	}
	plan := Plan{ID: "p", Goal: "two todos", Todos: []Todo{
		{ID: "todo_A", Title: "doomed", Status: Pending, Artifacts: []string{"a.go"}},
		{ID: "todo_B", Title: "fine", Status: Pending, Artifacts: []string{"b.go"}},
	}}
	o := NewOrchestrator(Config{MaxReworkCycles: 1, Concurrency: 1},
		fakePlanner{plan: plan, mode: Multi}, bus, rec, runner)
	o.Verifier = alwaysVerifier{}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := runWithWatchdog(t, o, ctx, plan.Goal, 5*time.Second)
	if !errors.Is(err, ErrPlanFailed) {
		t.Fatalf("Run = %v, want ErrPlanFailed", err)
	}
	if got := o.plan.StatusOf("todo_A"); got != Failed {
		t.Fatalf("todo_A = %v, want Failed (rework cap)", got)
	}
	if got := o.plan.StatusOf("todo_B"); got != Done {
		t.Fatalf("todo_B = %v, want Done", got)
	}

	pd := rec.only(t)
	if pd.Done {
		t.Errorf("plan.done Done = true for a plan with a failed todo — the event stream reported a green plan whose work is half missing")
	}
	if !strings.Contains(pd.Summary, "todo_A") {
		t.Errorf("plan.done Summary = %q, want it to name the failed todo todo_A", pd.Summary)
	}
}

// TestPlanDonePublishedWithoutVerifier is the fourth silent path, reported by
// the TUI peer and not in the original defect list: markDone returned early
// when Verifier was nil, so a caller that configured no verifier — the zero
// value, and every caller that is not the TUI — got no plan.done at all on a
// plan that fully succeeded. It is the same pathology as the failure and
// cancellation paths, and the single terminal publish closes it too.
//
// It also pins the answer to "what does Done mean when nobody verified": Done
// is true. "We never checked" and "we checked and it failed" are different
// facts, and Summary says which one this is. Reporting a completed, merged plan
// as not-done because no verifier was configured would make Done unusable as
// the TUI's success flag — it would render FAILED for every non-TUI caller.
func TestPlanDonePublishedWithoutVerifier(t *testing.T) {
	bus := event.New()
	defer func() { _ = bus.Close() }()
	rec := newPlanDoneRecorder(bus)
	runner := &perTodoRunner{
		bus:      rec,
		diffs:    map[string]string{"todo_A": "diff-A"},
		approve:  map[string]bool{"todo_A": true},
		testPass: map[string]bool{"todo_A": true},
	}
	plan := Plan{ID: "p", Goal: "one todo", Todos: []Todo{
		{ID: "todo_A", Title: "work", Status: Pending, Artifacts: []string{"a.go"}},
	}}
	o := NewOrchestrator(Config{MaxReworkCycles: 1, Concurrency: 1},
		fakePlanner{plan: plan, mode: Multi}, bus, rec, runner)
	// o.Verifier deliberately left nil.

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := runWithWatchdog(t, o, ctx, plan.Goal, 5*time.Second); err != nil {
		t.Fatalf("Run = %v, want nil", err)
	}

	pd := rec.only(t)
	if !pd.Done {
		t.Errorf("plan.done Done = false for a fully successful plan with no verifier — the TUI reads Done as its only success flag and would render FAILED")
	}
	if !pd.Merged {
		t.Errorf("plan.done Merged = false, want true: the diffs were combined")
	}
	if !strings.Contains(pd.Summary, "no verifier") {
		t.Errorf("plan.done Summary = %q, want it to say no verifier was configured rather than implying one passed", pd.Summary)
	}
}

// TestPlanDonePublishedWhenLastTodoFails is FIX 2: markDone was the sole
// publisher of plan.done, so when the LAST todo to reach a terminal state
// failed, no plan.done was emitted at all. A consumer waiting on it cannot tell
// total failure from still-running.
func TestPlanDonePublishedWhenLastTodoFails(t *testing.T) {
	bus := event.New()
	defer func() { _ = bus.Close() }()
	rec := newPlanDoneRecorder(bus)
	runner := &perTodoRunner{
		bus:      rec,
		diffs:    map[string]string{"todo_A": "diff-A", "todo_B": "diff-B"},
		approve:  map[string]bool{"todo_A": true, "todo_B": false}, // B (last) is doomed
		testPass: map[string]bool{"todo_A": true},
	}
	plan := Plan{ID: "p", Goal: "two todos", Todos: []Todo{
		{ID: "todo_A", Title: "fine", Status: Pending, Artifacts: []string{"a.go"}},
		{ID: "todo_B", Title: "doomed", Status: Pending, Artifacts: []string{"b.go"}},
	}}
	o := NewOrchestrator(Config{MaxReworkCycles: 1, Concurrency: 1},
		fakePlanner{plan: plan, mode: Multi}, bus, rec, runner)
	o.Verifier = alwaysVerifier{}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := runWithWatchdog(t, o, ctx, plan.Goal, 5*time.Second); !errors.Is(err, ErrPlanFailed) {
		t.Fatalf("Run = %v, want ErrPlanFailed", err)
	}

	pd := rec.only(t)
	if pd.Done {
		t.Errorf("plan.done Done = true, want false (todo_B failed)")
	}
	// The successful todo's diff is real work; it must have been merged and
	// reported rather than left in o.diffs for nobody.
	if !pd.Merged {
		t.Errorf("plan.done Merged = false — todo_A's diff was collected and then dropped on the floor")
	}
	if !strings.Contains(pd.Summary, "todo_B") {
		t.Errorf("plan.done Summary = %q, want it to name todo_B", pd.Summary)
	}
}

// TestPlanDonePublishedWhenAllTodosFail is FIX 2, total-failure shape: every
// todo fails, so markDone never ran and plan.done was never published.
func TestPlanDonePublishedWhenAllTodosFail(t *testing.T) {
	bus := event.New()
	defer func() { _ = bus.Close() }()
	rec := newPlanDoneRecorder(bus)
	runner := &perTodoRunner{bus: rec, diffs: map[string]string{}} // reviewer rejects everything
	plan := Plan{ID: "p", Goal: "two todos", Todos: []Todo{
		{ID: "todo_A", Title: "doomed", Status: Pending},
		{ID: "todo_B", Title: "also doomed", Status: Pending},
	}}
	o := NewOrchestrator(Config{MaxReworkCycles: 1, Concurrency: 1},
		fakePlanner{plan: plan, mode: Multi}, bus, rec, runner)
	o.Verifier = alwaysVerifier{}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := runWithWatchdog(t, o, ctx, plan.Goal, 5*time.Second); !errors.Is(err, ErrPlanFailed) {
		t.Fatalf("Run = %v, want ErrPlanFailed", err)
	}
	pd := rec.only(t)
	if pd.Done {
		t.Errorf("plan.done Done = true for an all-failed plan")
	}
	// The other half of the Canceled flag: a real failure must not claim to be
	// a cancellation, or the flag just relabels every unhappy ending.
	if pd.Canceled {
		t.Error("plan.done Canceled = true for a plan whose todos genuinely failed")
	}
	for _, id := range []string{"todo_A", "todo_B"} {
		if !strings.Contains(pd.Summary, id) {
			t.Errorf("plan.done Summary = %q, want it to name %s", pd.Summary, id)
		}
	}
}

// TestPlanDonePublishedOnCancel is FIX 2's cancel path. cancelAll published
// nothing at all. It must publish — and it must NOT call the plan failed: a
// cancelled plan has not failed, and the diffs the completed todos produced are
// still worth combining and reporting.
func TestPlanDonePublishedOnCancel(t *testing.T) {
	bus := event.New()
	defer func() { _ = bus.Close() }()
	rec := newPlanDoneRecorder(bus)
	plan := Plan{ID: "p", Goal: "two todos", Todos: []Todo{
		{ID: "todo_A", Title: "first", Status: Pending},
		{ID: "todo_B", Title: "second", Status: Pending, DependsOn: []string{"todo_A"}},
	}}
	o := NewOrchestrator(Config{MaxReworkCycles: 3, Concurrency: 1},
		fakePlanner{plan: plan, mode: Multi}, bus, rec, muteRunner{})
	o.Verifier = alwaysVerifier{}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	err := runWithWatchdog(t, o, ctx, plan.Goal, 5*time.Second)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run = %v, want the ctx error", err)
	}
	if errors.Is(err, ErrPlanFailed) {
		t.Errorf("Run = %v — a cancelled plan has not failed; reporting ErrPlanFailed is its own lie", err)
	}

	pd := rec.only(t)
	if pd.Done {
		t.Errorf("plan.done Done = true for a cancelled run")
	}
	if !strings.Contains(pd.Summary, "canceled") {
		t.Errorf("plan.done Summary = %q, want it to say the run was canceled rather than blaming the todos", pd.Summary)
	}
	// Summary is prose and a consumer must not have to string-match it to
	// recover a boolean. Canceled is the machine-readable half, and without it
	// the TUI renders the operator's own Ctrl-C as FAILED.
	if !pd.Canceled {
		t.Error("plan.done Canceled = false for a cancelled run — Done is false for a failure too, so nothing distinguishes them")
	}
}

// TestBusCloseFailsOutstandingTodos is FIX 6's undefended guard: removing
// Run's bus-close failOutstanding call left the suite GREEN while Run returned
// nil with todo_A InProgress and todo_B Pending — success reported over work
// that was never done.
func TestBusCloseFailsOutstandingTodos(t *testing.T) {
	bus := event.New()
	rec := newPlanDoneRecorder(bus)
	plan := Plan{ID: "p", Goal: "two todos", Todos: []Todo{
		{ID: "todo_A", Title: "first", Status: Pending},
		{ID: "todo_B", Title: "second", Status: Pending, DependsOn: []string{"todo_A"}},
	}}
	o := NewOrchestrator(Config{MaxReworkCycles: 3, Concurrency: 1},
		fakePlanner{plan: plan, mode: Multi}, bus, rec, muteRunner{})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Close the bus under the running plan: no further agent event can arrive.
	go func() {
		time.Sleep(20 * time.Millisecond)
		_ = bus.Close()
	}()
	err := runWithWatchdog(t, o, ctx, plan.Goal, 5*time.Second)

	// Status first: it is the pathology itself, and it stays legible even when
	// the closed bus also breaks the terminal plan.done publish.
	for _, id := range []string{"todo_A", "todo_B"} {
		if got := o.plan.StatusOf(id); got != Failed {
			t.Errorf("todo %s = %v after bus close, want Failed — a non-terminal todo is work reported as neither done nor failed", id, got)
		}
	}
	if err == nil {
		t.Fatalf("Run = nil after the bus closed mid-plan — success reported with the work never done")
	}
	if !errors.Is(err, ErrPlanFailed) {
		t.Fatalf("Run = %v, want ErrPlanFailed", err)
	}
}

// countingRunner records how many coder turns are running at the same instant.
// It is the measurement the "Concurrency is inert" finding rests on: eight
// independent todos with Concurrency 8 used to show a maximum of ONE.
type countingRunner struct {
	bus  EventPublisher
	hold time.Duration

	mu      sync.Mutex
	cur     int
	max     int
	started int
}

func (r *countingRunner) Run(ctx context.Context, role Role, task event.TaskAssignEvent) error {
	if role == RoleCoder {
		r.mu.Lock()
		r.cur++
		r.started++
		if r.cur > r.max {
			r.max = r.cur
		}
		r.mu.Unlock()
		select {
		case <-time.After(r.hold):
		case <-ctx.Done():
		}
		r.mu.Lock()
		r.cur--
		r.mu.Unlock()
		return r.bus.Publish(ctx, &event.CodeReadyEvent{
			PlanID: task.PlanID, TodoID: task.TodoID, Diff: "d-" + task.TodoID, SelfReport: "done",
		})
	}
	if role == RoleReviewer {
		return r.bus.Publish(ctx, &event.ReviewVerdictEvent{
			PlanID: task.PlanID, TodoID: task.TodoID, Approved: true,
		})
	}
	return r.bus.Publish(ctx, &event.TestReportEvent{
		PlanID: task.PlanID, TodoID: task.TodoID, Passed: true, Output: "ok",
	})
}

func (r *countingRunner) peak() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.max
}

// independentPlan builds n todos with no DependsOn edges — the shape the
// package doc promises dispatches in parallel.
func independentPlan(n int) Plan {
	p := Plan{ID: "p", Goal: "n independent todos"}
	for i := 0; i < n; i++ {
		p.Todos = append(p.Todos, Todo{
			ID:        fmt.Sprintf("todo_%02d", i),
			Title:     fmt.Sprintf("work %d", i),
			Status:    Pending,
			Artifacts: []string{fmt.Sprintf("f%02d.go", i)},
		})
	}
	return p
}

// TestConcurrencyRunsTodosInParallel is FIX 3's inertness half. The package doc
// says "Todos with no unmet DependsOn dispatch in parallel, each to its own
// Coder"; the scheduler called spawn synchronously inside its own walk, so
// Concurrency 8 over eight 20ms todos peaked at ONE simultaneous coder and took
// 8 × 20ms. An operator who set the knob got nothing, and nothing said so.
func TestConcurrencyRunsTodosInParallel(t *testing.T) {
	const n = 8
	const hold = 20 * time.Millisecond

	bus := event.New()
	defer func() { _ = bus.Close() }()
	runner := &countingRunner{bus: bus, hold: hold}
	plan := independentPlan(n)
	o := NewOrchestrator(Config{MaxReworkCycles: 3, Concurrency: n},
		fakePlanner{plan: plan, mode: Multi}, bus, bus, runner)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	if err := runWithWatchdog(t, o, ctx, plan.Goal, 10*time.Second); err != nil {
		t.Fatalf("Run: %v", err)
	}
	elapsed := time.Since(start)

	if peak := runner.peak(); peak < 2 {
		t.Errorf("max simultaneous coders = %d with Concurrency=%d — the knob is inert", peak, n)
	}
	// Serial execution costs n*hold. Anything under half of that could not have
	// been serial; the bound is loose so a slow CI box does not flake it.
	if elapsed > (n*hold)/2 {
		t.Errorf("elapsed = %s for %d × %s todos at Concurrency=%d — that is serial, not parallel",
			elapsed, n, hold, n)
	}
}

// TestWideFanOutDoesNotDeadlock is FIX 3's deadlock half. 32 todos × 2 coord.*
// events = 64 = the bus's subscriberBuf, and the synchronous spawn published
// them from inside the only goroutine draining that subscription. n ≤ 31
// returned; n = 32 and 33 wedged forever. context.Background() is deliberate —
// it is what `--plan` supplies, and with no deadline the old code never
// returned at all.
func TestWideFanOutDoesNotDeadlock(t *testing.T) {
	for _, n := range []int{31, 32, 33, 64} {
		t.Run(fmt.Sprintf("n=%d", n), func(t *testing.T) {
			bus := event.New()
			defer func() { _ = bus.Close() }()
			runner := &perTodoRunner{
				bus:      bus,
				diffs:    map[string]string{},
				approve:  map[string]bool{},
				testPass: map[string]bool{},
			}
			plan := independentPlan(n)
			for _, td := range plan.Todos {
				runner.approve[td.ID] = true
				runner.testPass[td.ID] = true
			}
			o := NewOrchestrator(Config{MaxReworkCycles: 3, Concurrency: n},
				fakePlanner{plan: plan, mode: Multi}, bus, bus, runner)

			// No deadline, exactly as `--plan` runs it. The watchdog is the
			// only thing that turns a wedge into a report.
			if err := runWithWatchdog(t, o, context.Background(), plan.Goal, 20*time.Second); err != nil {
				t.Fatalf("Run: %v", err)
			}
			for i := range o.plan.Todos {
				if o.plan.Todos[i].Status != Done {
					t.Fatalf("todo %s = %v, want Done", o.plan.Todos[i].ID, o.plan.Todos[i].Status)
				}
			}
		})
	}
}

// panicRunner panics on the named role for the named todo and behaves normally
// otherwise — a sub-agent that explodes mid-turn.
type panicRunner struct {
	bus        EventPublisher
	panicTodo  string
	panicRole  Role
	panicValue string
}

func (r *panicRunner) Run(ctx context.Context, role Role, task event.TaskAssignEvent) error {
	if role == r.panicRole && task.TodoID == r.panicTodo {
		panic(r.panicValue)
	}
	switch role {
	case RoleCoder:
		return r.bus.Publish(ctx, &event.CodeReadyEvent{
			PlanID: task.PlanID, TodoID: task.TodoID, Diff: "d-" + task.TodoID, SelfReport: "done",
		})
	case RoleReviewer:
		return r.bus.Publish(ctx, &event.ReviewVerdictEvent{
			PlanID: task.PlanID, TodoID: task.TodoID, Approved: true,
		})
	case RoleTester:
		return r.bus.Publish(ctx, &event.TestReportEvent{
			PlanID: task.PlanID, TodoID: task.TodoID, Passed: true, Output: "ok",
		})
	}
	return nil
}

// TestAgentPanicFailsTodoOnly is FIX 4. There was no recover() anywhere in
// internal/coord, so a panicking sub-agent unwound straight through
// Orchestrator.Run and killed the process — on the TUI path that is mid
// alt-screen, terminal left in raw mode, no transcript flush. It must become an
// ordinary todo failure that abandons neither the process nor the other todos,
// and it must carry the recovered value and a stack: a panic reported as a bare
// "failed" is its own information loss.
func TestAgentPanicFailsTodoOnly(t *testing.T) {
	bus := event.New()
	defer func() { _ = bus.Close() }()
	rec := newPlanDoneRecorder(bus)
	runner := &panicRunner{bus: rec, panicTodo: "todo_A", panicRole: RoleCoder, panicValue: "sub-agent exploded"}
	plan := Plan{ID: "p", Goal: "two todos", Todos: []Todo{
		{ID: "todo_A", Title: "explodes", Status: Pending, Artifacts: []string{"a.go"}},
		{ID: "todo_B", Title: "survives", Status: Pending, Artifacts: []string{"b.go"}},
	}}
	o := NewOrchestrator(Config{MaxReworkCycles: 3, Concurrency: 1},
		fakePlanner{plan: plan, mode: Multi}, bus, rec, runner)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := runWithWatchdog(t, o, ctx, plan.Goal, 5*time.Second)

	if !errors.Is(err, ErrPlanFailed) {
		t.Fatalf("Run = %v, want ErrPlanFailed (the panicking todo failed)", err)
	}
	if !errors.Is(err, ErrAgentPanic) {
		t.Errorf("Run = %v, want it to wrap ErrAgentPanic", err)
	}
	if !strings.Contains(err.Error(), "sub-agent exploded") {
		t.Errorf("Run error %q does not carry the recovered value", err)
	}
	if !strings.Contains(err.Error(), "goroutine") {
		t.Errorf("Run error %q carries no stack — a panic flattened to \"failed\" loses the only evidence there was", err)
	}
	if got := o.plan.StatusOf("todo_A"); got != Failed {
		t.Errorf("todo_A = %v, want Failed", got)
	}
	// The stated invariant (orchestrator.go: "One todo's failure must not
	// abandon the rest of the plan") applied to a panic.
	if got := o.plan.StatusOf("todo_B"); got != Done {
		t.Errorf("todo_B = %v, want Done — one todo's panic must not abandon the rest of the plan", got)
	}
	if pd := rec.only(t); pd.Done {
		t.Errorf("plan.done Done = true after a panicking todo")
	}
}

// TestAgentTimeoutFailsStalledTodo is FIX 5. muteRunner accepts the turn,
// returns nil, and publishes nothing — a hung provider connection is
// indistinguishable from a working one. With no per-agent cap the plan stalled
// until the PARENT context expired, and in production (`--plan`, the TUI) the
// parent has no deadline at all, so it stalled forever with no diagnostic. The
// parent deadline here is 10s and the cap is 60ms: if the cap is what ends the
// run, it ends in well under a second.
func TestAgentTimeoutFailsStalledTodo(t *testing.T) {
	bus := event.New()
	defer func() { _ = bus.Close() }()
	rec := newPlanDoneRecorder(bus)
	plan := Plan{ID: "p", Goal: "one hung todo", Todos: []Todo{
		{ID: "todo_A", Title: "hangs", Status: Pending},
	}}
	o := NewOrchestrator(Config{MaxReworkCycles: 3, Concurrency: 1, AgentTimeout: 60 * time.Millisecond},
		fakePlanner{plan: plan, mode: Multi}, bus, rec, muteRunner{})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	err := runWithWatchdog(t, o, ctx, plan.Goal, 10*time.Second)
	elapsed := time.Since(start)

	if elapsed > 2*time.Second {
		t.Fatalf("Run took %s with a 60ms agent cap — the cap is not enforced; the plan waited on the parent context", elapsed)
	}
	if errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, ErrAgentTimeout) {
		t.Fatalf("Run = %v — the run blamed the parent clock, not the agent that never reported", err)
	}
	if !errors.Is(err, ErrAgentTimeout) {
		t.Fatalf("Run = %v, want ErrAgentTimeout", err)
	}
	if !errors.Is(err, ErrPlanFailed) {
		t.Errorf("Run = %v, want ErrPlanFailed (the stalled todo must reach Failed)", err)
	}
	if got := o.plan.StatusOf("todo_A"); got != Failed {
		t.Errorf("todo_A = %v, want Failed", got)
	}
	if pd := rec.only(t); pd.Done {
		t.Errorf("plan.done Done = true for a plan whose only todo timed out")
	}
}

// TestZeroAgentTimeoutMeansNoCapNotElapsed pins the sign convention. A zero cap
// handed to context.WithTimeout is a context that has ALREADY expired, which
// would fail every todo on dispatch. Zero must mean "unset" (→
// DefaultAgentTimeout) and negative must mean "no cap" — this codebase has
// surfaced the zero-means-elapsed inversion six times.
func TestZeroAgentTimeoutMeansNoCapNotElapsed(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  Config
	}{
		{"zero selects the default", Config{MaxReworkCycles: 3, Concurrency: 1}},
		{"negative is the explicit opt-out", Config{MaxReworkCycles: 3, Concurrency: 1, AgentTimeout: -1}},
		{"role override of zero", Config{MaxReworkCycles: 3, Concurrency: 1,
			RoleTimeouts: map[Role]time.Duration{RoleCoder: 0}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bus := event.New()
			defer func() { _ = bus.Close() }()
			runner := &perTodoRunner{
				bus:      bus,
				diffs:    map[string]string{"todo_A": "d"},
				approve:  map[string]bool{"todo_A": true},
				testPass: map[string]bool{"todo_A": true},
			}
			plan := Plan{ID: "p", Goal: "one todo", Todos: []Todo{{ID: "todo_A", Title: "work", Status: Pending}}}
			o := NewOrchestrator(tc.cfg, fakePlanner{plan: plan, mode: Multi}, bus, bus, runner)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := runWithWatchdog(t, o, ctx, plan.Goal, 5*time.Second); err != nil {
				t.Fatalf("Run = %v, want nil — a zero limit was read as already elapsed", err)
			}
			if got := o.plan.StatusOf("todo_A"); got != Done {
				t.Errorf("todo_A = %v, want Done", got)
			}
		})
	}
}

// lingeringRunner blocks until its turn context is cancelled and then takes a
// visible moment to finish, the way a real agent flushing a patch does. It
// counts turns that are still inside Run.
type lingeringRunner struct {
	linger time.Duration

	mu      sync.Mutex
	inside  int
	entered int
}

func (r *lingeringRunner) Run(ctx context.Context, _ Role, _ event.TaskAssignEvent) error {
	r.mu.Lock()
	r.inside++
	r.entered++
	r.mu.Unlock()
	<-ctx.Done()
	time.Sleep(r.linger)
	r.mu.Lock()
	r.inside--
	r.mu.Unlock()
	return ctx.Err()
}

func (r *lingeringRunner) stillInside() (int, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.inside, r.entered
}

// TestNoAgentTurnRunsAfterRunReturns measures the part of the Ctrl-C problem
// these changes actually close. Cancelling the parent no longer leaves agents
// running past the orchestrator: Run cancels every inflight turn and WAITS for
// it before returning, so once Run has returned no AgentRunner call is still
// executing. That is the precondition cmd/yolo's runtimeAgentRunner.close()
// documents and relies on when it tears down shadow trees and sessions.
//
// What it does NOT close: a runner that ignores its context still runs to
// completion — it is simply waited for rather than abandoned, so the writes
// land before teardown instead of after it. Interrupting the agent itself is
// the runner's job, not the orchestrator's.
func TestNoAgentTurnRunsAfterRunReturns(t *testing.T) {
	bus := event.New()
	defer func() { _ = bus.Close() }()
	rec := newPlanDoneRecorder(bus)
	runner := &lingeringRunner{linger: 50 * time.Millisecond}
	plan := independentPlan(4)
	o := NewOrchestrator(Config{MaxReworkCycles: 3, Concurrency: 4},
		fakePlanner{plan: plan, mode: Multi}, bus, rec, runner)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- o.Run(ctx, plan.Goal) }()
	time.Sleep(30 * time.Millisecond) // let the turns get dispatched
	cancel()

	select {
	case <-errCh:
	case <-time.After(5 * time.Second):
		t.Fatalf("Run did not return 5s after cancel — the run is wedged")
	}

	inside, entered := runner.stillInside()
	if entered == 0 {
		t.Fatalf("no turn was ever dispatched — the test measured nothing")
	}
	if inside != 0 {
		t.Fatalf("%d of %d agent turns still executing after Run returned — a caller tearing the runner down now races live agents", inside, entered)
	}
}
