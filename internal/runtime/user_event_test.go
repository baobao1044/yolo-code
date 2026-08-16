// Sprint 13+ integration: runtime.Core consumes user.* events for WAIT_USER,
// PAUSED and CANCEL.

package runtime

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/baobao1044/yolo-code/internal/event"
	"github.com/baobao1044/yolo-code/internal/session"
)

// toolCognitive emits one tool call on the first Think and a final answer on
// the second. It exercises EXECUTE → WAIT_USER and then DONE.
type toolCognitive struct {
	calls int
}

func (t *toolCognitive) Think(_ context.Context, _ Prompt) (CognitiveTurn, error) {
	t.calls++
	if t.calls == 1 {
		return CognitiveTurn{
			Final: false,
			ToolCalls: []ToolCall{
				{Tool: "bash", Args: []byte(`{"command":"echo hi"}`)},
			},
		}, nil
	}
	return CognitiveTurn{Final: true, Text: "done"}, nil
}

func (*toolCognitive) HasMore(*session.Task) bool { return false }
func (*toolCognitive) Reflect(context.Context, *session.Task, Verdict, Observation) ReflectionDecision {
	return ReflectionDecision{Abort: true, Note: "stub reflect"}
}
func (*toolCognitive) RecordToolResult(string, string, string) {}
func (*toolCognitive) Reset()                                  {}

// approvalExecutor returns NeedsApproval=true so every tool call pauses in
// WAIT_USER.
type approvalExecutor struct{}

func (approvalExecutor) NeedsApproval(ToolCall) bool { return true }
func (approvalExecutor) Dispatch(context.Context, ToolCall) (Observation, error) {
	return Observation{Stdout: "approved run"}, nil
}

func newTestCore(t *testing.T) (*Core, *event.Bus, *session.Manager, session.ID) {
	t.Helper()
	dir := t.TempDir()
	bus := event.New()
	store := session.NewFileStore(dir)
	smgr := session.New(session.Deps{Store: store, Bus: bus, Git: session.NewInMemCheckpointer()})
	sid, err := smgr.OpenSession(context.Background(), "test", "test")
	if err != nil {
		t.Fatal(err)
	}
	core := New(Deps{
		Bus:       bus,
		Session:   smgr,
		Cognitive: &toolCognitive{},
		Exec:      approvalExecutor{},
	})
	return core, bus, smgr, sid
}

// TestUserApproveResumesFromWaitUser is the happy path: the verdict names the
// prompt it is answering and the task resumes.
//
// The subscription is the root wildcard rather than state.change alone because
// the approval id has to come from the approval.request the runtime actually
// published. An earlier version of this test invented the id ("1") and passed,
// which is precisely the hole it was meant to cover: nothing compared the id,
// so any string resumed the task. Echoing the real one is also what every
// production publisher does — internal/tui/input.go answers the prompt it is
// displaying — so the test now exercises the shipped contract.
func TestUserApproveResumesFromWaitUser(t *testing.T) {
	core, bus, _, sid := newTestCore(t)

	ch := bus.Subscribe(">")
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = core.Submit(context.Background(), sid, "run a command")
	}()

	var sawWaitUser, sawDone bool
	var req *event.ApprovalRequestEvent

	for env := range ch {
		if e, ok := env.Evt.(*event.ApprovalRequestEvent); ok && req == nil {
			req = e
		}
		sc, ok := env.Evt.(*event.StateChangeEvent)
		if !ok {
			continue
		}
		if sc.To == string(StateWaitUser) && !sawWaitUser {
			sawWaitUser = true
			_ = bus.Publish(context.Background(), &event.UserApproveEvent{
				Task:       string(sc.Task),
				ApprovalID: approvalIDOf(req),
			})
		}
		if sc.To == string(StateDone) {
			sawDone = true
			_ = bus.Close()
		}
	}

	wg.Wait()

	if !sawWaitUser {
		t.Fatal("never reached WAIT_USER")
	}
	if !sawDone {
		t.Fatal("approval did not resume to DONE")
	}
}

// TestApprovalParkPublishesApprovalRequest is the interactive-mode headline:
// parking in WAIT_USER must also ASK. internal/tui builds its approval pane
// from approval.request and nothing else, so a park without the event is a
// silent hang — the gate refuses to proceed and the screen never says why.
func TestApprovalParkPublishesApprovalRequest(t *testing.T) {
	core, bus, _, sid := newTestCore(t)

	ch := bus.Subscribe(">")
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = core.Submit(context.Background(), sid, "run a command")
	}()

	var req *event.ApprovalRequestEvent
	for env := range ch {
		switch e := env.Evt.(type) {
		case *event.ApprovalRequestEvent:
			if req == nil {
				req = e
			}
		case *event.StateChangeEvent:
			if e.To == string(StateWaitUser) {
				_ = bus.Publish(context.Background(), &event.UserApproveEvent{
					Task: string(e.Task), ApprovalID: approvalIDOf(req),
				})
			}
			if e.To == string(StateDone) {
				_ = bus.Close()
			}
		}
	}

	wg.Wait()

	if req == nil {
		t.Fatal("no approval.request published while the FSM parked in WAIT_USER — an interactive user is never asked")
	}
	if req.ApprovalID == "" {
		t.Error("ApprovalRequestEvent.ApprovalID is empty; the TUI echoes it back on y/n")
	}
	if req.Tool != "bash" {
		t.Errorf("ApprovalRequestEvent.Tool = %q, want bash (the call being gated)", req.Tool)
	}
	if req.Task == "" {
		t.Error("ApprovalRequestEvent.Task is empty; the prompt must name its task")
	}
	if req.Preview == "" {
		t.Error("ApprovalRequestEvent.Preview is empty; the user is approving an unseen command")
	}
}

// classifyingExecutor is an approvalExecutor that also classifies risk and
// records the calls it was handed, so a test can inspect what the drive loop
// actually dispatched after the human answered.
type classifyingExecutor struct {
	mu         sync.Mutex
	risk       event.Risk
	dispatched []ToolCall
}

func (e *classifyingExecutor) NeedsApproval(ToolCall) bool { return true }

func (e *classifyingExecutor) RiskOf(ToolCall) event.Risk { return e.risk }

func (e *classifyingExecutor) Dispatch(_ context.Context, call ToolCall) (Observation, error) {
	e.mu.Lock()
	e.dispatched = append(e.dispatched, call)
	e.mu.Unlock()
	return Observation{Stdout: "approved run"}, nil
}

func (e *classifyingExecutor) calls() []ToolCall {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]ToolCall(nil), e.dispatched...)
}

// TestApprovedCallReachesTheExecutorPreApproved covers the double prompt. Two
// gates guard one dispatch: this loop parks and asks, and then the executor's
// own gate asks again for the same call — so the user pressed y, watched the
// FSM resume, and was immediately asked the identical question. The answer has
// to travel with the call, and the class the prompt states has to come from the
// executor, since the runtime has no risk model of its own.
func TestApprovedCallReachesTheExecutorPreApproved(t *testing.T) {
	dir := t.TempDir()
	bus := event.New()
	smgr := session.New(session.Deps{
		Store: session.NewFileStore(dir), Bus: bus, Git: session.NewInMemCheckpointer(),
	})
	sid, err := smgr.OpenSession(context.Background(), "test", "test")
	if err != nil {
		t.Fatal(err)
	}
	exec := &classifyingExecutor{risk: "high"}
	core := New(Deps{Bus: bus, Session: smgr, Cognitive: &toolCognitive{}, Exec: exec})

	ch := bus.Subscribe(">")
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = core.Submit(context.Background(), sid, "run a command")
	}()

	var req *event.ApprovalRequestEvent
	for env := range ch {
		switch e := env.Evt.(type) {
		case *event.ApprovalRequestEvent:
			if req == nil {
				req = e
			}
		case *event.StateChangeEvent:
			if e.To == string(StateWaitUser) {
				_ = bus.Publish(context.Background(), &event.UserApproveEvent{
					Task: string(e.Task), ApprovalID: approvalIDOf(req),
				})
			}
			if e.To == string(StateDone) {
				_ = bus.Close()
			}
		}
	}
	wg.Wait()

	if req == nil {
		t.Fatal("no approval.request published")
	}
	if req.Risk != "high" {
		t.Errorf("ApprovalRequestEvent.Risk = %q, want high — the user is asked to approve a call without being told how dangerous it is", req.Risk)
	}
	calls := exec.calls()
	if len(calls) != 1 {
		t.Fatalf("dispatched %d calls, want 1", len(calls))
	}
	if !calls[0].PreApproved {
		t.Error("the approved call reached the executor without PreApproved — its own gate will ask the same question a second time")
	}
}

// approvalIDOf returns the id of a (possibly absent) approval request, so the
// test can answer the prompt without crashing when the prompt was never
// published — the failure it is there to report.
func approvalIDOf(req *event.ApprovalRequestEvent) string {
	if req == nil {
		return ""
	}
	return req.ApprovalID
}

// TestCancelWhileWaitingForApprovalUnwinds pins the Ctrl-C path: WAIT_USER
// blocks on the user-command channel, so without a ctx arm a cancelled task
// waits forever for an answer nobody will give.
func TestCancelWhileWaitingForApprovalUnwinds(t *testing.T) {
	core, bus, smgr, sid := newTestCore(t)
	t.Cleanup(func() { _ = bus.Close() })

	ch := bus.Subscribe(event.Topic("state.change"))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan session.TaskID, 1)
	go func() {
		tid, _ := core.Submit(ctx, sid, "run a command")
		done <- tid
	}()

	// Wait for the park, then cancel while the drive loop is blocked on the
	// command channel.
	waited := time.After(2 * time.Second)
	for parked := false; !parked; {
		select {
		case env := <-ch:
			if sc, ok := env.Evt.(*event.StateChangeEvent); ok && sc.To == string(StateWaitUser) {
				parked = true
			}
		case <-waited:
			t.Fatal("never reached WAIT_USER")
		}
	}
	cancel()

	select {
	case tid := <-done:
		if got := smgr.LoadTaskPublic(tid); got != nil && got.Status == session.StatusDone {
			t.Error("cancelled task reached DONE; want CANCELLED")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Submit did not return after cancel — WAIT_USER blocks with no ctx arm, so Ctrl-C during an approval prompt hangs")
	}
}

func TestUserCancelReachesCancelled(t *testing.T) {
	core, bus, _, sid := newTestCore(t)

	ch := bus.Subscribe(event.Topic("state.change"))
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = core.Submit(context.Background(), sid, "run a command")
	}()

	var sawCancelled bool

	for env := range ch {
		sc, ok := env.Evt.(*event.StateChangeEvent)
		if !ok {
			continue
		}
		if sc.To == string(StateWaitUser) {
			_ = bus.Publish(context.Background(), &event.UserCancelEvent{
				Task: event.TaskID(sc.Task),
			})
		}
		if sc.To == string(StateCancelled) {
			sawCancelled = true
			_ = bus.Close()
		}
	}

	wg.Wait()

	if !sawCancelled {
		t.Fatal("cancel did not reach CANCELLED")
	}
}

func TestUserPauseResume(t *testing.T) {
	core, bus, _, sid := newTestCore(t)

	ch := bus.Subscribe(event.Topic("state.change"))
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = core.Submit(context.Background(), sid, "run a command")
	}()

	var sawWaitUser, sawPaused, sawResumed bool

	for env := range ch {
		sc, ok := env.Evt.(*event.StateChangeEvent)
		if !ok {
			continue
		}
		switch sc.To {
		case string(StateWaitUser):
			if !sawWaitUser {
				sawWaitUser = true
				_ = bus.Publish(context.Background(), &event.UserPauseEvent{
					Task: event.TaskID(sc.Task),
				})
			}
		case string(StatePaused):
			if !sawPaused {
				sawPaused = true
				_ = bus.Publish(context.Background(), &event.UserResumeEvent{
					Task: event.TaskID(sc.Task),
				})
			}
		case string(StateLoadContext):
			if sawPaused {
				sawResumed = true
				_ = bus.Close()
			}
		}
	}

	wg.Wait()

	if !sawWaitUser {
		t.Fatal("never reached WAIT_USER")
	}
	if !sawPaused {
		t.Fatal("pause did not reach PAUSED")
	}
	if !sawResumed {
		t.Fatal("resume did not return to LOAD_CONTEXT")
	}
}
