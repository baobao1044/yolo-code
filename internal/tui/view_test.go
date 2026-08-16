// Tests for the lipgloss TUI layout (TUI-010, File 14 §14.11). View is pure:
// given a model with width/height set, it returns the rendered screen. These tests
// exercise the resize path, the startup fallback, and the presence of expected UI
// regions (header, chat, rail, input, status) without over-asserting ANSI
// escape sequences.
package tui

import (
	"strings"
	"testing"

	"github.com/baobao1044/yolo-code/internal/event"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// TestViewFallbackBeforeResize pins the cold-start behavior: before the first
// WindowSizeMsg, the model is not ready, so View() returns the welcome message.
func TestViewFallbackBeforeResize(t *testing.T) {
	m := newModelForTest()
	out := m.View()
	if !strings.Contains(out, "awaiting task") {
		t.Errorf("pre-resize View() = %q, want welcome message containing 'awaiting task'", out)
	}
}

// TestViewFallbackSmallTerminal guards graceful degradation on very narrow or
// short terminals: the welcome message is shown until the terminal is usable.
func TestViewFallbackSmallTerminal(t *testing.T) {
	m := newModelForTest()
	m.ready = true
	m.width = 10
	m.height = 4
	out := m.View()
	if !strings.Contains(out, "awaiting task") {
		t.Errorf("small terminal View() = %q, want fallback welcome", out)
	}
}

// TestUpdateWindowSizeSetsDimensions wires tea.WindowSizeMsg to the resize
// branch: width, height, and ready must be populated so View can render.
func TestUpdateWindowSizeSetsDimensions(t *testing.T) {
	m := newModelForTest()
	if m.ready {
		t.Fatal("new model must not be ready before first resize")
	}
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	mm := m2.(Model)
	if !mm.ready {
		t.Error("ready = false after WindowSizeMsg, want true")
	}
	if mm.width != 120 {
		t.Errorf("width = %d, want 120", mm.width)
	}
	if mm.height != 40 {
		t.Errorf("height = %d, want 40", mm.height)
	}
}

// TestViewRendersHeaderAndStatus exercises the full layout with a task started
// and a few messages. We assert that the rendered output contains the task id,
// goal, state label, input prompt, and status hints.
func TestViewRendersHeaderAndStatus(t *testing.T) {
	m := newModelForTest()
	m.ready = true
	m.width = 120
	m.height = 40
	m.taskID = "t-42"
	m.goal = "render the TUI"
	m.state = "EXECUTE"
	m.messages = []messageView{
		{role: "user", text: "render the TUI"},
		{role: "assistant", text: "ok"},
	}

	out := m.View()
	if out == "" {
		t.Fatal("View() returned empty string")
	}
	for _, want := range []string{"t-42", "render the TUI", "EXECUTE", ">", "ctrl+c quit"} {
		if !strings.Contains(out, want) {
			t.Errorf("View() missing expected content %q; output:\n%s", want, out)
		}
	}
}

// TestViewRendersApprovalRail asserts that when an approval is pending the rail
// shows the approval prompt with details.
func TestViewRendersApprovalRail(t *testing.T) {
	m := newModelForTest()
	m.ready = true
	m.width = 120
	m.height = 40
	m.taskID = "t-7"
	m.goal = "fix auth"
	m.state = "WAIT_USER"
	m.approval = &approvalView{id: "a-1", tool: "edit_file", summary: "modify auth.go", risk: "high"}

	out := m.View()
	if !strings.Contains(out, "Approval required") {
		t.Errorf("approval View() missing 'Approval required'; output:\n%s", out)
	}
	if !strings.Contains(out, "y: approve") {
		t.Errorf("approval View() missing 'y: approve'; output:\n%s", out)
	}
	if !strings.Contains(out, "edit_file") {
		t.Errorf("approval View() missing tool name; output:\n%s", out)
	}
	if !strings.Contains(out, "modify auth.go") {
		t.Errorf("approval View() missing summary; output:\n%s", out)
	}
}

// TestViewRendersDiffRail asserts that patch.applied state opens the diff rail.
func TestViewRendersDiffRail(t *testing.T) {
	m := newModelForTest()
	m.ready = true
	m.width = 120
	m.height = 40
	m.taskID = "t-9"
	m.goal = "patch auth.go"
	m.focus = paneDiff
	m.diff = &diffView{
		files: []event.PatchFile{
			{Path: "auth.go", Insertions: 3, Deletions: 1},
			{Path: "new_file.go", Insertions: 20, Deletions: 0, New: true},
		},
	}

	out := m.View()
	if !strings.Contains(out, "auth.go") {
		t.Errorf("diff rail View() missing 'auth.go'; output:\n%s", out)
	}
	if !strings.Contains(out, "+3") || !strings.Contains(out, "-1") {
		t.Errorf("diff rail View() missing counts; output:\n%s", out)
	}
	if !strings.Contains(out, "new") {
		t.Errorf("diff rail View() missing '(new)' tag for new file; output:\n%s", out)
	}
}

// TestViewRendersSpinner asserts spinner glyphs appear for active states.
func TestViewRendersSpinner(t *testing.T) {
	m := newModelForTest()
	m.ready = true
	m.width = 120
	m.height = 40
	m.taskID = "t-1"
	m.state = "EXECUTE"
	m.streaming = true
	m.spinnerFrame = 3

	out := m.View()
	// Should contain one of the spinner braille chars.
	found := false
	for _, c := range spinnerFrames {
		if strings.Contains(out, c) {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("spinner View() missing spinner glyph; output:\n%s", out)
	}
}

// TestViewRendersTerminalIcons asserts DONE/CANCELLED show static icons.
func TestViewRendersTerminalIcons(t *testing.T) {
	for _, tc := range []struct {
		state string
		want  string
	}{
		{"DONE", "✔"},
		{"CANCELLED", "✘"},
	} {
		m := newModelForTest()
		m.ready = true
		m.width = 120
		m.height = 40
		m.taskID = "t-1"
		m.state = tc.state

		out := m.View()
		if !strings.Contains(out, tc.want) {
			t.Errorf("state=%s View() missing %q; output:\n%s", tc.state, tc.want, out)
		}
	}
}

// TestViewRendersHelpOverlay asserts the help overlay toggles.
func TestViewRendersHelpOverlay(t *testing.T) {
	m := newModelForTest()
	m.ready = true
	m.width = 80
	m.height = 24
	m.showHelp = true

	out := m.View()
	// "keys and commands", not the old "key bindings": the overlay now lists the
	// slash commands as well, and titling it after only half its contents is how
	// /theme and /provider stayed undiscoverable in a screen that was supposed
	// to be where you find them.
	if !strings.Contains(out, "keys and commands") {
		t.Errorf("help overlay missing its title; output:\n%s", out)
	}
	if !strings.Contains(out, "PgUp") {
		t.Errorf("help overlay missing 'PgUp'; output:\n%s", out)
	}
}

// TestViewRendersFocusIndicator asserts focus tags appear in status line.
func TestViewRendersFocusIndicator(t *testing.T) {
	m := newModelForTest()
	m.ready = true
	m.width = 120
	m.height = 40
	m.taskID = "t-1"
	m.focus = paneChat

	out := m.View()
	if !strings.Contains(out, "[chat]") {
		t.Errorf("status line missing '[chat]' focus tag; output:\n%s", out)
	}
}

// TestViewRendersStateWhy asserts transition reason appears in header.
func TestViewRendersStateWhy(t *testing.T) {
	m := newModelForTest()
	m.ready = true
	m.width = 120
	m.height = 40
	m.taskID = "t-1"
	m.state = "EXECUTE"
	m.stateWhy = "tool requires approval"

	out := m.View()
	if !strings.Contains(out, "tool requires approval") {
		t.Errorf("header missing state why; output:\n%s", out)
	}
}

// TestLayoutWidths chooses the side-by-side layout only when the terminal is
// wide enough; otherwise it stacks chat and rail vertically.
func TestLayoutWidths(t *testing.T) {
	chatW, railW, sep := layoutWidths(120)
	if sep == "" {
		t.Error("wide terminal must use a separator")
	}
	if chatW <= railW {
		t.Errorf("wide layout chatW=%d railW=%d, want chat pane wider than rail pane", chatW, railW)
	}
	if chatW+railW+lipgloss.Width(sep) != 120 {
		t.Errorf("wide layout widths do not sum to terminal width: %d + %d + %d", chatW, railW, lipgloss.Width(sep))
	}

	chatW, railW, sep = layoutWidths(80)
	if sep != "" {
		t.Error("narrow terminal must not use a separator")
	}
	if chatW != 80 || railW != 80 {
		t.Errorf("narrow layout widths = (%d, %d), want (80, 80)", chatW, railW)
	}
}

// TestTruncateHeightKeepsLastNLines pins the chat/rail truncation helper used
// to keep panes within the allocated height.
func TestTruncateHeightKeepsLastNLines(t *testing.T) {
	in := "line1\nline2\nline3\nline4"
	got := truncateHeight(in, 2)
	want := "line3\nline4"
	if got != want {
		t.Errorf("truncateHeight(...) = %q, want %q", got, want)
	}
	if truncateHeight(in, 10) != in {
		t.Error("truncateHeight with ample height should return input unchanged")
	}
}

// TestScrollUp asserts that the scrollUp helper shows older content.
func TestScrollUp(t *testing.T) {
	in := "line1\nline2\nline3\nline4\nline5"
	got := scrollUp(in, 2, 1) // show 2 lines, 1 line up from bottom
	want := "line3\nline4"
	if got != want {
		t.Errorf("scrollUp(..., 2, 1) = %q, want %q", got, want)
	}
	// Offset larger than available lines → show from top.
	got = scrollUp(in, 3, 10)
	want = "line1\nline2\nline3"
	if got != want {
		t.Errorf("scrollUp(..., 3, 10) = %q, want %q", got, want)
	}
}

// TestNextFocus asserts Tab cycles through non-empty panes.
func TestNextFocus(t *testing.T) {
	m := newModelForTest()
	m.focus = paneChat

	// No diff, no board → stays on chat.
	if got := nextFocus(m); got != paneChat {
		t.Errorf("nextFocus(no diff/board) = %d, want paneChat", got)
	}

	// With diff → goes to diff.
	m.diff = &diffView{}
	if got := nextFocus(m); got != paneDiff {
		t.Errorf("nextFocus(with diff) = %d, want paneDiff", got)
	}

	// With diff + board → from diff goes to board.
	m.board = &boardView{}
	m.focus = paneDiff
	if got := nextFocus(m); got != paneBoard {
		t.Errorf("nextFocus(diff→board) = %d, want paneBoard", got)
	}

	// From board → back to chat.
	m.focus = paneBoard
	if got := nextFocus(m); got != paneChat {
		t.Errorf("nextFocus(board→chat) = %d, want paneChat", got)
	}
}

// TestTruncateJSON pins the JSON truncation helper.
func TestTruncateJSON(t *testing.T) {
	got := truncateJSON([]byte(`"hello world"`), 8)
	if got != "hello w…" {
		t.Errorf("truncateJSON string = %q, want %q", got, "hello w…")
	}
	got = truncateJSON([]byte(`"short"`), 80)
	if got != "short" {
		t.Errorf("truncateJSON short = %q, want %q", got, "short")
	}
	got = truncateJSON(nil, 80)
	if got != "" {
		t.Errorf("truncateJSON nil = %q, want empty", got)
	}
}

// --- Phase C: onboarding, help sections, statusDot text fallback ---

// TestViewEmptyStateOnboarding pins Phase C: before the first task (no
// messages, no taskID), the chat pane renders a welcome panel naming the
// agent, listing example prompts, and pointing to /help for help.
func TestViewEmptyStateOnboarding(t *testing.T) {
	m := newModelForTest()
	m.ready = true
	m.width = 120
	m.height = 40
	// No messages, no task → empty-state.

	out := m.View()
	if !strings.Contains(out, "AI assistant") {
		t.Errorf("empty-state missing welcome banner; output:\n%s", out)
	}
	if !strings.Contains(out, "examples") {
		t.Errorf("empty-state missing 'examples' section; output:\n%s", out)
	}
	if !strings.Contains(out, "/help for key bindings") {
		t.Errorf("empty-state missing help hint; output:\n%s", out)
	}
	// An example prompt should be visible.
	if !strings.Contains(out, "fibonacci") {
		t.Errorf("empty-state missing an example prompt; output:\n%s", out)
	}
}

// TestViewEmptyStateSuppressedAfterTask pins Phase C: once a task starts
// (taskID set), the empty-state panel is NOT shown — the chat pane shows
// "no messages" instead.
func TestViewEmptyStateSuppressedAfterTask(t *testing.T) {
	m := newModelForTest()
	m.ready = true
	m.width = 120
	m.height = 40
	m.taskID = "t-1"

	out := m.View()
	if strings.Contains(out, "AI assistant") {
		t.Errorf("empty-state shown after task start; output:\n%s", out)
	}
}

// TestViewHelpOverlaySections pins Phase C: the help overlay is grouped into
// three labeled sections — Navigation, Task control, Approval — so a user
// can scan to the right group.
func TestViewHelpOverlaySections(t *testing.T) {
	m := newModelForTest()
	m.ready = true
	m.width = 120
	m.height = 40
	m.showHelp = true

	out := m.View()
	for _, want := range []string{"Navigation", "Task control", "Approval"} {
		if !strings.Contains(out, want) {
			t.Errorf("help overlay missing section %q; output:\n%s", want, out)
		}
	}
	// The non-trapping note should appear under Approval.
	if !strings.Contains(out, "scroll, help, esc and quit still work") {
		t.Errorf("help overlay missing non-trapping note; output:\n%s", out)
	}
}

// TestStatusDotTextFallback pins Phase C accessibility: statusDot returns a
// text tag (not just a color-only glyph) so the status is readable in mono /
// NO_COLOR mode and distinguishable without color.
func TestStatusDotTextFallback(t *testing.T) {
	cases := map[string]string{
		"assigned":    "[~]",
		"coded":       "[~]",
		"approved":    "[+]",
		"tested:pass": "[+]",
		"rework":      "[!]",
		"tested:fail": "[!]",
		"unknown":     "[ ]",
	}
	for status, want := range cases {
		got := statusDot(status)
		if got != want {
			t.Errorf("statusDot(%q) = %q, want %q", status, got, want)
		}
	}
}

// TestStatusViewCollapsesOnNarrowWidth pins Phase 4: on a narrow terminal the
// status line drops low-priority hints (scroll, "type goal + Enter") and keeps
// high-priority ones (focus tags, quit/help) so the line fits.
func TestStatusViewCollapsesOnNarrowWidth(t *testing.T) {
	m := newModelForTest()
	m.ready = true
	m.width = 40 // narrow
	m.height = 24

	out := statusView(m)
	// Focus tag [chat] is priority 0 → always present.
	if !strings.Contains(out, "[chat]") {
		t.Errorf("narrow status line missing [chat] focus tag (P0): %q", out)
	}
	// "type goal + Enter" is priority 6 → should be dropped on width 40.
	if strings.Contains(out, "type goal + Enter") {
		t.Errorf("narrow status line kept P6 hint 'type goal + Enter' (should collapse): %q", out)
	}
}

// TestStatusViewFullOnWideTerminal pins Phase 4: on a wide terminal all hints
// appear — nothing is collapsed.
func TestStatusViewFullOnWideTerminal(t *testing.T) {
	m := newModelForTest()
	m.ready = true
	m.width = 120
	m.height = 40

	out := statusView(m)
	if !strings.Contains(out, "[chat]") {
		t.Errorf("wide status line missing [chat]: %q", out)
	}
	if !strings.Contains(out, "ctrl+c quit") {
		t.Errorf("wide status line missing quit hint: %q", out)
	}
	if !strings.Contains(out, "type goal + Enter") {
		t.Errorf("wide status line missing 'type goal + Enter' (should fit): %q", out)
	}
}
