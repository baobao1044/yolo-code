// Tests for L11-002 — the Scheduler + Task Queue + DAG ordering (File 12
// §12.5). The scheduler dispatches todos whose DependsOn are all Done, never
// re-dispatches an inflight todo, and bounds concurrency. MarkDone /
// MarkFailed re-dispatch dependents.

package coord

import (
	"testing"
)

// linearPlan builds A → B → C (B depends on A; C depends on B).
func linearPlan() *Plan {
	return &Plan{ID: "p", Todos: []Todo{
		{ID: "A", Status: Pending, Assignee: "coder"},
		{ID: "B", Status: Pending, Assignee: "coder", DependsOn: []string{"A"}},
		{ID: "C", Status: Pending, Assignee: "coder", DependsOn: []string{"B"}},
	}}
}

// parallelPlan builds A → {B, C} → D (B, C depend on A; D depends on B and C).
func parallelPlan() *Plan {
	return &Plan{ID: "p", Todos: []Todo{
		{ID: "A", Status: Pending, Assignee: "coder"},
		{ID: "B", Status: Pending, Assignee: "coder", DependsOn: []string{"A"}},
		{ID: "C", Status: Pending, Assignee: "coder", DependsOn: []string{"A"}},
		{ID: "D", Status: Pending, Assignee: "coder", DependsOn: []string{"B", "C"}},
	}}
}

// TestSchedulerLinearDAG: A→B→C dispatches in order. With bound 1, only A is
// ready; after MarkDone(A) only B; after MarkDone(B) only C.
func TestSchedulerLinearDAG(t *testing.T) {
	s := NewScheduler(linearPlan(), 1)
	var spawned []string
	spawn := func(td *Todo) { spawned = append(spawned, td.ID) }

	s.DispatchReady(spawn)
	if !equalIDs(spawned, []string{"A"}) {
		t.Errorf("first dispatch = %v, want [A]", spawned)
	}
	s.MarkDone("A", spawn)
	if !equalIDs(spawned, []string{"A", "B"}) {
		t.Errorf("after A done = %v, want [A B]", spawned)
	}
	s.MarkDone("B", spawn)
	if !equalIDs(spawned, []string{"A", "B", "C"}) {
		t.Errorf("after B done = %v, want [A B C]", spawned)
	}
}

// TestSchedulerParallelDAG: A → {B, C} → D. With bound 2, after A done both
// B and C dispatch; after both done, D dispatches.
func TestSchedulerParallelDAG(t *testing.T) {
	s := NewScheduler(parallelPlan(), 2)
	var spawned []string
	spawn := func(td *Todo) { spawned = append(spawned, td.ID) }

	s.DispatchReady(spawn)
	if !equalIDs(spawned, []string{"A"}) {
		t.Errorf("first dispatch = %v, want [A]", spawned)
	}
	s.MarkDone("A", spawn)
	// B and C are both ready now; order within a dispatch is the plan order.
	if !equalIDs(spawned, []string{"A", "B", "C"}) {
		t.Errorf("after A done = %v, want [A B C]", spawned)
	}
	s.MarkDone("B", spawn)
	if !equalIDs(spawned, []string{"A", "B", "C"}) {
		t.Errorf("D must NOT dispatch until both B and C done, got %v", spawned)
	}
	s.MarkDone("C", spawn)
	if !equalIDs(spawned, []string{"A", "B", "C", "D"}) {
		t.Errorf("after B and C done = %v, want [A B C D]", spawned)
	}
}

// TestSchedulerBlockedDependent: a dependent is held until its dependency is
// Done (not just InProgress).
func TestSchedulerBlockedDependent(t *testing.T) {
	p := &Plan{ID: "p", Todos: []Todo{
		{ID: "A", Status: Pending, Assignee: "coder"},
		{ID: "B", Status: Pending, Assignee: "coder", DependsOn: []string{"A"}},
	}}
	s := NewScheduler(p, 2)
	var spawned []string
	spawn := func(td *Todo) { spawned = append(spawned, td.ID) }

	s.DispatchReady(spawn)
	if !equalIDs(spawned, []string{"A"}) {
		t.Errorf("first dispatch = %v, want [A] (B is blocked)", spawned)
	}
	// Calling DispatchReady again does NOT re-dispatch A (inflight) and B is
	// still blocked (A not Done).
	s.DispatchReady(spawn)
	if !equalIDs(spawned, []string{"A"}) {
		t.Errorf("re-dispatch = %v, want still [A] (A inflight, B blocked)", spawned)
	}
}

// TestSchedulerInflightGuard: a todo already inflight is not dispatched again.
func TestSchedulerInflightGuard(t *testing.T) {
	p := &Plan{ID: "p", Todos: []Todo{
		{ID: "A", Status: Pending, Assignee: "coder"},
		{ID: "B", Status: Pending, Assignee: "coder"},
	}}
	s := NewScheduler(p, 4)
	var spawned []string
	spawn := func(td *Todo) { spawned = append(spawned, td.ID) }

	s.DispatchReady(spawn)
	if !equalIDs(spawned, []string{"A", "B"}) {
		t.Errorf("first dispatch = %v, want [A B]", spawned)
	}
	// Re-dispatch: both are inflight, nothing new.
	s.DispatchReady(spawn)
	if !equalIDs(spawned, []string{"A", "B"}) {
		t.Errorf("re-dispatch = %v, want no change (both inflight)", spawned)
	}
}

// TestSchedulerBoundSerializes: bound 1 dispatches at most one todo per
// DispatchReady even when many are ready.
func TestSchedulerBoundSerializes(t *testing.T) {
	p := &Plan{ID: "p", Todos: []Todo{
		{ID: "A", Status: Pending, Assignee: "coder"},
		{ID: "B", Status: Pending, Assignee: "coder"},
		{ID: "C", Status: Pending, Assignee: "coder"},
	}}
	s := NewScheduler(p, 1)
	var spawned []string
	spawn := func(td *Todo) { spawned = append(spawned, td.ID) }

	s.DispatchReady(spawn)
	if !equalIDs(spawned, []string{"A"}) {
		t.Errorf("bound 1 first dispatch = %v, want [A] only", spawned)
	}
	s.MarkDone("A", spawn)
	if !equalIDs(spawned, []string{"A", "B"}) {
		t.Errorf("after A done = %v, want [A B]", spawned)
	}
}

// TestSchedulerMarkFailedReleases: MarkFailed is terminal like MarkDone —
// dependents whose DependsOn includes a Failed todo are unblocked (the plan
// is AllDone once everything is Done or Failed).
func TestSchedulerMarkFailedReleases(t *testing.T) {
	p := &Plan{ID: "p", Todos: []Todo{
		{ID: "A", Status: Pending, Assignee: "coder"},
		{ID: "B", Status: Pending, Assignee: "coder", DependsOn: []string{"A"}},
	}}
	s := NewScheduler(p, 2)
	var spawned []string
	spawn := func(td *Todo) { spawned = append(spawned, td.ID) }

	s.DispatchReady(spawn)
	s.MarkFailed("A", spawn)
	// A failed — B should dispatch (a failed dependency does not block a
	// dependent; the orchestrator decides whether B can proceed without A).
	if !equalIDs(spawned, []string{"A", "B"}) {
		t.Errorf("after A failed = %v, want [A B] (failed dep releases dependent)", spawned)
	}
}

// equalIDs compares two ID slices for order-sensitive equality.
func equalIDs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestSchedulerDispatchReadyPlanOrder: within a single DispatchReady, ready
// todos dispatch in plan order (stable, deterministic).
func TestSchedulerDispatchReadyPlanOrder(t *testing.T) {
	p := &Plan{ID: "p", Todos: []Todo{
		{ID: "Z", Status: Pending, Assignee: "coder"},
		{ID: "Y", Status: Pending, Assignee: "coder"},
		{ID: "X", Status: Pending, Assignee: "coder"},
	}}
	s := NewScheduler(p, 4)
	var spawned []string
	spawn := func(td *Todo) { spawned = append(spawned, td.ID) }
	s.DispatchReady(spawn)
	// Must be plan order Z, Y, X — NOT sorted.
	if !equalIDs(spawned, []string{"Z", "Y", "X"}) {
		t.Errorf("dispatch order = %v, want plan order [Z Y X]", spawned)
	}
	// Sanity: the helper must not silently always return true.
	if equalIDs([]string{"Z"}, []string{"Y"}) {
		t.Fatalf("equalIDs sanity: returned true for [Z] vs [Y]")
	}
}
