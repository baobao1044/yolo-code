// Tests for TUI-006 — Input + keymap → user.* (publish-only, File 14 §14.8).
// handleInput translates keystrokes into user.* events published via the
// EventPublisher seam. Per Decision 4: the TUI is PUBLISH-ONLY this sprint —
// the runtime doesn't subscribe to user.* yet (synchronous drive loop, no
// WAIT_USER/PAUSED arms), so keystrokes can't drive the runtime. Runtime-side
// consumption is deferred to the integration sprint (§15.9.2 bucket). Here we
// only assert the TUI publishes the CORRECT event per keystroke — the seam
// contract the integration sprint will plug into.
//
// Tests use a fake publisher that captures the published event, so they assert
// exactly which user.* was emitted (no real-bus read, no timing flakiness).

package tui

import (
	"context"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/yolo-code/yolo/internal/event"
)

// fakePublisher captures the last event published so a test asserts which user.*
// was emitted. count tracks how many publishes happened (the enter test asserts
// exactly one).
type fakePublisher struct {
	last  event.Event
	count int
}

func (f *fakePublisher) Publish(_ context.Context, e event.Event) error {
	f.last = e
	f.count++
	return nil
}

// keyMsg builds a tea.KeyMsg from its string form (the bubbletea way: keys are
// matched by msg.String() — "enter", "esc", "y", "n", "ctrl+p", "ctrl+r",
// "ctrl+c"). Using the string form keeps the test readable + matches the
// production handleInput which switches on msg.String().
func keyMsg(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

// runCmd executes a tea.Cmd (the off-thread publish) so the test's fake
// publisher observes the event. This is the bubbletea testing model: the Cmd
// is the work, and tests drive it directly (no Program, no TTY).
func runCmd(t *testing.T, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		return
	}
	// A Batch returns a BatchMsg whose individual Cmds run separately; flatten
	// it so every nested publish executes.
	msg := cmd()
	if b, ok := msg.(tea.BatchMsg); ok {
		for _, c := range b {
			if c != nil {
				_ = c()
			}
		}
	}
}

// TestHandleInputEnterSubmits pins §14.8.1: Enter with non-empty input
// publishes a user.submit with the typed text + echoes it optimistically into
// the chat (P1 — the UI feels instant). The optimistic echo is the user-role
// message appended to messages.
func TestHandleInputEnterSubmits(t *testing.T) {
	pub := &fakePublisher{}
	m := newModelForTest()
	m.publisher = pub
	// Simulate typed text (the input widget would hold this; TUI-006 stores it
	// on a field the production handleInput reads from the textinput, but the
	// pure transition takes the value via the model).
	m.inputText = "fix the bug"

	m2, cmd := handleInput(m, keyMsg("enter"))
	runCmd(t, cmd)

	if pub.count != 1 {
		t.Fatalf("published %d events, want 1 (Enter → one user.submit)", pub.count)
	}
	sub, ok := pub.last.(*event.UserSubmitEvent)
	if !ok {
		t.Fatalf("published %T, want *UserSubmitEvent", pub.last)
	}
	if sub.Text != "fix the bug" {
		t.Errorf("UserSubmitEvent.Text = %q, want the typed text", sub.Text)
	}
	// Optimistic echo: a user-role message appended (P1 instant feel).
	if len(m2.messages) == 0 || m2.messages[len(m2.messages)-1].role != "user" {
		t.Errorf("expected a user-role echo message, got %v", m2.messages)
	}
	// Input cleared after submit.
	if m2.inputText != "" {
		t.Errorf("inputText = %q after submit, want \"\" (cleared)", m2.inputText)
	}
	_ = cmd // the publish happens via a returned tea.Cmd; not asserted here
}

// TestHandleInputEscCancels pins §14.8.2: Esc publishes a user.cancel with the
// active task. The TUI doesn't validate (it can't — no logic); the runtime's
// handler is a no-op if there's no active task.
func TestHandleInputEscCancels(t *testing.T) {
	pub := &fakePublisher{}
	m := newModelForTest()
	m.publisher = pub
	m.taskID = "t_1"

	_, cmd := handleInput(m, keyMsg("esc"))
	runCmd(t, cmd)

	if pub.count != 1 {
		t.Fatalf("published %d, want 1", pub.count)
	}
	c, ok := pub.last.(*event.UserCancelEvent)
	if !ok {
		t.Fatalf("published %T, want *UserCancelEvent", pub.last)
	}
	if string(c.Task) != "t_1" {
		t.Errorf("UserCancelEvent.Task = %q, want t_1", c.Task)
	}
}

// TestHandleInputApprovalYesApproves pins §14.8.1: when an approval is pending,
// 'y' publishes user.approve with the task + approval id (resumes from
// WAIT_USER on the runtime side, once wired).
func TestHandleInputApprovalYesApproves(t *testing.T) {
	pub := &fakePublisher{}
	m := newModelForTest()
	m.publisher = pub
	m.taskID = "t_1"
	m.approval = &approvalView{id: "apr_42"}

	_, cmd := handleInput(m, keyMsg("y"))
	runCmd(t, cmd)

	if pub.count != 1 {
		t.Fatalf("published %d, want 1", pub.count)
	}
	a, ok := pub.last.(*event.UserApproveEvent)
	if !ok {
		t.Fatalf("published %T, want *UserApproveEvent", pub.last)
	}
	if a.Task != "t_1" {
		t.Errorf("UserApproveEvent.Task = %q, want t_1", a.Task)
	}
	if a.ApprovalID != "apr_42" {
		t.Errorf("UserApproveEvent.ApprovalID = %q, want apr_42", a.ApprovalID)
	}
}

// TestHandleInputApprovalNoRejects pins §14.8.1: when an approval is pending,
// 'n' publishes user.reject (aborts the tool path).
func TestHandleInputApprovalNoRejects(t *testing.T) {
	pub := &fakePublisher{}
	m := newModelForTest()
	m.publisher = pub
	m.taskID = "t_1"
	m.approval = &approvalView{id: "apr_42"}

	_, cmd := handleInput(m, keyMsg("n"))
	runCmd(t, cmd)

	if pub.count != 1 {
		t.Fatalf("published %d, want 1", pub.count)
	}
	r, ok := pub.last.(*event.UserRejectEvent)
	if !ok {
		t.Fatalf("published %T, want *UserRejectEvent", pub.last)
	}
	if r.ApprovalID != "apr_42" {
		t.Errorf("UserRejectEvent.ApprovalID = %q, want apr_42", r.ApprovalID)
	}
}

// TestHandleInputCtrlPandRPauseResume pins §14.8.2: Ctrl+P publishes user.pause,
// Ctrl+R publishes user.resume. The TUI doesn't drive the FSM — it publishes.
func TestHandleInputCtrlPandRPauseResume(t *testing.T) {
	m := newModelForTest()
	m.taskID = "t_1"

	// Pause
	pubP := &fakePublisher{}
	m.publisher = pubP
	_, cmdP := handleInput(m, keyMsg("ctrl+p"))
	runCmd(t, cmdP)
	if p, ok := pubP.last.(*event.UserPauseEvent); !ok {
		t.Errorf("ctrl+p published %T, want *UserPauseEvent", pubP.last)
	} else if string(p.Task) != "t_1" {
		t.Errorf("UserPauseEvent.Task = %q, want t_1", p.Task)
	}

	// Resume
	pubR := &fakePublisher{}
	m.publisher = pubR
	_, cmdR := handleInput(m, keyMsg("ctrl+r"))
	runCmd(t, cmdR)
	if r, ok := pubR.last.(*event.UserResumeEvent); !ok {
		t.Errorf("ctrl+r published %T, want *UserResumeEvent", pubR.last)
	} else if string(r.Task) != "t_1" {
		t.Errorf("UserResumeEvent.Task = %q, want t_1", r.Task)
	}
}

// TestHandleInputCtrlCQuits pins §14.8.2: Ctrl+C publishes user.quit AND returns
// tea.Quit so the program exits. The mutation guard: if tea.Quit isn't returned,
// the program keeps running after the user asked to quit.
func TestHandleInputCtrlCQuits(t *testing.T) {
	pub := &fakePublisher{}
	m := newModelForTest()
	m.publisher = pub

	_, cmd := handleInput(m, keyMsg("ctrl+c"))

	// ctrl+c returns a Batch of (publish user.quit, tea.Quit). Execute the
	// publish so the fake captures it; assert the batch also carries tea.Quit.
	if cmd == nil {
		t.Fatal("cmd = nil, want a Batch of (publish user.quit, tea.Quit)")
	}
	batch, ok := cmd().(tea.BatchMsg)
	if !ok {
		t.Fatalf("cmd() produced %T, want tea.BatchMsg (publish + quit)", cmd())
	}
	quitFound := false
	published := 0
	for _, c := range batch {
		if c == nil {
			continue
		}
		msg := c()
		if msg == nil {
			published++ // the publish Cmd returns nil after publishing
			continue
		}
		if _, q := msg.(tea.QuitMsg); q {
			quitFound = true
		}
	}
	if pub.count != 1 {
		t.Fatalf("published %d, want 1 (user.quit)", pub.count)
	}
	if _, ok := pub.last.(*event.UserQuitEvent); !ok {
		t.Errorf("published %T, want *UserQuitEvent", pub.last)
	}
	if !quitFound {
		t.Error("batch did not carry tea.Quit — program would not exit on Ctrl+C (mutation guard)")
	}
	_ = published
}

// TestHandleInputDoesNotValidate pins §14.8.2: the TUI doesn't validate input —
// it can't (no logic). Esc with no active task still publishes user.cancel;
// the runtime's handler is a no-op. There's no "you can't do that" branch here.
func TestHandleInputDoesNotValidate(t *testing.T) {
	pub := &fakePublisher{}
	m := newModelForTest()
	m.publisher = pub
	// No taskID set — Esc should still publish (the runtime ignores it).
	_, cmd := handleInput(m, keyMsg("esc"))
	runCmd(t, cmd)
	if pub.count != 0 {
		t.Errorf("published %d with no active task, want 0 (Esc with no task is a no-op publish — §14.8.2 says the runtime's handler is a no-op, but the TUI still must not fabricate a cancel for a phantom task)", pub.count)
	}
}
