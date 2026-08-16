// Tests for TUI-001 — the busWatcher bridge (File 14 §14.3.1). busWatcher is a
// long-lived tea.Cmd: it blocks on the subscription channel off the render
// thread, emits one busMsg per envelope, and re-launches after each message.
// When the bus closes the channel (or cancel fires) it returns quitMsg so the
// program exits. These tests drive the channel directly (no TTY, no Program).

package tui

import (
	"strings"
	"testing"

	"github.com/baobao1044/yolo-code/internal/event"
	tea "github.com/charmbracelet/bubbletea"
)

// TestBusWatcherEmitsBusMsg pins §14.3.1: a buffered envelope arrives on the
// subscription channel → busWatcher returns a busMsg wrapping it. Pre-buffered
// so the Cmd's blocking read returns immediately.
func TestBusWatcherEmitsBusMsg(t *testing.T) {
	want := &event.TaskStartedEvent{Task: "t_1", Goal: "g"}
	ch := make(chan event.Envelope, 1)
	ch <- event.Envelope{Evt: want}
	cancel := make(chan struct{})

	msg := busWatcher(ch, cancel)()
	bm, ok := msg.(busMsg)
	if !ok {
		t.Fatalf("busWatcher returned %T, want busMsg", msg)
	}
	if bm.env.Evt != want {
		t.Errorf("busMsg.env.Evt = %p, want %p (the buffered envelope)", bm.env.Evt, want)
	}
}

// TestBusWatcherReturnsQuitOnBusClose pins §14.3.1: a closed subscription
// channel (the bus shut down) → busWatcher returns quitMsg so the program
// exits cleanly (the loop in fold re-launches the watcher, so this is the
// terminal condition).
func TestBusWatcherReturnsQuitOnBusClose(t *testing.T) {
	ch := make(chan event.Envelope)
	close(ch)
	cancel := make(chan struct{})

	msg := busWatcher(ch, cancel)()
	if _, ok := msg.(quitMsg); !ok {
		t.Fatalf("busWatcher on a closed channel returned %T, want quitMsg (bus closed → exit)", msg)
	}
}

// TestBusWatcherReturnsQuitOnCancel pins §14.3.1: a closed cancel channel
// (the user quit, or Run's defer) → busWatcher returns quitMsg without
// waiting for the bus.
func TestBusWatcherReturnsQuitOnCancel(t *testing.T) {
	ch := make(chan event.Envelope)
	cancel := make(chan struct{})
	close(cancel)

	msg := busWatcher(ch, cancel)()
	if _, ok := msg.(quitMsg); !ok {
		t.Fatalf("busWatcher on a canceled watcher returned %T, want quitMsg (cancel → exit)", msg)
	}
}

// TestSubscribeTopicsAreRenderingTopics pins §14.3.2: subscribe registers
// the rendering topic prefixes (NOT the root ">", which is Infra's job). The
// fake bus records what was subscribed; the test asserts the prefix set.
func TestSubscribeTopicsAreRenderingTopics(t *testing.T) {
	fb := &recordingSub{}
	sub := subscribe(fb)
	if sub == nil {
		t.Fatal("subscribe returned nil channel")
	}
	got := fb.topics
	want := []event.Topic{
		"task.>", "state.change", "context.built",
		"llm.>", "assistant.message", "tool.>", "observation.received",
		"approval.request", "verification.>", "reflection.note",
		"patch.applied", "memory.update", "coord.>", "cost.>", "error",
		"user.>", "command.response",
	}
	if len(got) != len(want) {
		t.Fatalf("subscribed %d topics, want %d: got %v", len(got), len(want), got)
	}
	have := map[event.Topic]bool{}
	for _, t := range got {
		have[t] = true
	}
	for _, w := range want {
		if !have[w] {
			t.Errorf("missing topic %q (got %v)", w, got)
		}
	}
	// The list above is a mirror of renderTopics and would happily agree with
	// itself while the TUI went deaf to the one event that ends a multi-agent
	// run. So assert the property instead of the spelling: the terminal
	// event's own Type() must be covered by something we subscribed to. This
	// is the check that was missing when PlanDoneEvent's bare "plan.done" sat
	// outside coord.> — and the check that keeps a future rename honest,
	// because it reads the topic off the event rather than off a literal.
	terminal := (&event.PlanDoneEvent{}).Type()
	if !coveredBy(got, terminal) {
		t.Errorf("no subscribed topic covers %q; a multi-agent run can never tell the TUI it finished (got %v)", terminal, got)
	}

	// Must NOT include the root wildcard — that's Infra's subscription.
	if have[">"] {
		t.Error("subscribed to root \">\" — that is Infra's job, not the TUI's (§14.3.2)")
	}
}

// recordingSub satisfies Subscribable by recording the topic list and returning
// an unclosed nil channel (the test never reads it; it only inspects topics).
type recordingSub struct {
	topics []event.Topic
}

func (r *recordingSub) Subscribe(topics ...event.Topic) <-chan event.Envelope {
	r.topics = append(r.topics, topics...)
	return make(chan event.Envelope)
}

// coveredBy reports whether any subscription pattern in got covers topic t,
// using the bus's own rules: bare ">" is the root wildcard, an exact pattern
// matches itself, and "prefix.>" matches anything under "prefix.".
//
// This restates event.matches, which is unexported. Reaching for the real one
// would mean exporting a bus internal purely so a TUI test could borrow it,
// and that is a worse trade than eight duplicated lines sitting next to the
// contract they check. If the wildcard grammar ever grows a third form, this
// is the copy that has to learn it.
func coveredBy(got []event.Topic, t event.Topic) bool {
	for _, w := range got {
		if w == ">" || w == t {
			return true
		}
		if strings.HasSuffix(string(w), ".>") &&
			strings.HasPrefix(string(t), strings.TrimSuffix(string(w), ">")) {
			return true
		}
	}
	return false
}

// keep tea referenced so the import is used even before busWatcher's real Cmd
// type is wired (the production busWatcher returns a tea.Cmd; the test calls
// it directly, but the package needs the import to compile the seam).
var _ tea.Msg = quitMsg{}
