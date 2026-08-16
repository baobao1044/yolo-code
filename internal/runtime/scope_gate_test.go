// The scope gate's failure mode is not that it lets a tool through — that is
// its documented design ("a tool call the Planner emitted is a signal the work
// must happen; we don't silently drop it"). The failure mode is what it does to
// the scope loop on the way through.
//
// scopeLevelForTool mirrors the W2 permission table by hand, and its default
// arm answers LevelRepo for any name it does not recognise. LevelRepo is
// read-only. So an unrecognised tool makes the runtime call
// Enter(LevelRepo, "tool requires broader scope") — a sentence that is false in
// both halves: the level does not permit the tool either, and for a loop
// already at Edit or Verify it is a narrowing, not a broadening. The controller
// pushes the real level onto its history stack, adopts read-only, publishes a
// scope.enter nobody asked for, and the tool dispatches anyway.
//
// "patch" is the live instance rather than a hypothetical. routableTools in
// cmd/yolo admits it deliberately, it has a real dispatch path to patch.Engine,
// and it writes to disk. It is not in the W2 table, so every model-emitted
// patch drives the scope loop to read-only and then writes.
//
// Nothing caught it because Deps.Scope had no test in this package at all: the
// noop controller allows every tool, so the gate is inert everywhere except the
// one path (cmd/yolo/coord_runner.go) that wires a real controller.

package runtime

import (
	"context"
	"sync"
	"testing"

	"github.com/baobao1044/yolo-code/internal/event"
	"github.com/baobao1044/yolo-code/internal/session"
)

// spyScope is a scope controller that records what the runtime does to it. It
// allows only the tools it is told to allow, which is how a real controller
// behaves at a level — the W2 table is per-level and exclusive, not cumulative.
type spyScope struct {
	mu      sync.Mutex
	level   ScopeLevel
	allow   map[string]bool
	entered []ScopeLevel
}

func (s *spyScope) Current() ScopeLevel {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.level
}

func (s *spyScope) Enter(l ScopeLevel, _ string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entered = append(s.entered, l)
	s.level = l
}

func (s *spyScope) Exit() ScopeLevel { return s.Current() }

func (s *spyScope) CanUseTool(tool string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.allow[tool]
}

func (s *spyScope) SuggestTransition(ScopeVerdict) ScopeTransition {
	return ScopeTransition{Action: ScopeActionNoOp}
}
func (*spyScope) RecordFact(string)             {}
func (*spyScope) RecordFailedHypothesis(string) {}
func (*spyScope) RecordPatch(int, string, bool) {}

func (s *spyScope) enters() []ScopeLevel {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]ScopeLevel(nil), s.entered...)
}

// scopeCog emits its tool calls on the first turn and finishes on the second,
// so Submit returns instead of driving forever.
type scopeCog struct {
	mu    sync.Mutex
	calls []ToolCall
	done  bool
}

func (c *scopeCog) Think(context.Context, Prompt) (CognitiveTurn, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.done {
		return CognitiveTurn{Final: true, Text: "done"}, nil
	}
	c.done = true
	return CognitiveTurn{ToolCalls: c.calls}, nil
}
func (*scopeCog) HasMore(*session.Task) bool { return false }
func (*scopeCog) Reflect(context.Context, *session.Task, Verdict, Observation) ReflectionDecision {
	return ReflectionDecision{Abort: true, Note: "stub reflect"}
}
func (*scopeCog) RecordToolResult(string, string, string) {}
func (*scopeCog) Reset()                                  {}

// openScopeExec dispatches everything without an approval gate: these tests are
// about the scope loop's state, not about approval.
type openScopeExec struct {
	mu  sync.Mutex
	got []string
}

func (*openScopeExec) NeedsApproval(ToolCall) bool { return false }

func (e *openScopeExec) Dispatch(_ context.Context, call ToolCall) (Observation, error) {
	e.mu.Lock()
	e.got = append(e.got, call.Tool)
	e.mu.Unlock()
	return Observation{Stdout: "ran " + call.Tool, Tool: call.Tool}, nil
}

func (e *openScopeExec) dispatchedTools() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.got...)
}

func runWithScope(t *testing.T, scope ScopeController, calls ...ToolCall) *openScopeExec {
	t.Helper()
	bus := event.New()
	t.Cleanup(func() { _ = bus.Close() })
	store := session.NewFileStore(t.TempDir())
	smgr := session.New(session.Deps{Store: store, Bus: bus, Git: session.NewInMemCheckpointer()})
	sid, err := smgr.OpenSession(context.Background(), "test", "scope gate")
	if err != nil {
		t.Fatal(err)
	}
	exec := &openScopeExec{}
	core := New(Deps{
		Bus:       bus,
		Session:   smgr,
		Cognitive: &scopeCog{calls: calls},
		Exec:      exec,
		Scope:     scope,
	})
	if _, err := core.Submit(context.Background(), sid, "drive one tool call"); err != nil {
		t.Fatal(err)
	}
	return exec
}

// TestUnknownToolDoesNotNarrowTheScope is the defect stated honestly.
//
// The loop is at LevelEdit — it has earned the right to write. A "patch" call
// arrives, which no level in the W2 table names. The gate must not answer that
// by moving the loop to read-only: a level chosen by a default arm does not
// permit the tool either, so the move buys nothing and costs the loop's real
// level plus a bogus frame on its history stack.
//
// The tool still dispatches. That half is the design, and this test pins it so
// a later fix cannot quietly turn the gate into a dropper.
func TestUnknownToolDoesNotNarrowTheScope(t *testing.T) {
	const levelEdit = ScopeLevel(4)
	scope := &spyScope{level: levelEdit, allow: map[string]bool{"edit_file": true, "write_file": true}}

	exec := runWithScope(t, scope, ToolCall{ID: "c1", Tool: "patch", Args: []byte(`{}`)})

	if got := scope.enters(); len(got) != 0 {
		t.Errorf("unknown tool moved the scope loop to %v; it should have been left alone", got)
	}
	if got := scope.Current(); got != levelEdit {
		t.Errorf("scope level = %d, want %d (LevelEdit) — a patch call demoted the loop to read-only", got, levelEdit)
	}
	if got := exec.dispatchedTools(); len(got) != 1 || got[0] != "patch" {
		t.Errorf("dispatched %v, want [patch] — the gate must not drop the call", got)
	}
}

// TestKnownToolStillBroadens is the other half: for a tool the table does name,
// broadening is the whole point and must keep working. Without this the fix
// above could be "never call Enter", which would disable the gate entirely.
func TestKnownToolStillBroadens(t *testing.T) {
	const levelTask, levelVerify = ScopeLevel(0), ScopeLevel(5)
	scope := &spyScope{level: levelTask, allow: map[string]bool{"plan": true, "decompose": true}}

	runWithScope(t, scope, ToolCall{ID: "c1", Tool: "bash", Args: []byte(`{"command":"go test ./..."}`)})

	got := scope.enters()
	if len(got) != 1 || got[0] != levelVerify {
		t.Errorf("Enter calls = %v, want exactly [%d] (LevelVerify) for a bash call", got, levelVerify)
	}
}

// TestScopeLevelForToolReportsWhatItDoesNotKnow pins the seam the fix rests on.
// The mapping is a hand-kept mirror of internal/scope's table — this package
// may not import internal/scope (L2 may not depend on the scope package), so
// the duplication is structural and drift is the expected failure. The one
// thing the mirror must never do is invent an answer: a name it does not carry
// has to come back not-ok so the caller can decline to act on a guess.
func TestScopeLevelForToolReportsWhatItDoesNotKnow(t *testing.T) {
	known := map[string]ScopeLevel{
		"list_files":    1,
		"grep":          1,
		"read_file":     1,
		"view_function": 3,
		"call_graph":    3,
		"edit_file":     4,
		"write_file":    4,
		"run_test":      5,
		"bash":          5,
		"git_diff":      5,
	}
	for tool, want := range known {
		got, ok := scopeLevelForTool(tool)
		if !ok || got != want {
			t.Errorf("scopeLevelForTool(%q) = %d, %v; want %d, true", tool, got, ok, want)
		}
	}
	// "patch" is the one that matters: routable, mutating, and absent from the
	// W2 table. "" covers a malformed call, and the third is any future tool.
	for _, tool := range []string{"patch", "", "some_tool_added_next_sprint"} {
		if got, ok := scopeLevelForTool(tool); ok {
			t.Errorf("scopeLevelForTool(%q) = %d, true; want not-ok — the table does not name it", tool, got)
		}
	}
}
