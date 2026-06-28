package cognitive

import (
	stdctx "context"
	"strings"
	"testing"
	"time"

	"github.com/yolo-code/yolo/internal/event"
	"github.com/yolo-code/yolo/internal/prompt"
	"github.com/yolo-code/yolo/internal/session"
)

// ctxWithTask returns a context carrying the given task ID, mirroring what the
// runtime attaches to the task-scoped context (session.WithTaskID).
func ctxWithTask(id session.TaskID) stdctx.Context {
	return session.WithTaskID(stdctx.Background(), id)
}

// newTestCore wires a Core over a mock provider + a real bus, returning both so
// the test can inspect published events.
func newTestCore(t *testing.T, chunks []Chunk) (*Core, *event.Bus) {
	t.Helper()
	bus := event.New()
	t.Cleanup(func() { _ = bus.Close() })
	core := New(NewMockProvider(chunks, 128_000), bus)
	return core, bus
}

// drain reads the first event of a topic, or fails after a timeout.
func drain(t *testing.T, ch <-chan event.Envelope) event.Envelope {
	t.Helper()
	select {
	case env := <-ch:
		return env
	case <-time.After(500 * time.Millisecond):
		t.Fatal("event not published within 500ms")
	}
	return event.Envelope{}
}

// TestThinkStreamsTokensAsEvents is the L6-001 exit criterion: the mock
// provider streams token deltas, and the Core publishes one llm.token event
// per non-empty Delta.
func TestThinkStreamsTokensAsEvents(t *testing.T) {
	chunks := []Chunk{
		{Delta: "Hello"},
		{Delta: ", "},
		{Delta: "world"},
	}
	core, bus := newTestCore(t, chunks)
	tokCh := bus.Subscribe("llm.token")

	turn, err := core.Think(ctxWithTask("t_1"), []prompt.Message{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatalf("Think: %v", err)
	}

	// Collect every token event published.
	var got []string
	for {
		select {
		case env := <-tokCh:
			te, ok := env.Evt.(*event.TokenEvent)
			if !ok {
				t.Fatalf("event type = %T, want *TokenEvent", env.Evt)
			}
			got = append(got, te.Delta)
		case <-time.After(100 * time.Millisecond):
			goto done
		}
	}
done:
	want := []string{"Hello", ", ", "world"}
	if len(got) != len(want) {
		t.Fatalf("published %d token events, want %d (%v)", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("token[%d] = %q, want %q", i, got[i], w)
		}
	}
	// The accumulated text must reconstruct the full answer.
	if turn.Text != "Hello, world" {
		t.Errorf("turn.Text = %q, want %q", turn.Text, "Hello, world")
	}
}

// TestThinkPublishesThinkingEvents pins that a chain-of-thought delta is
// published as an llm.thinking event, distinct from the visible answer's
// token events (File 07 §7.4).
func TestThinkPublishesThinkingEvents(t *testing.T) {
	chunks := []Chunk{
		{Thinking: "let me consider the options"},
		{Delta: "answer"},
	}
	core, bus := newTestCore(t, chunks)
	thinkCh := bus.Subscribe("llm.thinking")

	_, err := core.Think(ctxWithTask("t_2"), nil)
	if err != nil {
		t.Fatalf("Think: %v", err)
	}
	env := drain(t, thinkCh)
	te, ok := env.Evt.(*event.ThinkingEvent)
	if !ok {
		t.Fatalf("event type = %T, want *ThinkingEvent", env.Evt)
	}
	if te.Delta != "let me consider the options" {
		t.Errorf("thinking delta = %q, want the chain-of-thought text", te.Delta)
	}
}

// TestThinkCarriesTaskIDFromContext pins that the task ID threaded via context
// (session.WithTaskID) lands on the published TokenEvent.Task — so the event
// trace attributes tokens to the right task.
func TestThinkCarriesTaskIDFromContext(t *testing.T) {
	chunks := []Chunk{{Delta: "x"}}
	core, bus := newTestCore(t, chunks)
	tokCh := bus.Subscribe("llm.token")

	_, _ = core.Think(ctxWithTask("t_99"), nil)
	env := drain(t, tokCh)
	te, _ := env.Evt.(*event.TokenEvent)
	if te.Task != event.TaskID("t_99") {
		t.Errorf("TokenEvent.Task = %q, want %q (threaded via context)", te.Task, "t_99")
	}
}

// TestThinkFinalWhenNoToolCalls pins that a turn with no tool-call chunks is
// Final (the visible answer is the whole turn, File 07 §7.2.3). The parser
// lands in L6-002; Sprint 3's streaming path marks Final when no tool calls
// arrived.
func TestThinkFinalWhenNoToolCalls(t *testing.T) {
	chunks := []Chunk{{Delta: "just an answer"}}
	core, _ := newTestCore(t, chunks)
	turn, err := core.Think(ctxWithTask("t_3"), nil)
	if err != nil {
		t.Fatalf("Think: %v", err)
	}
	if !turn.Final {
		t.Error("turn.Final = false, want true (no tool calls → direct answer)")
	}
	if turn.Text != "just an answer" {
		t.Errorf("turn.Text = %q, want %q", turn.Text, "just an answer")
	}
}

// TestThinkPropagatesStreamError pins that an Err chunk terminates the stream
// and surfaces as Think's error.
func TestThinkPropagatesStreamError(t *testing.T) {
	chunks := []Chunk{
		{Delta: "partial"},
		{Err: errStream("provider: connection reset")},
	}
	core, _ := newTestCore(t, chunks)
	_, err := core.Think(ctxWithTask("t_4"), nil)
	if err == nil {
		t.Fatal("Think err = nil, want the stream error propagated")
	}
	if !strings.Contains(err.Error(), "connection reset") {
		t.Errorf("Think err = %v, want it to contain the stream error", err)
	}
}

// errStream is a tiny test error type for the stream-error case.
type errStream string

func (e errStream) Error() string { return string(e) }
