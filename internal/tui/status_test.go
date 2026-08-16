// Tests for TUI-003 — Status bar (File 14 §14.7.4). The status bar is one line,
// cheap + constant in size — the at-a-glance "what is the agent doing" answer.
// It's driven by:
//   state.change   → update the state label (m.state = To) + pick a spinner
//   context.built  → flash "context: N items" (N unknown — event has no count,
//                    spec gap; flash is presence-based)
//   memory.update  → flash "+N <store>"
//   task.completed/cancelled/paused → terminal-state header updates
// All are pure folds into render state; View renders the bar.

package tui

import (
	"strings"
	"testing"

	"github.com/baobao1044/yolo-code/internal/event"
)

// TestFoldStateChangeUpdatesState is the §14.7.4 core invariant: state.change
// copies the `To` label into m.state (the TUI does NOT model the FSM — it just
// labels it). This is the mutation guard: if m.state isn't set, the status bar
// never reflects the runtime's state and the user can't tell what's happening.
func TestFoldStateChangeUpdatesState(t *testing.T) {
	m := newModelForTest()
	m, _ = fold(m, env(&event.StateChangeEvent{Task: "t_1", From: "PLAN", To: "EXECUTE", Why: "tool dispatched"}))

	if m.state != "EXECUTE" {
		t.Errorf("state = %q, want %q (state.change copies To — the TUI labels the FSM, doesn't model it)", m.state, "EXECUTE")
	}
}

// TestFoldContextBuiltSetsFlash pins §14.5: context.built flashes a status line
// note. ContextBuiltEvent has no item/token count field (spec gap — the flash
// is presence-based, not "N items, B tokens" as File 14 §14.5 idealizes). The
// flash is non-empty so View shows it.
func TestFoldContextBuiltSetsFlash(t *testing.T) {
	m := newModelForTest()
	m, _ = fold(m, env(&event.ContextBuiltEvent{Task: "t_1"}))

	if m.contextFlash == "" {
		t.Error("contextFlash = \"\", want non-empty (context.built should flash the status bar)")
	}
}

// TestFoldMemoryUpdateSetsFlash pins §14.5: memory.update flashes "+N <store>".
// MemoryUpdateEvent has Store + Items, so the flash names both.
func TestFoldMemoryUpdateSetsFlash(t *testing.T) {
	m := newModelForTest()
	m, _ = fold(m, env(&event.MemoryUpdateEvent{Task: "t_1", Store: "preference", Items: 3}))

	if m.memoryFlash == "" {
		t.Fatal("memoryFlash = \"\", want non-empty")
	}
	// The flash should name the store + count (File 14 §14.5 "memory: +N <store>").
	if !contains(m.memoryFlash, "3") {
		t.Errorf("memoryFlash = %q, want it to mention the item count 3", m.memoryFlash)
	}
	if !contains(m.memoryFlash, "preference") {
		t.Errorf("memoryFlash = %q, want it to mention the store 'preference'", m.memoryFlash)
	}
}

// contains is a tiny helper (avoid pulling strings.Contains into the test for
// one call; keeps the assertion readable).
func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// TestFoldTaskCompletedUpdatesHeader pins §14.5: task.completed is a terminal
// state — the status bar reflects it. The header's state becomes "DONE" so the
// bar reads "task #N · goal | DONE".
func TestFoldTaskCompletedUpdatesHeader(t *testing.T) {
	m := newModelForTest()
	m.taskID = "t_1"
	m, _ = fold(m, env(&event.TaskCompletedEvent{Task: "t_1"}))

	if m.state != "DONE" {
		t.Errorf("state = %q after task.completed, want \"DONE\" (terminal header)", m.state)
	}
}

// TestFoldTaskCancelledSetsBanner pins §14.5: task.cancelled sets a banner
// (the cancel reason) so the user sees why the task stopped. Cancelled is a
// terminal state; the banner explains.
func TestFoldTaskCancelledSetsBanner(t *testing.T) {
	m := newModelForTest()
	m.taskID = "t_1"
	m, _ = fold(m, env(&event.TaskCancelledEvent{Task: "t_1", Reason: "user abort", Partial: "draft.md"}))

	if m.state != "CANCELLED" {
		t.Errorf("state = %q after task.cancelled, want \"CANCELLED\"", m.state)
	}
	if m.banner == "" {
		t.Error("banner = \"\", want non-empty (cancel reason surfaces as a banner)")
	}
}

// TestFoldTaskFailedStopsTheSpinner is the arm that was missing, and it asserts
// the consequence rather than the assignment.
//
// task.failed matched "task.>" and reached fold() from the day the runtime's
// hard-error path started publishing it, hit no case, and was dropped. The
// visible result was not a missing banner — it was a spinning header on a dead
// task. state.change had already written "ERROR", spinnerGlyph has no "ERROR"
// case, so it fell through to the `m.streaming || m.activeTool` branch; both of
// those are set on the way into a port call and cleared only by the
// assistant.message or tool.result that a failed call never sends.
//
// So the model here is set up the way a real mid-stream failure leaves it —
// streaming still true, a tool still active, state already "ERROR" — and the
// assertion is on the glyph.
func TestFoldTaskFailedStopsTheSpinner(t *testing.T) {
	m := newModelForTest()
	m.taskID = "t_1"
	m.state = "ERROR"
	m.streaming = true
	m.activeTool = "shell"

	if got := spinnerGlyph(m); contains(got, "✘") {
		t.Fatalf("precondition broken: spinnerGlyph already returns a terminal glyph (%q) "+
			"before task.failed is folded, so this test cannot detect the fix", got)
	}

	m, _ = fold(m, env(&event.TaskFailedEvent{Task: "t_1", Reason: "planner returned no plan"}))

	if m.state != "FAILED" {
		t.Errorf("state = %q after task.failed, want \"FAILED\"", m.state)
	}
	if m.banner != "planner returned no plan" {
		t.Errorf("banner = %q, want the failure reason; it is the only thing that "+
			"distinguishes one failure from another", m.banner)
	}
	if got := spinnerGlyph(m); !contains(got, "✘") {
		t.Errorf("spinnerGlyph = %q after task.failed, want the terminal ✘: a finished run "+
			"whose header keeps animating reads as still working", got)
	}
}

// TestFoldTaskFailedFromASubAgentLeavesTheHeaderAlone mirrors the scoping the
// completed and cancelled arms already have. A sub-agent Core walks its own FSM
// and mints its own task ids, and one of its todos failing is not the user's
// task failing — painting FAILED over the header would report the whole run as
// dead while the coordinator is still working through the rest of the plan.
func TestFoldTaskFailedFromASubAgentLeavesTheHeaderAlone(t *testing.T) {
	m := newModelForTest()
	m.taskID = "t_1"
	m.state = "EXECUTE"

	m, _ = fold(m, env(&event.TaskFailedEvent{Task: "t_someone_else", Reason: "not mine"}))

	if m.state != "EXECUTE" {
		t.Errorf("state = %q after another task's task.failed, want it untouched at \"EXECUTE\"", m.state)
	}
	if m.banner != "" {
		t.Errorf("banner = %q, want empty: another task's failure is not the user's banner", m.banner)
	}
}

// --- Settling the live stream when a task ends badly (see settleStream) ---

// liveMidStream builds the model state a task that dies mid-stream leaves
// behind: tokens have arrived, a tool is outstanding, and neither the
// assistant.message nor the tool.result that would clear them is ever coming.
func liveMidStream() Model {
	m := newModelForTest()
	m.taskID = "t_1"
	m.state = "EXECUTE"
	m.thinking = "weighing two approaches"
	m.liveAssistant = "The fix is to move the guard into"
	m.streaming = true
	m.activeTool = "shell"
	return m
}

// assertSettled pins what a terminal outcome must leave behind: no live
// bubbles, no dangling tool, and the partial answer preserved as a message
// rather than deleted.
func assertSettled(t *testing.T, m Model, want string) {
	t.Helper()
	if m.streaming {
		t.Error("streaming is still true; chatView keeps painting the live bubbles under a terminal header")
	}
	if m.liveAssistant != "" {
		t.Errorf("liveAssistant = %q, want it flushed", m.liveAssistant)
	}
	if m.thinking != "" {
		t.Errorf("thinking = %q, want it cleared", m.thinking)
	}
	if m.activeTool != "" {
		t.Errorf("activeTool = %q, want it cleared; chatView renders it ungated by streaming, "+
			"so a dangling tool reads as still running", m.activeTool)
	}
	var got *messageView
	for i := range m.messages {
		if m.messages[i].role == "partial" {
			got = &m.messages[i]
		}
	}
	if got == nil {
		t.Fatalf("no \"partial\" message; the streamed text %q was discarded rather than kept — "+
			"clearing the flag must not throw away output the user already watched arrive", want)
	}
	if got.text != want {
		t.Errorf("partial text = %q, want %q", got.text, want)
	}
}

// TestFoldTaskFailedSettlesTheLiveStream — the header stops animating on
// task.failed, but the body did not: chatView gates the thinking and assistant
// bubbles on m.streaming (view.go:284,289) and renders activeTool with no gate
// at all (view.go:292). All three are set on the way into a port call and
// cleared only by the assistant.message / tool.result a dead call never sends,
// so a finished-and-failed run kept a half-written answer flowing beneath a
// FAILED header, indistinguishable from output still arriving.
func TestFoldTaskFailedSettlesTheLiveStream(t *testing.T) {
	m := liveMidStream()
	want := m.liveAssistant

	m, _ = fold(m, env(&event.TaskFailedEvent{Task: "t_1", Reason: "planner returned no plan"}))

	assertSettled(t, m, want)
}

// TestFoldTaskCancelledSettlesTheLiveStream — the same defect on the cancel
// path, which is the more common one. It is tested separately rather than
// table-driven with the failure case because the two arms are separate code and
// a shared helper is exactly the kind of thing that gets called from one of them
// and not the other.
func TestFoldTaskCancelledSettlesTheLiveStream(t *testing.T) {
	m := liveMidStream()
	want := m.liveAssistant

	m, _ = fold(m, env(&event.TaskCancelledEvent{Task: "t_1", Reason: "user abort"}))

	assertSettled(t, m, want)
}

// TestSettleIsScopedToTheUsersOwnTask — settling is a mutation of the user's
// chat pane, so it belongs behind the same ownership guard the state and banner
// writes are behind. A sub-agent failing its todo must not flush the user's
// in-flight answer into the transcript as though it had been interrupted.
func TestSettleIsScopedToTheUsersOwnTask(t *testing.T) {
	m := liveMidStream()

	m, _ = fold(m, env(&event.TaskFailedEvent{Task: "t_someone_else", Reason: "not mine"}))

	if !m.streaming {
		t.Error("another task's failure ended the user's stream")
	}
	if m.liveAssistant == "" {
		t.Error("another task's failure flushed the user's live answer")
	}
	for _, msg := range m.messages {
		if msg.role == "partial" {
			t.Errorf("another task's failure wrote a partial message %q into the user's chat", msg.text)
		}
	}
}

// TestSettleKeepsQuietWhenThereIsNothingToSettle — the normal path. A task that
// completes has already had its assistant.message flush liveAssistant and clear
// streaming, so a terminal event arriving afterwards must not invent an empty
// partial message. This is why settleStream guards on liveAssistant != "".
func TestSettleKeepsQuietWhenThereIsNothingToSettle(t *testing.T) {
	m := newModelForTest()
	m.taskID = "t_1"
	before := len(m.messages)

	m, _ = fold(m, env(&event.TaskCancelledEvent{Task: "t_1", Reason: "user abort"}))

	if len(m.messages) != before {
		t.Errorf("messages grew from %d to %d with no live text to flush; an empty partial "+
			"bubble is noise", before, len(m.messages))
	}
}

// --- Header ownership: the TUI renders ONE task, the user's own ---

// submitGoal drives the real submit path (Enter on a non-empty input line) so
// the model records the goal the user asked for. The publish Cmd is dropped:
// these tests are about what the header shows, not what was published.
func submitGoal(m Model, text string) Model {
	m.setInput(text)
	m2, _ := handleInput(m, keyMsg("enter"))
	return m2
}

// TestHeaderIgnoresASubAgentsTaskLifecycle is the fix-1 regression. A
// multi-agent goal builds a runtime.Core per coder todo, and every one of
// those Cores publishes task.started / state.change / task.completed on the
// same bus the TUI renders. Folding them into the single global header made
// the screen read "✔ DONE" one todo into a three-todo plan, with the user's
// goal replaced by the sub-agent's brief.
//
// The sub-agent IDs here deliberately COLLIDE with the user's (t_1): the two
// session.Managers on this bus both start their counter at 0, so an ID filter
// alone cannot separate them — the nesting depth is what does.
func TestHeaderIgnoresASubAgentsTaskLifecycle(t *testing.T) {
	const goal = "refactor auth, add tests, and fix CI"
	m := newModelForTest()
	m.width = 120
	m = submitGoal(m, goal)

	// The user's own task starts and runs.
	m, _ = fold(m, env(&event.TaskStartedEvent{Task: "t_1", Session: "s_1", Goal: goal}))
	m, _ = fold(m, env(&event.StateChangeEvent{Task: "t_1", From: "INIT", To: "PLAN", Why: "go"}))

	// Sub-agent Core for todo_0 — same minted ID, different goal.
	m, _ = fold(m, env(&event.TaskStartedEvent{Task: "t_1", Session: "s_1", Goal: "refactor auth"}))
	m, _ = fold(m, env(&event.StateChangeEvent{Task: "t_1", From: "PLAN", To: "VERIFY", Why: "sub-agent"}))
	m, _ = fold(m, env(&event.TaskCompletedEvent{Task: "t_1"}))

	head := headerView(m)
	if !strings.Contains(head, goal) {
		t.Errorf("header lost the user's goal to a sub-agent's brief:\n%s\nwant it to contain %q", head, goal)
	}
	if strings.Contains(head, "DONE") {
		t.Errorf("header says DONE with 2 of 3 todos unstarted (a sub-agent's task.completed):\n%s", head)
	}
	if strings.Contains(head, "✔") {
		t.Errorf("header shows the terminal ✔ glyph for a sub-agent's completion:\n%s", head)
	}
	if !strings.Contains(head, "PLAN") {
		t.Errorf("header lost the user task's own state to a sub-agent's state.change:\n%s", head)
	}
}

// TestOwnTaskCompletionStillReachesTheHeader is the over-filter guard for the
// test above: once the nested sub-agent has completed, the user's OWN
// task.completed must still paint DONE. A header that ignores every completion
// is the same bug with the sign flipped.
func TestOwnTaskCompletionStillReachesTheHeader(t *testing.T) {
	m := newModelForTest()
	m.width = 120
	m = submitGoal(m, "ship it")
	m, _ = fold(m, env(&event.TaskStartedEvent{Task: "t_1", Goal: "ship it"}))
	// One nested sub-agent, start to finish.
	m, _ = fold(m, env(&event.TaskStartedEvent{Task: "t_1", Goal: "sub-task"}))
	m, _ = fold(m, env(&event.TaskCompletedEvent{Task: "t_1"}))
	// Now the user's own task finishes.
	m, _ = fold(m, env(&event.TaskCompletedEvent{Task: "t_1"}))

	head := headerView(m)
	if !strings.Contains(head, "DONE") {
		t.Errorf("the user's own task.completed no longer reaches the header:\n%s", head)
	}
	if !strings.Contains(head, "ship it") {
		t.Errorf("header lost the user's goal:\n%s", head)
	}
}

// TestEscCancelsTheUsersTaskNotASubAgents pins the secondary consequence of
// the header overwrite: Esc publishes user.cancel for m.taskID, so a header
// that had adopted a sub-agent's ID cancelled the wrong task.
func TestEscCancelsTheUsersTaskNotASubAgents(t *testing.T) {
	pub := &fakePublisher{}
	m := newModelForTest()
	m.publisher = pub
	m = submitGoal(m, "the user's goal")
	m, _ = fold(m, env(&event.TaskStartedEvent{Task: "t_1", Goal: "the user's goal"}))
	m, _ = fold(m, env(&event.TaskStartedEvent{Task: "t_2", Goal: "sub-agent brief"}))

	_, cmd := handleInput(m, keyMsg("esc"))
	runCmd(t, cmd)
	c, ok := pub.last.(*event.UserCancelEvent)
	if !ok {
		t.Fatalf("published %T, want *UserCancelEvent", pub.last)
	}
	if c.Task != "t_1" {
		t.Errorf("Esc cancelled %q, want the user's own task t_1 (a sub-agent's ID took the header)", c.Task)
	}
}

// TestFoldTaskPausedSetsState pins §14.5: task.paused sets the PAUSED state
// label (the spinner stops). The TUI labels it; it doesn't drive the FSM.
func TestFoldTaskPausedSetsState(t *testing.T) {
	m := newModelForTest()
	m.taskID = "t_1"
	m, _ = fold(m, env(&event.TaskPausedEvent{Task: "t_1"}))

	if m.state != "PAUSED" {
		t.Errorf("state = %q after task.paused, want \"PAUSED\"", m.state)
	}
}
