// Real Planner adapter for the coord orchestrator (Sprint 12 INT-007).
// This is a heuristic planner: it uses the existing coord.Classify ruleset to
// decide Single vs Multi and splits multi-clause goals into todos.
// Single-agent goals still bypass the orchestrator (the routing decision lives
// in cmd/yolo/main.go, INT-008).

package main

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/yolo-code/yolo/internal/coord"
)

// heuristicPlanner implements coord.Planner. It is stateful only to mint
// monotonic plan IDs within a process.
type heuristicPlanner struct {
	n atomic.Uint64
}

// Plan decomposes goal into a Plan + Mode using coord.Classify.
// Multi-clause goals are split on commas / "and"; otherwise one todo covers
// the whole goal.
func (p *heuristicPlanner) Plan(ctx context.Context, goal string) (coord.Plan, coord.Mode, error) {
	mode := coord.Classify(goal)
	items := clauses(goal, mode)
	pid := p.n.Add(1)

	plan := coord.Plan{
		ID:        fmt.Sprintf("plan-%d", pid),
		Goal:      goal,
		CreatedAt: time.Now().UTC(),
		Todos:     make([]coord.Todo, len(items)),
	}
	for i, title := range items {
		plan.Todos[i] = coord.Todo{
			ID:        fmt.Sprintf("%s-t%d", plan.ID, i+1),
			Title:     title,
			Assignee:  string(coord.RoleCoder),
			Status:    coord.Pending,
			Artifacts: artifactsFromTitle(title),
		}
	}
	return plan, mode, nil
}

// clauses splits a goal into actionable titles. For Multi mode, normalize
// ", and " to ", " then split on commas and " and ". Also strip explicit
// routing prefixes like "/plan " so they don't leak into todo titles.
func clauses(goal string, mode coord.Mode) []string {
	goal = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(goal, "/plan "), "/agent "))
	if mode != coord.Multi {
		return []string{goal}
	}
	goal = strings.ReplaceAll(goal, ", and ", ", ")
	var out []string
	for _, raw := range strings.Split(goal, ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		for _, clause := range strings.Split(raw, " and ") {
			clause = strings.TrimSpace(clause)
			if clause != "" {
				out = append(out, clause)
			}
		}
	}
	if len(out) == 0 {
		out = append(out, goal)
	}
	return out
}

// artifactsFromTitle extracts a likely file path from a todo title. It looks
// for the first whitespace-delimited token containing a dot.
func artifactsFromTitle(title string) []string {
	for _, w := range strings.Fields(title) {
		w = strings.Trim(w, ".,;:!?")
		if strings.Contains(w, ".") {
			return []string{w}
		}
	}
	return nil
}
