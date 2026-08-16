package runtime

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/baobao1044/yolo-code/internal/event"
	"github.com/baobao1044/yolo-code/internal/session"
)

// stallingSaveStore delays every SaveTask so the window between "the cascade
// unblocked the drive loop" and "the terminal status reached disk" is wide
// enough to observe deterministically instead of once in twenty runs. Only the
// test double is slow; production writes are untouched.
type stallingSaveStore struct {
	session.Store
	delay time.Duration
}

func (s stallingSaveStore) SaveTask(ctx context.Context, t *session.Task) error {
	time.Sleep(s.delay)
	return s.Store.SaveTask(ctx, t)
}

// TestCancelledStatusIsDurableWhenSubmitReturns pins the property a crash-safe
// cancel needs: once Submit has returned for a task the user cancelled, the
// CANCELLED status must already be on disk.
//
// It fails when the persisting half of the cancel runs on a goroutine nobody
// joins. The user-event goroutine cascades the context, which unblocks the
// drive loop; the drive loop reaches CANCELLED and Submit returns while the
// store write is still in flight behind it. Nothing after that point waits for
// it, so a process that exits when the task ends — esc, then quit — leaves the
// task recorded as non-terminal, and Resume's interrupted-task rule rehydrates
// a cancelled task as live PAUSED work.
func TestCancelledStatusIsDurableWhenSubmitReturns(t *testing.T) {
	dir := t.TempDir()
	bus := event.New()
	disk := session.NewFileStore(dir)
	store := stallingSaveStore{Store: disk, delay: 250 * time.Millisecond}
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

	ch := bus.Subscribe(event.Topic("state.change"))
	var wg sync.WaitGroup
	wg.Add(1)
	var tid session.TaskID
	go func() {
		defer wg.Done()
		id, _ := core.Submit(context.Background(), sid, "run a command")
		tid = id
	}()

	var sawCancelled bool
	for env := range ch {
		sc, ok := env.Evt.(*event.StateChangeEvent)
		if !ok {
			continue
		}
		if sc.To == string(StateWaitUser) {
			_ = bus.Publish(context.Background(), &event.UserCancelEvent{Task: event.TaskID(sc.Task)})
		}
		if sc.To == string(StateCancelled) {
			sawCancelled = true
			_ = bus.Close()
		}
	}

	wg.Wait()

	if !sawCancelled {
		t.Fatal("the task never reached CANCELLED; the cancel never took effect at all")
	}

	// Read straight through to the files on disk. A fresh Manager over the same
	// root is what the next launch does, and it is the only witness that
	// matters: in-memory state dies with the process.
	got, err := disk.LoadTask(context.Background(), tid)
	if err != nil {
		t.Fatalf("loading task %s from disk after Submit returned: %v", tid, err)
	}
	if got.Status != session.StatusCancelled {
		t.Fatalf("persisted status of cancelled task %s = %q, want %q: "+
			"Submit has returned, so every observer believes the cancel is finished, "+
			"but the terminal status is still only in memory on an unjoined goroutine. "+
			"A process that exits now loses the cancel and Resume brings the task back as live work.",
			tid, got.Status, session.StatusCancelled)
	}
	if got.EndedAt == nil || got.EndedAt.IsZero() {
		t.Errorf("persisted task %s has no EndedAt: the cancel's end stamp did not reach disk either", tid)
	}
}

// failingCognitive makes PLAN fail outright, which routes the drive loop
// through toError and out of Submit with the task still non-terminal.
type failingCognitive struct{ StubCognitive }

func (failingCognitive) Think(context.Context, Prompt) (CognitiveTurn, error) {
	return CognitiveTurn{}, errors.New("planner exploded")
}

// TestCancelIsStillRecordedWhenNoLoopIsDrivingTheTask guards the half of the
// split that is easy to lose. Handing the persisting half of a cancel to the
// drive goroutine only works while a drive goroutine exists; a handle can
// outlive its loop while its task is still non-terminal, and esc on it is a
// genuine cancel. If nothing records it the cancel vanishes silently — a worse
// failure than persisting it late, which is what the split was introduced to
// fix.
//
// This test used to reach that state by submitting a task whose planner
// exploded, because the ERROR path left the task non-terminal. That was never
// the property under test; it was borrowing a defect as a fixture. Once toError
// began recording FAILED the borrowed fixture disappeared, and the test did not
// fail — it hit its own t.Skipf and the package still reported PASS, which is
// how a regression guard gets switched off without anybody noticing.
//
// So the precondition is now built rather than provoked: a real task, known to
// the Session Manager and still non-terminal, with a handle in the Core marked
// not-driving. That is exactly the state the branch under test exists for, and
// it does not depend on any other code being broken to occur.
func TestCancelIsStillRecordedWhenNoLoopIsDrivingTheTask(t *testing.T) {
	dir := t.TempDir()
	bus := event.New()
	disk := session.NewFileStore(dir)
	smgr := session.New(session.Deps{Store: disk, Bus: bus, Git: session.NewInMemCheckpointer()})
	sid, err := smgr.OpenSession(context.Background(), "test", "test")
	if err != nil {
		t.Fatal(err)
	}
	core := New(Deps{Bus: bus, Session: smgr, Cognitive: failingCognitive{}})

	cancelled := bus.Subscribe(event.Topic("task.cancelled"))

	// A task the Manager knows about and has not ended. StartTask leaves it
	// PENDING, which is non-terminal, so a cancel on it is meaningful.
	tid, err := smgr.StartTask(context.Background(), sid, "work nobody is driving")
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	before, err := disk.LoadTask(context.Background(), tid)
	if err != nil {
		t.Fatalf("loading task %s: %v", tid, err)
	}
	if before.Status.IsTerminal() {
		t.Fatalf("precondition broken: a freshly started task is %q, which is terminal; "+
			"this test needs a live task to cancel", before.Status)
	}

	// A handle with no drive loop behind it: driving is false, which is the
	// signal userEventLoop uses to decide it is the last one who can persist.
	core.mu.Lock()
	core.tasks[tid] = &taskHandle{
		id:        tid,
		sessionID: sid,
		fsm:       newFSM(StateInit),
		events:    make(chan userCmd, 1),
		driving:   false,
	}
	core.mu.Unlock()

	_ = bus.Publish(context.Background(), &event.UserCancelEvent{Task: event.TaskID(tid)})

	select {
	case env := <-cancelled:
		if _, ok := env.Evt.(*event.TaskCancelledEvent); !ok {
			t.Fatalf("first task.* event was %T, want *TaskCancelledEvent", env.Evt)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelling a task whose drive loop already exited published nothing: " +
			"the cancel was dropped, so the task stays non-terminal forever and Resume revives it")
	}

	got, err := disk.LoadTask(context.Background(), tid)
	if err != nil {
		t.Fatalf("loading task %s after cancel: %v", tid, err)
	}
	if got.Status != session.StatusCancelled {
		t.Fatalf("persisted status of task %s = %q, want %q: a cancel arriving with no drive "+
			"loop to hand the persist to must still be recorded by whoever received it",
			tid, got.Status, session.StatusCancelled)
	}
}
