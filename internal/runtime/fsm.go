// Package runtime implements Layer 2 — the finite state machine that decides
// where the agent is at every instant (File 04). This file owns the FSM
// itself: the 13 states, the 20-entry transition table (§4.2), the invariants
// (§4.2.1), and the transition guard. The drive loop (§4.3) is layered on top
// in drive.go.
//
// The FSM is deliberately framework-free: it holds a current State and resolves
// transitions by (current state, signal), with a StateAny wildcard for the
// catch-all edges (T16–T19). It owns no goroutine and no I/O — it is pure
// control flow, which makes the transition table unit-testable in isolation.

package runtime

// State is one operational state of a task's FSM (File 04 §4.1.1).
type State string

const (
	StateInit        State = "INIT"
	StateLoadSession State = "LOAD_SESSION"
	StateLoadContext State = "LOAD_CONTEXT"
	StatePlan        State = "PLAN"
	StateExecute     State = "EXECUTE"
	StateWaitTool    State = "WAIT_TOOL"
	StateVerify      State = "VERIFY"
	StatePatch       State = "PATCH"
	StateDone        State = "DONE"
	StatePaused      State = "PAUSED"
	StateCancelled   State = "CANCELLED"
	StateError       State = "ERROR"
	StateWaitUser    State = "WAIT_USER"

	// StateAny is the wildcard `from` for catch-all transitions T16–T19
	// (pause/cancel/error can fire from almost any state). A specific (from,
	// signal) edge wins over a StateAny edge.
	StateAny State = "*"
)

// IsTerminal reports whether a state admits no further transitions (the only
// exit from a terminal state is via the ERROR→INIT recovery, T20, which is
// itself gated on the ERROR state, not these).
func (s State) IsTerminal() bool {
	switch s {
	case StateDone, StateCancelled:
		return true
	}
	return false
}

// Signal is the trigger that advances the FSM (File 04 §4.2, "Event/Signal"
// column). It is distinct from a bus event topic: the drive loop translates bus
// events and layer results into the FSM's signals.
type Signal string

const (
	SigStartTask        Signal = "start_task"
	SigSessionLoaded    Signal = "session_loaded"
	SigContextBuilt     Signal = "context_built"
	SigPlannerAnswer    Signal = "planner_answer"
	SigPlannerToolCall  Signal = "planner_tool_call"
	SigNeedsApproval    Signal = "needs_approval"
	SigDispatched       Signal = "dispatched"
	SigUserApprove      Signal = "user_approve"
	SigUserReject       Signal = "user_reject"
	SigObservation      Signal = "observation"
	SigVerifyPassMore   Signal = "verify_pass_more"
	SigVerifyPassDone   Signal = "verify_pass_done"
	SigVerifyFailPatch  Signal = "verify_fail_patch"
	SigVerifyFailReplan Signal = "verify_fail_replan"
	SigPatchApplied     Signal = "patch_applied"
	SigUserPause        Signal = "user_pause"
	SigUserResume       Signal = "user_resume"
	SigUserCancel       Signal = "user_cancel"
	SigHardError        Signal = "hard_error"
	SigUserAckError     Signal = "user_ack_error"
)

// edge is one row of the transition table (File 04 §4.2): from a state, on a
// signal, to a state.
type edge struct {
	From   State
	Signal Signal
	To     State
}

// transitionTable returns the complete T1–T20 transition table (File 04 §4.2).
// Order does not matter for dispatch (lookup matches by key), but the table is
// written in spec order for readability.
func transitionTable() []edge {
	return []edge{
		{StateInit, SigStartTask, StateLoadSession},            // T1
		{StateLoadSession, SigSessionLoaded, StateLoadContext}, // T2
		{StateLoadContext, SigContextBuilt, StatePlan},         // T3
		{StatePlan, SigPlannerAnswer, StateDone},               // T4
		{StatePlan, SigPlannerToolCall, StateExecute},          // T5
		{StateExecute, SigNeedsApproval, StateWaitUser},        // T6
		{StateExecute, SigDispatched, StateWaitTool},           // T7
		{StateWaitUser, SigUserApprove, StateExecute},          // T8
		{StateWaitUser, SigUserReject, StateCancelled},         // T9
		{StateWaitTool, SigObservation, StateVerify},           // T10
		{StateVerify, SigVerifyPassMore, StatePlan},            // T11
		{StateVerify, SigVerifyPassDone, StateDone},            // T12
		{StateVerify, SigVerifyFailPatch, StatePatch},          // T13
		{StateVerify, SigVerifyFailReplan, StatePlan},          // T14
		{StatePatch, SigPatchApplied, StateVerify},             // T15
		{StateAny, SigUserPause, StatePaused},                  // T16
		{StatePaused, SigUserResume, StateLoadContext},         // T17
		{StateAny, SigUserCancel, StateCancelled},              // T18
		{StateAny, SigHardError, StateError},                   // T19
		{StateError, SigUserAckError, StateInit},               // T20
	}
}

// allStates returns every concrete operational state (StateAny excluded) in a
// stable order.
func allStates() []State {
	return []State{
		StateInit, StateLoadSession, StateLoadContext, StatePlan, StateExecute,
		StateWaitTool, StateVerify, StatePatch, StateDone, StatePaused,
		StateCancelled, StateError, StateWaitUser,
	}
}

// fsm is a pure state holder + transition resolver. The drive loop owns one
// per task. It is not safe for concurrent use — invariant I1 (File 04
// §4.2.1): only the runtime goroutine mutates state.
type fsm struct {
	cur State
}

func newFSM(initial State) *fsm { return &fsm{cur: initial} }

func (f *fsm) current() State { return f.cur }

// lookup resolves the destination state for a (state, signal) pair. A specific
// `from` edge wins over a StateAny wildcard edge; if neither matches, ok is
// false.
func (f *fsm) lookup(state State, sig Signal) (State, bool) {
	// Specific edge first.
	for _, e := range transitionTable() {
		if e.From == state && e.Signal == sig {
			return e.To, true
		}
	}
	// Then the StateAny wildcard.
	for _, e := range transitionTable() {
		if e.From == StateAny && e.Signal == sig {
			return e.To, true
		}
	}
	return "", false
}

// transition advances the FSM on sig, applying the matched edge. It returns
// the (from, to) it applied so the drive loop can publish state.change.
//
// If no edge resolves from the current state (including signals into a terminal
// state), it returns ErrNoTransition — a *recoverable* dispatch miss. The
// drive loop logs and drops these; a true invariant violation (a stale event
// for an older task, I2) is the drive loop's responsibility to detect, not the
// FSM's, since the FSM only knows its own single state.
func (f *fsm) transition(sig Signal, _ string) (from, to State, err error) {
	next, ok := f.lookup(f.cur, sig)
	if !ok {
		return f.cur, "", ErrNoTransition
	}
	from, to = f.cur, next
	f.cur = next
	return from, to, nil
}

// ErrNoTransition means no table edge resolves from the current state on the
// given signal. Terminal states return this for every signal (they have no
// outgoing edges), so a drive loop that fires into a terminal state can detect
// it and stop cleanly.
var ErrNoTransition = errStr("runtime: no transition from current state")

type errString struct{ s string }

func errStr(s string) error        { return &errString{s} }
func (e *errString) Error() string { return e.s }
