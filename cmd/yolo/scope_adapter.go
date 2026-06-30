// Adapter wiring scope.Controller into runtime.ScopeController (Scope Loop
// Engineering). The runtime may not import internal/scope, so this adapter
// lives in the composition root and translates the runtime-local mirror types
// (runtime.ScopeLevel / ScopeVerdict / ScopeAction / ScopeTransition) to and
// from the real scope.* types.

package main

import (
	"github.com/baobao1044/yolo-code/internal/event"
	"github.com/baobao1044/yolo-code/internal/runtime"
	"github.com/baobao1044/yolo-code/internal/scope"
)

// scopeAdapter implements runtime.ScopeController using a scope.Controller.
type scopeAdapter struct {
	ctrl *scope.Controller
}

// newScopeAdapter builds a scope controller backed by the shared event bus.
// The bus is nil-safe (scope.Controller.Enter never panics on a nil bus), so a
// nil bus is acceptable for tests that don't assert on scope events.
func newScopeAdapter(bus *event.Bus) *scopeAdapter {
	return &scopeAdapter{ctrl: scope.New(bus)}
}

// runtimeLevelToScope maps a runtime.ScopeLevel to a scope.Level. Both are int
// enumerations with the same iota order (Task=0 … Verify=5); the conversion is
// a plain cast so the two packages never need to share a type.
func runtimeLevelToScope(l runtime.ScopeLevel) scope.Level {
	return scope.Level(l)
}

func scopeLevelToRuntime(l scope.Level) runtime.ScopeLevel {
	return runtime.ScopeLevel(l)
}

func runtimeActionToScope(a runtime.ScopeAction) scope.Action {
	switch a {
	case runtime.ScopeActionExpand:
		return scope.ActionExpand
	case runtime.ScopeActionContract:
		return scope.ActionContract
	case runtime.ScopeActionStay:
		return scope.ActionStay
	default:
		return scope.ActionNoOp
	}
}

func scopeActionToRuntime(a scope.Action) runtime.ScopeAction {
	switch a {
	case scope.ActionExpand:
		return runtime.ScopeActionExpand
	case scope.ActionContract:
		return runtime.ScopeActionContract
	case scope.ActionStay:
		return runtime.ScopeActionStay
	default:
		return runtime.ScopeActionNoOp
	}
}

func (a *scopeAdapter) Current() runtime.ScopeLevel {
	return scopeLevelToRuntime(a.ctrl.Current())
}

func (a *scopeAdapter) Enter(level runtime.ScopeLevel, reason string) {
	a.ctrl.Enter(runtimeLevelToScope(level), reason)
}

func (a *scopeAdapter) Exit() runtime.ScopeLevel {
	return scopeLevelToRuntime(a.ctrl.Exit())
}

func (a *scopeAdapter) CanUseTool(tool string) bool {
	return a.ctrl.CanUseTool(tool)
}

func (a *scopeAdapter) SuggestTransition(v runtime.ScopeVerdict) runtime.ScopeTransition {
	sv := scope.Verdict{Pass: v.Pass, Stage: v.Stage, Hint: v.Hint, Reason: v.Reason}
	tr := a.ctrl.SuggestTransition(sv)
	return runtime.ScopeTransition{
		TargetLevel: scopeLevelToRuntime(tr.TargetLevel),
		Action:      scopeActionToRuntime(tr.Action),
		Reason:      tr.Reason,
	}
}

func (a *scopeAdapter) RecordFact(fact string)          { a.ctrl.RecordFact(fact) }
func (a *scopeAdapter) RecordFailedHypothesis(h string) { a.ctrl.RecordFailedHypothesis(h) }
func (a *scopeAdapter) RecordPatch(seq int, summary string, accepted bool) {
	a.ctrl.RecordPatch(seq, summary, accepted)
}
