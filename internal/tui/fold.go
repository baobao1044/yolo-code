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

	// --- Chat pane (TUI-002, File 14 §14.5) ---
	case *event.ThinkingEvent:
		// llm.thinking deltas accumulate into the live thinking bubble; the
		// spinner turns on (TUI-007 coalesces the repaint; accumulation is the
		// correctness invariant). The bubble flushes on assistant.message.
		m.thinking += e.Delta
		m.streaming = true
	case *event.TokenEvent:
		// llm.token deltas accumulate into the live assistant bubble (separate
		// from thinking). Flushed to messages on assistant.message.
		m.liveAssistant += e.Delta
		m.streaming = true
	case *event.AssistantMessageEvent:
		// Finalize the assistant bubble (File 14 §14.5): append the final Text
		// as a message, clear the live + thinking bubbles, end streaming. The
		// streamed accumulation could fold in here (TUI-002 appends the event's
		// Text — the authoritative final answer); a hardening pass merges the
		// liveAssistant tail. Clearing thinking is the mutation guard.
		m.messages = append(m.messages, messageView{role: "assistant", text: e.Text})
		m.thinking = ""
		m.liveAssistant = ""
		m.streaming = false
	case *event.ToolCallEvent:
		// "calling <tool>" line; the spinner reads activeTool (TUI-007).
		m.activeTool = e.Tool
		m.messages = append(m.messages, messageView{role: "tool", text: "calling " + e.Tool})
	case *event.ToolResultEvent:
		// Tool finished: clear the active tool + append a summarized line.
		// ToolResultEvent has no outcome field (spec gap — no ✓/✗ badge); the
		// line names the tool. The full obs is display-truncated elsewhere.
		m.activeTool = ""
		m.messages = append(m.messages, messageView{role: "tool", text: e.Tool})
	case *event.ObservationEvent:
		// Truncated observation preview (display-only; full obs in the log).
		m.messages = append(m.messages, messageView{role: "observation", text: e.Tool})
	case *event.ReflectionEvent:
		// Dimmed inline note (File 14 §14.5).
		m.messages = append(m.messages, messageView{role: "reflection", text: e.Note})
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
