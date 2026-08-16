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

	"github.com/baobao1044/yolo-code/internal/event"
	"github.com/charmbracelet/bubbles/cursor"
	tea "github.com/charmbracelet/bubbletea"
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
	// Simulate typed text via the textinput widget (Phase B): SetValue seeds
	// the buffer the production handleInput reads via m.input.Value().
	m.input.SetValue("fix the bug")

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
	// Input cleared after submit (textinput.Reset() empties the buffer).
	if m2.input.Value() != "" {
		t.Errorf("input = %q after submit, want \"\" (cleared)", m2.input.Value())
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

// --- Phase B: textinput widget integration ---

// TestUpdateTextinputAcceptsPrintable pins Phase B: a printable keystroke
// routes to the textinput widget (via m.input.Update), not the hand-rolled
// char append. The widget owns the cursor + buffer.
func TestUpdateTextinputAcceptsPrintable(t *testing.T) {
	m := newModelForTest()
	m.ready = true
	m.width = 120
	m.height = 40

	m2, _ := m.Update(keyMsg("h"))
	m2, _ = m2.Update(keyMsg("i"))
	mm := m2.(Model)
	if mm.input.Value() != "hi" {
		t.Errorf("after typing 'hi', input.Value() = %q, want \"hi\"", mm.input.Value())
	}
}

// TestUpdateApprovalNonTrapping pins Phase B: when an approval is pending,
// scroll/help/quit keys still work (non-trapping) — only typing is suppressed.
func TestUpdateApprovalNonTrapping(t *testing.T) {
	pub := &fakePublisher{}
	m := newModelForTest()
	m.ready = true
	m.width = 120
	m.height = 40
	m.publisher = pub
	m.taskID = "t-1"
	m.approval = &approvalView{id: "apr_1"}
	m.scrollOffset = 0

	// PgUp should scroll even during approval.
	m2, _ := m.Update(keyMsg("pgup"))
	mm := m2.(Model)
	if mm.scrollOffset != 10 {
		t.Errorf("pgup during approval: scrollOffset = %d, want 10 (non-trapping)", mm.scrollOffset)
	}

	// ? should toggle help even during approval.
	m3, _ := mm.Update(keyMsg("?"))
	mm3 := m3.(Model)
	if !mm3.showHelp {
		t.Error("? during approval did not toggle help (non-trapping)")
	}
}

// TestUpdateApprovalSuppressesTyping pins Phase B: when an approval is pending,
// printable keystrokes are suppressed (don't append to the input buffer).
func TestUpdateApprovalSuppressesTyping(t *testing.T) {
	m := newModelForTest()
	m.ready = true
	m.width = 120
	m.height = 40
	m.taskID = "t-1"
	m.approval = &approvalView{id: "apr_1"}

	m2, _ := m.Update(keyMsg("x"))
	mm := m2.(Model)
	if mm.input.Value() != "" {
		t.Errorf("typing during approval: input.Value() = %q, want \"\" (suppressed)", mm.input.Value())
	}
}

// --- Focus-guarded single-character commands (§14.8.2, defect 4.1) ---

// keyModel returns a laid-out test model wired for keystroke tests. The cursor
// is static because these tests drain every returned Cmd, and the blinking
// cursor's Cmd is a ~0.5s sleep — one per keystroke otherwise.
func keyModel() Model {
	m := newModelForTest()
	m.ready = true
	m.width = 120
	m.height = 40
	m.input.Cursor.SetMode(cursor.CursorStatic)
	return m
}

// runCmdQuit executes a tea.Cmd (flattening a Batch) and reports whether any
// message it produced was tea.QuitMsg. Publishes run on the way, so a caller
// can assert both "did it quit" and "what did it publish" from one call.
func runCmdQuit(t *testing.T, cmd tea.Cmd) bool {
	t.Helper()
	if cmd == nil {
		return false
	}
	msgs := []tea.Msg{cmd()}
	if b, ok := msgs[0].(tea.BatchMsg); ok {
		msgs = nil
		for _, c := range b {
			if c != nil {
				msgs = append(msgs, c())
			}
		}
	}
	for _, msg := range msgs {
		if _, q := msg.(tea.QuitMsg); q {
			return true
		}
	}
	return false
}

// TestUpdateQIsTextWhileTyping is the defect-4.1 guard: a bare 'q' typed into a
// focused input is a CHARACTER, not the quit command. Before the focus guard
// this keystroke published user.quit and returned tea.Quit, which made the
// default interface unusable — you could not type "query", "quick", or any
// Vietnamese word containing q.
func TestUpdateQIsTextWhileTyping(t *testing.T) {
	pub := &fakePublisher{}
	m := keyModel()
	m.publisher = pub

	m2, cmd := m.Update(keyMsg("q"))
	mm := m2.(Model)

	if runCmdQuit(t, cmd) {
		t.Error("'q' typed into a focused input returned tea.Quit — the program would exit mid-word")
	}
	if pub.count != 0 {
		t.Errorf("'q' typed into a focused input published %d events (%T), want 0", pub.count, pub.last)
	}
	if mm.input.Value() != "q" {
		t.Errorf("input.Value() = %q after typing 'q', want \"q\"", mm.input.Value())
	}
}

// TestUpdateTypingQueryLandsInBuffer pins the whole word: every printable key
// that doubles as a command (q, ?, y, n) must reach the textinput while the
// input is focused, so a full word survives.
func TestUpdateTypingQueryLandsInBuffer(t *testing.T) {
	pub := &fakePublisher{}
	m := keyModel()
	m.publisher = pub

	var mm tea.Model = m
	for _, k := range []string{"q", "u", "e", "r", "y", "?"} {
		var cmd tea.Cmd
		mm, cmd = mm.Update(keyMsg(k))
		if runCmdQuit(t, cmd) {
			t.Fatalf("typing %q quit the program", k)
		}
	}
	got := mm.(Model)
	if got.input.Value() != "query?" {
		t.Errorf("input.Value() = %q after typing \"query?\", want \"query?\"", got.input.Value())
	}
	if got.showHelp {
		t.Error("'?' typed into a focused input toggled the help overlay instead of inserting a character")
	}
	if pub.count != 0 {
		t.Errorf("typing published %d events, want 0", pub.count)
	}
}

// TestUpdateSingleKeyCommandsWhenInputBlurred is the other half of the guard:
// with the input line NOT focused nothing is capturing text, so the single-key
// commands are global again — 'q' quits, '?' toggles help.
func TestUpdateSingleKeyCommandsWhenInputBlurred(t *testing.T) {
	pub := &fakePublisher{}
	m := keyModel()
	m.publisher = pub
	m.input.Blur()

	_, cmd := m.Update(keyMsg("q"))
	if !runCmdQuit(t, cmd) {
		t.Error("'q' with the input unfocused did not return tea.Quit")
	}
	if pub.count != 1 {
		t.Fatalf("'q' with the input unfocused published %d events, want 1 (user.quit)", pub.count)
	}
	if _, ok := pub.last.(*event.UserQuitEvent); !ok {
		t.Errorf("published %T, want *UserQuitEvent", pub.last)
	}

	m2, _ := m.Update(keyMsg("?"))
	if !m2.(Model).showHelp {
		t.Error("'?' with the input unfocused did not toggle the help overlay")
	}
}

// TestUpdateCtrlCQuitsInEveryFocusState pins the unconditional escape hatch:
// Ctrl+C quits whatever owns the keyboard — mid-word, with the input blurred,
// while an approval is pending, and with the help overlay up.
func TestUpdateCtrlCQuitsInEveryFocusState(t *testing.T) {
	typing := keyModel()
	typing.input.SetValue("query")

	blurred := keyModel()
	blurred.input.Blur()

	approving := keyModel()
	approving.taskID = "t-1"
	approving.approval = &approvalView{id: "apr_1"}

	helping := keyModel()
	helping.showHelp = true

	for name, m := range map[string]Model{
		"typing":   typing,
		"blurred":  blurred,
		"approval": approving,
		"help":     helping,
	} {
		pub := &fakePublisher{}
		m.publisher = pub
		_, cmd := m.Update(keyMsg("ctrl+c"))
		if !runCmdQuit(t, cmd) {
			t.Errorf("ctrl+c while %s did not return tea.Quit", name)
		}
		if _, ok := pub.last.(*event.UserQuitEvent); !ok {
			t.Errorf("ctrl+c while %s published %T, want *UserQuitEvent", name, pub.last)
		}
	}
}

// TestUpdateApprovalYNStaysGlobal is the regression guard for the y/n guard the
// focus rule is modelled on: an approval takes the keyboard away from the input
// line, so y/n answer it instead of being typed. Driven through Update (not
// handleInput) so the routing itself is covered.
func TestUpdateApprovalYNStaysGlobal(t *testing.T) {
	for key, want := range map[string]string{"y": "user.approve", "n": "user.reject"} {
		pub := &fakePublisher{}
		m := keyModel()
		m.publisher = pub
		m.taskID = "t-1"
		m.approval = &approvalView{id: "apr_1"}

		m2, cmd := m.Update(keyMsg(key))
		runCmd(t, cmd)

		if pub.count != 1 {
			t.Fatalf("%q during approval published %d events, want 1 (%s)", key, pub.count, want)
		}
		if m2.(Model).input.Value() != "" {
			t.Errorf("%q during approval was typed into the input (%q), want the approval answer",
				key, m2.(Model).input.Value())
		}
		switch key {
		case "y":
			if _, ok := pub.last.(*event.UserApproveEvent); !ok {
				t.Errorf("'y' published %T, want *UserApproveEvent", pub.last)
			}
		case "n":
			if _, ok := pub.last.(*event.UserRejectEvent); !ok {
				t.Errorf("'n' published %T, want *UserRejectEvent", pub.last)
			}
		}
	}
}

// TestUpdateApprovalYNAreTextOutsideApproval is the mirror: with no approval
// pending y/n are ordinary characters, not swallowed commands.
func TestUpdateApprovalYNAreTextOutsideApproval(t *testing.T) {
	m := keyModel()

	var mm tea.Model = m
	for _, k := range []string{"y", "n"} {
		mm, _ = mm.Update(keyMsg(k))
	}
	if got := mm.(Model).input.Value(); got != "yn" {
		t.Errorf("input.Value() = %q after typing \"yn\" outside approval, want \"yn\"", got)
	}
}

// TestUpdateHelpOverlayClosesOnAnyKey pins the overlay's own promise ("press any
// key to close"). It matters more once '?' is a plain character while typing:
// without this the overlay would be unclosable from the keyboard.
func TestUpdateHelpOverlayClosesOnAnyKey(t *testing.T) {
	for _, k := range []string{"?", "x", "esc", "enter"} {
		m := keyModel()
		m.showHelp = true

		m2, _ := m.Update(keyMsg(k))
		mm := m2.(Model)
		if mm.showHelp {
			t.Errorf("%q did not close the help overlay", k)
		}
		if mm.input.Value() != "" {
			t.Errorf("%q leaked into the input while closing the help overlay: %q", k, mm.input.Value())
		}
	}
}
