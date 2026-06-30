package workflow

import (
	"testing"
	"time"

	"github.com/baobao1044/yolo-code/internal/event"
)

// stubClassifier returns a fixed WorkflowType, letting a test force the engine
// down a code path (e.g. an unregistered type to exercise the default fallback).
type stubClassifier struct{ t WorkflowType }

func (s stubClassifier) Classify(string) WorkflowType { return s.t }

// fakeWF is a stand-in workflow for registry tests; its Name is configurable.
type fakeWF struct{ name string }

func (f fakeWF) Name() string                             { return f.name }
func (f fakeWF) Next(_ *State, _ WFEvent) (Action, error) { return Action{}, nil }

// TestEngine_Select verifies the engine returns the workflow matching the goal's
// classified type.
func TestEngine_Select(t *testing.T) {
	e := New(nil)
	for _, tc := range []struct {
		name string
		goal string
		want string
	}{
		{"bugfix", "fix the login bug", "bugfix"},
		{"refactor", "refactor the auth module", "refactor"},
		{"feature", "add dark mode", "feature"},
		{"default", "review the pull request", "bugfix"}, // no keyword → default (bugfix)
	} {
		t.Run(tc.name, func(t *testing.T) {
			wf := e.Select(tc.goal, &State{})
			if wf.Name() != tc.want {
				t.Errorf("Select(%q).Name() = %q, want %q", tc.goal, wf.Name(), tc.want)
			}
		})
	}
}

// TestEngine_SelectUnknownGoalFallsBackToDefault forces an unregistered type via
// a stub classifier and asserts Select returns the default workflow.
func TestEngine_SelectUnknownGoalFallsBackToDefault(t *testing.T) {
	e := New(nil)
	e.classifier = stubClassifier{t: WorkflowType("nonexistent")}
	wf := e.Select("anything", &State{})
	if wf == nil {
		t.Fatal("Select returned nil workflow")
	}
	if wf.Name() != e.Default().Name() {
		t.Errorf("fallback Name() = %q, want default %q", wf.Name(), e.Default().Name())
	}
	if wf.Name() != "bugfix" {
		t.Errorf("default workflow Name() = %q, want bugfix", wf.Name())
	}
}

// TestEngine_NextDispatchesToSelectedWorkflow confirms Next routes to the
// classified workflow: a bugfix goal in LOCALIZE on verify_fail yields the
// bugfix repair action and advances to REPAIR.
func TestEngine_NextDispatchesToSelectedWorkflow(t *testing.T) {
	e := New(nil)
	state := &State{Phase: "LOCALIZE"}
	act, err := e.Next("fix the login bug", state, WFEvent{Kind: EventVerifyFail})
	if err != nil {
		t.Fatalf("Next err = %v, want nil", err)
	}
	if act.Kind != ActionRepair {
		t.Errorf("Action.Kind = %q, want %q (repair_loop)", act.Kind, ActionRepair)
	}
	if state.Phase != "REPAIR" {
		t.Errorf("Phase = %q, want REPAIR", state.Phase)
	}
}

// TestEngine_NilBusSafe asserts Select and Next never panic when the bus is nil.
func TestEngine_NilBusSafe(t *testing.T) {
	e := New(nil)
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Select/Next panicked on nil bus: %v", r)
		}
	}()
	_ = e.Select("fix the bug", &State{})
	if _, err := e.Next("fix the bug", &State{Phase: "LOCALIZE"}, WFEvent{Kind: EventVerifyFail}); err != nil {
		t.Errorf("Next err = %v, want nil", err)
	}
}

// TestEngine_RegisterPanicsOnDuplicate mirrors the exec tool registry: a new
// type registers cleanly, but re-registering any type panics.
func TestEngine_RegisterPanicsOnDuplicate(t *testing.T) {
	e := New(nil)
	// A new type registers without panic.
	e.Register(WorkflowType("custom"), fakeWF{name: "custom"})

	// Re-registering it must panic.
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on duplicate registration")
		}
	}()
	e.Register(WorkflowType("custom"), fakeWF{name: "custom2"})
}

// TestEngine_PublishesSelectedEvent verifies that Select publishes a
// workflow.selected event on a wired bus, with the goal and chosen workflow
// populated.
func TestEngine_PublishesSelectedEvent(t *testing.T) {
	bus := event.New()
	defer bus.Close()
	ch := bus.Subscribe(event.Topic("workflow.selected"))

	e := New(bus)
	e.Select("add dark mode", &State{})

	select {
	case env := <-ch:
		evt, ok := env.Evt.(*event.WorkflowSelectedEvent)
		if !ok {
			t.Fatalf("got event %T, want *event.WorkflowSelectedEvent", env.Evt)
		}
		if evt.Type() != event.Topic("workflow.selected") {
			t.Errorf("Type() = %q, want workflow.selected", evt.Type())
		}
		if evt.Goal != "add dark mode" {
			t.Errorf("Goal = %q, want %q", evt.Goal, "add dark mode")
		}
		if evt.Workflow != "feature" {
			t.Errorf("Workflow = %q, want feature", evt.Workflow)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for workflow.selected event")
	}
}
