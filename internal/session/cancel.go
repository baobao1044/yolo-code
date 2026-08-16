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

	"github.com/baobao1044/yolo-code/internal/event"
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
	snap := t.clone()
	m.mu.Unlock()
	if err := m.store.SaveTask(ctx, snap); err != nil {
		return err
	}
	return m.bus.Publish(ctx, &event.TaskPausedEvent{Task: string(tid)})
}

// CancelSignal fires only step 1 of a cancel — the context cascade — and does
// no rollback, no store write and no publish. It is the half of Cancel that is
// safe to run on a goroutine other than the one driving the task.
//
// It exists because the two halves have opposite scheduling requirements, and
// running them together on one off-loop goroutine silently broke the second.
// The cascade MUST be off the drive goroutine: a loop parked inside a port call
// cannot cancel itself, and the cascade is the only thing that unblocks it. But
// the cascade is also the only step any observer can see promptly — it releases
// the drive loop, which transitions to CANCELLED, publishes state.change and
// returns, and Submit returns behind it. Everyone downstream now believes the
// cancel is complete while the rollback, the CANCELLED store write and
// task.cancelled are still queued on the off-loop goroutine that nobody joins.
// The visible symptom was a test failing its own TempDir teardown —
// "unlinkat .../tasks: directory not empty", a SaveTask landing after the last
// synchronisation point. The cost in production is worse than a flake: the
// terminal status reaches disk on an unjoined goroutine, so esc-then-quit can
// exit before it lands, and Resume's interrupted-task rule then rehydrates the
// cancelled task as live PAUSED work.
//
// Callers that only need the task to stop use this; whoever owns the task's
// goroutine calls Cancel, which is idempotent against it (a task already
// CANCELLED returns ErrTaskNotCancellable).
func (m *Manager) CancelSignal(tid TaskID) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if cancel, ok := m.cancels[tid]; ok {
		cancel()
	}
}

// Cancel stops the active task, cascades cancellation to the task's context,
// rolls back to the last checkpoint, sets the task CANCELLED (terminal), and
// publishes task.cancelled with the partial-work summary (File 03 §3.6.1).
//
// A task already in a terminal state is not cancellable: cancelling DONE work
// would be a destructive no-op, so it returns ErrTaskNotCancellable.
//
// Everything after step 1 is disk and bus work whose completion callers depend
// on, so it belongs on a goroutine somebody joins — see CancelSignal for what
// running it anywhere else cost.
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
	m.CancelSignal(tid)

	// 2. Roll back any in-flight, unverified work to the last checkpoint. No
	//    checkpoint means there was nothing to roll back; cancel still proceeds.
	//    The Checkpoint test moves inside the lock along with the history scan
	//    it guards; read bare, it raced every other mutator of the task.
	m.mu.Lock()
	var snap SnapshotRef
	if t.Checkpoint != "" {
		// Find the checkpoint's snapshot in history and roll back to it.
		for _, h := range t.History {
			if h.Summary == t.Checkpoint {
				snap = h.Snapshot
				break
			}
		}
	}
	m.mu.Unlock()
	if snap != "" {
		_ = m.git.Rollback(ctx, snap)
	}

	// 3. Mark the task CANCELLED (terminal) and stamp the end time.
	now := time.Now().UTC()
	m.mu.Lock()
	t.Status = StatusCancelled
	t.EndedAt = &now
	partial := t.Checkpoint
	persisted := t.clone()
	m.mu.Unlock()
	if err := m.store.SaveTask(ctx, persisted); err != nil {
		return err
	}

	// 4. Publish task.cancelled. The catalog (File 05 §5.4.1) carries Partial
	//    (the partial-work summary); the reason is the human/origin of cancel.
	return m.bus.Publish(ctx, &event.TaskCancelledEvent{
		Task: string(tid), Reason: reason, Partial: partial,
	})
}
