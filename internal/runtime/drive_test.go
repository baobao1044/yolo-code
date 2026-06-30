package runtime

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/baobao1044/yolo-code/internal/event"
	"github.com/baobao1044/yolo-code/internal/session"
)

// newCore wires a runtime Core over a fresh file store, bus, and the StubCognitive
// returning a canned answer. Returns the core + bus so tests capture events.
func newCore(t *testing.T, answer string) (*Core, *event.Bus, session.ID) {
	t.Helper()
	store := session.NewFileStore(filepath.Join(t.TempDir(), "store"))
	bus := event.New()
	t.Cleanup(func() { _ = bus.Close() })
	smgr := session.New(session.Deps{
		Store: store, Bus: bus, Git: session.NewInMemCheckpointer(),
	})
	core := New(Deps{
		Bus:       bus,
		Session:   smgr,
		Cognitive: StubCognitive{Answer: answer},
	})
	sid, err := smgr.OpenSession(context.Background(), "proj", "demo")
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	return core, bus, sid
}

// drain reads up to n envelopes with a short timeout, in arrival order.
func drain(t *testing.T, ch <-chan event.Envelope, n int) []event.Envelope {
	t.Helper()
	got := make([]event.Envelope, 0, n)
	for len(got) < n {
		select {
		case env := <-ch:
			got = append(got, env)
		case <-time.After(time.Second):
			t.Fatalf("timed out draining event %d/%d", len(got), n)
		}
	}
	return got
}

// TestDriveSingleTurnWalksInitToDone is the L2-002 headline + L2-005 stub:
// a prompt flows INIT→LOAD_SESSION→LOAD_CONTEXT→PLAN→DONE, producing a canned
// assistant.message, against the stubbed cognitive core.
func TestDriveSingleTurnWalksInitToDone(t *testing.T) {
	core, bus, sid := newCore(t, "hi there")
	ch := bus.Subscribe(">")
	tid, err := core.Submit(context.Background(), sid, "say hi")
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	// Expected events in order: task.started, then 4 state.change, then
	// assistant.message, then task.completed = 7 events.
	envs := drain(t, ch, 7)

	// First event: task.started.
	if _, ok := envs[0].Evt.(*event.TaskStartedEvent); !ok {
		t.Fatalf("event[0] = %T, want *TaskStartedEvent", envs[0].Evt)
	}

	// Then 4 state.change forming INIT→LOAD_SESSION→LOAD_CONTEXT→PLAN→DONE.
	wantSeq := []string{"INIT", "LOAD_SESSION", "LOAD_CONTEXT", "PLAN", "DONE"}
	gotStates := []string{"INIT"}
	for i := 1; i <= 4; i++ {
		ce, ok := envs[i].Evt.(*event.StateChangeEvent)
		if !ok {
			t.Fatalf("event[%d] = %T, want *StateChangeEvent", i, envs[i].Evt)
		}
		if ce.From != gotStates[i-1] || ce.To != wantSeq[i] {
			t.Errorf("state.change[%d] = %q→%q, want %q→%q", i, ce.From, ce.To, gotStates[i-1], wantSeq[i])
		}
		gotStates = append(gotStates, ce.To)
		if ce.Task != event.TaskID(tid) {
			t.Errorf("state.change[%d].Task = %q, want %q", i, ce.Task, tid)
		}
	}

	// Then assistant.message with the canned answer.
	if am, ok := envs[5].Evt.(*event.AssistantMessageEvent); !ok || am.Text != "hi there" || !am.Final {
		t.Errorf("event[5] = %+v, want assistant.message final Text=%q", envs[5].Evt, "hi there")
	}
	// Finally task.completed.
	if _, ok := envs[6].Evt.(*event.TaskCompletedEvent); !ok {
		t.Fatalf("event[6] = %T, want *TaskCompletedEvent", envs[6].Evt)
	}
}

// TestEveryTransitionPublishesStateChange is the L2-003 headline: every
// transition emits exactly one state.change with the right (from, to, why).
// We assert the count and the to-state sequence.
func TestEveryTransitionPublishesStateChange(t *testing.T) {
	core, bus, sid := newCore(t, "ok")
	ch := bus.Subscribe("state.change")
	_, _ = core.Submit(context.Background(), sid, "say ok")

	// Four transitions for the direct-answer path: INIT→LOAD_SESSION,
	// LOAD_SESSION→LOAD_CONTEXT, LOAD_CONTEXT→PLAN, PLAN→DONE.
	envs := drain(t, ch, 4)
	wantTo := []string{"LOAD_SESSION", "LOAD_CONTEXT", "PLAN", "DONE"}
	for i, w := range wantTo {
		ce, ok := envs[i].Evt.(*event.StateChangeEvent)
		if !ok {
			t.Fatalf("event[%d] = %T, want *StateChangeEvent", i, envs[i].Evt)
		}
		if ce.To != w {
			t.Errorf("state.change[%d].To = %q, want %q", i, ce.To, w)
		}
	}
}

// TestStubCognitiveReturnsCannedMessage is the L2-005 unit: the stub answers
// directly with its configured message, no tool calls.
func TestStubCognitiveReturnsCannedMessage(t *testing.T) {
	stub := StubCognitive{Answer: "canned"}
	turn, err := stub.Think(context.Background(), nil)
	if err != nil {
		t.Fatalf("Think: %v", err)
	}
	if !turn.Final || turn.Text != "canned" || len(turn.ToolCalls) != 0 {
		t.Errorf("Think = %+v, want Final=true Text=%q no tools", turn, "canned")
	}
	if stub.HasMore(nil) {
		t.Error("StubCognitive.HasMore should be false (single-turn)")
	}
}

// TestDriveReachesTerminalDone verifies the task ends in DONE (terminal), so
// the drive loop returns rather than spinning.
func TestDriveReachesTerminalDone(t *testing.T) {
	core, bus, sid := newCore(t, "done")
	ch := bus.Subscribe(">")
	tid, _ := core.Submit(context.Background(), sid, "go")

	// Drain the full single-turn sequence; then assert NO further events
	// arrive within a short window (the loop terminated at DONE).
	drain(t, ch, 7)
	select {
	case extra, ok := <-ch:
		t.Fatalf("drive loop did not terminate at DONE; got extra event %v (ok=%v)", extra, ok)
	case <-time.After(100 * time.Millisecond):
		// good: no extra events
	}
	smgr := core.session
	got := smgr.LoadTaskPublic(tid)
	if got == nil || got.Status != session.StatusDone {
		t.Errorf("task status = %+v, want DONE (terminal)", got)
	}
}

// newCoreWithCog wires a runtime Core like newCore but with an arbitrary
// cognitive core, so multi-turn stubs (which are not a single canned answer)
// can drive the loop.
func newCoreWithCog(t *testing.T, cog CognitiveCore) (*Core, *event.Bus, session.ID) {
	t.Helper()
	store := session.NewFileStore(filepath.Join(t.TempDir(), "store"))
	bus := event.New()
	t.Cleanup(func() { _ = bus.Close() })
	smgr := session.New(session.Deps{
		Store: store, Bus: bus, Git: session.NewInMemCheckpointer(),
	})
	core := New(Deps{
		Bus:       bus,
		Session:   smgr,
		Cognitive: cog,
	})
	sid, err := smgr.OpenSession(context.Background(), "proj", "demo")
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	return core, bus, sid
}

// multiTurnCog is a stub cognitive core for the multi-turn drive loop. Turn 1
// emits one tool call (Final=false); turn 2 emits nothing yet (Final=false, no
// tools — the Planner is still reasoning, so EXECUTE is re-entered with an
// empty pending queue and fires turn_done → PLAN, the T21 edge); turn 3
// returns the final answer. HasMore stays true so turn 1's VERIFY routes back
// to PLAN (T11) for turn 2. This is the shape a real multi-turn tool-using
// model takes; before the EXECUTE→PLAN edge existed, turn 2 dead-ended.
type multiTurnCog struct {
	calls int
}

func (m *multiTurnCog) Think(context.Context, Prompt) (CognitiveTurn, error) {
	m.calls++
	switch m.calls {
	case 1:
		return CognitiveTurn{Final: false, ToolCalls: []ToolCall{{Tool: "echo", Reason: "gather info"}}}, nil
	case 2:
		return CognitiveTurn{Final: false}, nil // still planning → EXECUTE→PLAN (T21)
	default:
		return CognitiveTurn{Final: true, Text: "all done"}, nil
	}
}

func (m *multiTurnCog) HasMore(*session.Task) bool      { return true }
func (m *multiTurnCog) RecordToolResult(string, string) {}
func (m *multiTurnCog) Reflect(context.Context, *session.Task, Verdict, Observation) ReflectionDecision {
	return ReflectionDecision{Abort: true}
}

// TestDriveMultiTurnToolLoopReachesDone proves the EXECUTE→PLAN edge (T21) lets
// a multi-turn tool-using agent loop reach DONE instead of terminating early.
// The loop walks INIT→LOAD_SESSION→LOAD_CONTEXT→PLAN→EXECUTE (turn 1 tool call)
// →WAIT_TOOL→VERIFY→PLAN (T11) →EXECUTE (turn 2, no tools) →PLAN (T21, turn_done)
// →DONE (T4). That is 10 state.change events; the EXECUTE→PLAN on turn_done
// (event 9) is the edge the fix adds. Without it, turn 2 fired the old
// SigPlannerAnswer from EXECUTE → ErrNoTransition → the drive loop returned
// mid-agent-loop, so the task never reached DONE.
func TestDriveMultiTurnToolLoopReachesDone(t *testing.T) {
	cog := &multiTurnCog{}
	core, bus, sid := newCoreWithCog(t, cog)
	ch := bus.Subscribe("state.change")
	tid, err := core.Submit(context.Background(), sid, "use a tool then answer")
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	// 10 state.change events (see the walk above); draining 10 times out if the
	// loop terminates early before DONE.
	envs := drain(t, ch, 10)

	// Event 9 (index 8) is the EXECUTE→PLAN turn_done transition — the T21 edge.
	td, ok := envs[8].Evt.(*event.StateChangeEvent)
	if !ok {
		t.Fatalf("event[8] = %T, want *StateChangeEvent (turn_done)", envs[8].Evt)
	}
	if td.From != string(StateExecute) || td.To != string(StatePlan) || td.Why != "turn_done" {
		t.Errorf("turn_done transition = %q→%q (%q), want EXECUTE→PLAN (turn_done)", td.From, td.To, td.Why)
	}

	// The last transition lands on DONE (T4).
	last, ok := envs[9].Evt.(*event.StateChangeEvent)
	if !ok {
		t.Fatalf("event[9] = %T, want *StateChangeEvent", envs[9].Evt)
	}
	if last.To != string(StateDone) {
		t.Errorf("last transition To = %q, want DONE", last.To)
	}

	// The task actually completed (terminal DONE), proving the multi-turn loop
	// did not terminate early at turn 2's EXECUTE.
	got := core.session.LoadTaskPublic(tid)
	if got == nil || got.Status != session.StatusDone {
		t.Errorf("task status = %+v, want DONE (multi-turn loop reached terminal)", got)
	}
}
