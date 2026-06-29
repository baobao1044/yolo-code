// Sprint 13+ integration: runtime.Core consumes user.* events for WAIT_USER,
// PAUSED and CANCEL.

package runtime

import (
	"context"
	"sync"
	"testing"

	"github.com/yolo-code/yolo/internal/event"
	"github.com/yolo-code/yolo/internal/session"
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
func (*toolCognitive) RecordToolResult(string, string) {}

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

func TestUserApproveResumesFromWaitUser(t *testing.T) {
	core, bus, _, sid := newTestCore(t)

	ch := bus.Subscribe(event.Topic("state.change"))
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = core.Submit(context.Background(), sid, "run a command")
	}()

	var sawWaitUser, sawDone bool

	for env := range ch {
		sc, ok := env.Evt.(*event.StateChangeEvent)
		if !ok {
			continue
		}
		if sc.To == string(StateWaitUser) && !sawWaitUser {
			sawWaitUser = true
			_ = bus.Publish(context.Background(), &event.UserApproveEvent{
				Task:       string(sc.Task),
				ApprovalID: "1",
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
