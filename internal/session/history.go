// History, checkpoints, and undo (File 03 §3.3/§3.4).
//
// History is a per-task stack of applied changes; each entry links "the model
// did something" to "the user can undo it". Checkpoints are a special entry
// kind that name a restorable safe state taken before any state-changing
// action. Undo and the engine's verify-rollback share one mechanism —
// Checkpointer.Rollback — so "the model reverted its own bad patch" and "the
// user hit undo" are the same operation seen from two callers (§3.3.2).

package session

import (
	"context"
	"time"

	"github.com/yolo-code/yolo/internal/event"
)

// RecordEntry appends a history entry to a task (wiring used by the runtime,
// File 04 §3.7). It assigns the monotonic Seq and stamps At; the caller
// supplies Kind/Snapshot/Summary/Paths/Reversible. The task is persisted so
// the history survives a restart.
func (m *Manager) RecordEntry(tid TaskID, e HistoryEntry) {
	m.mu.Lock()
	t, ok := m.tasks[tid]
	if !ok {
		m.mu.Unlock()
		return
	}
	e.Seq = len(t.History)
	if e.At.IsZero() {
		e.At = time.Now().UTC()
	}
	t.History = append(t.History, e)
	m.mu.Unlock()
	_ = m.store.SaveTask(context.Background(), t)
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
	m.mu.Unlock()
	if err := m.store.SaveTask(ctx, t); err != nil {
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
	m.mu.Unlock()
	if err := m.store.SaveTask(ctx, t); err != nil {
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
	m.mu.Unlock()
	if err := m.store.SaveTask(ctx, t); err != nil {
		return err
	}
	return m.bus.Publish(ctx, &event.RestoredEvent{Task: string(tid), Name: name})
}

// History returns a copy of the task's history entries (read-only view for the
// undo menu, File 14).
func (m *Manager) History(tid TaskID) []HistoryEntry {
	m.mu.Lock()
	t, ok := m.tasks[tid]
	m.mu.Unlock()
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
