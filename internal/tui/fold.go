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
	"encoding/json"
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
		// "calling <tool>: <reason>" line; the spinner reads activeTool (TUI-007).
		m.activeTool = e.Tool
		detail := e.Reason
		if detail == "" && len(e.Args) > 0 {
			detail = truncateJSON(e.Args, 80)
		}
		text := "calling " + e.Tool
		if detail != "" {
			text += ": " + detail
		}
		m.messages = append(m.messages, messageView{role: "tool", text: text})
	case *event.ToolResultEvent:
		// Tool finished: clear the active tool + append result line.
		m.activeTool = ""
		text := e.Tool
		if len(e.Obs) > 0 {
			text += " → " + truncateJSON(e.Obs, 120)
		}
		m.messages = append(m.messages, messageView{role: "tool", text: text})
	case *event.ObservationEvent:
		// Observation with preview content.
		text := e.Tool
		if len(e.Obs) > 0 {
			text += ": " + truncateJSON(e.Obs, 120)
		}
		m.messages = append(m.messages, messageView{role: "observation", text: text})
	case *event.ReflectionEvent:
		// Dimmed inline note (File 14 §14.5).
		m.messages = append(m.messages, messageView{role: "reflection", text: e.Note})

	case *event.ApprovalRequestEvent:
		// Pending approval: populate the approval view so the rail shows
		// tool/summary/risk and the y/n handler becomes active (TUI-006).
		m.approval = &approvalView{
			id:      e.ApprovalID,
			tool:    e.Tool,
			summary: e.Summary,
			preview: e.Preview,
			risk:    string(e.Risk),
		}

	case *event.ErrorEvent:
		// Surface runtime errors as red chat lines + banner (previously
		// subscribed but silently dropped).
		msg := e.Msg
		if e.Layer != "" {
			msg = e.Layer + ": " + msg
		}
		m.messages = append(m.messages, messageView{role: "error", text: msg})
		m.banner = msg

	// --- Status bar (TUI-003, File 14 §14.7.4) ---
	case *event.StateChangeEvent:
		// The status bar's core line: copy the `To` label into m.state. The TUI
		// does NOT model the FSM — it labels it (File 14 §14.4.2). This is the
		// mutation guard: without it the bar never reflects the runtime's state.
		m.state = e.To
		m.stateWhy = e.Why
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
	case *event.VerificationStageEvent:
		// Per-stage pass/fail indicator appended to chat.
		icon := successStyle.Render("✔")
		if e.Status == "fail" {
			icon = errorStyle.Render("✘")
		} else if e.Status == "warn" {
			icon = warningStyle.Render("⚠")
		} else if e.Status == "skip" {
			icon = mutedStyle.Render("○")
		}
		text := e.Stage
		if e.Detail != "" {
			text += ": " + e.Detail
		}
		m.messages = append(m.messages, messageView{role: "verification", text: icon + " " + text})

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
		// Append a todo column with the agent role + brief + status "assigned".
		if m.board != nil {
			m.board.todos = append(m.board.todos, todoView{
				todoID: e.TodoID,
				agent:  e.Agent,
				brief:  e.Brief,
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
		if len(e.Comments) > 0 {
			m.messages = append(m.messages, messageView{role: "review", text: "review: " + e.Comments[0]})
		}
	case *event.TestReportEvent:
		// Mark tested:pass / tested:fail by the Passed flag.
		status := "tested:fail"
		if e.Passed {
			status = "tested:pass"
		}
		boardUpdateTodo(m, e.TodoID, status)
		if !e.Passed && e.Output != "" {
			out := e.Output
			if len(out) > 200 {
				out = out[:199] + "…"
			}
			m.messages = append(m.messages, messageView{role: "error", text: "test failed: " + out})
		}

	// --- User echo-back: clear approval on resolve ---
	case *event.UserApproveEvent:
		m.approval = nil
	case *event.UserRejectEvent:
		m.approval = nil
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

// truncateJSON returns a human-readable snippet from a json.RawMessage,
// truncated to maxLen runes. Used for tool args, observations, and results.
func truncateJSON(raw json.RawMessage, maxLen int) string {
	if len(raw) == 0 {
		return ""
	}
	s := string(raw)
	// Try to prettify if it's valid JSON.
	var v interface{}
	if json.Unmarshal(raw, &v) == nil {
		b, err := json.Marshal(v)
		if err == nil {
			s = string(b)
		}
	}
	// Trim surrounding quotes for string values.
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		s = s[1 : len(s)-1]
	}
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen-1]) + "…"
}
