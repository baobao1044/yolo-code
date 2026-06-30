package session

import (
	"context"
	"testing"

	"github.com/baobao1044/yolo-code/internal/event"
)

func TestCheckpointRecordsEntryAndPublishes(t *testing.T) {
	m, store, bus := newTestManager(t)
	ctx := context.Background()

	sid, _ := m.OpenSession(ctx, "proj", "demo")
	tid, _ := m.StartTask(ctx, sid, "edit a.go")
	ch := bus.Subscribe("task.checkpoint")

	snap, err := m.Checkpoint(ctx, tid, "pre-edit", []string{"a.go"})
	if err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	if snap == "" {
		t.Error("Checkpoint returned empty SnapshotRef")
	}

	env, ok := recv(t, ch)
	if !ok {
		t.Fatal("task.checkpoint not published")
	}
	evt, ok := env.Evt.(*event.CheckpointEvent)
	if !ok {
		t.Fatalf("event type = %T, want *CheckpointEvent", env.Evt)
	}
	if evt.Task != string(tid) || evt.Name != "pre-edit" {
		t.Errorf("task.checkpoint = %+v, want task=%q name=%q", evt, tid, "pre-edit")
	}

	// The task's Checkpoint field advances and the history gained an entry.
	got, _ := store.LoadTask(ctx, tid)
	if got.Checkpoint != "pre-edit" {
		t.Errorf("task.Checkpoint = %q, want %q", got.Checkpoint, "pre-edit")
	}
	if len(got.History) != 1 {
		t.Fatalf("len(History) = %d, want 1", len(got.History))
	}
	if got.History[0].Kind != KindCheckpoint || got.History[0].Summary != "pre-edit" {
		t.Errorf("history[0] = %+v, want checkpoint/pre-edit", got.History[0])
	}
}

func TestUndoPopsReversibleEntryAndRollsBack(t *testing.T) {
	m, store, bus := newTestManager(t)
	ckpt := m.git.(*InMemCheckpointer)
	ctx := context.Background()

	sid, _ := m.OpenSession(ctx, "proj", "demo")
	tid, _ := m.StartTask(ctx, sid, "edit a.go")
	ch := bus.Subscribe("task.undone")

	snap, _ := m.Checkpoint(ctx, tid, "pre-edit", []string{"a.go"})

	if err := m.Undo(ctx, tid); err != nil {
		t.Fatalf("Undo: %v", err)
	}

	env, ok := recv(t, ch)
	if !ok {
		t.Fatal("task.undone not published")
	}
	if _, ok := env.Evt.(*event.UndoneEvent); !ok {
		t.Fatalf("event type = %T, want *UndoneEvent", env.Evt)
	}

	// The entry is popped and the rollback was called with its snapshot.
	got, _ := store.LoadTask(ctx, tid)
	if len(got.History) != 0 {
		t.Errorf("len(History) = %d, want 0 after undo", len(got.History))
	}
	if got.Checkpoint != "" {
		t.Errorf("Checkpoint = %q, want empty after undoing the only entry", got.Checkpoint)
	}
	if rolls := ckpt.Rollbacks(); len(rolls) != 1 || rolls[0] != snap {
		t.Errorf("rollbacks = %v, want [%q]", rolls, snap)
	}
}

func TestUndoNothingToUndo(t *testing.T) {
	m, _, _ := newTestManager(t)
	ctx := context.Background()

	sid, _ := m.OpenSession(ctx, "proj", "demo")
	tid, _ := m.StartTask(ctx, sid, "edit a.go")

	if err := m.Undo(ctx, tid); err != ErrNothingToUndo {
		t.Errorf("Undo empty history: err = %v, want ErrNothingToUndo", err)
	}
}

func TestUndoIrreversibleEntryRejected(t *testing.T) {
	m, _, _ := newTestManager(t)
	ctx := context.Background()

	sid, _ := m.OpenSession(ctx, "proj", "demo")
	tid, _ := m.StartTask(ctx, sid, "edit a.go")
	// Hand-place an irreversible entry so Undo sees it.
	m.RecordEntry(tid, HistoryEntry{Seq: 0, Kind: KindBash, Summary: "rm -rf", Reversible: false})

	if err := m.Undo(ctx, tid); err != ErrNotReversible {
		t.Errorf("Undo irreversible: err = %v, want ErrNotReversible", err)
	}
}

func TestRestoreJumpsToCheckpointAndDiscardsLaterHistory(t *testing.T) {
	m, store, bus := newTestManager(t)
	ctx := context.Background()

	sid, _ := m.OpenSession(ctx, "proj", "demo")
	tid, _ := m.StartTask(ctx, sid, "edit a.go")
	ch := bus.Subscribe("task.restored")

	// Three checkpoints; restore to the middle one.
	_, _ = m.Checkpoint(ctx, tid, "cp1", []string{"a.go"})
	_, _ = m.Checkpoint(ctx, tid, "cp2", []string{"a.go"})
	_, _ = m.Checkpoint(ctx, tid, "cp3", []string{"a.go"})

	if err := m.Restore(ctx, tid, "cp2"); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	env, ok := recv(t, ch)
	if !ok {
		t.Fatal("task.restored not published")
	}
	evt, ok := env.Evt.(*event.RestoredEvent)
	if !ok {
		t.Fatalf("event type = %T, want *RestoredEvent", env.Evt)
	}
	if evt.Name != "cp2" {
		t.Errorf("restored Name = %q, want cp2", evt.Name)
	}

	got, _ := store.LoadTask(ctx, tid)
	if got.Checkpoint != "cp2" {
		t.Errorf("Checkpoint = %q, want cp2", got.Checkpoint)
	}
	if len(got.History) != 2 {
		t.Fatalf("len(History) = %d, want 2 (cp1, cp2 retained; cp3 discarded)", len(got.History))
	}
	if got.History[1].Summary != "cp2" {
		t.Errorf("history[1].Summary = %q, want cp2", got.History[1].Summary)
	}
}

func TestRestoreUnknownCheckpointRejected(t *testing.T) {
	m, _, _ := newTestManager(t)
	ctx := context.Background()

	sid, _ := m.OpenSession(ctx, "proj", "demo")
	tid, _ := m.StartTask(ctx, sid, "edit a.go")

	if err := m.Restore(ctx, tid, "nope"); err != ErrUnknownCheckpoint {
		t.Errorf("Restore unknown: err = %v, want ErrUnknownCheckpoint", err)
	}
}

func TestResumeRestoresFullStateFromDisk(t *testing.T) {
	store := NewFileStore(t.TempDir())
	bus := event.New()
	t.Cleanup(func() { _ = bus.Close() })
	ctx := context.Background()

	// First manager: open a session + task, persist.
	m1 := New(Deps{Store: store, Bus: bus, Git: NewInMemCheckpointer()})
	sid, _ := m1.OpenSession(ctx, "proj", "demo")
	tid, _ := m1.StartTask(ctx, sid, "do the thing")
	_, _ = m1.Checkpoint(ctx, tid, "cp1", []string{"a.go"})

	// Restart: a brand-new manager over the same store. Resume must rehydrate
	// the session AND its latest task from disk, including history.
	m2 := New(Deps{Store: store, Bus: bus, Git: NewInMemCheckpointer()})
	sess, task, err := m2.Resume(ctx, sid)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if sess.ID != sid || len(sess.Tasks) != 1 || sess.Tasks[0] != tid {
		t.Errorf("resumed session = %+v, want id %q with task %q", sess, sid, tid)
	}
	if task == nil {
		t.Fatal("resumed task is nil")
	}
	if task.ID != tid || task.Goal != "do the thing" {
		t.Errorf("resumed task = %+v, want id %q goal %q", task, tid, "do the thing")
	}
	if task.Checkpoint != "cp1" || len(task.History) != 1 {
		t.Errorf("resumed task checkpoint/history = %q/%d, want cp1/1", task.Checkpoint, len(task.History))
	}
}

// TestResumeMarksInterruptedRunningAsPaused is the P2 safety rule (File 03
// §3.5.2): a task that was RUNNING when the process died is restored as PAUSED,
// never auto-resumed — an agent mid-patch when the laptop died must not silently
// continue mutating files on the next launch.
func TestResumeMarksInterruptedRunningAsPaused(t *testing.T) {
	store := NewFileStore(t.TempDir())
	bus := event.New()
	t.Cleanup(func() { _ = bus.Close() })
	ctx := context.Background()

	m1 := New(Deps{Store: store, Bus: bus, Git: NewInMemCheckpointer()})
	sid, _ := m1.OpenSession(ctx, "proj", "demo")
	tid, _ := m1.StartTask(ctx, sid, "do the thing")
	// Simulate the crash mid-run: hand-set the persisted task to RUNNING.
	task, _ := m1.task(tid)
	task.Status = StatusRunning
	_ = store.SaveTask(ctx, task)

	// Restart + resume.
	m2 := New(Deps{Store: store, Bus: bus, Git: NewInMemCheckpointer()})
	_, task, err := m2.Resume(ctx, sid)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if task.Status != StatusPaused {
		t.Errorf("interrupted task status = %q, want %q (resumes as PAUSED, never auto-resumed)", task.Status, StatusPaused)
	}
}

func TestResumeUnknownSession(t *testing.T) {
	m, _, _ := newTestManager(t)
	ctx := context.Background()
	if _, _, err := m.Resume(ctx, "nope"); err != ErrUnknownSession {
		t.Errorf("Resume unknown: err = %v, want ErrUnknownSession", err)
	}
}

func TestRecordEntryAppendsMonotonic(t *testing.T) {
	m, store, _ := newTestManager(t)
	ctx := context.Background()

	sid, _ := m.OpenSession(ctx, "proj", "demo")
	tid, _ := m.StartTask(ctx, sid, "edit a.go")

	m.RecordEntry(tid, HistoryEntry{Kind: KindPatch, Summary: "p1", Reversible: true})
	m.RecordEntry(tid, HistoryEntry{Kind: KindPatch, Summary: "p2", Reversible: true})

	got, _ := store.LoadTask(ctx, tid)
	if len(got.History) != 2 {
		t.Fatalf("len(History) = %d, want 2", len(got.History))
	}
	if got.History[0].Seq != 0 || got.History[1].Seq != 1 {
		t.Errorf("Seqs = %d,%d, want 0,1 (monotonic per task)", got.History[0].Seq, got.History[1].Seq)
	}
}
