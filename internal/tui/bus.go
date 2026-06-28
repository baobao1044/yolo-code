// TUI-001 bus bridge (File 14 §14.3). subscribe registers the rendering topic
// prefixes on the bus; busWatcher is the long-lived tea.Cmd that pumps
// envelopes to busMsg and re-launches after each one. When the bus closes the
// channel (or cancel fires) it returns quitMsg so the program exits cleanly.
//
// The TUI subscribes to the rendering topics, NOT the root ">" — that's
// Infrastructure's job (File 14 §14.3.2). Subscribing to fewer topics keeps
// the TUI's channel light. Subscribe is variadic + supports "prefix.>"
// wildcards (verified in event/bus.go matches), so each prefix collapses a
// family.

package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/yolo-code/yolo/internal/event"
)

// TUI-001 message set (File 14 §14.2.2). Every message is one of: an envelope
// off the bus (busMsg), shutdown (quitMsg). (inputMsg + tickMsg arrive in
// later tickets — TUI-006/TUI-007 — but are part of the same closed set.)

// busMsg wraps an event-bus envelope. Produced by the busWatcher Cmd; Update
// folds it via fold().
type busMsg struct{ env event.Envelope }

// quitMsg signals the program should exit. Produced by busWatcher when the
// bus closes the channel or cancel fires; also the terminal for user.quit
// (TUI-006).
type quitMsg struct{}

// renderTopics is the fixed prefix set the TUI subscribes to (File 14 §14.3.2,
// adapted to the real catalog). Each "xxx.>" collapses a topic family; bare
// topics are exact. This is NOT the root ">" (Infra's job).
var renderTopics = []event.Topic{
	"task.>", "state.change", "context.built",
	"llm.>", "assistant.message", "tool.>", "observation.received",
	"approval.request", "verification.>", "reflection.note",
	"patch.applied", "memory.update", "coord.>", "cost.>", "error",
}

// subscribe registers the rendering topics on the bus and returns the single
// fan-in channel busWatcher reads. File 14 §14.3.2 spec'd a SubscribeMulti
// helper that doesn't exist; the real Subscribe is variadic + wildcard-aware,
// so passing all prefixes at once yields one merged channel (File 05 §5.6 fan-in).
func subscribe(bus Subscribable) <-chan event.Envelope {
	return bus.Subscribe(renderTopics...)
}

// busWatcher is the long-lived tea.Cmd (File 14 §14.3.1): it blocks on the
// subscription channel off the render thread, emits one busMsg per envelope,
// and is re-launched after each message (fold returns busWatcher(...) so the
// bridge keeps pumping for the session). When the bus closes the channel
// (shutdown) or cancel fires (user quit / Run's defer), it returns quitMsg so
// the program exits. It never runs on the render thread — that's the whole
// point of a tea.Cmd (the render thread stays free to repaint + read stdin).
func busWatcher(sub <-chan event.Envelope, cancel <-chan struct{}) tea.Cmd {
	return func() tea.Msg {
		select {
		case env, ok := <-sub:
			if !ok {
				return quitMsg{} // bus closed → exit
			}
			return busMsg{env: env}
		case <-cancel:
			return quitMsg{}
		}
	}
}
