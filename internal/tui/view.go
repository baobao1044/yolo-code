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

// All styles now live in the package-level `theme` (theme.go), selected by
// loadTheme() from YOLO_THEME / NO_COLOR. The former package-level style vars
// (theme.header, theme.state, …) are replaced by theme.header, theme.state, …
// so the palette is swappable without touching call sites.

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
		line += fmt.Sprintf(" · %s %s", icon, theme.state.Render(m.state))
	}
	// C3: show transition reason dimmed below state.
	if m.stateWhy != "" {
		line += theme.muted.Render(" (" + m.stateWhy + ")")
	}
	return theme.header.Width(m.width).Render(line)
}

// spinnerGlyph returns the appropriate glyph for the current model state.
// B2: animates when streaming or tool active, static icon for terminal states.
// Phase C: when theme.noMotion is set, the animated braille frames are
// replaced by a steady ● so motion-sensitive users aren't troubled.
func spinnerGlyph(m Model) string {
	switch m.state {
	case "DONE":
		return theme.success.Render("✔")
	case "CANCELLED":
		return theme.errorStyle.Render("✘")
	}
	if m.streaming || m.activeTool != "" {
		if theme.noMotion {
			return theme.muted.Render("●") // steady, no animation
		}
		return spinnerFrames[m.spinnerFrame%len(spinnerFrames)]
	}
	return theme.muted.Render("●")
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
	style := theme.banner
	if m.banner != "" {
		style = theme.errorStyle
	}
	return style.Width(m.width).Render(line)
}

// inputView renders the input prompt using the bubbles/textinput widget
// (Phase B). The widget owns a real blinking cursor and the editable buffer;
// inputView just renders its View() string in the prompt style. The cursor
// mode (blink vs static) is set once in newModel from theme.noMotion.
func inputView(m Model) string {
	return theme.prompt.Width(m.width).Render(m.input.View())
}

// statusView renders the bottom status line with focus indicators (D3). The
// hints are ordered by priority (highest first) and the line collapses when the
// terminal is narrow: low-priority hints drop from the right until the line
// fits m.width (Codex-style width-aware footer).
func statusView(m Model) string {
	var hints []statusHint

	// P0: focus indicators (always shown).
	chatLabel := focusLabel("chat", m.focus == paneChat)
	diffLabel := focusLabel("diff", m.focus == paneDiff && m.diff != nil)
	boardLabel := focusLabel("board", m.focus == paneBoard && m.board != nil)
	hints = append(hints, statusHint{chatLabel, 0})
	if m.diff != nil {
		hints = append(hints, statusHint{diffLabel, 1})
	}
	if m.board != nil {
		hints = append(hints, statusHint{boardLabel, 2})
	}

	// P1: approval/pause (important context).
	if m.approval != nil {
		hints = append(hints, statusHint{"approval: y/n", 3})
	}
	if m.state == "PAUSED" {
		hints = append(hints, statusHint{"paused — ctrl+r to resume", 4})
	} else {
		hints = append(hints, statusHint{"q quit · esc cancel · ctrl+p pause · ? help", 5})
	}
	// P2: nice-to-have.
	if m.approval == nil && m.state != "PAUSED" {
		hints = append(hints, statusHint{"type goal + Enter", 6})
	}
	if m.scrollOffset > 0 {
		hints = append(hints, statusHint{fmt.Sprintf("↑%d lines", m.scrollOffset), 7})
	}

	// Build the joined line; drop highest-priority (least important) hints until
	// it fits within m.width (leave 1 cell margin).
	for len(hints) > 1 {
		line := joinHints(hints)
		if lipgloss.Width(line) <= m.width-1 {
			break
		}
		// Find and remove the hint with the highest priority number.
		maxIdx := 0
		for i, h := range hints {
			if h.priority > hints[maxIdx].priority {
				maxIdx = i
			}
		}
		hints = append(hints[:maxIdx], hints[maxIdx+1:]...)
	}

	return theme.muted.Width(m.width).Render(joinHints(hints))
}

// statusHint is one hint in the status line, with a priority (lower = more
// important; higher gets dropped first when the terminal is narrow).
type statusHint struct {
	text     string
	priority int
}

// joinHints joins hint texts with " · ".
func joinHints(hs []statusHint) string {
	parts := make([]string, len(hs))
	for i, h := range hs {
		parts[i] = h.text
	}
	return strings.Join(parts, " · ")
}

// focusLabel renders a focus indicator tag for a pane.
func focusLabel(name string, active bool) string {
	if active {
		return theme.focus.Render("[" + name + "]")
	}
	return theme.unfocus.Render("[" + name + "]")
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
			theme.chatPane.Width(m.width).Height(chatH).Render(chatText),
			theme.railPane.Width(m.width).Height(railH).Render(railText),
		)
	}

	chatText := chatView(m, chatW, h)
	railText := railView(m, railW, h)
	chatBlock := theme.chatPane.Width(chatW).Height(h).Render(chatText)
	railBlock := theme.railPane.Width(railW).Height(h).Render(railText)
	return lipgloss.JoinHorizontal(lipgloss.Top, chatBlock, sep, railBlock)
}

// layoutWidths returns chat width, rail width, and separator string for the
// current terminal width.
func layoutWidths(width int) (chatW int, railW int, sep string) {
	if width >= wideLayout {
		avail := width - 1 // vertical separator
		chatW = avail * 3 / 4
		railW = avail - chatW
		sep = theme.sep.Render("│")
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
			_, _ = fmt.Fprintf(&b, "%s\n", theme.thinking.Render("thinking: "+line))
		}
	}
	if m.streaming && m.liveAssistant != "" {
		_, _ = fmt.Fprintf(&b, "%s\n", theme.assistant.Render("│ "+m.liveAssistant))
	}
	if m.activeTool != "" {
		_, _ = fmt.Fprintf(&b, "%s\n", theme.tool.Render("  ▸ "+m.activeTool))
	}
	for _, msg := range m.messages {
		prefix := ""
		text := msg.text
		switch msg.role {
		case "user":
			_, _ = fmt.Fprintf(&b, "%s\n", theme.user.Render(text))
			continue
		case "assistant":
			prefix = theme.assistant.Render("│ ")
			text = theme.assistant.Render(text)
		case "tool":
			prefix = theme.tool.Render("  ▸ ")
		case "observation":
			prefix = theme.observation.Render("  ← ")
		case "reflection":
			prefix = theme.reflection.Render("  ⟳ ")
		case "error":
			prefix = theme.errorStyle.Render("  ✗ ")
		case "verification":
			prefix = theme.muted.Render("  ")
		case "review":
			prefix = theme.muted.Render("  ⊙ ")
		case "system":
			prefix = theme.muted.Render("  · ")
		default:
			prefix = theme.muted.Render("  " + msg.role + ": ")
		}
		_, _ = fmt.Fprintf(&b, "%s%s\n", prefix, text)
	}
	content := strings.TrimRight(b.String(), "\n")
	if content == "" {
		// Phase C onboarding: an empty chat (no messages, no task yet) renders a
		// welcome panel with example prompts + the help hint, so first-time
		// users know what to do instead of staring at "no messages".
		if len(m.messages) == 0 && m.taskID == "" {
			return emptyStateView(m, w, h)
		}
		return theme.muted.Render("no messages")
	}
	wrapped := lipgloss.NewStyle().Width(w).Render(content)

	// D1: apply scroll offset.
	if m.scrollOffset > 0 {
		return scrollUp(wrapped, h, m.scrollOffset)
	}
	return truncateHeight(wrapped, h)
}

// emptyStateView renders the onboarding welcome panel (Phase C) shown in the
// chat pane before the first task. It names the agent, lists three example
// prompts a new user can copy, and points to ? for help + "type a goal + Enter".
// Keeping it text-only (no glyph that depends on a Nerd Font) keeps the first
// impression legible across terminals.
func emptyStateView(m Model, w, h int) string {
	var b strings.Builder
	_, _ = fmt.Fprintf(&b, "%s\n", theme.banner.Render("yolo — AI assistant in the terminal"))
	_, _ = fmt.Fprintf(&b, "%s\n", theme.muted.Render("type a goal below and press Enter to start a task."))
	_, _ = fmt.Fprintf(&b, "\n%s\n", theme.header.Render("examples"))
	examples := []string{
		"  write a fibonacci function in go",
		"  refactor internal/auth/login.go to use context.Context",
		"  explain the patch engine in internal/patch",
	}
	for _, ex := range examples {
		_, _ = fmt.Fprintf(&b, "%s\n", theme.user.Render(ex))
	}
	_, _ = fmt.Fprintf(&b, "\n%s\n", theme.muted.Render("press ? for key bindings · q or ctrl+c to quit"))
	content := strings.TrimRight(b.String(), "\n")
	wrapped := lipgloss.NewStyle().Width(w).Render(content)
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
		_, _ = fmt.Fprintln(&b, theme.warning.Render("Approval required"))
		if m.approval.tool != "" {
			_, _ = fmt.Fprintf(&b, "tool: %s\n", m.approval.tool)
		}
		if m.approval.summary != "" {
			_, _ = fmt.Fprintf(&b, "%s\n", m.approval.summary)
		}
		if m.approval.risk != "" {
			riskStyle := theme.warning
			if m.approval.risk == "high" {
				riskStyle = theme.errorStyle
			}
			_, _ = fmt.Fprintf(&b, "risk: %s\n", riskStyle.Render(m.approval.risk))
		}
		if m.approval.preview != "" {
			preview := m.approval.preview
			if len(preview) > 120 {
				preview = preview[:119] + "…"
			}
			_, _ = fmt.Fprintf(&b, "%s\n", theme.muted.Render(preview))
		}
		_, _ = fmt.Fprintln(&b, "y: approve · n: reject")
	}

	if m.cost.aborted {
		_, _ = fmt.Fprintf(&b, "%s %s\n", theme.errorStyle.Render("cost aborted"), m.cost.abortReason)
	} else if m.cost.dollars > 0 || m.cost.tokensEst > 0 {
		// Phase D: the rail shows accumulated spend + a rough token estimate
		// (dollars exact from cost.incurred; tokensEst ≈ len(token delta)/4 —
		// the provider doesn't parse usage on the live path). The ~ prefix
		// marks the estimate as rough.
		_, _ = fmt.Fprintf(&b, "cost: $%.2f · ~%d tok\n", m.cost.dollars, m.cost.tokensEst)
		if m.cost.level != "" {
			_, _ = fmt.Fprintf(&b, "level: %s\n", m.cost.level)
		}
	} else if m.cost.level != "" {
		_, _ = fmt.Fprintf(&b, "%s %s\n", "cost level:", m.cost.level)
	}

	if m.focus == paneDiff && m.diff != nil {
		_, _ = fmt.Fprintln(&b, "Diff viewer")
		if m.diff.reason != "" {
			_, _ = fmt.Fprintf(&b, "%s\n", theme.errorStyle.Render(m.diff.reason))
		}
		for _, f := range m.diff.files {
			suffix := ""
			if f.New {
				suffix = theme.success.Render(" (new)")
			}
			_, _ = fmt.Fprintf(&b, "%s +%d -%d%s\n", f.Path, f.Insertions, f.Deletions, suffix)
		}
		if len(m.diff.files) == 0 && m.diff.reason == "" {
			_, _ = fmt.Fprintln(&b, "(no files)")
		}
		// Phase D: render the real diff hunks when the event carried them.
		// Each line is colored by prefix: + → success, - → error, else muted
		// (context), with a 1-based line number for orientation.
		if m.diff.diff != "" {
			_, _ = fmt.Fprintln(&b)
			for i, line := range strings.Split(m.diff.diff, "\n") {
				num := fmt.Sprintf("%3d ", i+1)
				switch {
				case strings.HasPrefix(line, "+"):
					_, _ = fmt.Fprintf(&b, "%s%s\n", theme.muted.Render(num), theme.success.Render(line))
				case strings.HasPrefix(line, "-"):
					_, _ = fmt.Fprintf(&b, "%s%s\n", theme.muted.Render(num), theme.errorStyle.Render(line))
				default:
					_, _ = fmt.Fprintf(&b, "%s%s\n", theme.muted.Render(num), theme.muted.Render(line))
				}
			}
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
		return theme.muted.Render("no rail items")
	}
	wrapped := lipgloss.NewStyle().Width(w).Render(content)
	return truncateHeight(wrapped, h)
}

// statusDot maps a todo status to a glyph + short text label (Phase C
// accessibility). The text tag is the color-blind fallback: the status is
// readable in mono/NO_COLOR mode and distinguishable without relying on
// color alone. Glyph + text together: ~ assigned, + approved, ! rework.
func statusDot(status string) string {
	switch status {
	case "assigned", "coded":
		return theme.warning.Render("[~]")
	case "approved", "tested:pass":
		return theme.success.Render("[+]")
	case "rework", "tested:fail":
		return theme.errorStyle.Render("[!]")
	}
	return theme.muted.Render("[ ]")
}

// helpView renders the help overlay (Phase C). Keys are grouped into three
// sections — Navigation, Task control, Approval — so a user looking for a
// specific action can scan to the right group. A note tells the user scroll
// and help still work while an approval is pending (Phase B non-trapping).
func helpView(m Model) string {
	var b strings.Builder
	_, _ = fmt.Fprintf(&b, "%s\n\n", theme.header.Render("yolo — key bindings"))

	// Navigation.
	_, _ = fmt.Fprintf(&b, "%s\n", theme.state.Render("Navigation"))
	_, _ = fmt.Fprintf(&b, "  %-14s %s\n", "Tab", "switch focus: chat → diff → board")
	_, _ = fmt.Fprintf(&b, "  %-14s %s\n", "PgUp / PgDn", "scroll chat up / down")
	_, _ = fmt.Fprintf(&b, "  %-14s %s\n", "?", "toggle this help")

	// Task control.
	_, _ = fmt.Fprintf(&b, "\n%s\n", theme.state.Render("Task control"))
	_, _ = fmt.Fprintf(&b, "  %-14s %s\n", "Enter", "submit goal / message")
	_, _ = fmt.Fprintf(&b, "  %-14s %s\n", "Esc", "cancel current task")
	_, _ = fmt.Fprintf(&b, "  %-14s %s\n", "Ctrl+P", "pause task")
	_, _ = fmt.Fprintf(&b, "  %-14s %s\n", "Ctrl+R", "resume paused task")
	_, _ = fmt.Fprintf(&b, "  %-14s %s\n", "q / Ctrl+C", "quit")

	// Approval.
	_, _ = fmt.Fprintf(&b, "\n%s\n", theme.state.Render("Approval"))
	_, _ = fmt.Fprintf(&b, "  %-14s %s\n", "y / n", "approve / reject (when approval pending)")
	_, _ = fmt.Fprintf(&b, "\n%s\n", theme.muted.Render("scroll, help, esc and quit still work while an approval is pending"))

	_, _ = fmt.Fprintf(&b, "\n%s\n", theme.muted.Render("press any key to close"))
	body := b.String()

	// Frame the help in a bordered box so it reads as an overlay, not inline text.
	return lipgloss.NewStyle().
		Width(m.width).
		Height(m.height).
		Align(lipgloss.Center, lipgloss.Center).
		Render(lipgloss.NewStyle().
			Padding(1, 2).
			Border(lipgloss.RoundedBorder()).
			BorderForeground(theme.state.GetForeground()).
			Render(body))
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
