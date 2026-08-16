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
	"errors"
	"fmt"
	"os"
)

// Resume rehydrates a session and its latest task from the store. If the latest
// task was interrupted mid-run (status RUNNING on disk), it is restored as
// PAUSED so the user must explicitly resume it (File 03 §3.5.2).
//
// It returns (nil, nil, ErrUnknownSession) for a session ID with no record.
// Anything else that stops the load — a truncated file, EACCES, EISDIR — comes
// back wrapped and named, and callers must use errors.Is to test for the
// sentinel.
func (m *Manager) Resume(ctx context.Context, sid ID) (*Session, *Task, error) {
	sess, err := m.store.LoadSession(ctx, sid)
	if err != nil {
		// Only a genuinely absent record is "unknown session". Every failure
		// used to collapse into that sentinel, so a session damaged by a crash
		// mid-save read as one that never existed — and the user's answer to
		// "unknown session" is to start a new one, which then wrote over the
		// damaged but partially recoverable file.
		if errors.Is(err, ErrNotFound) || errors.Is(err, os.ErrNotExist) {
			return nil, nil, ErrUnknownSession
		}
		return nil, nil, fmt.Errorf("resume session %s: %w", sid, err)
	}
	m.mu.Lock()
	m.sessions[sess.ID] = sess
	m.mu.Unlock()

	var task *Task
	if len(sess.Tasks) > 0 {
		last := sess.Tasks[len(sess.Tasks)-1]
		loaded, lerr := m.store.LoadTask(ctx, last)
		switch {
		case lerr == nil:
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
		case errors.Is(lerr, ErrNotFound) || errors.Is(lerr, os.ErrNotExist):
			// The session outlived its task file. The framing is still usable,
			// so resume the session with no active task rather than failing.
		default:
			// A task file that exists but will not decode is damage, and
			// reporting it as "this session has no task" would invite the user
			// to start one that overwrites it.
			return nil, nil, fmt.Errorf("resume session %s: task %s: %w", sid, last, lerr)
		}
	}

	// session.* events are not in the v1 catalog (File 05 §5.4); the task.* and
	// state.change events already render session context in the TUI. A future
	// catalog revision may add session.opened/resumed/closed if needed.
	return sess, task, nil
}
