// Retaining a *Session across goroutines.
//
// The Manager keeps one live *Session per open session and StartTask mutates it
// — appends to Tasks, stamps UpdatedAt — under m.mu. Any caller that gets that
// pointer and holds it is reading a struct the Manager goes on writing, without
// the Manager's lock. internal/runtime is that caller: Submit puts the session
// into the taskHandle and passes it into every ContextRequest for the length of
// the task, so a second Submit on the same session raced the first one's whole
// drive loop.
//
// These tests are the Session-shaped counterpart of the *Task snapshot tests:
// one deterministic assertion that the returned object stops tracking the
// Manager's, and one under -race that pins the actual write/read pair.

package session

import (
	"context"
	"testing"
)

// managerForSnapshot builds a Manager on a temp store with one open session.
func managerForSnapshot(t *testing.T) (*Manager, ID) {
	t.Helper()
	m := New(Deps{Store: NewFileStore(t.TempDir()), Bus: drainingBus(t), Git: NewInMemCheckpointer()})
	sid, err := m.OpenSession(context.Background(), "proj", "goal")
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	return m, sid
}

// TestSnapshotSessionStopsTrackingTheManagersCopy is the deterministic form of
// the bug, and it fails without a copy rather than merely being flaky: the
// retained session's Tasks slice grew from 1 to 2 when the next task started,
// with no goroutines and no scheduler luck involved. That growth is the visible
// half of an invisible problem — the same append is an unsynchronized write to
// a slice header a drive loop on another goroutine is reading.
func TestSnapshotSessionStopsTrackingTheManagersCopy(t *testing.T) {
	ctx := context.Background()
	m, sid := managerForSnapshot(t)

	if _, err := m.StartTask(ctx, sid, "one"); err != nil {
		t.Fatalf("StartTask one: %v", err)
	}
	held := m.SnapshotSession(sid)
	if held == nil {
		t.Fatal("SnapshotSession returned nil for an open session")
	}
	before := len(held.Tasks)
	if before != 1 {
		t.Fatalf("snapshot has %d tasks, want 1 — the copy is not of the current state", before)
	}

	if _, err := m.StartTask(ctx, sid, "two"); err != nil {
		t.Fatalf("StartTask two: %v", err)
	}
	if got := len(held.Tasks); got != before {
		t.Errorf("retained session tracked the Manager's: Tasks went %d -> %d. "+
			"It is a copy or it is a race; there is no third option", before, got)
	}
	// The Manager's own view must still be correct — a copy that fixed the race
	// by not recording the task would be a worse bug than the one it replaced.
	if fresh := m.SnapshotSession(sid); fresh == nil || len(fresh.Tasks) != 2 {
		t.Errorf("Manager's session lost a task: %v", fresh)
	}
}

// TestSnapshotSessionIsRaceFreeUnderStartTask is the same fact stated the way
// -race can check it. It reproduces the original defect exactly: hold the
// session the way the runtime does, then start another task on it. Without the
// copy this reports a write at manager.go's `sess.Tasks = append(...)` against
// the read below. It is a no-op without -race, which is fine — this one is here
// for the detector, and the test above is here for everyone else.
func TestSnapshotSessionIsRaceFreeUnderStartTask(t *testing.T) {
	ctx := context.Background()
	m, sid := managerForSnapshot(t)
	if _, err := m.StartTask(ctx, sid, "one"); err != nil {
		t.Fatalf("StartTask one: %v", err)
	}
	held := m.SnapshotSession(sid)

	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := m.StartTask(ctx, sid, "two"); err != nil {
			t.Errorf("concurrent StartTask: %v", err)
		}
	}()
	// Read the fields the runtime reads, hard enough to overlap the append.
	for i := 0; i < 100_000; i++ {
		_ = len(held.Tasks)
		_ = held.UpdatedAt
	}
	<-done
}

// TestSnapshotSessionIsNilForUnknown pins the miss case. Submit turns a nil
// into ErrUnknownSession, which is the error Resume returned before it; a
// snapshot that invented an empty Session instead would send the drive loop
// off with a session that does not exist.
func TestSnapshotSessionIsNilForUnknown(t *testing.T) {
	m, _ := managerForSnapshot(t)
	if got := m.SnapshotSession(ID("s_does_not_exist")); got != nil {
		t.Errorf("SnapshotSession(unknown) = %+v, want nil", got)
	}
}
