package scope

import (
	"testing"
	"time"

	"github.com/baobao1044/yolo-code/internal/event"
)

// TestController_EnterExitHistory verifies Enter pushes and Exit pops the level
// history in LIFO order, and that Exit on an empty history is a no-op.
func TestController_EnterExitHistory(t *testing.T) {
	c := New(nil)
	if got := c.Current(); got != LevelTask {
		t.Fatalf("initial Current = %v, want TASK", got)
	}

	c.Enter(LevelRepo, "explore repo")
	if got := c.Current(); got != LevelRepo {
		t.Errorf("after Enter(REPO), Current = %v, want REPO", got)
	}
	c.Enter(LevelFile, "drill into file")
	if got := c.Current(); got != LevelFile {
		t.Errorf("after Enter(FILE), Current = %v, want FILE", got)
	}

	if got := c.Exit(); got != LevelRepo {
		t.Errorf("first Exit = %v, want REPO (LIFO)", got)
	}
	if got := c.Exit(); got != LevelTask {
		t.Errorf("second Exit = %v, want TASK (LIFO)", got)
	}
	// History empty: Exit returns the current level unchanged.
	if got := c.Exit(); got != LevelTask {
		t.Errorf("Exit with empty history = %v, want TASK", got)
	}
}

// TestController_CanUseTool walks every level × a representative tool and
// asserts the W2 permission table gates access correctly.
func TestController_CanUseTool(t *testing.T) {
	for _, tc := range []struct {
		level Level
		tool  string
		want  bool
	}{
		// Task: only plan/decompose.
		{LevelTask, "plan", true},
		{LevelTask, "decompose", true},
		{LevelTask, "edit_file", false},
		// Repo: read-only.
		{LevelRepo, "list_files", true},
		{LevelRepo, "grep", true},
		{LevelRepo, "read_file", true},
		{LevelRepo, "edit_file", false},
		// File.
		{LevelFile, "read_file", true},
		{LevelFile, "grep", true},
		{LevelFile, "write_file", false},
		// Function.
		{LevelFunction, "read_file", true},
		{LevelFunction, "view_function", true},
		{LevelFunction, "call_graph", true},
		{LevelFunction, "edit_file", false},
		// Edit.
		{LevelEdit, "edit_file", true},
		{LevelEdit, "write_file", true},
		{LevelEdit, "read_file", false},
		// Verify.
		{LevelVerify, "run_test", true},
		{LevelVerify, "bash", true},
		{LevelVerify, "git_diff", true},
		{LevelVerify, "edit_file", false},
	} {
		c := New(nil)
		c.Enter(tc.level, "test")
		if got := c.CanUseTool(tc.tool); got != tc.want {
			t.Errorf("CanUseTool(%q) at %v = %v, want %v", tc.tool, tc.level, got, tc.want)
		}
	}
}

// TestController_SuggestTransition covers the W3 rules: the pass case plus the
// three failure rules (expand, stay, contract) and the LevelTask contract floor.
func TestController_SuggestTransition(t *testing.T) {
	for _, tc := range []struct {
		name       string
		current    Level
		verdict    Verdict
		wantLevel  Level
		wantAction Action
	}{
		{
			name:       "pass stays at current level",
			current:    LevelVerify,
			verdict:    Verdict{Pass: true},
			wantLevel:  LevelVerify,
			wantAction: ActionNoOp,
		},
		{
			name:       "test missing_import expands to repo",
			current:    LevelFile,
			verdict:    Verdict{Stage: "test", Hint: "missing_import"},
			wantLevel:  LevelRepo,
			wantAction: ActionExpand,
		},
		{
			name:       "compile failure stays to fix syntax",
			current:    LevelEdit,
			verdict:    Verdict{Stage: "compile"},
			wantLevel:  LevelEdit,
			wantAction: ActionStay,
		},
		{
			name:       "other failure contracts one level",
			current:    LevelFile,
			verdict:    Verdict{Stage: "test", Hint: "wrong_value"},
			wantLevel:  LevelRepo,
			wantAction: ActionContract,
		},
		{
			name:       "contract floors at LevelTask",
			current:    LevelTask,
			verdict:    Verdict{Stage: "test", Hint: "nope"},
			wantLevel:  LevelTask,
			wantAction: ActionContract,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := New(nil)
			c.Enter(tc.current, "test")
			tr := c.SuggestTransition(tc.verdict)
			if tr.TargetLevel != tc.wantLevel {
				t.Errorf("TargetLevel = %v, want %v", tr.TargetLevel, tc.wantLevel)
			}
			if tr.Action != tc.wantAction {
				t.Errorf("Action = %v, want %v", tr.Action, tc.wantAction)
			}
		})
	}
}

// TestController_NilBusSafe asserts Enter and Exit never panic when the bus is
// nil — the controller's bus interaction is best-effort.
func TestController_NilBusSafe(t *testing.T) {
	c := New(nil)
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Enter/Exit panicked on nil bus: %v", r)
		}
	}()
	c.Enter(LevelRepo, "should not panic")
	c.Enter(LevelFile, "again")
	_ = c.Exit()
	c.Enter(LevelEdit, "third")
}

// TestController_LoopGuard drives the controller's RecordPatch through the W3
// threshold and checks the loop guard trips after more than 10 patches.
func TestController_LoopGuard(t *testing.T) {
	c := New(nil)
	if c.Memory().LoopGuard() {
		t.Fatal("fresh controller should not trip the loop guard")
	}
	for i := 0; i < 11; i++ {
		c.RecordPatch(i, "attempt", false)
	}
	if !c.Memory().LoopGuard() {
		t.Fatal("LoopGuard should trip after >10 patches")
	}
}

// TestController_PublishesEnterEvent verifies that Enter publishes a
// scope.enter event on a wired bus, with the level and reason populated.
func TestController_PublishesEnterEvent(t *testing.T) {
	bus := event.New()
	defer bus.Close()
	ch := bus.Subscribe(event.Topic("scope.enter"))

	c := New(bus)
	c.Enter(LevelRepo, "explore")

	select {
	case env := <-ch:
		evt, ok := env.Evt.(*event.ScopeEnterEvent)
		if !ok {
			t.Fatalf("got event %T, want *event.ScopeEnterEvent", env.Evt)
		}
		if evt.Type() != event.Topic("scope.enter") {
			t.Errorf("Type() = %q, want scope.enter", evt.Type())
		}
		if evt.Level != LevelRepo.String() {
			t.Errorf("Level = %q, want %q", evt.Level, LevelRepo.String())
		}
		if evt.Reason != "explore" {
			t.Errorf("Reason = %q, want explore", evt.Reason)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for scope.enter event")
	}
}
