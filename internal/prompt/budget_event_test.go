// The §6.6.3 TokenBudgetEvent is the answer to a specific question the tree
// could not previously answer: what did the model actually receive?
//
// Before it, trimming left two kinds of trace. Three of the four passes write an
// "[N … omitted]" marker into the prompt itself, which only helps someone
// reading the wire; the conversation hard cut writes nothing at all, because a
// marker between kept turns would read as a turn. So the single most aggressive
// pass — the one that drops whole turns of history — was invisible everywhere.
// That is the gap these tests pin.

package prompt

import (
	"testing"
	"time"

	econtext "github.com/baobao1044/yolo-code/internal/context"
	"github.com/baobao1044/yolo-code/internal/event"
)

// collectBudget compiles pkg on a Compiler wired to a live bus and returns the
// TokenBudgetEvent it published, failing if none arrives.
func collectBudget(t *testing.T, pkg econtext.ContextPackage) *event.TokenBudgetEvent {
	t.Helper()
	bus := event.New()
	t.Cleanup(func() { _ = bus.Close() })

	sub := bus.Subscribe("prompt.budget")
	defer bus.Unsubscribe(sub)

	// Compiled on its own goroutine: Publish applies backpressure until every
	// subscriber accepts, and this test IS the subscriber, so compiling inline
	// would wedge on its own delivery.
	go New(nil, bus).Compile(pkg)

	for {
		select {
		case env := <-sub:
			if e, ok := env.Evt.(*event.TokenBudgetEvent); ok {
				return e
			}
		case <-time.After(2 * time.Second):
			t.Fatal("Compile published no prompt.budget event")
			return nil
		}
	}
}

func parts(texts ...string) []econtext.Part {
	out := make([]econtext.Part, 0, len(texts))
	for _, s := range texts {
		out = append(out, econtext.Part{Text: s})
	}
	return out
}

// TestBudgetEventReportsTheConversationHardCut is the case with no other
// witness. A window too small for the history forces Pass B, which drops turns
// and leaves no marker; the event must say how many went.
func TestBudgetEventReportsTheConversationHardCut(t *testing.T) {
	pkg := econtext.ContextPackage{
		Task:   "t_1",
		System: parts("you are a coding agent"),
		User:   parts("fix the parser"),
		Conversation: parts(
			"turn one with a fair amount of text in it to spend tokens",
			"turn two with a fair amount of text in it to spend tokens",
			"turn three with a fair amount of text in it to spend tokens",
			"turn four with a fair amount of text in it to spend tokens",
		),
		Budget: econtext.Budget{Window: 30},
	}

	e := collectBudget(t, pkg)
	if e.Task != "t_1" {
		t.Errorf("Task = %q, want t_1 — the event cannot be correlated to a task without it", e.Task)
	}
	if e.Window != 30 {
		t.Errorf("Window = %d, want 30", e.Window)
	}
	if n := e.Dropped["conversation turns"]; n == 0 {
		t.Errorf("Dropped = %v, want a nonzero \"conversation turns\" — the hard cut has no other trace", e.Dropped)
	}
}

// TestBudgetEventReportsSizeWhenUnbudgeted covers the path that trims nothing.
// Window 0 means "no cap", and the temptation is to skip the report entirely —
// but prompt size is exactly what nobody could measure, and an unbudgeted
// prompt is still a prompt. Used must be real; Dropped must be absent rather
// than an empty map, so a consumer can test it for emptiness without
// distinguishing nil from {}.
func TestBudgetEventReportsSizeWhenUnbudgeted(t *testing.T) {
	pkg := econtext.ContextPackage{
		Task:   "t_2",
		System: parts("you are a coding agent"),
		User:   parts("fix the parser"),
		// No Budget: the unbudgeted path.
	}

	e := collectBudget(t, pkg)
	if e.Window != 0 {
		t.Errorf("Window = %d, want 0 (unbudgeted)", e.Window)
	}
	if e.Used <= 0 {
		t.Errorf("Used = %d, want > 0 — an unbudgeted prompt still has a size, and reporting 0 would make"+
			" \"no window\" and \"no content\" the same reading", e.Used)
	}
	if len(e.Dropped) != 0 {
		t.Errorf("Dropped = %v, want empty — nothing is trimmed without a window", e.Dropped)
	}
}

// TestBudgetEventIsSkippedWithoutABus pins the nil-bus contract. Every
// applyBudget test builds a Compiler with New(nil, nil); if publishing were not
// nil-safe this package's own suite would panic, but "it would have crashed" is
// not a guarantee — a later refactor could reintroduce the dereference behind a
// branch none of those tests take.
func TestBudgetEventIsSkippedWithoutABus(t *testing.T) {
	New(nil, nil).Compile(econtext.ContextPackage{
		Task:   "t_3",
		System: parts("system"),
		User:   parts("go"),
		Budget: econtext.Budget{Window: 1},
	})
}
