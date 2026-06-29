// No-op stub ports for the Sprint 1 single-turn loop (File 15 §15.4, L2-005).
//
// The real layers land in Sprints 2–6; until then, the drive loop runs against
// stubs so the spine is testable now. Only the cognitive core has real canned
// behavior (a final answer) — everything else is a no-op that returns benign
// results. L2-005's stubbed cognitive.Core lives here too.

package runtime

import (
	"context"

	"github.com/baobao1044/yolo-code/internal/session"
)

// noopContextBuilder returns an empty context package.
type noopContextBuilder struct{}

func (noopContextBuilder) Build(context.Context, ContextRequest) (ContextPackage, error) {
	return nil, nil
}

// noopPromptCompiler returns the package unchanged.
type noopPromptCompiler struct{}

func (noopPromptCompiler) Compile(pkg ContextPackage) Prompt { return pkg }

// StubCognitive is the L2-005 stubbed cognitive core: it always answers
// directly (Final == true) with a canned message, so a prompt flows to a
// canned assistant message → DONE. The headless demo relies on this to make
// the single-turn loop observable without a real LLM.
type StubCognitive struct {
	Answer string
}

func (s StubCognitive) Think(context.Context, Prompt) (CognitiveTurn, error) {
	return CognitiveTurn{Final: true, Text: s.Answer}, nil
}

func (StubCognitive) HasMore(*session.Task) bool { return false }

func (StubCognitive) RecordToolResult(string, string) {}

// Reflect on a StubCognitive aborts (the stub never takes the tool path, so a
// verify failure here would be a wiring bug — abort surfaces it loudly rather
// than spinning). Real cores (cmd/yolo) override this with the LLM reflection.
func (StubCognitive) Reflect(context.Context, *session.Task, Verdict, Observation) ReflectionDecision {
	return ReflectionDecision{Abort: true, Note: "stub cognitive has no reflection"}
}

// noopVerifier passes everything (the stubbed loop never reaches VERIFY). Wired
// as the default in New so a nil Deps.Verify doesn't nil-panic once the drive
// loop drives the VERIFY state.
type noopVerifier struct{}

func (noopVerifier) Verify(context.Context, Observation, *session.Task, VerifyPolicy) (Verdict, error) {
	return Verdict{Pass: true, Severity: "pass", Reason: "noop"}, nil
}

// noopExecutor never needs approval and returns an empty observation. Default
// in New so a nil Deps.Exec doesn't nil-panic.
type noopExecutor struct{}

func (noopExecutor) NeedsApproval(ToolCall) bool { return false }
func (noopExecutor) Dispatch(context.Context, ToolCall) (Observation, error) {
	return Observation{}, nil
}

// noopPatcher accepts nothing (the stubbed loop never reaches PATCH). Default in
// New so a nil Deps.Patch doesn't nil-panic.
type noopPatcher struct{}

func (noopPatcher) Apply(context.Context, PatchOp) (PatchResult, error) {
	return PatchResult{Reason: "noop patcher"}, nil
}

// noopRestorer is a no-op rollback seam. Default in New so a nil Deps.Restore
// doesn't nil-panic; the real adapter wires session.Manager.Restore.
type noopRestorer struct{}

func (noopRestorer) Restore(context.Context, session.TaskID, string) error { return nil }

// noopScopeController is the disabled-scope stub: every tool is allowed, it
// never suggests a transition, and recorders are no-ops. New uses this when
// Deps.Scope is nil, so the drive loop's optional scope-control calls are
// safe but inert — preserving the pre-scope behaviour.
type noopScopeController struct{}

func (noopScopeController) Current() ScopeLevel      { return ScopeLevel(0) }
func (noopScopeController) Enter(ScopeLevel, string) {}
func (noopScopeController) Exit() ScopeLevel         { return ScopeLevel(0) }
func (noopScopeController) CanUseTool(string) bool   { return true }
func (noopScopeController) SuggestTransition(ScopeVerdict) ScopeTransition {
	return ScopeTransition{Action: ScopeActionNoOp}
}
func (noopScopeController) RecordFact(string)             {}
func (noopScopeController) RecordFailedHypothesis(string) {}
func (noopScopeController) RecordPatch(int, string, bool) {}

// noopWorkflowEngine returns a submit action immediately: when no dynamic
// workflow is wired, the runtime relies on its fixed FSM flow and the engine
// never overrides a routing decision. Next returns WFActionSubmit so any caller
// that consults it falls through to the legacy path.
type noopWorkflowEngine struct{}

func (noopWorkflowEngine) Next(string, *WFState, WFEvent) (WFAction, error) {
	return WFAction{Kind: WFActionSubmit, Note: "no workflow engine wired"}, nil
}
