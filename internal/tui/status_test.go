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
	"testing"

	"github.com/yolo-code/yolo/internal/event"
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
