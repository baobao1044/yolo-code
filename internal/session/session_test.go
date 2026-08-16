package session

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/baobao1044/yolo-code/internal/event"
)

// newTestManager wires a Manager backed by a fresh file store in a temp dir,
// an in-memory event bus, and an in-memory checkpointer. It returns the
// manager, the store (for direct persistence assertions), and the bus (for
// event capture).
func newTestManager(t *testing.T) (*Manager, *FileStore, *event.Bus) {
	t.Helper()
	dir := t.TempDir()
	store := NewFileStore(dir)
	bus := event.New()
	t.Cleanup(func() { _ = bus.Close() })
	m := New(Deps{Store: store, Bus: bus, Git: NewInMemCheckpointer()})
	return m, store, bus
}

// recv pulls one event of any type with a short timeout.
func recv(t *testing.T, ch <-chan event.Envelope) (event.Envelope, bool) {
	t.Helper()
	select {
	case env, ok := <-ch:
		return env, ok
	case <-time.After(500 * time.Millisecond):
		return event.Envelope{}, false
	}
}

func TestOpenSessionPersistsAndAssignsID(t *testing.T) {
	m, store, _ := newTestManager(t)
	ctx := context.Background()

	sid, err := m.OpenSession(ctx, "proj-1", "demo")
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	if sid == "" {
		t.Fatal("OpenSession returned empty ID")
	}

	// Persisted: a fresh read of the store returns the session.
	got, err := store.LoadSession(ctx, sid)
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if got.ID != sid {
		t.Errorf("persisted ID = %q, want %q", got.ID, sid)
	}
	if got.ProjectID != "proj-1" || got.Title != "demo" {
		t.Errorf("persisted session = %+v, want project proj-1 / title demo", got)
	}
	if got.CreatedAt.IsZero() {
		t.Error("CreatedAt is zero; OpenSession must stamp it")
	}
}

func TestStartTaskAppendsToSessionAndPublishesStarted(t *testing.T) {
	m, store, bus := newTestManager(t)
	ctx := context.Background()

	sid, _ := m.OpenSession(ctx, "proj", "demo")
	ch := bus.Subscribe("task.started")

	tid, err := m.StartTask(ctx, sid, "say hi")
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	if tid == "" {
		t.Fatal("StartTask returned empty TaskID")
	}

	// Event published with the right framing.
	env, ok := recv(t, ch)
	if !ok {
		t.Fatal("task.started not published")
	}
	evt, ok := env.Evt.(*event.TaskStartedEvent)
	if !ok {
		t.Fatalf("event type = %T, want *TaskStartedEvent", env.Evt)
	}
	if evt.Task != string(tid) || evt.Session != string(sid) || evt.Goal != "say hi" {
		t.Errorf("task.started = %+v, want task=%q session=%q goal=%q", evt, tid, sid, "say hi")
	}

	// Task is PENDING and appended to the session's task list.
	got, err := store.LoadTask(ctx, tid)
	if err != nil {
		t.Fatalf("LoadTask: %v", err)
	}
	if got.Status != StatusPending {
		t.Errorf("new task Status = %q, want %q", got.Status, StatusPending)
	}
	if got.Goal != "say hi" || got.SessionID != sid {
		t.Errorf("persisted task = %+v", got)
	}
	if got.RetryMax <= 0 {
		t.Errorf("RetryMax = %d, want > 0 (default reflection cap)", got.RetryMax)
	}

	sess, _ := store.LoadSession(ctx, sid)
	if len(sess.Tasks) != 1 || sess.Tasks[0] != tid {
		t.Errorf("session.Tasks = %v, want [%q]", sess.Tasks, tid)
	}
}

func TestCompleteTaskSetsDoneAndPublishesCompleted(t *testing.T) {
	m, store, bus := newTestManager(t)
	ctx := context.Background()

	sid, _ := m.OpenSession(ctx, "proj", "demo")
	tid, _ := m.StartTask(ctx, sid, "say hi")
	ch := bus.Subscribe("task.completed")

	if err := m.CompleteTask(ctx, tid); err != nil {
		t.Fatalf("CompleteTask: %v", err)
	}

	env, ok := recv(t, ch)
	if !ok {
		t.Fatal("task.completed not published")
	}
	if _, ok := env.Evt.(*event.TaskCompletedEvent); !ok {
		t.Fatalf("event type = %T, want *TaskCompletedEvent", env.Evt)
	}

	got, _ := store.LoadTask(ctx, tid)
	if got.Status != StatusDone {
		t.Errorf("Status = %q, want %q", got.Status, StatusDone)
	}
	if got.EndedAt == nil || got.EndedAt.IsZero() {
		t.Error("EndedAt not set on completion")
	}
}

// TestCreateRunDonePersistsEndToEnd is the L1-001 headline exit: the full
// create→run→done sequence survives a reload from disk.
func TestCreateRunDonePersistsEndToEnd(t *testing.T) {
	m, store, bus := newTestManager(t)
	ctx := context.Background()
	bus.Subscribe(">") // keep the bus draining so publishes never block

	sid, _ := m.OpenSession(ctx, "proj", "demo")
	tid, _ := m.StartTask(ctx, sid, "do the thing")
	if err := m.CompleteTask(ctx, tid); err != nil {
		t.Fatalf("CompleteTask: %v", err)
	}

	// Simulate a process restart: a brand-new Manager over the same store, and
	// actually drive it. The Manager built here used to be assigned to _, so the
	// test named after restart safety exercised nothing — a second Manager
	// reissuing s_1/t_1 and truncating the first run's records went unnoticed.
	restart := New(Deps{Store: store, Bus: bus, Git: NewInMemCheckpointer()})
	rsess, rtask, err := restart.Resume(ctx, sid)
	if err != nil {
		t.Fatalf("restart Resume: %v", err)
	}
	if rsess.ID != sid || rtask == nil || rtask.ID != tid || rtask.Status != StatusDone {
		t.Errorf("restart Resume = session %q / task %+v, want %q / %q DONE", rsess.ID, rtask, sid, tid)
	}
	// The restarted Manager's own allocations must not land on the first run's.
	rsid, err := restart.OpenSession(ctx, "proj", "second launch")
	if err != nil {
		t.Fatalf("restart OpenSession: %v", err)
	}
	rtid, err := restart.StartTask(ctx, rsid, "second goal")
	if err != nil {
		t.Fatalf("restart StartTask: %v", err)
	}
	if rsid == sid {
		t.Errorf("restart reissued session id %q", sid)
	}
	if rtid == tid {
		t.Errorf("restart reissued task id %q", tid)
	}

	// The session file lists the task; the task file shows it DONE.
	sess, err := store.LoadSession(ctx, sid)
	if err != nil {
		t.Fatalf("reload session: %v", err)
	}
	if len(sess.Tasks) != 1 || sess.Tasks[0] != tid {
		t.Errorf("reloaded session.Tasks = %v, want [%q]", sess.Tasks, tid)
	}
	got, err := store.LoadTask(ctx, tid)
	if err != nil {
		t.Fatalf("reload task: %v", err)
	}
	if got.Status != StatusDone || got.Goal != "do the thing" {
		t.Errorf("reloaded task = %+v, want DONE / goal %q", got, "do the thing")
	}

	// Files actually exist on disk.
	if _, err := statFile(filepath.Join(store.root, "sessions", string(sid)+".json")); err != nil {
		t.Errorf("session file missing on disk: %v", err)
	}
	if _, err := statFile(filepath.Join(store.root, "tasks", string(tid)+".json")); err != nil {
		t.Errorf("task file missing on disk: %v", err)
	}
}

// statFile is a thin os.Stat wrapper so the persistence assertions read clearly.
func statFile(path string) (os.FileInfo, error) { return os.Stat(path) }

// TestSnapshotTaskDoesNotRaceTheManager pins the accessor the runtime needs.
//
// LoadTaskPublic hands out the Manager's live object, so a caller that wants to
// keep a task has to copy it — and the copy is itself an unsynchronized read of
// a struct Cancel and CompleteTask are still writing. The window is narrow but
// real: the runtime holds a task for the whole drive loop, and a cancel
// arriving between StartTask and the copy lands inside it.
//
// SnapshotTask closes it by taking the copy under m.mu. Removing that lock (or
// pointing the loop below at LoadTaskPublic + an external copy) makes this fail
// under -race, which is what makes it a test rather than a comment.
func TestSnapshotTaskDoesNotRaceTheManager(t *testing.T) {
	m, _, _ := newTestManager(t)
	ctx := context.Background()

	sid, err := m.OpenSession(ctx, "/repo", "snapshot")
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}

	// One cancel per task: Cancel is terminal, so re-cancelling the same task
	// stops writing and the race window closes with it.
	const n = 40
	tids := make([]TaskID, 0, n)
	for i := 0; i < n; i++ {
		tid, err := m.StartTask(ctx, sid, "goal")
		if err != nil {
			t.Fatalf("StartTask %d: %v", i, err)
		}
		tids = append(tids, tid)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for _, tid := range tids {
			_ = m.Cancel(ctx, tid, "test")
		}
	}()
	for _, tid := range tids {
		if snap := m.SnapshotTask(tid); snap != nil && snap.ID != tid {
			t.Errorf("SnapshotTask(%s) returned task %s", tid, snap.ID)
		}
	}
	<-done

	if m.SnapshotTask("t_nope") != nil {
		t.Error("SnapshotTask returned non-nil for an unknown task")
	}
}

// TestSnapshotTaskIsACopy pins the other half: the returned task must not alias
// the Manager's, or the caller is back to writing inside this package's
// critical section from its own goroutine. History is the field that matters —
// Undo and Restore reslice the Manager's backing array.
func TestSnapshotTaskIsACopy(t *testing.T) {
	m, _, _ := newTestManager(t)
	ctx := context.Background()

	sid, err := m.OpenSession(ctx, "/repo", "snapshot")
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	tid, err := m.StartTask(ctx, sid, "goal")
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	m.RecordEntry(tid, HistoryEntry{Kind: KindBash, Summary: "one"})

	snap := m.SnapshotTask(tid)
	if snap == nil {
		t.Fatal("SnapshotTask returned nil for a known task")
	}
	if snap == m.LoadTaskPublic(tid) {
		t.Fatal("SnapshotTask returned the live pointer, not a copy")
	}
	if len(snap.History) != 1 {
		t.Fatalf("snapshot carries %d history entries, want 1", len(snap.History))
	}
	snap.History[0].Summary = "mutated by the caller"
	if live := m.LoadTaskPublic(tid); live.History[0].Summary != "one" {
		t.Errorf("writing the snapshot's history reached the Manager's task: %q",
			live.History[0].Summary)
	}
}
