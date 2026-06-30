// Sprint 9 exit bar (File 14 §15.12.2). The TUI renders a full agent run purely
// from events + publishes the correct user.* per keystroke, with no layer
// import except event. Verified by:
//  (a) a replay test — feed the canonical event sequence a real run emits
//      (task.started → state.change → llm.thinking → tool.call → tool.result →
//       patch.applied → state.change verify → task.completed) through fold, and
//       assert the Model reflects the full run + View() is non-empty.
//  (b) S1/S2/S6 latencies measured as pure-function timings (no TTY needed):
//       S1 <200ms cold→first paint (newModel + first View), S2 <50ms
//       token→screen (fold *TokenEvent + View), S6 <1 keypress→"what's it
//       doing" (handleInput).
//  (c) import-clean (TUI-008 lint).
// Per Decision 4: end-to-end interactive drive is out of Sprint 9 scope
// (runtime doesn't subscribe to user.* yet — deferred to the integration
// sprint). The replay + pure-latency checks prove the renderer is correct in
// isolation; the integration sprint wires it end-to-end.

package tui

import (
	"testing"
	"time"

	"github.com/baobao1044/yolo-code/internal/event"
)

// canonicalRun is the event sequence a single "fix the bug in auth.go" task
// emits, traced through File 14 §14.6's canonical flow. The replay folds each
// into the model in order; the test then asserts the model reflects the full
// run (header set, state DONE, messages present, diff opened, etc.).
var canonicalRun = []event.Event{
	&event.TaskStartedEvent{Task: "t_1", Session: "s_1", Goal: "fix the bug in auth.go"},
	&event.StateChangeEvent{Task: "t_1", From: "INIT", To: "PLAN", Why: "go"},
	&event.ThinkingEvent{Task: "t_1", Delta: "I'll locate the nil check"},
	&event.StateChangeEvent{Task: "t_1", From: "PLAN", To: "EXECUTE", Why: "tool dispatched"},
	&event.ToolCallEvent{Task: "t_1", Tool: "edit_file", Reason: "guard the nil"},
	&event.ToolResultEvent{Task: "t_1", Tool: "edit_file", Obs: []byte(`{"ok":true}`)},
	&event.PatchAppliedEvent{Task: "t_1", Files: []event.PatchFile{{Path: "auth.go", Insertions: 3, Deletions: 1}}, Insertions: 3, Deletions: 1},
	&event.StateChangeEvent{Task: "t_1", From: "EXECUTE", To: "VERIFY", Why: "patch applied"},
	&event.TaskCompletedEvent{Task: "t_1"},
}

// TestSprint9ExitBarReplayRendersFullRun is the §15.12.2 exit bar: folding the
// canonical run produces a model that reflects every stage — header (taskID +
// goal), state DONE, ≥1 chat message (the tool call line), diff opened (from
// patch.applied), and View() renders a non-empty screen. This proves the TUI
// renders a full agent run purely from events.
func TestSprint9ExitBarReplayRendersFullRun(t *testing.T) {
	m := newModelForTest()
	for _, e := range canonicalRun {
		m, _ = fold(m, env(e))
	}

	// Header reflects the run.
	if m.taskID != "t_1" {
		t.Errorf("taskID = %q, want t_1 (task.started folded)", m.taskID)
	}
	if m.goal != "fix the bug in auth.go" {
		t.Errorf("goal = %q, want the run's goal", m.goal)
	}
	// State reflects the terminal state (task.completed → DONE).
	if m.state != "DONE" {
		t.Errorf("state = %q, want DONE (task.completed folded)", m.state)
	}
	// Chat has messages (the tool call line at minimum).
	if len(m.messages) == 0 {
		t.Error("messages empty — the run's tool call/result should have appended chat lines")
	}
	// Diff viewer opened (patch.applied).
	if m.diff == nil {
		t.Error("diff = nil — patch.applied should have opened the viewer")
	} else if len(m.diff.files) != 1 || m.diff.files[0].Path != "auth.go" {
		t.Errorf("diff.files = %#v, want one file auth.go (patch.applied folded)", m.diff.files)
	}
	// View renders a non-empty screen.
	if out := m.View(); out == "" {
		t.Error("View() returned empty string — the run should render a non-empty screen")
	}
}

// TestSprint9ExitBarS1ColdStartToFirstPaint measures S1 (<200ms cold→first
// paint): newModel + the first View() after one fold. No TTY — pure-function
// timing. The bound is generous (200ms) vs the measured cost (microseconds).
func TestSprint9ExitBarS1ColdStartToFirstPaint(t *testing.T) {
	start := time.Now()
	m := newModel(nil, nil)
	m, _ = fold(m, env(&event.TaskStartedEvent{Task: "t_1", Goal: "g"}))
	_ = m.View()
	elapsed := time.Since(start)

	const bound = 200 * time.Millisecond
	if elapsed > bound {
		t.Errorf("S1 cold→first paint = %v, want < %v (S1 bound, §15.12.2)", elapsed, bound)
	}
	t.Logf("S1 cold→first paint = %v (bound %v)", elapsed, bound)
}

// TestSprint9ExitBarS2TokenToScreen measures S2 (<50ms token→screen): fold a
// *TokenEvent + View(). The bound is generous; the measured cost is
// microseconds (no rendering work — View just formats strings).
func TestSprint9ExitBarS2TokenToScreen(t *testing.T) {
	m := newModelForTest()
	m, _ = fold(m, env(&event.TaskStartedEvent{Task: "t_1", Goal: "g"}))

	start := time.Now()
	m, _ = fold(m, env(&event.TokenEvent{Task: "t_1", Delta: "hello"}))
	_ = m.View()
	elapsed := time.Since(start)

	const bound = 50 * time.Millisecond
	if elapsed > bound {
		t.Errorf("S2 token→screen = %v, want < %v (S2 bound, §15.12.2)", elapsed, bound)
	}
	t.Logf("S2 token→screen = %v (bound %v)", elapsed, bound)
}

// TestSprint9ExitBarS6KeypressLatency measures S6 (<1 keypress→"what is it
// doing"): handleInput latency. The bound is generous; the measured cost is
// microseconds (a keymap switch + a publish Cmd construction).
func TestSprint9ExitBarS6KeypressLatency(t *testing.T) {
	m := newModelForTest()
	m.taskID = "t_1"

	start := time.Now()
	_, _ = handleInput(m, keyMsg("esc"))
	elapsed := time.Since(start)

	const bound = 1 * time.Millisecond
	if elapsed > bound {
		t.Errorf("S6 keypress latency = %v, want < %v (S6 bound, §15.12.2)", elapsed, bound)
	}
	t.Logf("S6 keypress latency = %v (bound %v)", elapsed, bound)
}
