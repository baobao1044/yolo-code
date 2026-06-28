// scheduler.go — the Scheduler + Task Queue + DAG ordering (File 12 §12.5).
//
// The scheduler dispatches todos whose DependsOn are all terminal (Done or
// Failed), never re-dispatches an inflight todo, and bounds concurrency so a
// 50-todo plan doesn't spawn 50 agent conversations (File 12 §12.5.1). Todos
// with no unmet DependsOn dispatch in parallel, each to its own Coder.
//
// Spec note (File 12 §12.5.1): a Failed dependency releases its dependents —
// the orchestrator decides downstream whether a dependent can proceed without
// the failed todo. The scheduler's job is ordering, not policy; depsMet treats
// Done and Failed as "terminal → dependency resolved".

package coord

// Scheduler owns the Plan + the inflight set + a concurrency bound. It is the
// File 12 §12.5 "Task Queue" lifted to a pure dispatcher: DispatchReady /
// MarkDone / MarkFailed are the only mutation points.
type Scheduler struct {
	plan     *Plan
	inflight map[string]bool
	bound    int
}

// NewScheduler wraps a Plan with a concurrency bound. bound ≤ 0 means
// unbounded (use runtime.NumCPU at the composition root for the real
// default; tests pass an explicit value).
func NewScheduler(plan *Plan, bound int) *Scheduler {
	if bound < 0 {
		bound = 0
	}
	return &Scheduler{
		plan:     plan,
		inflight: make(map[string]bool),
		bound:    bound,
	}
}

// DispatchReady dispatches every ready, non-inflight Pending todo (in plan
// order) via spawn, marking each InProgress and inflight. Respects the
// concurrency bound: once `bound` todos are inflight, no further todo is
// dispatched this call. Idempotent within a call: a todo already inflight is
// never re-spawned.
func (s *Scheduler) DispatchReady(spawn func(*Todo)) {
	for i := range s.plan.Todos {
		t := &s.plan.Todos[i]
		if t.Status != Pending {
			continue
		}
		if s.inflight[t.ID] {
			continue
		}
		if !s.depsMet(t) {
			continue
		}
		if s.bound > 0 && s.inflightCount() >= s.bound {
			return // bound reached; wait for a MarkDone/MarkFailed to free a slot
		}
		t.Status = InProgress
		s.inflight[t.ID] = true
		spawn(t)
	}
}

// MarkDone marks the todo Done, frees its inflight slot, and re-dispatches
// dependents that became ready (File 12 §12.5).
func (s *Scheduler) MarkDone(id string, spawn func(*Todo)) {
	s.markTerminal(id, Done, spawn)
}

// MarkFailed marks the todo Failed, frees its inflight slot, and re-dispatches
// dependents. A Failed dependency releases its dependents (the scheduler does
// not block downstream on a failed todo — the orchestrator decides policy).
func (s *Scheduler) MarkFailed(id string, spawn func(*Todo)) {
	s.markTerminal(id, Failed, spawn)
}

// markTerminal is the shared Done/Failed path: set the status, free the slot,
// re-dispatch.
func (s *Scheduler) markTerminal(id string, status TodoStatus, spawn func(*Todo)) {
	t := s.plan.Todo(id)
	if t == nil {
		return
	}
	t.Status = status
	delete(s.inflight, id)
	s.DispatchReady(spawn)
}

// depsMet reports whether every DependsOn is terminal (Done OR Failed). An
// unknown dependency is treated as Pending (not done) via Plan.StatusOf, so a
// typo never silently unblocks a dependent.
func (s *Scheduler) depsMet(t *Todo) bool {
	for _, dep := range t.DependsOn {
		st := s.plan.StatusOf(dep)
		if st != Done && st != Failed {
			return false
		}
	}
	return true
}

// inflightCount returns the number of currently-dispatched todos.
func (s *Scheduler) inflightCount() int {
	return len(s.inflight)
}
