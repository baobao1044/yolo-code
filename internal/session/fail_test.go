// Fail is the terminal outcome the Manager was missing.
//
// CompleteTask wrote DONE, Cancel wrote CANCELLED, Pause wrote PAUSED, and
// nothing anywhere in the tree ever wrote FAILED — the status was declared
// terminal in types.go and had zero writers. The runtime's hard-error path
// therefore left the task at whatever non-terminal status it was last saved
// with, which is what Resume reads to decide a task is unfinished work worth
// offering the user again.
//
// These tests pin the contract at the level it is implemented, which the
// runtime test cannot reach: the terminal guard in particular has no route
// through the drive loop, because every toError caller returns immediately, so
// it would go untested if only tested from above.

package session

import (
	"context"
	"errors"
	"testing"

	"github.com/baobao1044/yolo-code/internal/event"
)

func TestFailMarksTaskFailedAndPersists(t *testing.T) {
	m, store, bus := newTestManager(t)
	ctx := context.Background()
	ch := bus.Subscribe("task.failed")

	sid, err := m.OpenSession(ctx, "proj", "demo")
	if err != nil {
		t.Fatal(err)
	}
	tid, err := m.StartTask(ctx, sid, "do the thing")
	if err != nil {
		t.Fatal(err)
	}

	if err := m.Fail(ctx, tid, "planner exploded"); err != nil {
		t.Fatalf("Fail: %v", err)
	}

	// Read from the store, not the in-memory map: the whole point of recording
	// a terminal status is that it survives the process.
	got, err := store.LoadTask(ctx, tid)
	if err != nil {
		t.Fatalf("LoadTask: %v", err)
	}
	if got.Status != StatusFailed {
		t.Errorf("persisted status = %q, want %q", got.Status, StatusFailed)
	}
	if !got.Status.IsTerminal() {
		t.Errorf("status %q is not terminal", got.Status)
	}
	if got.EndedAt == nil || got.EndedAt.IsZero() {
		t.Error("Fail did not stamp EndedAt; CompleteTask and Cancel both do, and " +
			"without it a failed task has no measurable duration")
	}

	env, ok := recv(t, ch)
	if !ok {
		t.Fatal("Fail published nothing on task.failed")
	}
	ev, ok := env.Evt.(*event.TaskFailedEvent)
	if !ok {
		t.Fatalf("event was %T, want *event.TaskFailedEvent", env.Evt)
	}
	if ev.Reason != "planner exploded" {
		t.Errorf("Reason = %q, want %q; the cause is the only thing distinguishing "+
			"one failure from another in the log", ev.Reason, "planner exploded")
	}
}

// TestFailLeavesAnAlreadyTerminalTaskAlone is the guard against a late error
// rewriting a recorded outcome. A task that completed and then tripped an error
// on the way out has still done its work; reporting it as FAILED would be a
// lie, and would also flip a DONE task's EndedAt to a later time.
func TestFailLeavesAnAlreadyTerminalTaskAlone(t *testing.T) {
	m, store, _ := newTestManager(t)
	ctx := context.Background()

	sid, err := m.OpenSession(ctx, "proj", "demo")
	if err != nil {
		t.Fatal(err)
	}
	tid, err := m.StartTask(ctx, sid, "do the thing")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.CompleteTask(ctx, tid); err != nil {
		t.Fatalf("CompleteTask: %v", err)
	}
	done, err := store.LoadTask(ctx, tid)
	if err != nil {
		t.Fatal(err)
	}
	wantEnded := done.EndedAt

	err = m.Fail(ctx, tid, "a late error")
	if !errors.Is(err, ErrTaskAlreadyTerminal) {
		t.Fatalf("Fail on a DONE task returned %v, want ErrTaskAlreadyTerminal", err)
	}

	got, err := store.LoadTask(ctx, tid)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusDone {
		t.Errorf("status after a refused Fail = %q, want %q: the completion must stand",
			got.Status, StatusDone)
	}
	if wantEnded != nil && (got.EndedAt == nil || !got.EndedAt.Equal(*wantEnded)) {
		t.Error("a refused Fail moved EndedAt; it must not touch the task at all")
	}
}

func TestFailOnUnknownTask(t *testing.T) {
	m, _, _ := newTestManager(t)
	if err := m.Fail(context.Background(), TaskID("t_nope"), "boom"); !errors.Is(err, ErrUnknownTask) {
		t.Fatalf("Fail on an unknown task returned %v, want ErrUnknownTask", err)
	}
}
