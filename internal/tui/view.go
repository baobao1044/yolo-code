// TUI layout and rendering (File 14 §14.11). View owns the lipgloss-based
// painting of the header, chat rail, input bar, status line, banner, and
// multi-agent board. It reads the projection built by fold and renders it.

package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// simple fallback on startup before the first resize arrives.
const welcomeMsg = "yolo — awaiting task (press q or ctrl+c to quit)"

// Layout constants in terminal cells.
const (
	headerHeight = 1
	statusHeight = 1
	inputHeight  = 1
	minHeight    = 8
	wideLayout   = 100 // use side-by-side rail only when wide enough
)

// Spinner frames (braille animation).
var spinnerFrames = []string{"⣾", "⣽", "⣻", "⢿", "⡿", "⣟", "⣯", "⣷"}

var (
	// Role colors used in the chat pane.
	headerStyle      = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("86"))
	stateStyle       = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("11"))
	bannerStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	promptStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	userStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("7"))
	assistantStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("86"))
	thinkingStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	toolStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("246"))
	observationStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("220"))
	reflectionStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("208"))
	errorStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	successStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("82"))
	warningStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	mutedStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	focusStyle       = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("86"))
	unfocusStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))

	chatPaneStyle = lipgloss.NewStyle().
			Border(lipgloss.NormalBorder(), false, true, false, false).
			Padding(0, 1)
	railPaneStyle = lipgloss.NewStyle().Padding(0, 1)
	sepStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
)

// View renders the full screen. It is called by Bubble Tea on every frame.
func View(m Model) string {
	if !m.ready || m.width < 20 || m.height < minHeight {
		return welcomeMsg
	}

	// Help overlay takes over the entire screen.
	if m.showHelp {
		return helpView(m)
	}

	// Fixed chrome — each is one logical line, bounded by m.width.
	head := headerView(m)
	banner := bannerView(m)
	inputLine := inputView(m)
	statusLine := statusView(m)

	headH := lipgloss.Height(head)
	bannerH := lipgloss.Height(banner)
	inputH := lipgloss.Height(inputLine)
	statusH := lipgloss.Height(statusLine)
	contentH := m.height - headH - bannerH - inputH - statusH

	// B3: guard negative contentH.
	if contentH < minHeight/2 {
		contentH = minHeight / 2
	}

	body := bodyView(m, contentH)

	return lipgloss.JoinVertical(
		lipgloss.Left,
		head,
		banner,
		body,
		inputLine,
		statusLine,
	)
}

// headerView renders the task id, goal, current state, and spinner/status icon.
func headerView(m Model) string {
	tid := m.taskID
	if tid == "" {
		tid = "-"
	}
	goal := m.goal
	if goal == "" {
		goal = "-"
	}
	line := fmt.Sprintf("yolo — task %s · %s", tid, goal)
	if m.state != "" {
		// B2: render spinner or terminal icon next to state.
		icon := spinnerGlyph(m)
		line += fmt.Sprintf(" · %s %s", icon, stateStyle.Render(m.state))
	}
	// C3: show transition reason dimmed below state.
	if m.stateWhy != "" {
		line += mutedStyle.Render(" (" + m.stateWhy + ")")
	}
	return headerStyle.Width(m.width).Render(line)
}

// spinnerGlyph returns the appropriate glyph for the current model state.
// B2: animates when streaming or tool active, static icon for terminal states.
func spinnerGlyph(m Model) string {
	switch m.state {
	case "DONE":
		return successStyle.Render("✔")
	case "CANCELLED":
		return errorStyle.Render("✘")
	}
	if m.streaming || m.activeTool != "" {
		return spinnerFrames[m.spinnerFrame%len(spinnerFrames)]
	}
	return mutedStyle.Render("●")
}

// bannerView renders transient flashes (errors, context ready, memory update).
func bannerView(m Model) string {
	parts := make([]string, 0, 3)
	if m.banner != "" {
		parts = append(parts, m.banner)
	}
	if m.contextFlash != "" {
		parts = append(parts, m.contextFlash)
	}
	if m.memoryFlash != "" {
		parts = append(parts, m.memoryFlash)
	}
	if len(parts) == 0 {
		return strings.Repeat(" ", m.width)
	}
	line := strings.Join(parts, " · ")
	style := bannerStyle
	if m.banner != "" {
		style = errorStyle
	}
	return style.Width(m.width).Render(line)
}

// inputView renders the input prompt with a blinking cursor (D4).
func inputView(m Model) string {
	cursor := "▎"
	if m.spinnerFrame%10 < 5 {
		cursor = " "
	}
	text := m.inputText + cursor
	if m.inputText == "" && cursor == " " {
		text = "_"
	}
	return promptStyle.Width(m.width).Render("> " + text)
}

// statusView renders the bottom status line with focus indicators (D3).
func statusView(m Model) string {
	var hints []string

	// D3: focus indicator.
	chatLabel := focusLabel("chat", m.focus == paneChat)
	diffLabel := focusLabel("diff", m.focus == paneDiff && m.diff != nil)
	boardLabel := focusLabel("board", m.focus == paneBoard && m.board != nil)
	hints = append(hints, chatLabel)
	if m.diff != nil {
		hints = append(hints, diffLabel)
	}
	if m.board != nil {
		hints = append(hints, boardLabel)
	}

	if m.approval != nil {
		hints = append(hints, "approval: y/n")
	}
	if m.state == "PAUSED" {
		hints = append(hints, "paused — ctrl+r to resume")
	} else {
		hints = append(hints, "q quit · esc cancel · ctrl+p pause · ? help")
	}
	if m.approval == nil && m.state != "PAUSED" {
		hints = append(hints, "type goal + Enter")
	}
	if m.scrollOffset > 0 {
		hints = append(hints, fmt.Sprintf("↑%d lines", m.scrollOffset))
	}
	return mutedStyle.Width(m.width).Render(strings.Join(hints, " · "))
}

// focusLabel renders a focus indicator tag for a pane.
func focusLabel(name string, active bool) string {
	if active {
		return focusStyle.Render("[" + name + "]")
	}
	return unfocusStyle.Render("[" + name + "]")
}

// bodyView renders the chat pane and, when the terminal is wide enough, a
// side rail.
func bodyView(m Model, h int) string {
	chatW, railW, sep := layoutWidths(m.width)

	if sep == "" {
		// B1: narrow layout — split height in half, not double it.
		chatH := h / 2
		if chatH < 2 {
			chatH = 2
		}
		railH := h - chatH
		chatText := chatView(m, chatW, chatH)
		railText := railView(m, railW, railH)
		return lipgloss.JoinVertical(
			lipgloss.Left,
			chatPaneStyle.Width(m.width).Height(chatH).Render(chatText),
			railPaneStyle.Width(m.width).Height(railH).Render(railText),
		)
	}

	chatText := chatView(m, chatW, h)
	railText := railView(m, railW, h)
	chatBlock := chatPaneStyle.Width(chatW).Height(h).Render(chatText)
	railBlock := railPaneStyle.Width(railW).Height(h).Render(railText)
	return lipgloss.JoinHorizontal(lipgloss.Top, chatBlock, sep, railBlock)
}

// layoutWidths returns chat width, rail width, and separator string for the
// current terminal width.
func layoutWidths(width int) (chatW int, railW int, sep string) {
	if width >= wideLayout {
		avail := width - 1 // vertical separator
		chatW = avail * 3 / 4
		railW = avail - chatW
		sep = sepStyle.Render("│")
		return
	}
	chatW = width
	railW = width
	sep = ""
	return
}

// chatView renders the chat pane. Supports scroll offset (D1).
func chatView(m Model, w, h int) string {
	var b strings.Builder
	if m.streaming && m.thinking != "" {
		for _, line := range strings.Split(m.thinking, "\n") {
			_, _ = fmt.Fprintf(&b, "%s\n", thinkingStyle.Render("thinking: "+line))
		}
	}
	if m.streaming && m.liveAssistant != "" {
		_, _ = fmt.Fprintf(&b, "%s\n", assistantStyle.Render(m.liveAssistant))
	}
	if m.activeTool != "" {
		_, _ = fmt.Fprintf(&b, "%s\n", toolStyle.Render(fmt.Sprintf("tool: %s", m.activeTool)))
	}
	for _, msg := range m.messages {
		prefix := ""
		text := msg.text
		switch msg.role {
		case "user":
			_, _ = fmt.Fprintf(&b, "%s\n", userStyle.Render("> "+text))
			continue
		case "assistant":
			prefix = assistantStyle.Render("assistant: ")
			text = assistantStyle.Render(text)
		case "tool":
			prefix = toolStyle.Render("tool: ")
		case "observation":
			prefix = observationStyle.Render("obs: ")
		case "reflection":
			prefix = reflectionStyle.Render("reflection: ")
		case "error":
			prefix = errorStyle.Render("error: ")
		case "verification":
			prefix = mutedStyle.Render("")
		case "review":
			prefix = mutedStyle.Render("review: ")
		default:
			prefix = mutedStyle.Render(msg.role + ": ")
		}
		_, _ = fmt.Fprintf(&b, "%s%s\n", prefix, text)
	}
	content := strings.TrimRight(b.String(), "\n")
	if content == "" {
		return mutedStyle.Render("no messages")
	}
	wrapped := lipgloss.NewStyle().Width(w).Render(content)

	// D1: apply scroll offset.
	if m.scrollOffset > 0 {
		return scrollUp(wrapped, h, m.scrollOffset)
	}
	return truncateHeight(wrapped, h)
}

// scrollUp shows older content by offset lines from the top. When offset is
// large enough, it shows the earliest lines. Falls back to truncateHeight when
// offset exceeds available lines.
func scrollUp(text string, h, offset int) string {
	lines := strings.Split(text, "\n")
	total := len(lines)
	start := total - h - offset
	if start < 0 {
		start = 0
	}
	end := start + h
	if end > total {
		end = total
	}
	return strings.Join(lines[start:end], "\n")
}

// railView renders the side panel: approval prompt, cost rail, diff viewer, or
// multi-agent board depending on what is active.
func railView(m Model, w, h int) string {
	var b strings.Builder

	// C2: approval rail with full details.
	if m.approval != nil {
		_, _ = fmt.Fprintln(&b, warningStyle.Render("Approval required"))
		if m.approval.tool != "" {
			_, _ = fmt.Fprintf(&b, "tool: %s\n", m.approval.tool)
		}
		if m.approval.summary != "" {
			_, _ = fmt.Fprintf(&b, "%s\n", m.approval.summary)
		}
		if m.approval.risk != "" {
			riskStyle := warningStyle
			if m.approval.risk == "high" {
				riskStyle = errorStyle
			}
			_, _ = fmt.Fprintf(&b, "risk: %s\n", riskStyle.Render(m.approval.risk))
		}
		if m.approval.preview != "" {
			preview := m.approval.preview
			if len(preview) > 120 {
				preview = preview[:119] + "…"
			}
			_, _ = fmt.Fprintf(&b, "%s\n", mutedStyle.Render(preview))
		}
		_, _ = fmt.Fprintln(&b, "y: approve · n: reject")
	}

	if m.cost.aborted {
		_, _ = fmt.Fprintf(&b, "%s %s\n", errorStyle.Render("cost aborted"), m.cost.abortReason)
	} else if m.cost.level != "" {
		_, _ = fmt.Fprintf(&b, "%s %s\n", "cost level:", m.cost.level)
	}

	if m.focus == paneDiff && m.diff != nil {
		_, _ = fmt.Fprintln(&b, "Diff viewer")
		if m.diff.reason != "" {
			_, _ = fmt.Fprintf(&b, "%s\n", errorStyle.Render(m.diff.reason))
		}
		for _, f := range m.diff.files {
			suffix := ""
			if f.New {
				suffix = successStyle.Render(" (new)")
			}
			_, _ = fmt.Fprintf(&b, "%s +%d -%d%s\n", f.Path, f.Insertions, f.Deletions, suffix)
		}
		if len(m.diff.files) == 0 && m.diff.reason == "" {
			_, _ = fmt.Fprintln(&b, "(no files)")
		}
	}

	if m.board != nil {
		_, _ = fmt.Fprintf(&b, "%s %s\n", "plan:", m.board.planID)
		for _, td := range m.board.todos {
			status := statusDot(td.status)
			line := fmt.Sprintf("%s %s · %s", status, td.agent, td.status)
			if td.brief != "" {
				brief := td.brief
				if len(brief) > 40 {
					brief = brief[:39] + "…"
				}
				line += " — " + brief
			}
			_, _ = fmt.Fprintf(&b, "%s\n", line)
		}
	}

	content := strings.TrimRight(b.String(), "\n")
	if content == "" {
		return mutedStyle.Render("no rail items")
	}
	wrapped := lipgloss.NewStyle().Width(w).Render(content)
	return truncateHeight(wrapped, h)
}

// statusDot maps a todo status to a colored glyph.
func statusDot(status string) string {
	switch status {
	case "assigned", "coded":
		return warningStyle.Render("●")
	case "approved", "tested:pass":
		return successStyle.Render("●")
	case "rework", "tested:fail":
		return errorStyle.Render("●")
	}
	return mutedStyle.Render("○")
}

// helpView renders the help overlay (D2).
func helpView(m Model) string {
	var b strings.Builder
	_, _ = fmt.Fprintf(&b, "%s\n\n", headerStyle.Render("yolo — key bindings"))
	_, _ = fmt.Fprintf(&b, "  %-16s %s\n", "Enter", "submit goal/message")
	_, _ = fmt.Fprintf(&b, "  %-16s %s\n", "Esc", "cancel current task")
	_, _ = fmt.Fprintf(&b, "  %-16s %s\n", "Ctrl+P", "pause task")
	_, _ = fmt.Fprintf(&b, "  %-16s %s\n", "Ctrl+R", "resume paused task")
	_, _ = fmt.Fprintf(&b, "  %-16s %s\n", "q / Ctrl+C", "quit")
	_, _ = fmt.Fprintf(&b, "  %-16s %s\n", "y / n", "approve / reject (when approval pending)")
	_, _ = fmt.Fprintf(&b, "  %-16s %s\n", "Tab", "switch focus: chat → diff → board")
	_, _ = fmt.Fprintf(&b, "  %-16s %s\n", "PgUp / PgDn", "scroll chat up / down")
	_, _ = fmt.Fprintf(&b, "  %-16s %s\n", "Backspace", "delete last character")
	_, _ = fmt.Fprintf(&b, "  %-16s %s\n", "?", "toggle this help")
	_, _ = fmt.Fprintf(&b, "\n%s\n", mutedStyle.Render("Press any key to close"))
	return lipgloss.NewStyle().
		Width(m.width).
		Height(m.height).
		Render(b.String())
}

// truncateHeight keeps only the last h lines of text.
func truncateHeight(text string, h int) string {
	if h <= 0 {
		return ""
	}
	lines := strings.Split(text, "\n")
	if len(lines) <= h {
		return text
	}
	return strings.Join(lines[len(lines)-h:], "\n")
}

// or returns value when non-empty, otherwise other.
func or(value, other string) string {
	if value != "" {
		return value
	}
	return other
}
