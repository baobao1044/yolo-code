// Tests for the heuristic coord Planner adapter (Sprint 12 INT-007).

package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/baobao1044/yolo-code/internal/coord"
	"github.com/baobao1044/yolo-code/internal/event"
)

func TestHeuristicPlannerMultiClause(t *testing.T) {
	p := &heuristicPlanner{}
	plan, mode, err := p.Plan(context.Background(), "refactor X, add tests, and fix CI")
	if err != nil {
		t.Fatalf("Plan = %v, want nil", err)
	}
	if mode != coord.Multi {
		t.Fatalf("mode = %v, want Multi", mode)
	}
	if len(plan.Todos) != 3 {
		t.Fatalf("len(Todos) = %d, want 3; todos = %v", len(plan.Todos), plan.Todos)
	}
	for i, want := range []string{"refactor X", "add tests", "fix CI"} {
		if plan.Todos[i].Title != want {
			t.Errorf("todo %d title = %q, want %q", i, plan.Todos[i].Title, want)
		}
		if plan.Todos[i].Assignee != string(coord.RoleCoder) {
			t.Errorf("todo %d assignee = %q", i, plan.Todos[i].Assignee)
		}
	}
}

func TestHeuristicPlannerSingleClause(t *testing.T) {
	p := &heuristicPlanner{}
	plan, mode, err := p.Plan(context.Background(), "explain core.go")
	if err != nil {
		t.Fatalf("Plan = %v, want nil", err)
	}
	if mode != coord.Single {
		t.Fatalf("mode = %v, want Single", mode)
	}
	if len(plan.Todos) != 1 {
		t.Fatalf("len(Todos) = %d, want 1", len(plan.Todos))
	}
	if plan.Todos[0].Title != "explain core.go" {
		t.Errorf("title = %q", plan.Todos[0].Title)
	}
}

// TestHeuristicPlannerDrivesOrchestrator proves the real planner works with
// the orchestrator and produces the canonical coord.plan.ready + task.assign
// sequence for a multi-clause goal.
func TestHeuristicPlannerDrivesOrchestrator(t *testing.T) {
	bus := event.New()
	rec := newRecordingSub(bus)
	defer rec.close()

	runner := &fakeAgentRunner{
		bus:       bus,
		codeReady: map[string]string{"plan-1-t1": "d1", "plan-1-t2": "d2", "plan-1-t3": "d3"},
		verdict:   true,
		testPass:  true,
	}
	o := coord.NewOrchestrator(coord.Config{Concurrency: 1}, &heuristicPlanner{}, bus, bus, runner)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := o.Run(ctx, "/plan refactor X, add tests, fix CI"); err != nil && !strings.Contains(err.Error(), "context") {
		t.Fatalf("orchestrator Run: %v", err)
	}
	_ = bus.Close()
	time.Sleep(10 * time.Millisecond)

	if len(rec.types) == 0 || rec.types[0] != "coord.plan.ready" {
		t.Fatalf("first event = %v, want coord.plan.ready", rec.types)
	}
	for _, id := range []string{"plan-1-t1", "plan-1-t2", "plan-1-t3"} {
		if seq := rec.byTodo[id]; len(seq) < 2 {
			t.Errorf("todo %s events = %v, want assign+code.ready", id, seq)
		}
	}
}

func TestHeuristicPlannerExplicitPlanMode(t *testing.T) {
	p := &heuristicPlanner{}
	plan, mode, err := p.Plan(context.Background(), "/plan rewrite the store")
	if err != nil {
		t.Fatalf("Plan = %v, want nil", err)
	}
	if mode != coord.Multi {
		t.Fatalf("mode = %v, want Multi", mode)
	}
	if len(plan.Todos) != 1 || plan.Todos[0].Title != "rewrite the store" {
		t.Fatalf("unexpected todos for explicit plan: %v", plan.Todos)
	}
}
