// Tests for the task failure path (4.16a/4.16b): every way a todo can fail
// must reach the Failed terminal state, and every failure must reach the
// caller through Run's error.
//
// The shape of the bug these pin: the orchestrator used to drop the AgentRunner
// and EventPublisher errors on the floor (`_ = o.runner.Run(...)`). A coder
// that never started publishes no code.ready, so the todo sat InProgress and
// the event loop blocked until ctx expired — the run then reported
// context.DeadlineExceeded, blaming the clock for an agent that never ran. Each
// test below asserts BOTH halves: the todo is Failed, and Run returns
// ErrPlanFailed naming it rather than a deadline.

package coord

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/baobao1044/yolo-code/internal/event"
)

// errAgentUnavailable is what the scripted runner returns when a test tells it
// to fail a role.
var errAgentUnavailable = errors.New("agent unavailable")

// scriptedRunner is an AgentRunner a test can make fail per role (and
// optionally only for named todos). Every role it is not told to fail behaves
// like the happy-path fake: it publishes the canned event to the bus.
type scriptedRunner struct {
	bus      EventPublisher
	failRole map[Role]bool
	onlyTodo map[string]bool // restrict the failure to these todos (nil ⇒ all)
	diff     string
	verdict  bool
	testPass bool
}

func (r *scriptedRunner) Run(ctx context.Context, role Role, task event.TaskAssignEvent) error {
	if r.failRole[role] && (r.onlyTodo == nil || r.onlyTodo[task.TodoID]) {
		return errAgentUnavailable
	}
	switch role {
	case RoleCoder:
		return r.bus.Publish(ctx, &event.CodeReadyEvent{
			PlanID: task.PlanID, TodoID: task.TodoID, Diff: r.diff, SelfReport: "done",
		})
	case RoleReviewer:
		return r.bus.Publish(ctx, &event.ReviewVerdictEvent{
			PlanID: task.PlanID, TodoID: task.TodoID, Approved: r.verdict,
		})
	case RoleTester:
		return r.bus.Publish(ctx, &event.TestReportEvent{
			PlanID: task.PlanID, TodoID: task.TodoID, Passed: r.testPass, Output: "ok",
		})
	}
	return nil
}

// muteRunner accepts every turn and publishes nothing — the agent that hangs.
// The event loop has nothing to make progress on, so only ctx ends the run.
type muteRunner struct{}

func (muteRunner) Run(context.Context, Role, event.TaskAssignEvent) error { return nil }

// rejectAssign forwards every event to the bus except task.assign, which it
// rejects — the "the board row could not be published" case.
type rejectAssign struct{ bus EventPublisher }

func (p rejectAssign) Publish(ctx context.Context, e event.Event) error {
	if _, ok := e.(*event.TaskAssignEvent); ok {
		return errors.New("bus rejected task.assign")
	}
	return p.bus.Publish(ctx, e)
}

// runFailurePlan wires an orchestrator over a real bus with the given plan,
// publisher and runner, runs it with a deadline generous enough that hitting
// the deadline means the loop stalled (not that the test is slow), and returns
// Run's error.
func runFailurePlan(t *testing.T, plan Plan, pub EventPublisher, bus *event.Bus, runner AgentRunner) (*Orchestrator, error) {
	t.Helper()
	o := NewOrchestrator(Config{MaxReworkCycles: 3, Concurrency: 1},
		fakePlanner{plan: plan, mode: Multi}, bus, pub, runner)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := o.Run(ctx, plan.Goal)
	_ = bus.Close()
	return o, err
}

// onePlan is a single-todo plan for the per-role failure tests.
func onePlan() Plan {
	return Plan{ID: "p", Goal: "implement X", Todos: []Todo{
		{ID: "todo_A", Title: "implement X", Assignee: "coder", Status: Pending},
	}}
}

// assertFailedTodo is the shared assertion: the todo is terminal-Failed, Run
// blamed the plan (not the clock), and the error names the todo.
func assertFailedTodo(t *testing.T, o *Orchestrator, err error, id string) {
	t.Helper()
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run = %v — the loop stalled waiting for an agent event that can never arrive", err)
	}
	if !errors.Is(err, ErrPlanFailed) {
		t.Fatalf("Run = %v, want ErrPlanFailed", err)
	}
	if !strings.Contains(err.Error(), id) {
		t.Errorf("Run error %q does not name the failed todo %q", err, id)
	}
	if td := o.plan.Todo(id); td == nil || td.Status != Failed {
		t.Errorf("todo %s status = %v, want Failed", id, o.plan.StatusOf(id))
	}
}

// TestCoderSpawnErrorFailsTodo: the coder will not start → no code.ready is
// ever published → the todo must be Failed, not left InProgress.
func TestCoderSpawnErrorFailsTodo(t *testing.T) {
	bus := event.New()
	runner := &scriptedRunner{bus: bus, failRole: map[Role]bool{RoleCoder: true}, verdict: true, testPass: true}
	o, err := runFailurePlan(t, onePlan(), bus, bus, runner)
	assertFailedTodo(t, o, err, "todo_A")
	if !strings.Contains(err.Error(), errAgentUnavailable.Error()) {
		t.Errorf("Run error %q does not carry the runner's cause", err)
	}
}

// TestReviewerSpawnErrorFailsTodo: the coder reported but the reviewer will not
// start → no review.verdict is ever published → the todo must be Failed.
func TestReviewerSpawnErrorFailsTodo(t *testing.T) {
	bus := event.New()
	runner := &scriptedRunner{bus: bus, failRole: map[Role]bool{RoleReviewer: true}, diff: "d", verdict: true, testPass: true}
	o, err := runFailurePlan(t, onePlan(), bus, bus, runner)
	assertFailedTodo(t, o, err, "todo_A")
}

// TestTesterSpawnErrorFailsTodo: the reviewer approved but the tester will not
// start → no test.report is ever published → the todo must be Failed.
func TestTesterSpawnErrorFailsTodo(t *testing.T) {
	bus := event.New()
	runner := &scriptedRunner{bus: bus, failRole: map[Role]bool{RoleTester: true}, diff: "d", verdict: true, testPass: true}
	o, err := runFailurePlan(t, onePlan(), bus, bus, runner)
	assertFailedTodo(t, o, err, "todo_A")
}

// TestTaskAssignPublishErrorFailsTodo: the bus rejected task.assign, so the
// board never gets the row and the coder never gets dispatched. Dropping that
// error left the todo InProgress forever.
func TestTaskAssignPublishErrorFailsTodo(t *testing.T) {
	bus := event.New()
	runner := &scriptedRunner{bus: bus, diff: "d", verdict: true, testPass: true}
	o, err := runFailurePlan(t, onePlan(), rejectAssign{bus: bus}, bus, runner)
	assertFailedTodo(t, o, err, "todo_A")
}

// TestSpawnFailureReleasesDependents: a todo whose coder would not start is
// Failed through the SCHEDULER, so its dependents are released and still run.
// A failure that skipped the scheduler would strand todo_B as Blocked forever.
func TestSpawnFailureReleasesDependents(t *testing.T) {
	bus := event.New()
	plan := Plan{ID: "p", Goal: "two todos", Todos: []Todo{
		{ID: "todo_A", Title: "first", Assignee: "coder", Status: Pending},
		{ID: "todo_B", Title: "second", Assignee: "coder", Status: Pending, DependsOn: []string{"todo_A"}},
	}}
	runner := &scriptedRunner{
		bus:      bus,
		failRole: map[Role]bool{RoleCoder: true},
		onlyTodo: map[string]bool{"todo_A": true}, // only A's coder refuses
		diff:     "d", verdict: true, testPass: true,
	}
	o, err := runFailurePlan(t, plan, bus, bus, runner)
	if !errors.Is(err, ErrPlanFailed) {
		t.Fatalf("Run = %v, want ErrPlanFailed (todo_A never started)", err)
	}
	if got := o.plan.StatusOf("todo_A"); got != Failed {
		t.Errorf("todo_A status = %v, want Failed", got)
	}
	if got := o.plan.StatusOf("todo_B"); got != Done {
		t.Errorf("todo_B status = %v, want Done (a failed dependency releases its dependents)", got)
	}
	if failed := o.plan.FailedTodos(); len(failed) != 1 || failed[0] != "todo_A" {
		t.Errorf("FailedTodos = %v, want [todo_A]", failed)
	}
}

// TestMergeFailureIsReported: every todo reached Done but the Verifier rejected
// the merged patch. The run did not succeed, so Run must say so — the merge
// error used to go only into the plan.done summary and never to the caller.
func TestMergeFailureIsReported(t *testing.T) {
	bus := event.New()
	runner := &scriptedRunner{bus: bus, diff: "diff-A", verdict: true, testPass: true}
	o := NewOrchestrator(Config{MaxReworkCycles: 3, Concurrency: 1},
		fakePlanner{plan: onePlan(), mode: Multi}, bus, bus, runner)
	o.Verifier = errVerifier{}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := o.Run(ctx, "implement X")
	_ = bus.Close()

	if err == nil {
		t.Fatalf("Run = nil, want the verifier's error (the merged patch did not verify)")
	}
	if !strings.Contains(err.Error(), "verifier crashed") {
		t.Errorf("Run error %q does not carry the verifier's cause", err)
	}
	// The todo itself genuinely passed; only the merge failed.
	if got := o.plan.StatusOf("todo_A"); got != Done {
		t.Errorf("todo_A status = %v, want Done", got)
	}
}

// TestCancelFailsEveryOutstandingTodo: a canceled run leaves nothing Pending.
// Pending todos will never be dispatched, so reporting them as still-pending
// would make the plan neither done nor failed.
func TestCancelFailsEveryOutstandingTodo(t *testing.T) {
	bus := event.New()
	plan := Plan{ID: "p", Goal: "two todos", Todos: []Todo{
		{ID: "todo_A", Title: "first", Assignee: "coder", Status: Pending},
		{ID: "todo_B", Title: "second", Assignee: "coder", Status: Pending, DependsOn: []string{"todo_A"}},
	}}
	o := NewOrchestrator(Config{MaxReworkCycles: 3, Concurrency: 1},
		fakePlanner{plan: plan, mode: Multi}, bus, bus, muteRunner{})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := o.Run(ctx, plan.Goal)
	_ = bus.Close()

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run = %v, want the ctx error", err)
	}
	for _, id := range []string{"todo_A", "todo_B"} {
		if got := o.plan.StatusOf(id); got != Failed {
			t.Errorf("todo %s status = %v after cancel, want Failed", id, got)
		}
	}
}
