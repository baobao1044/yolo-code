// TUI-006 — Input handling (File 14 §14.8). handleInput translates keystrokes
// into user.* events published via the EventPublisher seam. It is a pure
// (Model, Cmd) transition — no I/O; the publish happens off-thread via the
// returned tea.Cmd (so even a slow bus can't stall Update).
//
// Per Decision 4: PUBLISH-ONLY this sprint. The runtime doesn't subscribe to
// user.* today (synchronous drive loop, no WAIT_USER/PAUSED arms), so
// keystrokes can't drive the runtime. Runtime-side consumption is deferred to
// the integration sprint (§15.9.2 bucket). Here the TUI only publishes the
// CORRECT event per keystroke — the seam contract the integration sprint plugs
// into. The TUI never validates (it can't — no logic); if Esc is pressed with
// no active task, nothing is published and the runtime's (future) handler is
// a no-op (File 14 §14.8.2).

package tui

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/baobao1044/yolo-code/internal/event"
)

// handleInput translates a keystroke into a model transition + a publish Cmd.
// Keymap (File 14 §14.8.2):
//
//	approval pending: y → user.approve, n → user.reject
//	esc              → user.cancel (only if there's an active task)
//	ctrl+p           → user.pause
//	ctrl+r           → user.resume
//	ctrl+c           → user.quit + tea.Quit (program exits)
//	enter (non-empty)→ user.submit + optimistic echo, input cleared
//	else             → (no-op here; the production path routes to textinput)
//
// Every publish returns a tea.Cmd so it runs off the render thread. handleInput
// is a pure function of (model, key) — no I/O.
func handleInput(m Model, key tea.KeyMsg) (Model, tea.Cmd) {
	s := key.String()

	// Approval pending → y/n short-circuits before the global keymap (§14.8.1).
	if m.approval != nil {
		switch s {
		case "y":
			return m, publish(m.publisher, &event.UserApproveEvent{Task: m.taskID, ApprovalID: m.approval.id})
		case "n":
			return m, publish(m.publisher, &event.UserRejectEvent{Task: m.taskID, ApprovalID: m.approval.id})
		}
	}

	// Global keymap.
	switch s {
	case "esc":
		// Don't fabricate a cancel for a phantom task (§14.8.2: the TUI doesn't
		// validate, but it also doesn't invent a task that doesn't exist — no
		// active task means no publish; the runtime's future handler is a no-op).
		if m.taskID == "" {
			return m, nil
		}
		return m, publish(m.publisher, &event.UserCancelEvent{Task: event.TaskID(m.taskID)})
	case "ctrl+p":
		if m.taskID == "" {
			return m, nil
		}
		return m, publish(m.publisher, &event.UserPauseEvent{Task: event.TaskID(m.taskID)})
	case "ctrl+r":
		if m.taskID == "" {
			return m, nil
		}
		return m, publish(m.publisher, &event.UserResumeEvent{Task: event.TaskID(m.taskID)})
	case "ctrl+c", "q":
		// Quit: publish user.quit AND return tea.Quit so the program exits.
		// Batch the publish + the quit so both run (publish off-thread, then quit).
		return m, tea.Batch(publish(m.publisher, &event.UserQuitEvent{}), tea.Quit)
	case "?":
		// Toggle help overlay.
		m.showHelp = !m.showHelp
		return m, nil
	case "tab":
		// Cycle focus through non-empty panes.
		m.focus = nextFocus(m)
		return m, nil
	case "enter":
		// Submit on Enter with non-empty input: optimistic echo + publish +
		// clear the input line. The echo keeps the UI feeling instant (P1);
		// the bus closes the loop (a rejected submit arrives as an error event).
		if m.inputText == "" {
			return m, nil
		}
		text := m.inputText
		m.messages = append(m.messages, messageView{role: "user", text: text})
		m.inputText = ""
		m.scrollOffset = 0 // reset scroll on new message
		return m, publish(m.publisher, &event.UserSubmitEvent{Text: text})
	}
	// Otherwise: no-op for the pure test path (the production path routes to
	// the bubbles/textinput widget here; TUI-006 keeps the widget out of the
	// pure transition so it's testable without a TTY).
	return m, nil
}

// publish returns a tea.Cmd that calls publisher.Publish off the render thread
// (File 14 §14.8.1). Even a slow bus can't stall Update — the publish runs as a
// command and reports back (we don't need the result, so the Cmd returns nil).
// A nil publisher is a no-op (the test path may not wire one for non-publish
// branches, though every branch here passes a fake in the publish tests).
func publish(pub EventPublisher, e event.Event) tea.Cmd {
	return func() tea.Msg {
		if pub == nil {
			return nil
		}
		_ = pub.Publish(context.Background(), e)
		return nil
	}
}
