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

	"github.com/baobao1044/yolo-code/internal/event"
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
		//
		// Only the FIRST task.started after a submit is the user's; every later
		// one, until that task ends, is a sub-agent Core the orchestrator built
		// for a todo (see Model.awaitingTask/nested). Those keep their depth and
		// leave the header alone — the user's goal is not a scratch variable.
		if m.taskID == "" || m.awaitingTask {
			m.taskID = e.Task
			if e.Goal != "" {
				m.goal = e.Goal
			}
			m.state = "" // reset for a new task; state.change repopulates (TUI-003)
			m.awaitingTask = false
			m.nested = 0
		} else {
			m.nested++
		}

	// --- Chat pane (TUI-002, File 14 §14.5) ---
	case *event.ThinkingEvent:
		// llm.thinking deltas accumulate into the live thinking bubble; the
		// spinner turns on (TUI-007 coalesces the repaint; accumulation is the
		// correctness invariant). The bubble flushes on assistant.message.
		m.thinking += e.Delta
		m.streaming = true
	case *event.TokenEvent:
		// llm.token deltas accumulate into the live assistant bubble (separate
		// from thinking). Flushed to messages on assistant.message. Phase D:
		// also accumulate a rough token estimate (≈4 chars/token) for the cost
		// meter — the event carries a text delta and no counts, so this layer
		// has nothing better to divide. See costView for why the real counts,
		// which now exist upstream, do not reach here.
		m.liveAssistant += e.Delta
		m.streaming = true
		// Accumulate characters; the ÷4 happens once, in costView.tokensEst.
		m.cost.tokenChars += len(e.Delta)
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
		// Scoped to the header's own task: a sub-agent Core walks the same FSM,
		// and its transitions must not drive the user's status bar. While a
		// sub-agent is outstanding every transition on the bus is its own — the
		// driver runs one submission at a time — so depth alone disqualifies
		// them, which is what catches the colliding "t_1" the two session
		// Managers both mint.
		if m.nested > 0 || !ownsTask(m, string(e.Task)) {
			break
		}
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
		// Terminal state (File 14 §14.5): the bar reads "DONE". A sub-agent
		// finishing its todo is NOT the user's task finishing — closeNested
		// absorbs it and the header keeps running.
		if closeNested(&m) || !ownsTask(m, e.Task) {
			break
		}
		m.state = "DONE"
	case *event.TaskCancelledEvent:
		// Terminal state + banner (the cancel reason). Partial work is noted.
		// Same scoping as task.completed: a cancelled sub-agent closes its own
		// depth without painting CANCELLED over the user's task.
		if closeNested(&m) || !ownsTask(m, e.Task) {
			break
		}
		m.state = "CANCELLED"
		m.banner = e.Reason
		settleStream(&m)
	case *event.TaskFailedEvent:
		// The third terminal outcome, and the reason the header stops animating
		// on a hard error. Without this arm the event matched "task.>", reached
		// this switch, hit no case, and was discarded: m.state stayed at the
		// "ERROR" that state.change had just written, and spinnerGlyph has no
		// case for "ERROR", so it fell through to `m.streaming || m.activeTool`
		// — both of which are set on the way into a port call and cleared only
		// by the assistant.message or tool.result that a failed call never
		// sends. A dead task kept spinning, which is exactly what the comment
		// on the CANCELLED/FAILED arm of spinnerGlyph says must never happen.
		//
		// Same scoping as task.completed and task.cancelled: a sub-agent that
		// fails closes its own depth rather than painting FAILED over the
		// user's task. The reason goes to the banner as the cancel reason does;
		// it is the only thing that distinguishes one failure from another, and
		// the ErrorEvent published alongside it puts the same text in the chat
		// pane as a red line.
		if closeNested(&m) || !ownsTask(m, e.Task) {
			break
		}
		m.state = "FAILED"
		m.banner = e.Reason
		settleStream(&m)
	case *event.TaskPausedEvent:
		// The TUI labels PAUSED (it doesn't drive the FSM; the runtime does).
		// Not terminal, so it doesn't close a nested task — it's just ignored
		// while a sub-agent owns the depth.
		if m.nested > 0 || !ownsTask(m, e.Task) {
			break
		}
		m.state = "PAUSED"

	// --- Diff viewer (TUI-004, File 14 §14.7.3) ---
	case *event.PatchAppliedEvent:
		// Open the diff viewer focused, with the file list + counts + the
		// rendered diff hunks (Phase D: PatchAppliedEvent now carries the
		// unified-diff string). The viewer replaces any previous diff (§14.6.1:
		// the latest change is what the user reviews, not a stack). Edits come
		// only from patch.applied events (§14.1.1); the viewer never edits.
		m.diff = &diffView{files: e.Files, insertions: e.Insertions, deletions: e.Deletions, diff: e.Diff}
		m.focus = paneDiff
	case *event.VerificationFailedEvent:
		// Open the diff viewer focused on the failing file, reason staged so the
		// user sees why verification broke.
		m.diff = &diffView{reason: e.Reason}
		m.focus = paneDiff
	case *event.VerificationStageEvent:
		// Per-stage pass/fail indicator appended to chat.
		icon := theme.success.Render("✔")
		if e.Status == "fail" {
			icon = theme.errorStyle.Render("✘")
		} else if e.Status == "warn" {
			icon = theme.warning.Render("⚠")
		} else if e.Status == "skip" {
			icon = theme.muted.Render("○")
		}
		text := e.Stage
		if e.Detail != "" {
			text += ": " + e.Detail
		}
		m.messages = append(m.messages, messageView{role: "verification", text: icon + " " + text})

	// --- Cost meter (TUI-005, File 14 §14.7.5) ---
	case *event.CostIncurredEvent:
		// One event per tool call, so calls is the one exact number the rail
		// has. Dollars is whatever rate the operator configured — 0 by default,
		// which is why the rail leads with the call count and mentions money
		// only when the operator asked for it.
		m.cost.calls++
		m.cost.dollars += e.Dollars
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
		// A plan has no task.started of its own, so without this the first
		// sub-agent Core's task.started would be adopted as the user's task and
		// the header would show a todo brief instead of the goal. The plan IS
		// the thing this TUI submitted: the header names it, and every
		// task.started from here on is a sub-agent's.
		m.taskID = e.PlanID
		m.awaitingTask = false
		m.nested = 0
	case *event.TaskAssignEvent:
		// One row per todo, looked up by TodoID: coord re-publishes task.assign
		// for the SAME todo on every rework cycle, and appending unconditionally
		// grew a duplicate row that boardUpdateTodo (first match wins) then
		// never updated again — a finished plan rendered with a ghost row stuck
		// at "[~] assigned" forever. A rework resets the row to "assigned"
		// because the coder really is working on it again.
		if m.board != nil {
			if td := boardTodo(m, e.TodoID); td != nil {
				td.agent = e.Agent
				td.brief = e.Brief
				td.status = "assigned"
			} else {
				m.board.todos = append(m.board.todos, todoView{
					todoID: e.TodoID,
					agent:  e.Agent,
					brief:  e.Brief,
					status: "assigned",
				})
			}
		}
	case *event.PlanDoneEvent:
		// The multi-agent run's only terminal signal, carried on
		// coord.plan.done alongside its siblings and picked up by renderTopics'
		// coord.> prefix.
		//
		// Done is the only success flag: Merged says a patch was produced, not
		// that it verified (the orchestrator itself publishes Done:false with
		// Merged:true for "merged patch not verified"). coord now publishes
		// this on failure and cancellation too, and Canceled is what separates
		// the two — without it a Ctrl-C renders identically to a real failure.
		if m.board != nil && m.board.planID != "" && e.PlanID != "" && m.board.planID != e.PlanID {
			break // a different plan's terminal
		}
		// The plan is over, so no sub-agent is outstanding any more — clear the
		// depth rather than carry a leaked one into the next submission.
		m.nested = 0
		switch {
		case e.Done:
			m.state = "DONE"
			m.banner = or(e.Summary, "plan complete")
		case e.Canceled:
			// "CANCELLED" not "CANCELED": the event field takes the Go stdlib
			// spelling, the state string joins an existing vocabulary that
			// view.go and the single-agent path at fold.go:171 already use.
			m.state = "CANCELLED"
			msg := "plan cancelled"
			if e.Summary != "" {
				msg += ": " + e.Summary
			}
			m.banner = msg
		default:
			m.state = "FAILED"
			msg := "plan did not complete"
			if e.Summary != "" {
				msg += ": " + e.Summary
			}
			m.banner = msg
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
	case *event.CommandResponseEvent:
		// Phase 4 slash commands: the driver's text response folds into the
		// chat pane as a system-role message (e.g. "model: gpt-4o", "provider:
		// groq (llama-3.3-70b-versatile)").
		m.messages = append(m.messages, messageView{role: "system", text: e.Text})
	}
	return m, relaunchWatcher(m)
}

// settleStream ends the live stream when a task stops without finishing.
//
// The three live-render flags are all set on the way into a port call and
// cleared by exactly one event each: m.streaming and m.liveAssistant by
// assistant.message, m.activeTool by tool.result. A task that dies mid-call
// sends neither, so all three stayed set forever. The header was fixed by
// giving task.failed and task.cancelled a terminal m.state — spinnerGlyph
// returns its glyph before it ever consults these flags — but chatView does
// consult them: view.go:284 and :289 gate the thinking and assistant bubbles on
// m.streaming, and :292 renders activeTool with no gate at all. So the header
// went still while the body kept flowing, which is a worse reading than either
// alone: it looks like a finished run that is somehow still typing.
//
// The partial text is committed rather than dropped. Simply clearing the flag
// would make it vanish, and the user already watched it arrive — deleting
// output on the way to reporting a failure is how a transcript ends up less
// informative than the screen was a moment earlier. It goes in under its own
// "partial" role so the chat can say what it is; rendering it as a plain
// assistant message would assert it is the complete answer.
//
// Guarded on liveAssistant != "" so the normal path stays quiet: a task that
// completes has already had assistant.message flush the text, and an empty
// bubble appended to every clean run is noise.
//
// Not called from the task.completed arm. There the flush has already happened
// in order, and adding a second one only creates a window for double-rendering
// if the two events ever cross.
func settleStream(m *Model) {
	if m.liveAssistant != "" {
		m.messages = append(m.messages, messageView{role: "partial", text: m.liveAssistant})
	}
	m.thinking = ""
	m.liveAssistant = ""
	m.streaming = false
	m.activeTool = ""
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

// ownsTask reports whether a task-scoped event belongs to the task the header
// is showing. An empty header task accepts anything (the TUI hasn't adopted a
// task yet — the headless-observer path, and every fold test that drives a
// bare model); once a task is adopted, a different ID is somebody else's.
//
// This is a necessary but NOT a sufficient filter: the driver's session
// Manager and the agent runner's are separate instances whose task counters
// both start at zero, so a sub-agent's first task is also "t_1". The nesting
// depth (closeNested / m.nested) is what separates those.
func ownsTask(m Model, task string) bool {
	return m.taskID == "" || m.taskID == task
}

// closeNested reports whether a terminal task event belongs to an outstanding
// sub-agent rather than to the header's own task, closing that sub-agent's
// depth as it goes. Sub-agent Cores start and finish in balanced pairs
// (coord runs todos one at a time), so the user's own terminal event is the
// one that arrives at depth zero.
func closeNested(m *Model) bool {
	if m.nested == 0 {
		return false
	}
	m.nested--
	return true
}

// boardTodo returns a pointer to the board row with the given TodoID, or nil
// when no board is open or the row doesn't exist yet. The pointer is into the
// shared boardView (the Model's board is a pointer), which is how the existing
// status updates mutate rows.
func boardTodo(m Model, todoID string) *todoView {
	if m.board == nil {
		return nil
	}
	for i := range m.board.todos {
		if m.board.todos[i].todoID == todoID {
			return &m.board.todos[i]
		}
	}
	return nil
}

// boardUpdateTodo advances a board todo's status (TUI-009). Looks up the todo
// by TodoID; a no-op when no board is open or the todo doesn't exist yet
// (robustness — the TUI never fabricates a todo). Pure: only mutates render
// state when the lookup succeeds.
func boardUpdateTodo(m Model, todoID, status string) {
	if td := boardTodo(m, todoID); td != nil {
		td.status = status
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
