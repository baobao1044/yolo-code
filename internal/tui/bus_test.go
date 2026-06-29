// Tests for TUI-001 — the busWatcher bridge (File 14 §14.3.1). busWatcher is a
// long-lived tea.Cmd: it blocks on the subscription channel off the render
// thread, emits one busMsg per envelope, and re-launches after each message.
// When the bus closes the channel (or cancel fires) it returns quitMsg so the
// program exits. These tests drive the channel directly (no TTY, no Program).

package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/yolo-code/yolo/internal/event"
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
		"user.>",
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

// keep tea referenced so the import is used even before busWatcher's real Cmd
// type is wired (the production busWatcher returns a tea.Cmd; the test calls
// it directly, but the package needs the import to compile the seam).
var _ tea.Msg = quitMsg{}
