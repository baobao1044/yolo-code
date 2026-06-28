// TUI-001 fold — the pure projection (File 14 §14.4.2). fold is a pure
// (Model, Cmd) transition: it type-switches on env.Evt (NOT env.Str() — that
// accessor doesn't exist; Envelope.Evt is a typed event.Event), folds the
// event into render state, and re-launches busWatcher so the bridge keeps
// pumping. It never calls the runtime — pinned by the nil-seam safety test.
//
// Spec gap (File 14 §14.4.2): the doc uses env.Str("task_id") / env.Str("kind")
// — those don't exist. Every event has a pointer receiver, so the type switch
// is on *XxxEvent and reads typed fields (e.g. e.Task, e.Goal). Field growth is
// ticket-driven: TUI-001 handles task.started; later tickets append cases.

package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/yolo-code/yolo/internal/event"
)

// fold folds one bus envelope into the model and returns the re-launched
// busWatcher Cmd (so the bridge pumps the next event). It is a pure function
// of (model, env) — no I/O, no runtime call. The re-launched watcher is nil
// when there's no subscription channel (the pure-projection test path), so
// fold is callable without a bus.
func fold(m Model, env event.Envelope) (Model, tea.Cmd) {
	switch e := env.Evt.(type) {
	case *event.TaskStartedEvent:
		// Header (TUI-001). TaskStartedEvent has Task/Session/Goal — NO Kind
		// field (spec gap: File 14 §14.4.2 reads env.Str("kind")). The header
		// shows the goal instead.
		m.taskID = e.Task
		m.goal = e.Goal
		m.state = "" // reset for a new task; state.change repopulates (TUI-003)
	}
	return m, relaunchWatcher(m)
}

// relaunchWatcher returns the next busWatcher Cmd, or nil when there's no
// subscription channel (the pure-projection test path). Centralized so every
// fold case re-launches the bridge identically.
func relaunchWatcher(m Model) tea.Cmd {
	if m.sub == nil {
		return nil
	}
	return busWatcher(m.sub, m.cancel)
}
