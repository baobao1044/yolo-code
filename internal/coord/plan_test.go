// Tests for L11-001 — the Plan/Todo model (File 12 §12.2.2) + the heuristic
// planner. The typed Plan/Todo live in internal/coord; PlanReadyEvent.Plan
// stays json.RawMessage on the wire (event contract unchanged).

package coord

import (
	"context"
	"testing"
)

// TestPlanAllDone: a plan is AllDone only when every todo is Done OR Failed
// (File 12 §12.6: "When all todos are done (or failed), the orchestrator
// produces a report"). A Pending/InProgress/Blocked todo means not done.
func TestPlanAllDone(t *testing.T) {
	allDone := Plan{Todos: []Todo{{ID: "a", Status: Done}, {ID: "b", Status: Failed}}}
	if !allDone.AllDone() {
		t.Errorf("AllDone() = false, want true (Done + Failed both terminal)")
	}
	notDone := Plan{Todos: []Todo{{ID: "a", Status: Done}, {ID: "b", Status: InProgress}}}
	if notDone.AllDone() {
		t.Errorf("AllDone() = true, want false (InProgress is not terminal)")
	}
	pending := Plan{Todos: []Todo{{ID: "a", Status: Pending}}}
	if pending.AllDone() {
		t.Errorf("AllDone() = true, want false (Pending is not terminal)")
	}
	empty := Plan{}
	if !empty.AllDone() {
		t.Errorf("AllDone() = false, want true (empty plan is trivially done)")
	}
}

// TestPlanTodo: Todo(id) returns a pointer to the todo with that ID, or nil
// if absent. The orchestrator mutates the returned todo's status/rework
// counter in place (File 12 §12.4.1 reassignCoder).
func TestPlanTodo(t *testing.T) {
	p := Plan{Todos: []Todo{{ID: "a", Title: "first"}, {ID: "b", Title: "second"}}}
	a := p.Todo("a")
	if a == nil || a.Title != "first" {
		t.Fatalf("Todo(a) = %+v, want first", a)
	}
	if p.Todo("missing") != nil {
		t.Errorf("Todo(missing) = non-nil, want nil")
	}
}

// TestPlanStatusOf: StatusOf(id) returns the status of the dependency, or
// Pending if absent (the scheduler's depsMet treats an unknown dependency as
// not-done so a typo never silently unblocks).
func TestPlanStatusOf(t *testing.T) {
	p := Plan{Todos: []Todo{{ID: "a", Status: Done}, {ID: "b", Status: Pending}}}
	if p.StatusOf("a") != Done {
		t.Errorf("StatusOf(a) = %v, want Done", p.StatusOf("a"))
	}
	if p.StatusOf("b") != Pending {
		t.Errorf("StatusOf(b) = %v, want Pending", p.StatusOf("b"))
	}
	if p.StatusOf("missing") != Pending {
		t.Errorf("StatusOf(missing) = %v, want Pending (unknown dep = not done)",
			p.StatusOf("missing"))
	}
}

// TestHeuristicPlanner is the L11-001 exit bar: a complex goal decomposes
// into ≥3 todos. The heuristic planner splits on "and"/commas/clauses so the
// test is reproducible (the LLM planner is the integration sprint).
func TestHeuristicPlanner(t *testing.T) {
	p := heuristicPlanner{}
	plan, mode, err := p.Plan(context.Background(), "refactor X, add tests, fix CI")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if mode != Multi {
		t.Errorf("mode = %v, want Multi", mode)
	}
	if len(plan.Todos) < 3 {
		t.Errorf("got %d todos, want ≥3 (exit bar)", len(plan.Todos))
	}
	if plan.Goal != "refactor X, add tests, fix CI" {
		t.Errorf("Goal = %q, want the original goal", plan.Goal)
	}
	if plan.ID == "" {
		t.Errorf("ID is empty, want a generated plan ID")
	}
	if plan.CreatedAt.IsZero() {
		t.Errorf("CreatedAt is zero, want a timestamp")
	}
}

// TestHeuristicPlannerAssignsCoder: each todo's Assignee defaults to "coder"
// (the Coder is the role that implements a todo; Reviewer/Tester are spawned
// by the orchestrator, not assigned per-todo).
func TestHeuristicPlannerAssignsCoder(t *testing.T) {
	p := heuristicPlanner{}
	plan, _, err := p.Plan(context.Background(), "refactor auth, update callers, add tests")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	for _, td := range plan.Todos {
		if td.Assignee != "coder" {
			t.Errorf("todo %q Assignee = %q, want coder", td.ID, td.Assignee)
		}
		if td.Status != Pending {
			t.Errorf("todo %q Status = %v, want Pending", td.ID, td.Status)
		}
	}
}
