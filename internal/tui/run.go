// TUI-001 run — the bubbletea program surface (File 14 §14.2, §14.11). Update
// folds messages into the model; View renders; Run is the entry point.
//
// Run() (tea.NewProgram(...).Run()) is the one untested surface: it needs a
// TTY, so it can't be unit-tested. It's a thin driver — accepted, like
// infra.Stop. Every ticket's logic lives in the pure fold/handleInput/tick/
// View functions, which ARE tested directly. Update just dispatches to them.

package tui

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"
)

// Update is the Elm-architecture transition (File 14 §14.2.3): it dispatches
// a message to the right pure function. Update itself does NO I/O — it only
// folds a message into the model and possibly returns a command. The actual
// work happens in fold/handleInput/tick (later tickets), which are pure.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		s := msg.String()

		// Ctrl+C is the one unconditional escape hatch — it quits whatever owns
		// the keyboard (typing, help overlay, pending approval). Every other
		// binding below is conditional; this one must never be.
		if s == "ctrl+c" {
			return handleInput(m, msg)
		}

		// The help overlay is modal: any other key closes it and is consumed
		// (that's what the overlay itself advertises). It has to be modal now
		// that '?' is an ordinary character while typing — otherwise the only
		// way out of the overlay would be /help, which you can't see behind it.
		if m.showHelp {
			m.showHelp = false
			return m, nil
		}

		// Command keys route to handleInput in BOTH approval and normal mode
		// (Phase B: the approval trap is gone — ?, scroll, esc, quit all still
		// work while an approval is pending, only typing is suppressed).
		switch s {
		case "enter", "esc", "ctrl+p", "ctrl+r", "tab", "pgup", "pgdown":
			// Non-printable keys can't be text, so they're unconditional.
			return handleInput(m, msg)
		case "q", "?", "y", "n":
			// Printable keys are commands ONLY when nothing is capturing text.
			// Unguarded, 'q' quit on the first keystroke of "query" (and '?'
			// popped the help overlay), which made the input box unusable.
			if !capturingText(m) {
				return handleInput(m, msg)
			}
		}

		// Non-command key while an approval is pending: suppress typing, but
		// don't swallow scroll/help/quit — those were already handled above.
		if m.approval != nil {
			return m, nil
		}

		// Everything else (printable runes, backspace, ←/→, Home/End,
		// Ctrl-A/E/W, Delete) goes to the textinput widget, which owns the
		// cursor and editing semantics. This replaces the hand-rolled char
		// append that dropped non-ASCII and lacked cursor/word-delete.
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd

	case busMsg:
		// A bus event folds into render state + re-launches the watcher. TUI-007
		// coalesces the repaint: fold accumulates every delta, and the 60 Hz
		// tick (armed once in Init, re-arming itself in the tickMsg case) drives
		// the actual repaint — so a fast token stream doesn't paint per-event.
		return fold(m, msg.env)
	case tickMsg:
		// 60 Hz repaint + spinner advance (TUI-007). Re-arms itself via nextTick
		// so the loop continues without a per-event re-arm (which would leak
		// a goroutine per event).
		return tick(m)
	case quitMsg:
		return m, tea.Quit
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.ready = true
		return m, nil
	}
	return m, nil
}

// capturingText reports whether the input line currently owns the keyboard, in
// which case a printable keystroke is a character and never a command. The
// input widget is focused for the whole session, so the only thing that takes
// the keyboard back is a pending approval — that's the pre-existing y/n rule
// (§14.8.1), generalised here to every printable binding.
func capturingText(m Model) bool {
	return m.approval == nil && m.input.Focused()
}

// View is pure: a string from the model, no I/O (File 14 §14.11). TUI-001
// renders a minimal header line; later tickets (TUI-002→009) append the chat
// pane, status bar, diff viewer, cost rail, and board. The full layout lands
// incrementally so each ticket's View is testable in isolation.
func (m Model) View() string {
	// The full lipgloss layout lives in view.go (TUI-010).
	return View(m)
}

// Run is the TUI entry point (File 14 §14.11): subscribe the rendering topics,
// build the model, and block on tea.Program.Run() until tea.Quit (the
// busWatcher returns quitMsg when the bus closes or the user quits). The
// caller owns the bus + publisher (the composition root passes the real
// *event.Bus). Untested — needs a TTY; the pure surface is unit-tested.
func Run(ctx context.Context, bus Subscribable, pub EventPublisher) error {
	sub := subscribe(bus)
	cancel := make(chan struct{})
	defer close(cancel)

	m := newModel(sub, pub)
	m.cancel = cancel

	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithContext(ctx))
	_, err := p.Run()
	return err
}
