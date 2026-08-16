// The approval id is the whole gate.
//
// The runtime mints an id per prompt and puts it in approval.request; the
// answer carries it back. Before these tests nothing ever compared the two, so
// the id was decoration: any string — never issued, belonging to a prompt the
// user already answered, or belonging to a different task entirely — resumed
// whatever call happened to be waiting. The dangerous shape is not a malicious
// one. A user approves an `echo`, the next call in the same turn is an `rm -rf`,
// and a repeated keypress replays the id they just sent; the TUI clears its
// pane on the bus echo, so the second prompt is answered before it is read.
//
// Every test here answers with an id it observed on the bus rather than one it
// invented, except where sending a wrong id IS the assertion. A test that makes
// up an id and passes is not testing the gate — that is exactly how the hole
// survived a green suite.

package runtime

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/baobao1044/yolo-code/internal/event"
	"github.com/baobao1044/yolo-code/internal/session"
)

// gateCog emits the same gated tool calls on every turn. Nothing in these tests
// is meant to reach DONE — each one ends by cancelling — so the cog needs no
// per-task turn counter, which keeps it correct when two tasks drive it at once.
type gateCog struct{ calls []ToolCall }

func (g *gateCog) Think(_ context.Context, _ Prompt) (CognitiveTurn, error) {
	return CognitiveTurn{ToolCalls: g.calls}, nil
}
func (*gateCog) HasMore(*session.Task) bool { return false }
func (*gateCog) Reflect(context.Context, *session.Task, Verdict, Observation) ReflectionDecision {
	return ReflectionDecision{Abort: true, Note: "stub reflect"}
}
func (*gateCog) RecordToolResult(string, string, string) {}
func (*gateCog) Reset()                                  {}

// gateExec gates every call and records what actually got dispatched. The
// recording is the assertion in most of these tests: a bypassed gate is not
// visible in the state machine (WAIT_USER → EXECUTE looks identical whoever
// authorised it), it is visible in the command that ran.
type gateExec struct {
	mu  sync.Mutex
	got []ToolCall
}

func (*gateExec) NeedsApproval(ToolCall) bool { return true }

func (e *gateExec) Dispatch(_ context.Context, call ToolCall) (Observation, error) {
	e.mu.Lock()
	e.got = append(e.got, call)
	e.mu.Unlock()
	return Observation{Stdout: "ran " + call.Tool, Tool: call.Tool}, nil
}

func (e *gateExec) dispatched() []ToolCall {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]ToolCall(nil), e.got...)
}

func (e *gateExec) tools() string {
	var names []string
	for _, c := range e.dispatched() {
		names = append(names, c.Tool)
	}
	return strings.Join(names, ",")
}

func newGateCore(t *testing.T, calls ...ToolCall) (*Core, *event.Bus, session.ID, *gateExec) {
	t.Helper()
	bus := event.New()
	store := session.NewFileStore(t.TempDir())
	smgr := session.New(session.Deps{Store: store, Bus: bus, Git: session.NewInMemCheckpointer()})
	sid, err := smgr.OpenSession(context.Background(), "test", "test")
	if err != nil {
		t.Fatal(err)
	}
	exec := &gateExec{}
	core := New(Deps{Bus: bus, Session: smgr, Cognitive: &gateCog{calls: calls}, Exec: exec})
	return core, bus, sid, exec
}

// publishAsync sends a verdict without blocking the caller's drain loop. Publish
// applies backpressure until every subscriber accepts, and the test IS one of
// the subscribers, so publishing inline from inside the loop can wedge itself.
func publishAsync(bus *event.Bus, evt event.Event) {
	go func() { _ = bus.Publish(context.Background(), evt) }()
}

// TestReplayedApprovalIDDoesNotApproveTheNextCall is the shape that made this a
// bug worth fixing rather than a tidiness complaint.
//
// One turn, two gated calls: a harmless echo and an `rm -rf /`. The user
// approves the echo. The id they sent is now spent, but nothing invalidated it
// on the wire — and the next prompt is up within milliseconds. Replaying that
// same id must not authorise the second call. The assertion is on the executor,
// not the FSM: what matters is whether the destructive command ran.
func TestReplayedApprovalIDDoesNotApproveTheNextCall(t *testing.T) {
	core, bus, sid, exec := newGateCore(t,
		ToolCall{ID: "c1", Tool: "echo", Args: []byte(`{"command":"echo hi"}`)},
		ToolCall{ID: "c2", Tool: "danger", Args: []byte(`{"command":"rm -rf /"}`)},
	)

	ch := bus.Subscribe(">")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); _, _ = core.Submit(ctx, sid, "two gated calls") }()

	var ids []string
	parks, replayed := 0, false
	deadline := time.After(10 * time.Second)

loop:
	for {
		select {
		case env := <-ch:
			switch e := env.Evt.(type) {
			case *event.ApprovalRequestEvent:
				ids = append(ids, e.ApprovalID)
			case *event.ErrorEvent:
				if e.Code == "approval_id_mismatch" {
					cancel() // the replay was caught; stop the task and assert
				}
			case *event.StateChangeEvent:
				if e.To != string(StateWaitUser) {
					// A dispatch after the replay means the gate let it through.
					if replayed && e.To == string(StateWaitTool) {
						cancel()
					}
					continue
				}
				parks++
				switch parks {
				case 1:
					publishAsync(bus, &event.UserApproveEvent{Task: string(e.Task), ApprovalID: ids[0]})
				case 2:
					replayed = true
					// The stale id: correct for prompt one, wrong for prompt two.
					publishAsync(bus, &event.UserApproveEvent{Task: string(e.Task), ApprovalID: ids[0]})
				}
			}
		case <-done:
			break loop
		case <-deadline:
			cancel()
			t.Fatalf("task never settled; approval ids seen=%v dispatched=%q", ids, exec.tools())
		}
	}

	if parks < 2 {
		t.Fatalf("reached %d approval prompts, want 2 — the setup never got as far as the replay", parks)
	}
	if len(ids) < 2 || ids[0] == ids[1] {
		t.Fatalf("the two prompts must carry different ids, got %v", ids)
	}
	got := exec.dispatched()
	if len(got) != 1 || got[0].Tool != "echo" {
		t.Fatalf("dispatched %q, want just \"echo\": replaying the id of an already-answered prompt approved the call behind it", exec.tools())
	}
}

// TestUnissuedApprovalIDIsRefused is the same hole in its simplest form: an
// approval naming a prompt that was never published. No production publisher
// can produce this — internal/tui echoes the id it is displaying — so anything
// arriving with one is either a replay, a crossed wire, or a forgery, and none
// of the three is a reason to run the command.
func TestUnissuedApprovalIDIsRefused(t *testing.T) {
	core, bus, sid, exec := newGateCore(t,
		ToolCall{ID: "c1", Tool: "danger", Args: []byte(`{"command":"rm -rf /"}`)},
	)

	ch := bus.Subscribe(">")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); _, _ = core.Submit(ctx, sid, "one gated call") }()

	answered := false
	deadline := time.After(10 * time.Second)

loop:
	for {
		select {
		case env := <-ch:
			switch e := env.Evt.(type) {
			case *event.ErrorEvent:
				if e.Code == "approval_id_mismatch" {
					cancel()
				}
			case *event.StateChangeEvent:
				if e.To == string(StateWaitUser) && !answered {
					answered = true
					publishAsync(bus, &event.UserApproveEvent{
						Task: string(e.Task), ApprovalID: "this-id-was-never-issued",
					})
				}
				if answered && e.To == string(StateWaitTool) {
					cancel()
				}
			}
		case <-done:
			break loop
		case <-deadline:
			cancel()
			t.Fatal("task never settled")
		}
	}

	if !answered {
		t.Fatal("never reached the approval prompt")
	}
	if got := exec.dispatched(); len(got) != 0 {
		t.Fatalf("dispatched %q on an approval id the runtime never issued", exec.tools())
	}
}

// TestApprovalIDIsTaskScopedAndSequential pins the id's CONTENT, not merely its
// presence. The generator's doc comment argues that the "rt-<task>-<n>" shape
// keeps runtime-issued ids from colliding with the Executor's own "appr-N"
// namespace — an argument that only holds if the shape is actually produced.
// Replacing the whole body with a constant left the rest of the suite green,
// which is the tell that nothing was checking.
//
// Task-scoped and monotonic per task is the property the comparison relies on:
// two prompts in one task must differ (or a replay would be indistinguishable
// from an answer) and the task id must be in there (or the same counter value
// in a sibling task would match).
func TestApprovalIDIsTaskScopedAndSequential(t *testing.T) {
	core, bus, sid, _ := newGateCore(t,
		ToolCall{ID: "c1", Tool: "echo", Args: []byte(`{"command":"echo one"}`)},
		ToolCall{ID: "c2", Tool: "echo", Args: []byte(`{"command":"echo two"}`)},
	)

	ch := bus.Subscribe(">")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); _, _ = core.Submit(ctx, sid, "two gated calls") }()

	var ids []string
	var taskID string
	parks := 0
	deadline := time.After(10 * time.Second)

loop:
	for {
		select {
		case env := <-ch:
			switch e := env.Evt.(type) {
			case *event.ApprovalRequestEvent:
				ids = append(ids, e.ApprovalID)
				taskID = string(e.Task)
			case *event.StateChangeEvent:
				if e.To != string(StateWaitUser) {
					continue
				}
				parks++
				if parks == 1 {
					publishAsync(bus, &event.UserApproveEvent{Task: string(e.Task), ApprovalID: ids[0]})
					continue
				}
				cancel() // second prompt reached; both ids observed
			}
		case <-done:
			break loop
		case <-deadline:
			cancel()
			t.Fatalf("only saw ids %v", ids)
		}
	}

	if len(ids) < 2 {
		t.Fatalf("saw %d approval requests, want 2", len(ids))
	}
	want := []string{"rt-" + taskID + "-1", "rt-" + taskID + "-2"}
	for i, w := range want {
		if ids[i] != w {
			t.Errorf("approval id %d = %q, want %q", i+1, ids[i], w)
		}
	}
}

// TestApprovalIDFromAnotherTaskDoesNotApprove closes the third replay route.
// The verdict is routed to a task by its Task field alone, so an id minted for
// task A reaches task B's handle intact; before the comparison existed it
// approved B, because B never looked. Including the task id in the id is what
// makes this a mismatch rather than a coincidence — both prompts are the first
// of their task, so a bare counter would have matched.
func TestApprovalIDFromAnotherTaskDoesNotApprove(t *testing.T) {
	core, bus, sid, exec := newGateCore(t,
		ToolCall{ID: "c1", Tool: "danger", Args: []byte(`{"command":"rm -rf /"}`)},
	)

	ch := bus.Subscribe(">")
	ctxA, cancelA := context.WithCancel(context.Background())
	defer cancelA()
	ctxB, cancelB := context.WithCancel(context.Background())
	defer cancelB()

	doneA := make(chan struct{})
	go func() { defer close(doneA); _, _ = core.Submit(ctxA, sid, "task A") }()

	// Wait for A's prompt and keep it unanswered: we only want its id.
	var idA, taskA string
	deadline := time.After(10 * time.Second)
	for idA == "" {
		select {
		case env := <-ch:
			if e, ok := env.Evt.(*event.ApprovalRequestEvent); ok {
				idA, taskA = e.ApprovalID, string(e.Task)
			}
		case <-deadline:
			t.Fatal("task A never asked for approval")
		}
	}

	doneB := make(chan struct{})
	go func() { defer close(doneB); _, _ = core.Submit(ctxB, sid, "task B") }()

	answered := false
	for {
		stop := false
		select {
		case env := <-ch:
			switch e := env.Evt.(type) {
			case *event.ApprovalRequestEvent:
				if string(e.Task) != taskA && !answered {
					answered = true
					// A's id, delivered to B.
					publishAsync(bus, &event.UserApproveEvent{Task: string(e.Task), ApprovalID: idA})
				}
			case *event.ErrorEvent:
				if e.Code == "approval_id_mismatch" {
					stop = true
				}
			case *event.StateChangeEvent:
				if answered && e.To == string(StateWaitTool) {
					stop = true
				}
			}
		case <-deadline:
			t.Fatalf("task B never settled (answered=%v)", answered)
		}
		if stop {
			break
		}
	}

	cancelA()
	cancelB()
	<-doneA
	<-doneB

	if !answered {
		t.Fatal("task B never asked for approval, so the cross-task replay was never attempted")
	}
	if got := exec.dispatched(); len(got) != 0 {
		t.Fatalf("dispatched %q: an approval id minted for a different task authorised this one", exec.tools())
	}
}

// TestBlankApproveIsRefused and TestBlankRejectIsHonoured pin the deliberate
// asymmetry in how a verdict with no id at all is treated. See
// answersOutstandingPrompt: approve fails closed, reject fails open, because a
// blank approve has no production publisher (internal/tui always sends the id
// it is displaying) while a blank reject does — cmd/yolo's headless resolver
// answers a park it saw on state.change, which carries no approval id, and
// refusing it would hang every unattended run on its first gated call.
func TestBlankApproveIsRefused(t *testing.T) {
	core, bus, sid, exec := newGateCore(t,
		ToolCall{ID: "c1", Tool: "danger", Args: []byte(`{"command":"rm -rf /"}`)},
	)

	ch := bus.Subscribe(">")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); _, _ = core.Submit(ctx, sid, "one gated call") }()

	answered := false
	deadline := time.After(10 * time.Second)

loop:
	for {
		select {
		case env := <-ch:
			switch e := env.Evt.(type) {
			case *event.ErrorEvent:
				if e.Code == "approval_id_mismatch" {
					cancel()
				}
			case *event.StateChangeEvent:
				if e.To == string(StateWaitUser) && !answered {
					answered = true
					publishAsync(bus, &event.UserApproveEvent{Task: string(e.Task)})
				}
				if answered && e.To == string(StateWaitTool) {
					cancel()
				}
			}
		case <-done:
			break loop
		case <-deadline:
			cancel()
			t.Fatal("task never settled")
		}
	}

	if got := exec.dispatched(); len(got) != 0 {
		t.Fatalf("dispatched %q on an approve carrying no approval id at all", exec.tools())
	}
}

func TestBlankRejectIsHonoured(t *testing.T) {
	core, bus, sid, exec := newGateCore(t,
		ToolCall{ID: "c1", Tool: "danger", Args: []byte(`{"command":"rm -rf /"}`)},
	)

	ch := bus.Subscribe(">")
	done := make(chan struct{})
	go func() { defer close(done); _, _ = core.Submit(context.Background(), sid, "one gated call") }()

	answered, cancelled := false, false
	deadline := time.After(10 * time.Second)

loop:
	for {
		select {
		case env := <-ch:
			e, ok := env.Evt.(*event.StateChangeEvent)
			if !ok {
				continue
			}
			if e.To == string(StateWaitUser) && !answered {
				answered = true
				// No ApprovalID: exactly what cmd/yolo's headless resolver sends.
				publishAsync(bus, &event.UserRejectEvent{Task: string(e.Task)})
			}
			if e.To == string(StateCancelled) {
				cancelled = true
			}
		case <-done:
			break loop
		case <-deadline:
			t.Fatal("a blank reject left the task parked; every unattended headless run would hang here")
		}
	}

	if !cancelled {
		t.Error("blank reject did not cancel the task")
	}
	if got := exec.dispatched(); len(got) != 0 {
		t.Fatalf("dispatched %q after a reject", exec.tools())
	}
}

// TestMismatchedApproveReAsksTheOutstandingPrompt covers the operator-facing
// half of the fix, which is not optional. Dropping a mismatched verdict in
// silence is safe and unusable: the TUI clears its approval pane when it sees
// the verdict echo on the bus, so a silent drop leaves a live gate behind a
// blank screen and the run looks frozen. The runtime says what it ignored and
// re-publishes the SAME prompt — same id, deliberately not a fresh one, so the
// answer the user is about to give is still the answer to this call.
func TestMismatchedApproveReAsksTheOutstandingPrompt(t *testing.T) {
	core, bus, sid, _ := newGateCore(t,
		ToolCall{ID: "c1", Tool: "danger", Args: []byte(`{"command":"rm -rf /"}`)},
	)

	ch := bus.Subscribe(">")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); _, _ = core.Submit(ctx, sid, "one gated call") }()

	var ids []string
	var errEvt *event.ErrorEvent
	answered := false
	deadline := time.After(10 * time.Second)

loop:
	for {
		select {
		case env := <-ch:
			switch e := env.Evt.(type) {
			case *event.ApprovalRequestEvent:
				ids = append(ids, e.ApprovalID)
				if len(ids) > 1 {
					cancel() // the re-ask arrived
				}
			case *event.ErrorEvent:
				if e.Code == "approval_id_mismatch" && errEvt == nil {
					errEvt = e
				}
			case *event.StateChangeEvent:
				if e.To == string(StateWaitUser) && !answered {
					answered = true
					publishAsync(bus, &event.UserApproveEvent{Task: string(e.Task), ApprovalID: "wrong"})
				}
			}
		case <-done:
			break loop
		case <-deadline:
			cancel()
			t.Fatalf("no re-ask after a mismatched approve; approval requests seen=%v", ids)
		}
	}

	if errEvt == nil {
		t.Fatal("no approval_id_mismatch error published: the operator has no way to tell an ignored keypress from a hang")
	}
	if !strings.Contains(errEvt.Msg, "wrong") {
		t.Errorf("mismatch message %q does not name the id that was ignored", errEvt.Msg)
	}
	if len(ids) < 2 {
		t.Fatalf("saw %d approval requests, want the prompt re-published after the mismatch", len(ids))
	}
	if ids[1] != ids[0] {
		t.Errorf("re-ask carries id %q, want the outstanding %q — minting a new id would invalidate the answer the user is already typing", ids[1], ids[0])
	}
}
