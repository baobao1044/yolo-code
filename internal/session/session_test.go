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

	// Simulate a process restart: a brand-new Manager over the same store.
	restart := New(Deps{Store: store, Bus: bus, Git: NewInMemCheckpointer()})
	_ = restart

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
