package session

import (
	"context"
	"testing"

	"github.com/yolo-code/yolo/internal/event"
)

func TestCancelSetsCancelledRollsBackAndPublishes(t *testing.T) {
	m, store, bus := newTestManager(t)
	ckpt := m.git.(*InMemCheckpointer)
	ctx := context.Background()

	sid, _ := m.OpenSession(ctx, "proj", "demo")
	tid, _ := m.StartTask(ctx, sid, "edit a.go")
	snap, _ := m.Checkpoint(ctx, tid, "pre-edit", []string{"a.go"})
	// Register a cancel the runtime would have attached, so Cancel can cascade.
	called := false
	m.AttachCancel(tid, func() { called = true })

	ch := bus.Subscribe("task.cancelled")

	if err := m.Cancel(ctx, tid, "user"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	// 1. The task's cancel func was invoked (cascade to L4/L5/L8/L9 work).
	if !called {
		t.Error("Cancel did not invoke the attached cancel func (no cascade)")
	}
	// 2. The checkpoint was rolled back to (safe state).
	if rolls := ckpt.Rollbacks(); len(rolls) != 1 || rolls[0] != snap {
		t.Errorf("rollbacks = %v, want [%q] (cancel must restore the checkpoint)", rolls, snap)
	}
	// 3. Status is CANCELLED (terminal) and EndedAt is stamped.
	got, _ := store.LoadTask(ctx, tid)
	if got.Status != StatusCancelled {
		t.Errorf("Status = %q, want %q", got.Status, StatusCancelled)
	}
	if !got.Status.IsTerminal() {
		t.Error("CANCELLED must be terminal")
	}
	if got.EndedAt == nil || got.EndedAt.IsZero() {
		t.Error("EndedAt not stamped on cancel")
	}
	// 4. task.cancelled published with reason + partial summary.
	env, ok := recv(t, ch)
	if !ok {
		t.Fatal("task.cancelled not published")
	}
	evt, ok := env.Evt.(*event.TaskCancelledEvent)
	if !ok {
		t.Fatalf("event type = %T, want *TaskCancelledEvent", env.Evt)
	}
	if evt.Task != string(tid) || evt.Reason != "user" {
		t.Errorf("task.cancelled = %+v, want task=%q reason=%q", evt, tid, "user")
	}
}

func TestCancelWithoutCheckpointStillCancels(t *testing.T) {
	m, _, bus := newTestManager(t)
	ctx := context.Background()
	bus.Subscribe(">") // drain

	sid, _ := m.OpenSession(ctx, "proj", "demo")
	tid, _ := m.StartTask(ctx, sid, "edit a.go")

	if err := m.Cancel(ctx, tid, "user"); err != nil {
		t.Fatalf("Cancel (no checkpoint): %v", err)
	}
	// No checkpoint → no rollback attempted; status still CANCELLED.
	got, _ := m.task(tid)
	if got.Status != StatusCancelled {
		t.Errorf("Status = %q, want %q", got.Status, StatusCancelled)
	}
}

func TestCancelOnTerminalTaskRejected(t *testing.T) {
	m, _, bus := newTestManager(t)
	ctx := context.Background()
	bus.Subscribe(">")
	sid, _ := m.OpenSession(ctx, "proj", "demo")
	tid, _ := m.StartTask(ctx, sid, "edit a.go")
	_ = m.CompleteTask(ctx, tid) // DONE (terminal)

	if err := m.Cancel(ctx, tid, "user"); err != ErrTaskNotCancellable {
		t.Errorf("Cancel on terminal: err = %v, want ErrTaskNotCancellable", err)
	}
}

func TestPauseSetsPausedAndPublishes(t *testing.T) {
	m, store, bus := newTestManager(t)
	ctx := context.Background()

	sid, _ := m.OpenSession(ctx, "proj", "demo")
	tid, _ := m.StartTask(ctx, sid, "edit a.go")
	// Mimic the runtime having picked the task up.
	task, _ := m.task(tid)
	task.Status = StatusRunning
	_ = store.SaveTask(ctx, task)

	ch := bus.Subscribe("task.paused")
	if err := m.Pause(ctx, tid); err != nil {
		t.Fatalf("Pause: %v", err)
	}

	env, ok := recv(t, ch)
	if !ok {
		t.Fatal("task.paused not published")
	}
	if _, ok := env.Evt.(*event.TaskPausedEvent); !ok {
		t.Fatalf("event type = %T, want *TaskPausedEvent", env.Evt)
	}
	got, _ := store.LoadTask(ctx, tid)
	if got.Status != StatusPaused {
		t.Errorf("Status = %q, want %q", got.Status, StatusPaused)
	}
}

func TestPauseThenResumeIsReversibleContinuation(t *testing.T) {
	m, store, bus := newTestManager(t)
	ctx := context.Background()
	bus.Subscribe(">")
	sid, _ := m.OpenSession(ctx, "proj", "demo")
	tid, _ := m.StartTask(ctx, sid, "edit a.go")

	// Pause, then the runtime resumes the task back to RUNNING. Unlike Cancel,
	// pause keeps the task alive: it is resumed in place, not restarted.
	task, _ := m.task(tid)
	task.Status = StatusRunning
	_ = store.SaveTask(ctx, task)
	_ = m.Pause(ctx, tid)

	// The resumed task must still be the SAME task, retain its goal, and leave
	// the history intact (cancel would have rolled back).
	_, _ = m.Checkpoint(ctx, tid, "cp1", []string{"a.go"}) // history present
	got, _ := store.LoadTask(ctx, tid)
	if got.Status != StatusPaused {
		t.Fatalf("pre-resume Status = %q, want PAUSED", got.Status)
	}
	if len(got.History) != 1 {
		t.Fatalf("history len = %d after pause; pause must not discard history", len(got.History))
	}
	// Resume the session — the interrupted PAUSED task rehydrates as PAUSED
	// (it was paused, not running, so the rule does not change it), and the
	// runtime can flip it back to RUNNING. The point: the task is alive.
	m2 := New(Deps{Store: store, Bus: bus, Git: NewInMemCheckpointer()})
	_, resumed, err := m2.Resume(ctx, sid)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if resumed == nil || resumed.ID != tid || resumed.Goal != "edit a.go" {
		t.Errorf("resumed task = %+v, want same id/goal retained after pause", resumed)
	}
	if resumed.Status != StatusPaused {
		t.Errorf("resumed paused task = %q, want PAUSED (pause preserves continuation)", resumed.Status)
	}
}
