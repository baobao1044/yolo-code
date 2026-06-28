// planner.go — the heuristic planner (File 12 §12.2.1).
//
// Sprint 10 uses a deterministic heuristic planner that splits a complex
// goal on "and"/commas into a Plan of Todos. This makes tests reproducible
// (no LLM). The LLM-driven Planner (read-only Read/Grep/Glob tools, File 12
// §12.2.1) is the integration sprint (spec gap, logged).
//
// Each todo's Assignee defaults to "coder" (the Coder implements a todo;
// Reviewer/Tester are spawned by the orchestrator, not assigned per-todo).
// Status defaults to Pending; ReworkCycles to 0.

package coord

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strconv"
	"strings"
	"time"
)

// heuristicPlanner implements Planner with a deterministic split. It is the
// Sprint 10 stand-in for the LLM Planner.
type heuristicPlanner struct{}

// Plan splits goal on "and"/commas into ≥1 todo. If the goal classifies as
// Multi (≥3 clauses, or /plan), the plan carries ≥3 todos (the L11-001 exit
// bar). For a Single goal, a single todo wraps the whole goal (the fallback
// path; routing decides whether to orchestrate).
func (heuristicPlanner) Plan(_ context.Context, goal string) (Plan, Mode, error) {
	mode := Classify(goal)
	clauses := splitClauses(goal)
	if len(clauses) == 0 {
		clauses = []string{goal}
	}
	todos := make([]Todo, 0, len(clauses))
	for i, c := range clauses {
		t := strings.TrimSpace(c)
		if t == "" {
			continue
		}
		todos = append(todos, Todo{
			ID:       newTodoID(i),
			Title:    t,
			Assignee: "coder",
			Status:   Pending,
		})
	}
	if len(todos) == 0 {
		todos = []Todo{{ID: newTodoID(0), Title: goal, Assignee: "coder", Status: Pending}}
	}
	return Plan{
		ID:        newPlanID(),
		Goal:      goal,
		Todos:     todos,
		CreatedAt: time.Now(),
	}, mode, nil
}

// splitClauses breaks a goal into clauses on commas and " and ".
func splitClauses(goal string) []string {
	parts := splitAnd(strings.Split(goal, ","))
	var out []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// splitAnd flattens a comma-split list by also splitting each part on " and ".
func splitAnd(parts []string) []string {
	var out []string
	for _, p := range parts {
		for _, s := range strings.Split(p, " and ") {
			out = append(out, s)
		}
	}
	return out
}

// newPlanID returns a short hex ID for a plan. crypto/rand keeps IDs unique
// across plans in the same process.
func newPlanID() string {
	return "plan_" + randHex(4)
}

// newTodoID returns a stable, index-based todo ID.
func newTodoID(i int) string {
	return "todo_" + strconv.Itoa(i)
}

// randHex returns n bytes of hex (2n hex chars).
func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
