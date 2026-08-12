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
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/baobao1044/yolo-code/internal/event"
)

// handleInput translates a keystroke into a model transition + a publish Cmd.
// Keymap (File 14 §14.8.2):
//
//	approval pending: y → user.approve, n → user.reject
//	                  (but ?/scroll/esc/quit still work — Phase B non-trapping)
//	esc              → user.cancel (only if there's an active task)
//	ctrl+p           → user.pause
//	ctrl+r           → user.resume
//	ctrl+c / q       → user.quit + tea.Quit (program exits)
//	?                → toggle help overlay
//	tab              → cycle pane focus
//	pgup / pgdown    → scroll the chat pane
//	enter (non-empty)→ user.submit + optimistic echo, input reset
//	else             → (no-op; editable keys never arrive here — Update
//	                   routes them to m.input.Update before handleInput)
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

	// Global keymap (also reachable during approval — Phase B non-trapping).
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
	case "pgup":
		m.scrollOffset += 10
		return m, nil
	case "pgdown":
		m.scrollOffset -= 10
		if m.scrollOffset < 0 {
			m.scrollOffset = 0
		}
		return m, nil
	case "enter":
		// Submit on Enter with non-empty input. A line starting with "/" is a
		// slash command: local commands (/help, /clear, /theme) mutate the
		// model directly; runtime commands (/model, /provider, /status) publish
		// a UserCommandEvent the driver handles (swap provider, build status).
		// Regular text → optimistic echo + publish user.submit.
		if m.input.Value() == "" {
			return m, nil
		}
		text := m.input.Value()
		m.input.Reset()
		m.scrollOffset = 0 // reset scroll on new message
		return handleSlashCommand(m, text)
	}
	// Otherwise: no-op. Editable keys (printable, backspace, arrows, etc.)
	// never reach handleInput — Update routes them to m.input.Update before
	// calling handleInput. Only command keys arrive here.
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

// handleSlashCommand routes a "/foo args" line. Local commands mutate the
// model directly (no event); runtime commands publish a UserCommandEvent the
// driver handles (swap provider, build status). Regular text (not starting
// with "/") is a normal submit. All branches reset the input widget (the
// caller already did m.input.Reset() before calling this).
func handleSlashCommand(m Model, text string) (Model, tea.Cmd) {
	// Regular text (no slash prefix) → normal submit with optimistic echo.
	if !strings.HasPrefix(text, "/") {
		m.messages = append(m.messages, messageView{role: "user", text: text})
		return m, publish(m.publisher, &event.UserSubmitEvent{Text: text})
	}

	// Parse "/cmd args...".
	cmd, args, _ := strings.Cut(strings.TrimPrefix(text, "/"), " ")
	cmd = strings.ToLower(strings.TrimSpace(cmd))
	args = strings.TrimSpace(args)

	switch cmd {
	// --- Local commands (TUI handles directly, no event) ---
	case "help":
		m.showHelp = !m.showHelp
		return m, nil
	case "clear":
		m.messages = nil
		m.scrollOffset = 0
		return m, nil
	case "theme":
		if args == "" {
			m.messages = append(m.messages, messageView{
				role: "system",
				text: "themes: dark light contrast mono (current: " + currentThemeName() + ")",
			})
			return m, nil
		}
		if setTheme(args) {
			m.messages = append(m.messages, messageView{
				role: "system",
				text: "theme: " + strings.ToLower(args),
			})
		} else {
			m.messages = append(m.messages, messageView{
				role: "system",
				text: "unknown theme: " + args + " (try: dark light contrast mono)",
			})
		}
		return m, nil
	// --- Runtime commands (publish UserCommandEvent, driver responds) ---
	case "model", "provider", "status":
		m.messages = append(m.messages, messageView{role: "user", text: text}) // echo the command
		return m, publish(m.publisher, &event.UserCommandEvent{Command: cmd, Args: args})
	// --- Unknown ---
	default:
		m.messages = append(m.messages, messageView{
			role: "system",
			text: "unknown command: /" + cmd + " (try /help)",
		})
		return m, nil
	}
}
