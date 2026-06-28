// Tests for TUI-001 — Foundation: the subscribe-only TUI's pure fold + the
// busWatcher bridge (File 14 §14.3, §14.4). fold is a pure (Model, Cmd)
// transition — type-switch on env.Evt, fold into render state, re-launch the
// watcher. busWatcher is the long-lived tea.Cmd that pumps envelopes to busMsg.
// These are the two TDD-enablable seams; tea.Program.Run() is an untested
// driver (needs a TTY, accepted like infra.Stop).

package tui

import (
	"testing"

	"github.com/yolo-code/yolo/internal/event"
)

// newModelForTest builds a Model with nil seams (fold doesn't need them) so a
// fold test can drive the pure projection without a bus or publisher.
func newModelForTest() Model {
	m := newModel(nil, nil)
	return m
}

// env wraps an event in an Envelope (the bus stamps Seq/At in prod; fold only
// reads Evt, so a zero Seq/At is fine for the pure projection).
func env(e event.Event) event.Envelope {
	return event.Envelope{Evt: e}
}

// TestFoldTaskStartedSetsHeader is the TUI-001 exit bar: a task.started event
// folds into the header — taskID + goal are set (File 14 §14.4.2; the real
// TaskStartedEvent has Task/Session/Goal, NO Kind — see spec gap, header
// shows the goal). fold must NOT call the runtime; it only mutates the model.
func TestFoldTaskStartedSetsHeader(t *testing.T) {
	m := newModelForTest()
	m2, _ := fold(m, env(&event.TaskStartedEvent{Task: "t_1", Session: "s_9", Goal: "fix the auth bug"}))

	if m2.taskID != "t_1" {
		t.Errorf("taskID = %q, want %q", m2.taskID, "t_1")
	}
	if m2.goal != "fix the auth bug" {
		t.Errorf("goal = %q, want %q (header shows the goal — TaskStartedEvent has no Kind field, spec gap)", m2.goal, "fix the auth bug")
	}
}

// TestFoldTaskStartedDoesNotTouchRuntime pins the subscribe-only contract
// (File 14 §14.1.1): fold holds a nil publisher + nil sub here, and must not
// panic dereferencing them — it only mutates render state.
func TestFoldTaskStartedDoesNotTouchRuntime(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("fold panicked on a nil-seam model: %v (must be a pure projection, no runtime calls)", r)
		}
	}()
	m := newModelForTest()
	_, _ = fold(m, env(&event.TaskStartedEvent{Task: "t_1", Goal: "g"}))
}
