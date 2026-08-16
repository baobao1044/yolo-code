// A task that dies of a hard error was never recorded as having died.
//
// toError moved the FSM to ERROR and published an error event, and that was the
// whole of it — it never touched the Session Manager. Five lines below it,
// handleCancel goes through c.session.Cancel, so the Core plainly can write a
// terminal status; the error path just did not. The task's persisted status
// stayed RUNNING forever.
//
// That is not a cosmetic gap, because RUNNING is the one non-terminal status
// Resume treats as interrupted work: resume.go rewrites a loaded RUNNING task
// to PAUSED, so a task that died of a planner error comes back on the next
// launch advertised as resumable. The user is offered the corpse.
//
// The status the spec wants already existed and had no writer at all —
// StatusFailed is declared terminal in types.go and nothing in the tree ever
// assigned it. Nor was this an oversight in one function: the Session Manager
// had CompleteTask for success and Cancel for abort and no third sibling, the
// event catalog had task.completed/cancelled/paused and no task.failed, and yet
// 03-Session_Manager.md §3.2 draws "RUNNING --> FAILED: retries exhausted /
// hard error", §5.4.1 lists task.failed among the lifecycle events, and
// docs/user/commands.md documents task.failed to users. The failure path was
// fully specified and entirely unbuilt.
//
// These tests pin the two halves a caller can actually observe: the status that
// survives the process, and the event that announces it.

package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/baobao1044/yolo-code/internal/event"
	"github.com/baobao1044/yolo-code/internal/session"
)

// newFailingCore builds a Core whose planner explodes on the first Think, plus
// the on-disk store to read the outcome back from. failingCognitive lives in
// cancel_persist_test.go; it is the shortest route into toError.
func newFailingCore(t *testing.T) (*Core, *event.Bus, session.Store, session.ID) {
	t.Helper()
	dir := t.TempDir()
	bus := event.New()
	disk := session.NewFileStore(dir)
	smgr := session.New(session.Deps{Store: disk, Bus: bus, Git: session.NewInMemCheckpointer()})
	sid, err := smgr.OpenSession(context.Background(), "test", "test")
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	core := New(Deps{Bus: bus, Session: smgr, Cognitive: failingCognitive{}})
	return core, bus, disk, sid
}

// TestHardErrorPersistsFailed is the headline. It reads the task back through a
// fresh store handle rather than the Manager's in-memory map, because in-memory
// state dies with the process and the bug's whole cost is paid on the next
// launch.
func TestHardErrorPersistsFailed(t *testing.T) {
	core, _, disk, sid := newFailingCore(t)

	tid, err := core.Submit(context.Background(), sid, "explode")
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	got, err := disk.LoadTask(context.Background(), tid)
	if err != nil {
		t.Fatalf("loading task %s from disk: %v", tid, err)
	}
	if got.Status != session.StatusFailed {
		t.Fatalf("persisted status of a task killed by a hard error = %q, want %q.\n"+
			"toError moved the FSM to ERROR and published an error event but never told "+
			"the Session Manager, so the task is still recorded as live work. Resume's "+
			"interrupted-task rule turns a stored RUNNING into PAUSED, which means the "+
			"next launch offers this dead task back to the user as resumable.",
			got.Status, session.StatusFailed)
	}
	if !got.Status.IsTerminal() {
		t.Errorf("status %q is not terminal; a hard error must end the task", got.Status)
	}
	if got.EndedAt == nil || got.EndedAt.IsZero() {
		t.Error("failed task has no EndedAt: CompleteTask and Cancel both stamp an end " +
			"time, so a task that ends by failing must too, or its duration is unbounded")
	}
}

// TestHardErrorPublishesTaskFailed pins the announcement. Without it the only
// bus trace of a dead task is an ErrorEvent, which every layer publishes for
// every kind of trouble and which says nothing about the task's lifecycle — so
// a consumer watching task.* to learn when work ends never learns this task
// ended. docs/user/commands.md already promises users this event.
func TestHardErrorPublishesTaskFailed(t *testing.T) {
	core, bus, _, sid := newFailingCore(t)

	failed := bus.Subscribe(event.Topic("task.failed"))

	if _, err := core.Submit(context.Background(), sid, "explode"); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	select {
	case env := <-failed:
		ev, ok := env.Evt.(*event.TaskFailedEvent)
		if !ok {
			t.Fatalf("event on task.failed was %T, want *event.TaskFailedEvent", env.Evt)
		}
		if ev.Reason == "" {
			t.Error("task.failed carries no reason; the cause is the only thing that " +
				"distinguishes one failure from another for anyone reading the log")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a task killed by a hard error published nothing on task.failed. " +
			"03-Session_Manager.md §5.4.1 lists it as a lifecycle event and " +
			"docs/user/commands.md documents it to users, but nothing could emit it.")
	}
}
