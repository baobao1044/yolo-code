// Sprint 10 exit bar — the L11 coordination layer replay test (roadmap
// §15.13.2). Runs the full canonical loop with fake agents + a fake Verifier +
// a fake CostLedger + the real event.Bus, and asserts the §15.13.2
// acceptance criteria:
//
//   - A complex task decomposes into a plan (heuristicPlanner, ≥3 todos).
//   - The Coder writes, the Reviewer critiques, the Tester verifies, the
//     Orchestrator merges, and the merged patch passes the Verifier.
//   - A rework loop hits the cap (MaxReworkCycles=3) and escalates rather
//     than looping forever.
//   - A trivial task stays single-agent (orchestrator NOT started).
//   - The shared budget holds: one NewTask for the plan, all agents share the
//     PlanID.
//
// The replay uses fake agents that publish canned events synchronously; the
// real per-agent drive (cognitive core + exec engine + scoped tools against a
// live repo) is the integration sprint (Decision 1, spec gap). The board
// contract (coord.* → TUI) is pinned by L11-007's cross-package test; this
// test pins the orchestration logic + the merge + the budget + the fallback.

package coord

import (
	"context"
	"testing"
	"time"

	"github.com/yolo-code/yolo/internal/event"
)

// exitRunner is an AgentRunner that publishes canned events per role. The
// reviewer approves (verdict=true) so the happy path completes; the rework
// test uses verdictAlwaysRejectRunner.
type exitRunner struct {
	bus       EventPublisher
	codeReady map[string]string
	verdict   bool
	testPass  bool
}

func (r *exitRunner) Run(ctx context.Context, role Role, task event.TaskAssignEvent) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	switch role {
	case RoleCoder:
		_ = r.bus.Publish(ctx, &event.CodeReadyEvent{
			PlanID: task.PlanID, TodoID: task.TodoID, Diff: r.codeReady[task.TodoID], SelfReport: "done",
		})
	case RoleReviewer:
		_ = r.bus.Publish(ctx, &event.ReviewVerdictEvent{
			PlanID: task.PlanID, TodoID: task.TodoID, Approved: r.verdict,
		})
	case RoleTester:
		_ = r.bus.Publish(ctx, &event.TestReportEvent{
			PlanID: task.PlanID, TodoID: task.TodoID, Passed: r.testPass, Output: "ok",
		})
	}
	return nil
}

// TestSprint10ExitBarComplexRunDecomposesAndMerges: a complex goal decomposes
// into ≥3 todos, each goes Done through the canonical loop, and the merged
// patch passes the Verifier.
func TestSprint10ExitBarComplexRunDecomposesAndMerges(t *testing.T) {
	// Use the heuristic planner (the L11-001 stand-in for the LLM Planner).
	bus := event.New()
	runner := &exitRunner{
		bus: bus, verdict: true, testPass: true,
		codeReady: map[string]string{},
	}
	verifier := fakeVerifier{pass: true}

	// Plan via the heuristic planner; assign distinct artifacts so merge has
	// no conflict.
	hp := heuristicPlanner{}
	plan, mode, err := hp.Plan(context.Background(), "refactor auth, update callers, add tests")
	if err != nil {
		t.Fatalf("heuristic planner: %v", err)
	}
	if mode != Multi {
		t.Fatalf("mode = %v, want Multi (complex goal)", mode)
	}
	if len(plan.Todos) < 3 {
		t.Fatalf("decomposed into %d todos, want ≥3 (exit bar)", len(plan.Todos))
	}
	// Give each todo a distinct artifact file so merge doesn't conflict.
	for i := range plan.Todos {
		plan.Todos[i].Artifacts = []string{"file_" + plan.Todos[i].ID + ".go"}
		runner.codeReady[plan.Todos[i].ID] = "diff-" + plan.Todos[i].ID
	}

	o := NewOrchestrator(Config{MaxReworkCycles: 3, Concurrency: 1},
		fakePlanner{plan: plan, mode: Multi}, bus, bus, runner)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := o.Run(ctx, plan.Goal); err != nil {
		t.Fatalf("orchestrator Run: %v", err)
	}
	_ = bus.Close()

	// Every todo ended Done.
	for i, td := range o.plan.Todos {
		if td.Status != Done {
			t.Errorf("todo[%d] %q status = %v, want Done", i, td.ID, td.Status)
		}
	}

	// Merge the per-todo diffs and re-verify → passes.
	diffs := map[string]string{}
	for _, td := range o.plan.Todos {
		diffs[td.ID] = "diff-" + td.ID
	}
	mp, err := Merge(context.Background(), o.plan, diffs, verifier)
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if !mp.Verified {
		t.Errorf("merged patch Verified = false, want true (verifier passed)")
	}
	if len(mp.Conflicts) != 0 {
		t.Errorf("Conflicts = %v, want empty (distinct files)", mp.Conflicts)
	}
	if mp.Summary.Done != len(o.plan.Todos) {
		t.Errorf("summary.Done = %d, want %d", mp.Summary.Done, len(o.plan.Todos))
	}
}

// TestSprint10ExitBarReworkCapEscalates: a todo the reviewer always rejects
// hits the cap (MaxReworkCycles=3) and escalates to Failed — no infinite loop.
func TestSprint10ExitBarReworkCapEscalates(t *testing.T) {
	bus := event.New()
	runner := &exitRunner{
		bus: bus, verdict: false, testPass: true, // reviewer always rejects
		codeReady: map[string]string{"todo_A": "diff-A"},
	}
	plan := Plan{ID: "p", Goal: "refactor X, add tests, fix CI"}
	plan.Todos = append(plan.Todos, Todo{ID: "todo_A", Title: "implement X", Assignee: "coder", Status: Pending})

	o := NewOrchestrator(Config{MaxReworkCycles: 3, Concurrency: 1},
		fakePlanner{plan: plan, mode: Multi}, bus, bus, runner)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := o.Run(ctx, plan.Goal); err != nil {
		t.Fatalf("orchestrator Run: %v", err)
	}
	_ = bus.Close()

	if o.plan.Todos[0].Status != Failed {
		t.Errorf("todo status = %v, want Failed (rework cap escalated)", o.plan.Todos[0].Status)
	}
	// AllDone is true because Failed is terminal — the plan terminated (the
	// exit bar: it did NOT loop forever).
	if !o.plan.AllDone() {
		t.Errorf("plan not AllDone after cap — the run must terminate, not loop")
	}
}

// TestSprint10ExitBarTrivialStaysSingle: a trivial goal stays single-agent —
// Route returns DelegateSingle, so the orchestrator is never started (no
// plan, no spawn).
func TestSprint10ExitBarTrivialStaysSingle(t *testing.T) {
	for _, goal := range []string{
		"explain this function",
		"what does core.go do",
		"fix this bug in file X",
	} {
		d := Route(Classify(goal))
		if d != DelegateSingle {
			t.Errorf("Route(Classify(%q)) = %v, want DelegateSingle (trivial stays single-agent)", goal, d)
		}
		if ShouldOrchestrate(goal) {
			t.Errorf("ShouldOrchestrate(%q) = true, want false (no coordination tax)", goal)
		}
	}
}

// TestSprint10ExitBarSharedBudgetRegistersOnce: the Budget registers the plan
// exactly once (one NewTask), so all agent events share one ledger entry —
// the §12.6 shared-budget invariant (one agent's spend counts against the
// whole).
func TestSprint10ExitBarSharedBudgetRegistersOnce(t *testing.T) {
	ledger := &fakeCostLedger{deadline: time.Now().Add(1 * time.Hour), deadlineIsSet: true}
	b := NewBudget(event.TaskID("plan_exit"), ledger)
	b.Register()
	b.Register()
	b.Register()
	if ledger.newTaskCalls != 1 {
		t.Errorf("NewTask called %d times, want 1 (one ledger entry per plan)", ledger.newTaskCalls)
	}
	// Under the deadline, dispatch proceeds (the budget doesn't abort).
	if !b.CheckBeforeDispatch() {
		t.Errorf("CheckBeforeDispatch = false, want true (within deadline)")
	}
	b.End()
	if ledger.endTaskCalls != 1 {
		t.Errorf("EndTask called %d times, want 1", ledger.endTaskCalls)
	}
}
