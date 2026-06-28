// Resume (File 03 §3.5). A session is closed by quitting; reopening lists
// sessions and selecting one calls Resume, which rehydrates the session and
// its latest task from the store.
//
// The interrupted-task rule (§3.5.2, a P2 safety property): a task that was
// RUNNING when the process died is restored as PAUSED, never auto-resumed. An
// agent mid-patch when the laptop died must not silently continue mutating
// files on the next launch; the user explicitly resumes it.

package session

import (
	"context"
)

// Resume rehydrates a session and its latest task from the store. If the latest
// task was interrupted mid-run (status RUNNING on disk), it is restored as
// PAUSED so the user must explicitly resume it (File 03 §3.5.2).
//
// It returns (nil, nil, ErrUnknownSession) for an unknown session ID.
func (m *Manager) Resume(ctx context.Context, sid ID) (*Session, *Task, error) {
	sess, err := m.store.LoadSession(ctx, sid)
	if err != nil {
		return nil, nil, ErrUnknownSession
	}
	m.mu.Lock()
	m.sessions[sess.ID] = sess
	m.mu.Unlock()

	var task *Task
	if len(sess.Tasks) > 0 {
		last := sess.Tasks[len(sess.Tasks)-1]
		loaded, lerr := m.store.LoadTask(ctx, last)
		if lerr == nil {
			// The interrupted-task rule: a task that was RUNNING when the
			// process died resumes as PAUSED, never auto-resumed.
			if loaded.Status == StatusRunning {
				loaded.Status = StatusPaused
				_ = m.store.SaveTask(ctx, loaded)
			}
			task = loaded
			m.mu.Lock()
			m.tasks[loaded.ID] = loaded
			m.mu.Unlock()
		}
	}

	// session.* events are not in the v1 catalog (File 05 §5.4); the task.* and
	// state.change events already render session context in the TUI. A future
	// catalog revision may add session.opened/resumed/closed if needed.
	return sess, task, nil
}
