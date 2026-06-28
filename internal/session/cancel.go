// Cancel vs. pause (File 03 §3.6).
//
// The distinction matters (§3.6.2):
//   - Pause (RUNNING → PAUSED): the task stops at the next safe boundary (after
//     a verify pass, before the next loop iteration). It can be resumed in
//     place. Used for HITL approval waits and explicit user pause.
//   - Cancel (* → CANCELLED): the task stops and rolls back to the last
//     checkpoint. Resuming a cancelled task starts a fresh task with the same
//     goal rather than continuing the old one.
//
// Pause is reversible continuation; cancel is a controlled abort to a known
// safe state.

package session

import (
	"context"
	"time"

	"github.com/yolo-code/yolo/internal/event"
)

// Pause halts a running task at a safe boundary and marks it PAUSED. The task
// stays resumable in place — its history and checkpoint are preserved (File 03
// §3.6.2). It publishes task.paused.
func (m *Manager) Pause(ctx context.Context, tid TaskID) error {
	t, err := m.task(tid)
	if err != nil {
		return err
	}
	m.mu.Lock()
	t.Status = StatusPaused
	m.mu.Unlock()
	if err := m.store.SaveTask(ctx, t); err != nil {
		return err
	}
	return m.bus.Publish(ctx, &event.TaskPausedEvent{Task: string(tid)})
}

// Cancel stops the active task, cascades cancellation to the task's context,
// rolls back to the last checkpoint, sets the task CANCELLED (terminal), and
// publishes task.cancelled with the partial-work summary (File 03 §3.6.1).
//
// A task already in a terminal state is not cancellable: cancelling DONE work
// would be a destructive no-op, so it returns ErrTaskNotCancellable.
func (m *Manager) Cancel(ctx context.Context, tid TaskID, reason string) error {
	t, err := m.task(tid)
	if err != nil {
		return err
	}
	m.mu.Lock()
	terminal := t.Status.IsTerminal()
	m.mu.Unlock()
	if terminal {
		return ErrTaskNotCancellable
	}

	// 1. Cascade cancellation to the task's context (closes the LLM stream,
	//    kills tool processes — wired by the runtime via AttachCancel).
	m.mu.Lock()
	if cancel, ok := m.cancels[tid]; ok {
		cancel()
	}
	m.mu.Unlock()

	// 2. Roll back any in-flight, unverified work to the last checkpoint. No
	//    checkpoint means there was nothing to roll back; cancel still proceeds.
	if t.Checkpoint != "" {
		// Find the checkpoint's snapshot in history and roll back to it.
		m.mu.Lock()
		var snap SnapshotRef
		for _, h := range t.History {
			if h.Summary == t.Checkpoint {
				snap = h.Snapshot
				break
			}
		}
		m.mu.Unlock()
		if snap != "" {
			_ = m.git.Rollback(ctx, snap)
		}
	}

	// 3. Mark the task CANCELLED (terminal) and stamp the end time.
	now := time.Now().UTC()
	m.mu.Lock()
	t.Status = StatusCancelled
	t.EndedAt = &now
	partial := t.Checkpoint
	m.mu.Unlock()
	if err := m.store.SaveTask(ctx, t); err != nil {
		return err
	}

	// 4. Publish task.cancelled. The catalog (File 05 §5.4.1) carries Partial
	//    (the partial-work summary); the reason is the human/origin of cancel.
	return m.bus.Publish(ctx, &event.TaskCancelledEvent{
		Task: string(tid), Reason: reason, Partial: partial,
	})
}
