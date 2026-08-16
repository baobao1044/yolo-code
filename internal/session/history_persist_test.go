// What RecordEntry does when the disk says no.
//
// RecordEntry is the only SaveTask call in this package that used to discard
// its error (`_ = m.store.SaveTask(...)`, history.go:37). Every other one —
// Checkpoint, Undo, Restore, CompleteTask, Cancel — returns it. The asymmetry
// was not a decision, it was the signature: RecordEntry returned nothing, so
// there was nowhere to put the error and the blank identifier absorbed it.
//
// What that costs is specific. RecordEntry appends to the in-memory *Task and
// then persists it. If the persist fails, the entry exists in memory and does
// not exist on disk, and nobody is told. The next Resume loads the task from
// disk and the entry is simply gone — including a Reversible one, which means
// the undo stack silently loses a rung. "Undo did nothing" and "there was
// nothing to undo" are the same observation to a user.
//
// These tests pin both halves: the error comes out, and the in-memory append
// still happened (RecordEntry is not transactional, and pretending otherwise
// by rolling back the append would be a different, larger change).

package session

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/baobao1044/yolo-code/internal/event"
)

// errSaveDenied is what the store returns once arm() has been called.
var errSaveDenied = errors.New("store: disk is full")

// armedFailStore wraps a real FileStore and fails SaveTask on demand. Setup
// (OpenSession/StartTask) needs a working store, so the failure is armed after
// the task exists rather than from the start.
type armedFailStore struct {
	Store
	mu    sync.Mutex
	armed bool
}

func (s *armedFailStore) arm() {
	s.mu.Lock()
	s.armed = true
	s.mu.Unlock()
}

func (s *armedFailStore) SaveTask(ctx context.Context, t *Task) error {
	s.mu.Lock()
	armed := s.armed
	s.mu.Unlock()
	if armed {
		return errSaveDenied
	}
	return s.Store.SaveTask(ctx, t)
}

func newFailingManager(t *testing.T) (*Manager, *armedFailStore) {
	t.Helper()
	store := &armedFailStore{Store: NewFileStore(t.TempDir())}
	bus := event.New()
	t.Cleanup(func() { _ = bus.Close() })
	return New(Deps{Store: store, Bus: bus, Git: NewInMemCheckpointer()}), store
}

// TestRecordEntryReturnsTheSaveError is the headline. A history entry that
// reached memory but not disk is a lie the next Resume tells.
func TestRecordEntryReturnsTheSaveError(t *testing.T) {
	m, store := newFailingManager(t)
	ctx := context.Background()

	sid, err := m.OpenSession(ctx, "proj", "demo")
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	tid, err := m.StartTask(ctx, sid, "edit a.go")
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}

	store.arm()
	err = m.RecordEntry(tid, HistoryEntry{Kind: KindPatch, Summary: "p1", Reversible: true})
	if !errors.Is(err, errSaveDenied) {
		t.Fatalf("RecordEntry error = %v, want %v.\n"+
			"The entry is in memory and not on disk. Silently, the undo stack now "+
			"has a rung that disappears on the next Resume.", err, errSaveDenied)
	}
}

// TestRecordEntryStillAppendsWhenSaveFails pins the other half: the error is
// reported, not compensated. Callers that treat a failed persist as "the entry
// was rejected" would be wrong about the in-memory task, so say so here.
func TestRecordEntryStillAppendsWhenSaveFails(t *testing.T) {
	m, store := newFailingManager(t)
	ctx := context.Background()

	sid, _ := m.OpenSession(ctx, "proj", "demo")
	tid, _ := m.StartTask(ctx, sid, "edit a.go")

	store.arm()
	_ = m.RecordEntry(tid, HistoryEntry{Kind: KindPatch, Summary: "p1", Reversible: true})

	if got := m.History(tid); len(got) != 1 {
		t.Errorf("in-memory History = %d entries, want 1 — RecordEntry appends "+
			"before it persists and does not roll back on a save failure", len(got))
	}
}

// TestRecordEntryReportsUnknownTask closes the other silent return. The
// original body returned early with no error when the id was not in the map,
// so recording against a finished or mistyped task looked exactly like
// recording against a live one.
func TestRecordEntryReportsUnknownTask(t *testing.T) {
	m, _, _ := newTestManager(t)
	err := m.RecordEntry(TaskID("t_nope"), HistoryEntry{Kind: KindPatch, Summary: "p"})
	if !errors.Is(err, ErrUnknownTask) {
		t.Errorf("RecordEntry on an unknown task = %v, want ErrUnknownTask", err)
	}
}
