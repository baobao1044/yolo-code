package runtime

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/yolo-code/yolo/internal/event"
	"github.com/yolo-code/yolo/internal/session"
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
