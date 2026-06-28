// Package coord implements Layer 11 — the coordination layer: an
// orchestrator that decomposes complex tasks across specialist agents and
// merges their work (File 12).
//
// Allowed imports: event, cognitive, exec, verify, patch, memory, infra.
package coord

import (
	"time"
)

// TodoStatus is the lifecycle of a todo within a Plan (File 12 §12.2.2).
// Pending → InProgress → (Done | Failed); Blocked is set by the scheduler
// when a dependency is unmet.
type TodoStatus int

const (
	// Pending is the initial status: not yet dispatched.
	Pending TodoStatus = iota
	// InProgress: the orchestrator has dispatched this todo to an agent.
	InProgress
	// Blocked: a DependsOn todo is not yet Done (scheduler holds the todo).
	Blocked
	// Done: the todo completed (reviewer approved + tester passed).
	Done
	// Failed: the rework cap was exceeded, or the todo was cancelled.
	Failed
)

// Plan is the decomposed goal (File 12 §12.2.2). The typed Plan lives in
// internal/coord; on the wire, PlanReadyEvent.Plan is json.RawMessage (the
// event contract, File 05, is unchanged). The orchestrator marshals the
// typed Plan to RawMessage when publishing coord.plan.ready.
type Plan struct {
	ID        string
	Goal      string
	Todos     []Todo
	CreatedAt time.Time
}

// Todo is one unit of work in a Plan (File 12 §12.2.2). DependsOn encodes the
// DAG so independent todos run concurrently and dependent ones serialize.
// ReworkCycles is incremented by reassignCoder up to MaxReworkCycles (File
// 12 §12.4.1).
type Todo struct {
	ID           string
	Title        string
	Acceptance   string
	DependsOn    []string
	Status       TodoStatus
	Assignee     string
	Artifacts    []string
	ReworkCycles int
}

// AllDone reports whether every todo is terminal (Done OR Failed). The
// orchestrator merges and reports when AllDone is true (File 12 §12.6). An
// empty plan is trivially done.
func (p *Plan) AllDone() bool {
	for i := range p.Todos {
		s := p.Todos[i].Status
		if s != Done && s != Failed {
			return false
		}
	}
	return true
}

// Todo returns a pointer to the todo with the given ID, or nil if absent.
// Callers mutate the returned todo in place (File 12 §12.4.1 reassignCoder).
func (p *Plan) Todo(id string) *Todo {
	for i := range p.Todos {
		if p.Todos[i].ID == id {
			return &p.Todos[i]
		}
	}
	return nil
}

// StatusOf returns the status of the todo with the given ID, or Pending if
// absent. The scheduler's depsMet treats an unknown dependency as not-done so
// a typo never silently unblocks a dependent.
func (p *Plan) StatusOf(id string) TodoStatus {
	if t := p.Todo(id); t != nil {
		return t.Status
	}
	return Pending
}
