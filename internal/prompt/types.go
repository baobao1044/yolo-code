// Package prompt implements Layer 5 — the Prompt Compiler (File 06 §6.5). It
// turns a ContextPackage into a budgeted, ordered wire prompt ([]Message) via a
// deterministic pipeline: dedup → summarize → applyBudget → order. The Context
// Engine (Layer 4) produces the package; this layer decides the order the model
// reads it in and trims it to the token budget.
//
// Sprint 2 scope (L5-001…003): the four-stage pipeline, the §6.6.1 token budget
// enforcement (trimming over-budget groups, conversation-focused per §6.7), the
// deterministic §6.6.2 ordering, and the XML+Markdown wire format (§6.6.2).
// Summarize is a no-op stub (File 08 §8.5 supplies the real 1-line summaries);
// the LLM-driven trimming pass (§6.7.2 pass 3) is deferred to Sprint 3.
//
// Allowed imports: event, context. The stdlib `context` collides with Layer 4's
// package name, so Layer 4 is imported under the alias `econtext`.
package prompt

import (
	"context"

	econtext "github.com/baobao1044/yolo-code/internal/context"
	"github.com/baobao1044/yolo-code/internal/event"
)

// Message is one element of the compiled prompt the Cognitive Core (Layer 6)
// consumes. Role is "system" | "user" | "assistant"; Content is the wire-format
// string (XML-tagged structure inside a Markdown body, File 06 §6.6.2).
type Message struct {
	Role    string
	Content string
}

// Counter estimates the token length of a string. The real version (Sprint 3,
// wired to the provider's tokenizer) is exact; Sprint 2 uses a cheap,
// deterministic whitespace-split heuristic (S5: identical input → identical
// count). Defined as an interface so the provider's tokenizer injects later
// without touching the compiler.
type Counter interface {
	Count(s string) int
}

// Compiler is the Prompt Compiler (File 06 §6.5). Compile runs the four-stage
// pipeline on a ContextPackage and returns the ordered Messages.
type Compiler struct {
	counter Counter
	trimmer *Trimmer

	// bus carries the §6.6.3 TokenBudgetEvent Compile publishes after budgeting.
	// Nil is supported and means "don't publish" — every applyBudget test builds
	// a Compiler without one.
	//
	// This field was dead for a long time, and the shape of what that cost is
	// worth keeping: with no event, metric or log recording prompt size or
	// trimming decisions, two budget defects (the Preferences group being
	// emitted but never counted, and allocate()'s reserve floor zeroing every
	// group cap on small windows) ran unnoticed until an audit read the
	// arithmetic. A model that received none of its retrieved context looked,
	// from every observable surface, exactly like one that received all of it.
	//
	// Animating it needed three things outside this package, all now in place:
	// the event declared in internal/event/events.go, registered in catalog.go
	// (an unregistered topic makes any durability log containing it unreplayable
	// — see codec.go UnmarshalJSON), and cmd/yolo's golden headless transcript
	// regenerated, since it hashes every envelope on the root wildcard.
	bus *event.Bus
}

// New constructs a Compiler. counter defaults to a whitespace heuristic when
// nil; a nil bus disables TokenBudgetEvent publication.
func New(c Counter, bus *event.Bus) *Compiler {
	comp := &Compiler{counter: c, bus: bus}
	if comp.counter == nil {
		comp.counter = whitespaceCounter{}
	}
	comp.trimmer = &Trimmer{counter: comp.counter}
	return comp
}

// Compile runs the deterministic pipeline (File 06 §6.5): dedup → summarize →
// applyBudget → order. The result is the ordered, budgeted wire prompt.
func (c *Compiler) Compile(pkg econtext.ContextPackage) []Message {
	// Read before the stages run: dedup/summarize/applyBudget each return a
	// rebuilt package, and the causal id is the one field the report needs from
	// the input rather than the output.
	task := event.TaskID(pkg.Task)
	pkg = c.dedup(pkg)
	pkg = c.summarize(pkg)
	pkg, rep := c.applyBudget(pkg)
	c.publishBudget(task, rep)
	return c.order(pkg)
}

// publishBudget emits the §6.6.3 TokenBudgetEvent. Best-effort and nil-bus-safe,
// with context.Background() because Compile is a pure synchronous stage with no
// context of its own — the same arrangement scope.Controller.Enter uses for
// scope.enter, and for the same reason.
func (c *Compiler) publishBudget(task event.TaskID, rep budgetReport) {
	if c.bus == nil {
		return
	}
	_ = c.bus.Publish(context.Background(), &event.TokenBudgetEvent{
		Task:    task,
		Window:  rep.window,
		Used:    rep.used,
		Dropped: rep.dropped,
	})
}

// whitespaceCounter is the Sprint 2 default Counter: tokens ≈ whitespace-split
// fields. Crude but deterministic and monotonic in text length, which is all
// budgeting needs before the real tokenizer arrives.
type whitespaceCounter struct{}

func (whitespaceCounter) Count(s string) int {
	if s == "" {
		return 0
	}
	n := 0
	inField := false
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			inField = false
		} else if !inField {
			inField = true
			n++
		}
	}
	return n
}

// CompilePackage is a thin wrapper letting callers pass a *ContextPackage built
// by Layer 4 (which returns a pointer). It dereferences and forwards to Compile.
func (c *Compiler) CompilePackage(pkg *econtext.ContextPackage) []Message {
	if pkg == nil {
		return nil
	}
	return c.Compile(*pkg)
}
