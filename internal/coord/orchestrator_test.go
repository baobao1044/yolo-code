// Tests for L11-004 — the Orchestrator + rework cap (File 12 §12.4, §12.4.1).
//
// The orchestrator runs the canonical 5-event loop: Planner → Plan → publish
// plan.ready → DispatchReady → per todo: spawn coder (AgentRunner) → coder
// publishes code.ready → spawn reviewer (direct) → reviewer publishes
// review.verdict → approved? spawn tester / rework → tester publishes
// test.report → pass? MarkDone + dispatch dependents / rework. The rework cap
// (MaxReworkCycles=3) escalates a stuck todo to Failed instead of looping.
//
// Sprint 10 uses fake agents (AgentRunner seam) that publish canned events
// synchronously, and a fake publisher that records published events. Real
// per-agent drive is the integration sprint (Decision 1).

package coord

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/baobao1044/yolo-code/internal/event"
)

// recordingPublisher records every published event AND forwards it to a
// delegate (the bus). The orchestrator uses it as its EventPublisher so its
// plan.ready / task.assign events reach the bus (the event loop drains the
// same bus) AND are recorded for assertions. It implements EventPublisher.
type recordingPublisher struct {
	mu       sync.Mutex
	log      []event.Event
	byID     map[string][]event.Event // events keyed by plan/todo id
	delegate EventPublisher           // the bus (so events reach the loop)
}

func newRecordingPublisher(bus EventPublisher) *recordingPublisher {
	return &recordingPublisher{byID: map[string][]event.Event{}, delegate: bus}
}

func (f *recordingPublisher) Publish(ctx context.Context, e event.Event) error {
	f.mu.Lock()
	f.log = append(f.log, e)
	switch v := e.(type) {
	case *event.TaskAssignEvent:
		f.byID[v.TodoID] = append(f.byID[v.TodoID], e)
	case *event.CodeReadyEvent:
		f.byID[v.TodoID] = append(f.byID[v.TodoID], e)
	case *event.ReviewVerdictEvent:
		f.byID[v.TodoID] = append(f.byID[v.TodoID], e)
	case *event.TestReportEvent:
		f.byID[v.TodoID] = append(f.byID[v.TodoID], e)
	case *event.PlanReadyEvent:
		f.byID[v.PlanID] = append(f.byID[v.PlanID], e)
	}
	f.mu.Unlock()
	return f.delegate.Publish(ctx, e) // forward to the bus so the loop sees it
}

// fakeRunner is an AgentRunner that publishes a scripted response per role.
// It publishes to the BUS (EventPublisher) so the orchestrator's event loop —
// which subscribed coord.> on the same bus — receives the agent events. The
// run is synchronous (no goroutine) so the test is deterministic: spawnCoder
// calls runner.Run, which publishes into the bus buffer; the event loop
// drains the buffer on the next iteration.
type fakeRunner struct {
	bus EventPublisher // the bus — agent events go here, not a recorder

	// Per-role canned behavior. codeReady is keyed by todo ID; the verdict
	// and test report are role-wide defaults (override per-test).
	codeReady map[string]string // todoID -> diff string
	verdict   bool              // reviewer approves?
	verdictN  int               // reject the first verdictN reviews, then approve
	testPass  bool
}

func (r *fakeRunner) Run(ctx context.Context, role Role, task event.TaskAssignEvent) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	// A real runner whose event never reached the bus has not done its turn,
	// so the publish error is returned rather than dropped — dropping it here
	// would hide an orchestrator regression behind a stalled event loop.
	switch role {
	case RoleCoder:
		diff := r.codeReady[task.TodoID]
		return r.bus.Publish(ctx, &event.CodeReadyEvent{
			PlanID: task.PlanID, TodoID: task.TodoID, Diff: diff, SelfReport: "done",
		})
	case RoleReviewer:
		approved := r.verdict
		if r.verdictN > 0 {
			approved = false
			r.verdictN--
		}
		return r.bus.Publish(ctx, &event.ReviewVerdictEvent{
			PlanID: task.PlanID, TodoID: task.TodoID, Approved: approved,
		})
	case RoleTester:
		return r.bus.Publish(ctx, &event.TestReportEvent{
			PlanID: task.PlanID, TodoID: task.TodoID, Passed: r.testPass, Output: "ok",
		})
	}
	return nil
}

// fakePlanner implements Planner by returning a canned Plan.
type fakePlanner struct {
	plan Plan
	mode Mode
}

func (p fakePlanner) Plan(_ context.Context, _ string) (Plan, Mode, error) {
	return p.plan, p.mode, nil
}

// newTestOrchestrator wires an orchestrator with fakes + a real event bus,
// subscribes the orchestrator to coord.>, and returns the pieces.
//
// The orchestrator's event loop reads from the bus's coord.> subscription;
// fake agents publish to the same bus. A 50ms ctx timeout guards against
// infinite loops (a rework-cap regression would otherwise hang).
func newTestOrchestrator(t *testing.T, todos []string, verdict bool, verdictN int, testPass bool) (
	*Orchestrator, *event.Bus, *fakeRunner, *recordingPublisher,
) {
	t.Helper()
	bus := event.New()
	rec := newRecordingPublisher(bus) // wraps the bus; records + forwards
	runner := &fakeRunner{
		bus:       bus, // agent events go straight to the bus (the loop reads it)
		codeReady: map[string]string{},
		verdict:   verdict,
		verdictN:  verdictN,
		testPass:  testPass,
	}
	plan := Plan{ID: "p1", Goal: "refactor X, add tests, fix CI"}
	for i, title := range todos {
		id := "todo_" + string(rune('A'+i))
		plan.Todos = append(plan.Todos, Todo{ID: id, Title: title, Assignee: "coder", Status: Pending})
		runner.codeReady[id] = "diff-for-" + id
	}
	o := NewOrchestrator(Config{
		MaxReworkCycles: 3,
		Concurrency:     1, // bound 1 → deterministic single-inflight
	}, fakePlanner{plan: plan, mode: Multi}, bus, rec, runner)
	return o, bus, runner, rec
}

// TestOrchestratorHappyPath: a single-todo run goes
// plan.ready → task.assign(coder) → code.ready → (reviewer) → review.verdict
// (approved) → (tester) → test.report (pass) → task done. The todo ends Done.
func TestOrchestratorHappyPath(t *testing.T) {
	o, bus, _, pub := newTestOrchestrator(t, []string{"implement X"}, true, 0, true)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	// The orchestrator subscribes to coord.> inside Start; Run runs the event
	// loop (planner + publish plan.ready + DispatchReady + drain coord.>).
	o.Start(ctx)
	if err := o.Run(ctx, "refactor X, add tests, fix CI"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	o.Stop(ctx)
	_ = bus.Close()

	// The plan was published as plan.ready exactly once.
	planReady := 0
	taskAssign := 0
	for _, e := range pub.log {
		switch e.(type) {
		case *event.PlanReadyEvent:
			planReady++
		case *event.TaskAssignEvent:
			taskAssign++
		}
	}
	if planReady != 1 {
		t.Errorf("plan.ready published %d times, want 1", planReady)
	}
	if taskAssign != 1 {
		t.Errorf("task.assign published %d times, want 1 (one coder)", taskAssign)
	}
	// The single todo ended Done.
	if o.plan == nil || o.plan.Todos[0].Status != Done {
		t.Errorf("todo status = %v, want Done", o.plan.Todos[0].Status)
	}
}

// TestOrchestratorReworkCap: a todo the reviewer keeps rejecting hits the
// rework cap (MaxReworkCycles=3) and escalates to Failed — NO infinite loop.
// After 3 rejections the todo is Failed and Run returns ErrPlanFailed (no 4th
// spawn). Run must NOT return nil: AllDone is true for a Failed todo, so a nil
// return would make "the plan gave up" indistinguishable from "the plan
// succeeded".
func TestOrchestratorReworkCap(t *testing.T) {
	o, bus, runner, pub := newTestOrchestrator(t, []string{"implement X"}, true, 99, true)
	// verdictN=99 → reviewer always rejects; cap should fire after 3 cycles.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	o.Start(ctx)
	err := o.Run(ctx, "refactor X, add tests, fix CI")
	if !errors.Is(err, ErrPlanFailed) {
		t.Fatalf("Run = %v, want ErrPlanFailed (the rework cap gave up on todo_A)", err)
	}
	if !strings.Contains(err.Error(), "todo_A") {
		t.Errorf("Run error %q does not name the failed todo", err)
	}
	o.Stop(ctx)
	_ = bus.Close()

	if o.plan == nil || o.plan.Todos[0].Status != Failed {
		t.Errorf("todo status = %v, want Failed (rework cap escalated)", o.plan.Todos[0].Status)
	}
	// File 12 §12.4.1 increments BEFORE the cap check, so the value at the
	// point of failure is cap+1 (4 with the default 3). The point: no 5th
	// coder spawn — the loop terminates.
	if o.plan.Todos[0].ReworkCycles != 4 {
		t.Errorf("ReworkCycles = %d, want 4 (cap+1; incremented past MaxReworkCycles=3)", o.plan.Todos[0].ReworkCycles)
	}
	// The coder was spawned: 1 initial + 3 rework = 4 task.assign events for
	// this todo. A 5th assign (a 4th rework) would mean the cap didn't fire.
	assigns := 0
	for _, e := range pub.byID["todo_A"] {
		if _, ok := e.(*event.TaskAssignEvent); ok {
			assigns++
		}
	}
	if assigns != 4 {
		t.Errorf("coder spawned %d times, want 4 (1 initial + 3 rework, cap then fires — no 5th)", assigns)
	}
	_ = runner
}

// TestOrchestratorCancel: a canceled ctx aborts the run. This test used to have
// no assertion at all — its only conditional was an empty `if err == nil {}`,
// so it could not go red, and having no watchdog it would have BLOCKED the
// suite rather than reported if the run wedged. It is the only test named for
// cancellation, so it now pins the whole contract:
//
//   - Run returns, and returns promptly (watchdog).
//   - It returns the context's error, so the caller can tell cancellation from
//     completion.
//   - It does NOT return ErrPlanFailed: the operator stopping a run is not the
//     plan giving up.
//   - No todo is left non-terminal — a canceled run that leaves a todo Pending
//     reports the plan as neither done nor failed.
//   - plan.done is still published, because it is the only signal a consumer
//     can wait on.
func TestOrchestratorCancel(t *testing.T) {
	bus := event.New()
	defer func() { _ = bus.Close() }()
	rec := newPlanDoneRecorder(bus)
	plan := Plan{ID: "p1", Goal: "refactor X, add tests, fix CI", Todos: []Todo{
		{ID: "todo_A", Title: "implement X", Assignee: "coder", Status: Pending},
		{ID: "todo_B", Title: "test X", Assignee: "coder", Status: Pending, DependsOn: []string{"todo_A"}},
	}}
	// muteRunner accepts the turn and publishes nothing, so the run is still in
	// flight when the cancel lands (a synchronous fake would finish first and
	// never exercise the cancel path — which is what the old test did).
	o := NewOrchestrator(Config{MaxReworkCycles: 3, Concurrency: 1},
		fakePlanner{plan: plan, mode: Multi}, bus, rec, muteRunner{})

	ctx, cancel := context.WithCancel(context.Background())
	o.Start(ctx)
	errCh := make(chan error, 1)
	go func() { errCh <- o.Run(ctx, plan.Goal) }()
	time.Sleep(10 * time.Millisecond) // let the first todo get dispatched
	cancel()

	var err error
	select {
	case err = <-errCh:
	case <-time.After(5 * time.Second):
		t.Fatalf("Run did not return 5s after cancel — the run is wedged")
	}

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v, want context.Canceled", err)
	}
	if errors.Is(err, ErrPlanFailed) {
		t.Errorf("Run = %v — a canceled plan has not failed", err)
	}
	for _, id := range []string{"todo_A", "todo_B"} {
		if got := o.plan.StatusOf(id); got != Failed {
			t.Errorf("todo %s = %v after cancel, want Failed (terminal)", id, got)
		}
	}
	if pd := rec.only(t); pd.Done {
		t.Errorf("plan.done Done = true for a canceled run")
	}

	stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Second)
	defer stopCancel()
	if err := o.Stop(stopCtx); err != nil {
		t.Errorf("Stop: %v", err)
	}
}

// TestOrchestratorStopIdempotent: Stop is idempotent (sync.Once) — calling it
// twice is a no-op and does not panic or double-close.
func TestOrchestratorStopIdempotent(t *testing.T) {
	o, bus, _, _ := newTestOrchestrator(t, []string{"implement X"}, true, 0, true)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	o.Start(ctx)
	_ = o.Run(ctx, "refactor X, add tests, fix CI")
	if err := o.Stop(ctx); err != nil {
		t.Fatalf("first Stop: %v", err)
	}
	if err := o.Stop(ctx); err != nil {
		t.Errorf("second Stop: %v, want nil (idempotent)", err)
	}
	_ = bus.Close()
}

// TestOrchestratorNoLeak: after Stop, the done channel is closed (the event
// loop goroutine exited). This is the no-leak exit bar (mirrors infra).
func TestOrchestratorNoLeak(t *testing.T) {
	o, bus, _, _ := newTestOrchestrator(t, []string{"implement X"}, true, 0, true)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	o.Start(ctx)
	_ = o.Run(ctx, "refactor X, add tests, fix CI")
	_ = bus.Close() // ends the coord.> range → done closes
	_ = o.Stop(ctx)
	select {
	case <-o.Done():
	default:
		t.Errorf("Done() not closed after Stop+bus.Close — goroutine leaked")
	}
}
