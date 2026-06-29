// Tests for the lipgloss TUI layout (TUI-010, File 14 §14.11). View is pure:
// given a model with width/height set, it returns the rendered screen. These tests
// exercise the resize path, the startup fallback, and the presence of expected UI
// regions (header, chat, rail, input, status) without over-asserting ANSI
// escape sequences.
package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/yolo-code/yolo/internal/event"
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
// shows the approval prompt.
func TestViewRendersApprovalRail(t *testing.T) {
	m := newModelForTest()
	m.ready = true
	m.width = 120
	m.height = 40
	m.taskID = "t-7"
	m.goal = "fix auth"
	m.state = "WAIT_USER"
	m.approval = &approvalView{id: "a-1"}

	out := m.View()
	if !strings.Contains(out, "Approval required") {
		t.Errorf("approval View() missing 'Approval required'; output:\n%s", out)
	}
	if !strings.Contains(out, "y: approve") {
		t.Errorf("approval View() missing 'y: approve'; output:\n%s", out)
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
		files: []event.PatchFile{{Path: "auth.go", Insertions: 3, Deletions: 1}},
	}

	out := m.View()
	if !strings.Contains(out, "auth.go") {
		t.Errorf("diff rail View() missing 'auth.go'; output:\n%s", out)
	}
	if !strings.Contains(out, "+3") || !strings.Contains(out, "-1") {
		t.Errorf("diff rail View() missing counts; output:\n%s", out)
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
