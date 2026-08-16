// History, checkpoints, and undo (File 03 §3.3/§3.4).
//
// History is a per-task stack of applied changes; each entry links "the model
// did something" to "the user can undo it". Checkpoints are a special entry
// kind that name a restorable safe state taken before any state-changing
// action. Undo and the engine's verify-rollback share one mechanism —
// Checkpointer.Rollback — so "the model reverted its own bad patch" and "the
// user hit undo" are the same operation seen from two callers (§3.3.2).
//
// DEAD SEAM — the whole file, and it is the largest one in the tree. None of
// RecordEntry, Checkpoint, Undo, Restore, or History has a caller outside this
// package's own tests. Not "underused": zero. The one thing that looks like a
// counterexample is not one — patch/engine.go:134 does call a Checkpoint, but on
// patch.Checkpointer, its own interface, satisfied in cmd/yolo by
// newShadowCheckpointer; internal/patch does not import internal/session at all.
// So checkpointing works, and this manager never hears about it.
//
// Because nothing writes it, Task.History is empty for the whole life of every
// production task, and three things downstream read as live while being dead:
//
//   - context/engine.go:272 gatherConversation turns the history into the
//     Conversation part group, which is therefore always empty — while
//     context/budget.go gives PctConversation a 45% share that allocate()
//     subtracts from the User remainder whether or not anything fills it.
//   - cancel.go:106 walks the history to build a cancelled task's Partial
//     payload, so a cancelled task always reports having done nothing.
//   - task.undone and task.restored are topics nothing ever publishes.
//
// What is NOT broken by this matters more than what is: the model still sees its
// own turns, because cognitive.Core keeps a separate c.history and merges each
// freshly compiled prompt into it. So the conversation the token budget accounts
// for is always empty, and the conversation actually sent to the provider is the
// unaccounted one — bounded by maxHistoryMessages = 200, a message count with no
// token accounting at all.
//
// Pinned by cmd/yolo/deadseam_test.go: four of the five methods by name, and
// Restore by hand, because the matcher there cannot tell it apart from the
// runtime's own Restorer port. Wiring this subsystem or deleting it is a spec
// decision; what is not defensible is leaving it looking connected.

package session

import (
	"context"
	"time"

	"github.com/baobao1044/yolo-code/internal/event"
)

// RecordEntry appends a history entry to a task (wiring used by the runtime,
// File 04 §3.7). It assigns the monotonic Seq and stamps At; the caller
// supplies Kind/Snapshot/Summary/Paths/Reversible. The task is persisted so
// the history survives a restart.
//
// It returns ErrUnknownTask for an id the manager does not hold, and the
// store's error if the persist fails. Both used to be swallowed — the method
// returned nothing, so `_ = m.store.SaveTask(...)` was the only thing the
// signature allowed. That made it the one SaveTask in this package that did
// not report (compare Checkpoint, Undo, Restore, CompleteTask, Cancel), and
// the failure it hid is not cosmetic: the entry is in memory and not on disk,
// so the next Resume loads a task whose undo stack is one rung shorter with
// nobody having been told. "Undo did nothing" and "there was nothing to undo"
// look identical from the outside.
//
// The append is NOT rolled back when the persist fails. RecordEntry is not
// transactional and this does not make it one; the caller is told what
// happened and owns the decision. See history_persist_test.go, which pins both
// halves.
func (m *Manager) RecordEntry(tid TaskID, e HistoryEntry) error {
	m.mu.Lock()
	t, ok := m.tasks[tid]
	if !ok {
		m.mu.Unlock()
		return ErrUnknownTask
	}
	e.Seq = len(t.History)
	if e.At.IsZero() {
		e.At = time.Now().UTC()
	}
	t.History = append(t.History, e)
	snap := t.clone()
	m.mu.Unlock()
	return m.store.SaveTask(context.Background(), snap)
}

// Checkpoint takes a snapshot of paths, records a checkpoint history entry,
// advances the task's Checkpoint pointer, publishes task.checkpoint, and
// returns the snapshot ref (File 03 §3.4.2).
func (m *Manager) Checkpoint(ctx context.Context, tid TaskID, name string, paths []string) (SnapshotRef, error) {
	t, err := m.task(tid)
	if err != nil {
		return "", err
	}
	snap, err := m.git.Snapshot(ctx, paths)
	if err != nil {
		return "", err
	}
	m.mu.Lock()
	t.History = append(t.History, HistoryEntry{
		Seq:        len(t.History),
		Kind:       KindCheckpoint,
		Snapshot:   snap,
		Summary:    name,
		Paths:      paths,
		Reversible: true,
		At:         time.Now().UTC(),
	})
	t.Checkpoint = name
	persisted := t.clone()
	m.mu.Unlock()
	if err := m.store.SaveTask(ctx, persisted); err != nil {
		return "", err
	}
	if err := m.bus.Publish(ctx, &event.CheckpointEvent{
		Task: string(tid), Name: name, Snapshot: []byte(snap),
	}); err != nil {
		return "", err
	}
	return snap, nil
}

// Undo pops the most recent reversible entry and rolls back to its snapshot
// (File 03 §3.3.2). It walks the checkpoint pointer back to the previous
// entry (or clears it). Returns ErrNothingToUndo on an empty stack and
// ErrNotReversible on an irreversible top entry.
func (m *Manager) Undo(ctx context.Context, tid TaskID) error {
	t, err := m.task(tid)
	if err != nil {
		return err
	}
	m.mu.Lock()
	if len(t.History) == 0 {
		m.mu.Unlock()
		return ErrNothingToUndo
	}
	last := t.History[len(t.History)-1]
	if !last.Reversible {
		m.mu.Unlock()
		return ErrNotReversible
	}
	m.mu.Unlock()

	if err := m.git.Rollback(ctx, last.Snapshot); err != nil {
		return err
	}

	m.mu.Lock()
	t.History = t.History[:len(t.History)-1]
	if len(t.History) > 0 {
		t.Checkpoint = t.History[len(t.History)-1].Summary
	} else {
		t.Checkpoint = ""
	}
	snap := t.clone()
	m.mu.Unlock()
	if err := m.store.SaveTask(ctx, snap); err != nil {
		return err
	}
	return m.bus.Publish(ctx, &event.UndoneEvent{
		Task: string(tid), Entry: []byte(summaryOf(last)),
	})
}

// Restore jumps the task back to a named checkpoint, rolling back to its
// snapshot and discarding every history entry after it (File 03 §3.4.3).
// Returns ErrUnknownCheckpoint if the name is not in the task's history.
func (m *Manager) Restore(ctx context.Context, tid TaskID, name string) error {
	t, err := m.task(tid)
	if err != nil {
		return err
	}
	m.mu.Lock()
	idx := -1
	for i, h := range t.History {
		if h.Summary == name {
			idx = i
			break
		}
	}
	if idx < 0 {
		m.mu.Unlock()
		return ErrUnknownCheckpoint
	}
	target := t.History[idx]
	m.mu.Unlock()

	if err := m.git.Rollback(ctx, target.Snapshot); err != nil {
		return err
	}

	m.mu.Lock()
	t.History = t.History[:idx+1] // discard everything after the checkpoint
	t.Checkpoint = name
	snap := t.clone()
	m.mu.Unlock()
	if err := m.store.SaveTask(ctx, snap); err != nil {
		return err
	}
	return m.bus.Publish(ctx, &event.RestoredEvent{Task: string(tid), Name: name})
}

// History returns a copy of the task's history entries (read-only view for the
// undo menu, File 14). The copy is taken under the lock: taken after releasing
// it, as it was, the TUI's render raced every append the drive goroutine made.
func (m *Manager) History(tid TaskID) []HistoryEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[tid]
	if !ok {
		return nil
	}
	return append([]HistoryEntry(nil), t.History...)
}

// summaryOf is a placeholder payload encoder for the UndoneEvent entry field,
// which the catalog carries as json.RawMessage. Sprint 1 records the summary
// so the TUI can show what was undone; the owning layer may refine it later.
func summaryOf(e HistoryEntry) string {
	return e.Summary
}
