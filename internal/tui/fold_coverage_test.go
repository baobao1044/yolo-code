// Anti-drift: every event the TUI subscribes to must either be rendered or be
// on a list that says why it isn't.
//
// TestSubscribeTopicsAreRenderingTopics already pins the subscription half, and
// it pins it well — it reads the terminal event's topic off the event rather
// than off a literal, which is what caught PlanDoneEvent's bare "plan.done"
// sitting outside coord.>. But subscribing is only half of rendering. fold() is
// a type switch with no default arm, so an event that arrives without a case
// falls out of the switch and is discarded in total silence: no line, no
// banner, no state change, and no test failure anywhere.
//
// That is not hypothetical. task.failed was added to the catalog and published
// by the runtime's hard-error path, matched "task.>", reached fold(), and was
// dropped — while every package still reported PASS. The event existed, the
// subscription covered it, the producer emitted it, and the user saw nothing.
//
// So this asserts the other half. The expected set is derived from
// ../event/events.go by parsing it, the same technique
// event.TestCatalogCoversEveryDeclaredEvent uses on the same file, so a new
// event cannot be declared into existence and quietly go unrendered.

package tui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"testing"

	"github.com/baobao1044/yolo-code/internal/event"
)

// deliberatelyUnfolded is the set of subscribed events the TUI is not expected
// to render, each with the reason. Being on this list is a decision; being
// absent from both this list and fold() is an accident, which is the whole
// point of the test.
var deliberatelyUnfolded = map[string]string{
	// The TUI publishes these itself from input.go and receives its own echo
	// back off the bus. There is no TUI-local state to update on the echo: what
	// the user should see is the runtime's *response* (state.change,
	// task.cancelled, task.paused, command.response), which is folded. The two
	// user.* events that ARE folded — UserApproveEvent, UserRejectEvent — are
	// folded precisely because they do have local state to update: the approval
	// modal has to dismiss itself.
	"UserSubmitEvent":  "TUI publishes it (input.go:157); the runtime's task.started is what renders",
	"UserCancelEvent":  "TUI publishes it (input.go:70); task.cancelled is what renders",
	"UserPauseEvent":   "TUI publishes it (input.go:75); task.paused is what renders",
	"UserResumeEvent":  "TUI publishes it (input.go:80); state.change is what renders",
	"UserQuitEvent":    "TUI publishes it (input.go:84) and quits via tea.Quit; quitMsg handles shutdown, not fold",
	"UserCommandEvent": "TUI publishes it (input.go:197); command.response carries the reply and is folded",
	"UserPreferenceEvent": "recorded by the memory store; nothing in the chrome shows a preference write, " +
		"and memory.update already flashes that a store learned something",

	// The File 03 history/undo subsystem. These have no fold arm AND no
	// producer — nothing in the tree publishes them — so they are dead on both
	// ends, not merely unrendered. They are listed here rather than left absent
	// so that wiring the subsystem up trips this test and forces the rendering
	// question to be answered at the same time.
	"CheckpointEvent": "history/undo subsystem is unwired; no producer exists yet",
	"RestoredEvent":   "history/undo subsystem is unwired; no producer exists yet",
	"UndoneEvent":     "history/undo subsystem is unwired; no producer exists yet",
}

// eventTypeByTopic parses the event package's events.go and returns the Go type
// name behind every declared topic, read off each Type() method's return
// literal. Parsed rather than listed for the reason the event package's own
// test gives: a hand-maintained mirror drifts, and the drift is invisible.
func eventTypeByTopic(t *testing.T) map[event.Topic]string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "../event/events.go", nil, 0)
	if err != nil {
		t.Fatalf("parse ../event/events.go: %v", err)
	}
	out := map[event.Topic]string{}
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "Type" || fn.Recv == nil || fn.Body == nil {
			continue
		}
		star, ok := fn.Recv.List[0].Type.(*ast.StarExpr)
		if !ok {
			continue
		}
		ident, ok := star.X.(*ast.Ident)
		if !ok {
			continue
		}
		for _, stmt := range fn.Body.List {
			ret, ok := stmt.(*ast.ReturnStmt)
			if !ok || len(ret.Results) != 1 {
				continue
			}
			lit, ok := ret.Results[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				continue
			}
			topic, err := strconv.Unquote(lit.Value)
			if err != nil {
				t.Fatalf("unquote %s: %v", lit.Value, err)
			}
			out[event.Topic(topic)] = ident.Name
		}
	}
	if len(out) == 0 {
		t.Fatal("parsed no Type() methods out of ../event/events.go; the derivation is broken, not the fold")
	}
	return out
}

// foldedTypes parses fold.go and returns every event type named in a type-switch
// case. Read from the AST rather than by grep so a case that is commented out,
// or one written in a different but equivalent spelling, is judged by what the
// compiler sees.
func foldedTypes(t *testing.T) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "fold.go", nil, 0)
	if err != nil {
		t.Fatalf("parse fold.go: %v", err)
	}
	out := map[string]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		cc, ok := n.(*ast.CaseClause)
		if !ok {
			return true
		}
		for _, expr := range cc.List {
			star, ok := expr.(*ast.StarExpr)
			if !ok {
				continue
			}
			sel, ok := star.X.(*ast.SelectorExpr)
			if !ok {
				continue
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok || pkg.Name != "event" {
				continue
			}
			out[sel.Sel.Name] = true
		}
		return true
	})
	if len(out) == 0 {
		t.Fatal("parsed no *event.X cases out of fold.go; the derivation is broken, not the fold")
	}
	return out
}

// TestEverySubscribedEventIsRenderedOrExcused is the check that was missing when
// task.failed arrived on the bus and vanished.
func TestEverySubscribedEventIsRenderedOrExcused(t *testing.T) {
	folded := foldedTypes(t)
	for topic, typeName := range eventTypeByTopic(t) {
		if !coveredBy(renderTopics, topic) {
			continue // not subscribed; rendering it is not this test's business
		}
		if folded[typeName] {
			continue
		}
		if _, excused := deliberatelyUnfolded[typeName]; excused {
			continue
		}
		t.Errorf("the TUI subscribes to %q (%s) but fold() has no case for it, and it is not on "+
			"deliberatelyUnfolded; the event reaches the type switch, matches nothing, and is "+
			"discarded with no line, no banner, and no state change", topic, typeName)
	}
}

// TestUnfoldedExcusesAreStillReal keeps the excuse list from rotting. An entry
// naming an event that no longer exists, or one that has since been given a
// fold arm, is a stale comment asserting something untrue about the code — and
// worse, it is a hole the real check above will happily skip through if the
// name is ever reused.
func TestUnfoldedExcusesAreStillReal(t *testing.T) {
	folded := foldedTypes(t)
	declared := map[string]bool{}
	for _, typeName := range eventTypeByTopic(t) {
		declared[typeName] = true
	}
	for typeName, why := range deliberatelyUnfolded {
		if !declared[typeName] {
			t.Errorf("deliberatelyUnfolded names %s (%q) but no such event is declared in "+
				"../event/events.go; the excuse outlived the event", typeName, why)
		}
		if folded[typeName] {
			t.Errorf("deliberatelyUnfolded says %s is not rendered (%q) but fold() has a case "+
				"for it; remove the excuse", typeName, why)
		}
	}
}
