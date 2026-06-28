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
	"strconv"

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

	// --- Status bar (TUI-003, File 14 §14.7.4) ---
	case *event.StateChangeEvent:
		// The status bar's core line: copy the `To` label into m.state. The TUI
		// does NOT model the FSM — it labels it (File 14 §14.4.2). This is the
		// mutation guard: without it the bar never reflects the runtime's state.
		m.state = e.To
	case *event.ContextBuiltEvent:
		// Flash "context ready" (File 14 §14.5). ContextBuiltEvent has no
		// item/token count field (spec gap — File 14 idealizes "N items, B
		// tokens"; the real event carries only Task). The flash is presence-
		// based; a hardening pass that adds counts to L4 fills the figure.
		m.contextFlash = "context ready"
	case *event.MemoryUpdateEvent:
		// Flash "+N <store>" (File 14 §14.5). MemoryUpdateEvent has Store + Items.
		m.memoryFlash = "+" + strconv.Itoa(e.Items) + " " + e.Store
	case *event.TaskCompletedEvent:
		// Terminal state (File 14 §14.5): the bar reads "DONE".
		m.state = "DONE"
	case *event.TaskCancelledEvent:
		// Terminal state + banner (the cancel reason). Partial work is noted.
		m.state = "CANCELLED"
		m.banner = e.Reason
	case *event.TaskPausedEvent:
		// The TUI labels PAUSED (it doesn't drive the FSM; the runtime does).
		m.state = "PAUSED"

	// --- Diff viewer (TUI-004, File 14 §14.7.3) ---
	case *event.PatchAppliedEvent:
		// Open the diff viewer focused, with the file list + counts. The viewer
		// replaces any previous diff (§14.6.1: the latest change is what the
		// user reviews, not a stack). PatchAppliedEvent has NO diff-hunks text
		// (spec gap: only Snapshot + Files + Insertions/Deletions) — the viewer
		// renders the file list + counts (hunk-colored in View), not hunks.
		// Edits come only from patch.applied events (§14.1.1); the viewer never
		// edits.
		m.diff = &diffView{files: e.Files, insertions: e.Insertions, deletions: e.Deletions}
		m.focus = paneDiff
	case *event.VerificationFailedEvent:
		// Open the diff viewer focused on the failing file, reason staged so the
		// user sees why verification broke.
		m.diff = &diffView{reason: e.Reason}
		m.focus = paneDiff

	// --- Cost meter (TUI-005, File 14 §14.7.5) ---
	case *event.CostDegradedEvent:
		// Set the degradation level the rail displays. Spec gap: File 14 §14.5
		// reads cost.degraded.level, but CostDegradedEvent's field is `Stage`
		// (it carries the level). The rail shows "level: <Stage>". The mutation
		// guard: without this, the rail never reflects the degradation.
		m.cost.level = e.Stage
	case *event.CostAbortEvent:
		// Abort: set the flag + reason + surface a banner (§14.5). Per Decision
		// 2, dollars/loops stay blank — the catalog has no CostSpendEvent/
		// CostLoopEvent (spec gap; deferred to the integration sprint). The TUI
		// never imports infra for a snapshot (import matrix).
		m.cost.aborted = true
		m.cost.abortReason = e.Reason
		m.banner = e.Reason

	// --- Multi-agent board (TUI-009, File 14 §14.7.6) ---
	case *event.PlanReadyEvent:
		// Open the board with the planID (skeleton). PlanReadyEvent.Plan is a
		// json.RawMessage — the TUI doesn't unpack it (no schema here; parsing
		// belongs in the coord layer). Todos fill from the subsequent
		// coord.task.assign events. The full plan body is an integration-sprint
		// fill (spec gap, documented).
		m.board = &boardView{planID: e.PlanID}
	case *event.TaskAssignEvent:
		// Append a todo column with the agent role + status "assigned". Ignored
		// if no board is open (the board opens only on coord.plan.ready — the
		// TUI doesn't fabricate one).
		if m.board != nil {
			m.board.todos = append(m.board.todos, todoView{
				todoID: e.TodoID,
				agent:  e.Agent,
				status: "assigned",
			})
		}
	case *event.CodeReadyEvent:
		// Mark the todo "coded" (looked up by TodoID). A code.ready for an
		// unknown todo is ignored (robustness — no fabrication).
		boardUpdateTodo(m, e.TodoID, "coded")
	case *event.ReviewVerdictEvent:
		// Mark approved/rework by the Approved flag.
		status := "rework"
		if e.Approved {
			status = "approved"
		}
		boardUpdateTodo(m, e.TodoID, status)
	case *event.TestReportEvent:
		// Mark tested:pass / tested:fail by the Passed flag.
		status := "tested:fail"
		if e.Passed {
			status = "tested:pass"
		}
		boardUpdateTodo(m, e.TodoID, status)
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

// boardUpdateTodo advances a board todo's status (TUI-009). Looks up the todo
// by TodoID; a no-op when no board is open or the todo doesn't exist yet
// (robustness — the TUI never fabricates a todo). Pure: only mutates render
// state when the lookup succeeds.
func boardUpdateTodo(m Model, todoID, status string) {
	if m.board == nil {
		return
	}
	for i := range m.board.todos {
		if m.board.todos[i].todoID == todoID {
			m.board.todos[i].status = status
			return
		}
	}
}
