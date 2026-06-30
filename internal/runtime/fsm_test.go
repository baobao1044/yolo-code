package runtime

import (
	"testing"
)

// allStates lists every state the FSM can be in (File 04 §4.1.1). The ticket
// says "12 states"; the table in §4.1.1 actually defines 13 (the off-by-one is
// in the brief, not the spec). This test pins the real count so a missing
// state is caught.
func TestAllStatesCoversTheCatalog(t *testing.T) {
	got := allStates()
	// 13 operational states: INIT, LOAD_SESSION, LOAD_CONTEXT, PLAN, EXECUTE,
	// WAIT_TOOL, VERIFY, PATCH, DONE, PAUSED, CANCELLED, ERROR, WAIT_USER.
	if len(got) != 13 {
		t.Fatalf("allStates has %d states, want 13 (File 04 §4.1.1)", len(got))
	}
	want := []State{
		StateInit, StateLoadSession, StateLoadContext, StatePlan, StateExecute,
		StateWaitTool, StateVerify, StatePatch, StateDone, StatePaused,
		StateCancelled, StateError, StateWaitUser,
	}
	for i, s := range want {
		if got[i] != s {
			t.Errorf("allStates[%d] = %q, want %q", i, got[i], s)
		}
	}
}

func TestTerminalStates(t *testing.T) {
	for _, s := range []State{StateDone, StateCancelled} {
		if !s.IsTerminal() {
			t.Errorf("%q should be terminal", s)
		}
	}
	for _, s := range []State{StateInit, StatePlan, StateVerify, StatePaused, StateError, StateWaitUser} {
		if s.IsTerminal() {
			t.Errorf("%q should NOT be terminal", s)
		}
	}
}

// TestTransitionTableCoversAll21Edges is the L2-001 headline: the table must
// contain every transition T1–T21 from File 04 §4.2, no more, no less. A
// missing or extra edge is an FSM contract regression. (T21 — EXECUTE→PLAN on
// turn_done — was added so a Planner turn whose tool calls are all dispatched
// hands back to PLAN instead of dead-ending.)
func TestTransitionTableCoversAll21Edges(t *testing.T) {
	table := transitionTable()
	if len(table) != 21 {
		t.Fatalf("transition table has %d edges, want 21 (T1–T21)", len(table))
	}
	// Every edge must be uniquely keyed by (from, signal) — the FSM dispatches
	// on (current state, incoming signal), so duplicates are ambiguous.
	seen := map[string]int{}
	for _, e := range table {
		key := string(e.From) + "|" + string(e.Signal)
		seen[key]++
		if seen[key] > 1 {
			t.Errorf("duplicate edge from %q on signal %q — dispatch is ambiguous", e.From, e.Signal)
		}
	}
}

// TestTransitionEdgesMatchSpec asserts each T1–T21 edge has the (from, signal,
// to) triple the spec fixes. A drift here changes the agent's observable
// behavior, so it is caught explicitly.
func TestTransitionEdgesMatchSpec(t *testing.T) {
	want := []struct {
		name     string
		from, to State
		signal   Signal
	}{
		{"T1", StateInit, StateLoadSession, SigStartTask},
		{"T2", StateLoadSession, StateLoadContext, SigSessionLoaded},
		{"T3", StateLoadContext, StatePlan, SigContextBuilt},
		{"T4", StatePlan, StateDone, SigPlannerAnswer},
		{"T5", StatePlan, StateExecute, SigPlannerToolCall},
		{"T6", StateExecute, StateWaitUser, SigNeedsApproval},
		{"T7", StateExecute, StateWaitTool, SigDispatched},
		{"T8", StateWaitUser, StateExecute, SigUserApprove},
		{"T9", StateWaitUser, StateCancelled, SigUserReject},
		{"T10", StateWaitTool, StateVerify, SigObservation},
		{"T11", StateVerify, StatePlan, SigVerifyPassMore},
		{"T12", StateVerify, StateDone, SigVerifyPassDone},
		{"T13", StateVerify, StatePatch, SigVerifyFailPatch},
		{"T14", StateVerify, StatePlan, SigVerifyFailReplan},
		{"T15", StatePatch, StateVerify, SigPatchApplied},
		{"T16", StateAny, StatePaused, SigUserPause},
		{"T17", StatePaused, StateLoadContext, SigUserResume},
		{"T18", StateAny, StateCancelled, SigUserCancel},
		{"T19", StateAny, StateError, SigHardError},
		{"T20", StateError, StateInit, SigUserAckError},
		{"T21", StateExecute, StatePlan, SigTurnDone},
	}
	table := transitionTable()
	got := make(map[string]edge, len(table))
	for _, e := range table {
		got[string(e.From)+"|"+string(e.Signal)] = e
	}
	for _, w := range want {
		key := string(w.from) + "|" + string(w.signal)
		e, ok := got[key]
		if !ok {
			t.Errorf("%s: missing edge from %q on %q", w.name, w.from, w.signal)
			continue
		}
		if e.To != w.to {
			t.Errorf("%s: from %q on %q → %q, want %q", w.name, w.from, w.signal, e.To, w.to)
		}
	}
}

// TestLookupResolvesEdgeByStateAndSignal verifies the FSM resolves a transition
// by (current state, signal), with StateAny matching every non-specialized
// state. This is the dispatch the drive loop relies on.
func TestLookupResolvesEdgeByStateAndSignal(t *testing.T) {
	fsm := newFSM(StateInit)

	// Specific edge wins over the StateAny wildcard.
	next, ok := fsm.lookup(StateVerify, SigVerifyPassDone)
	if !ok || next != StateDone {
		t.Errorf("lookup(VERIFY, verify_pass_done) = (%q, %v), want (DONE, true)", next, ok)
	}

	// StateAny wildcard: cancel applies from any state.
	next, ok = fsm.lookup(StatePlan, SigUserCancel)
	if !ok || next != StateCancelled {
		t.Errorf("lookup(PLAN, user_cancel) = (%q, %v), want (CANCELLED, true) via StateAny", next, ok)
	}
	next, ok = fsm.lookup(StateVerify, SigUserCancel)
	if !ok || next != StateCancelled {
		t.Errorf("lookup(VERIFY, user_cancel) = (%q, %v), want (CANCELLED, true) via StateAny", next, ok)
	}

	// Unknown (state, signal) → no edge.
	if _, ok := fsm.lookup(StateDone, SigStartTask); ok {
		t.Error("lookup(DONE, start_task) returned an edge; DONE is terminal")
	}
}

// TestFSMTransitionAppliesAndReturnsTo confirms a transition mutates the current
// state and reports the (from, to, why) it applied — the drive loop uses these
// to publish state.change.
func TestFSMTransitionAppliesAndReturnsTo(t *testing.T) {
	fsm := newFSM(StateInit)
	from, to, err := fsm.transition(SigStartTask, "start")
	if err != nil {
		t.Fatalf("transition: %v", err)
	}
	if from != StateInit || to != StateLoadSession {
		t.Errorf("transition(start) = (%q→%q), want INIT→LOAD_SESSION", from, to)
	}
	if fsm.current() != StateLoadSession {
		t.Errorf("current = %q, want LOAD_SESSION after transition", fsm.current())
	}
}

// TestFSMTransitionFromWrongStateReturnsNoTransition verifies a signal that has
// no edge from the current state returns ErrNoTransition (recoverable) rather
// than panicking. A true invariant violation (a stale event for an older task,
// I2) is the drive loop's concern, not the FSM's.
func TestFSMTransitionFromWrongStateReturnsNoTransition(t *testing.T) {
	fsm := newFSM(StatePlan)
	_, _, err := fsm.transition(SigStartTask, "wrong") // INIT→LOAD_SESSION, but we're in PLAN
	if err != ErrNoTransition {
		t.Errorf("transition from wrong state: err = %v, want ErrNoTransition", err)
	}
	if fsm.current() != StatePlan {
		t.Errorf("after failed transition, current = %q, want unchanged PLAN", fsm.current())
	}
}

// TestFSMIgnoresTerminalTransitions verifies a terminal state never transitions
// further (the only exit from terminal is the ERROR→INIT recovery, which is
// itself gated on the ERROR state, not these).
func TestFSMIgnoresTerminalTransitions(t *testing.T) {
	fsm := newFSM(StateDone)
	_, _, err := fsm.transition(SigStartTask, "noop")
	if err != ErrNoTransition {
		t.Errorf("transition from DONE: err = %v, want ErrNoTransition", err)
	}
	if fsm.current() != StateDone {
		t.Errorf("terminal DONE transitioned to %q; terminal states must not move", fsm.current())
	}
}

// TestFSMExecuteTurnDoneGoesToPlan is the regression test for the missing
// EXECUTE→PLAN edge. When a Planner turn's tool calls are all dispatched,
// the drive loop fires SigTurnDone (T21) to hand back to PLAN for the next
// turn. Without T21 the loop dead-ended: SigPlannerAnswer has no edge from
// EXECUTE (only PLAN→DONE, T4), so transition returned ErrNoTransition and
// the drive loop terminated the task mid-agent-loop. This test pins both the
// fix (turn_done resolves) and the old behavior (planner_answer from EXECUTE
// is now correctly a no-transition, documenting why the signal changed).
func TestFSMExecuteTurnDoneGoesToPlan(t *testing.T) {
	fsm := newFSM(StateExecute)

	// T21: EXECUTE --turn_done--> PLAN.
	from, to, err := fsm.transition(SigTurnDone, "turn_done")
	if err != nil {
		t.Fatalf("transition(EXECUTE, turn_done): %v, want nil (T21)", err)
	}
	if from != StateExecute || to != StatePlan {
		t.Errorf("transition(EXECUTE, turn_done) = (%q→%q), want EXECUTE→PLAN", from, to)
	}
	if fsm.current() != StatePlan {
		t.Errorf("current = %q, want PLAN after T21", fsm.current())
	}

	// The OLD signal from EXECUTE must now return ErrNoTransition — the only
	// SigPlannerAnswer edge is PLAN→DONE (T4), so firing it from EXECUTE was
	// the bug. This documents the fix.
	bad := newFSM(StateExecute)
	_, _, err = bad.transition(SigPlannerAnswer, "turn_done")
	if err != ErrNoTransition {
		t.Errorf("transition(EXECUTE, planner_answer): err = %v, want ErrNoTransition (no edge — T4 is PLAN→DONE only)", err)
	}
	if bad.current() != StateExecute {
		t.Errorf("after failed transition, current = %q, want unchanged EXECUTE", bad.current())
	}
}
