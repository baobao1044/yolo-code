// Adapter wiring cognitive.Core into runtime.CognitiveCore (Sprint 12 INT-004).
// The runtime's CognitiveCore port is opaque so this bridge lives in the
// composition root and keeps internal/runtime free of cognitive imports
// (§15.15.2 import matrix). contextAdapter and promptAdapter already live in
// adapters.go; this file adds the cognitive bridge only.

package main

import (
	"context"

	cog "github.com/baobao1044/yolo-code/internal/cognitive"
	"github.com/baobao1044/yolo-code/internal/event"
	"github.com/baobao1044/yolo-code/internal/prompt"
	"github.com/baobao1044/yolo-code/internal/runtime"
	"github.com/baobao1044/yolo-code/internal/session"
)

// cognitiveAdapter implements runtime.CognitiveCore using cognitive.Core.
type cognitiveAdapter struct {
	core *cog.Core
}

// Think satisfies runtime.CognitiveCore. It converts the opaque runtime.Prompt
// back into []prompt.Message and bridges the cognitive.Turn to runtime types.
func (a *cognitiveAdapter) Think(ctx context.Context, p runtime.Prompt) (runtime.CognitiveTurn, error) {
	msgs, _ := p.([]prompt.Message)
	turn, err := a.core.Think(ctx, msgs)
	return cognitiveToRuntimeTurn(turn), err
}

// HasMore satisfies runtime.CognitiveCore.
func (a *cognitiveAdapter) HasMore(task *session.Task) bool {
	return a.core.HasMore(task)
}

// RecordToolResult satisfies runtime.CognitiveCore. Feeds the tool's output
// into the cognitive Core's conversation history so the next Think sees it,
// keyed by the provider's tool_call_id so the result pairs to the exact call it
// answers (an empty callID falls back to the Core's name/position guess).
func (a *cognitiveAdapter) RecordToolResult(callID, toolName, result string) {
	a.core.RecordToolResultID(callID, toolName, result)
}

// Reset satisfies runtime.CognitiveCore. Drops the accumulated conversation so
// the next task starts from a clean transcript.
func (a *cognitiveAdapter) Reset() { a.core.Reset() }

// Reflect satisfies runtime.CognitiveCore. It converts runtime verdict and
// observation into cognitive shapes, then adapts the decision back.
func (a *cognitiveAdapter) Reflect(ctx context.Context, task *session.Task, v runtime.Verdict, obs runtime.Observation) runtime.ReflectionDecision {
	cogVerdict := cog.Verdict{Pass: v.Pass, Reason: v.Reason}
	cogObs := cog.Observation{Text: obs.Stdout}
	dec := a.core.Reflect(ctx, task, cogVerdict, cogObs)
	return cognitiveToRuntimeDecision(dec)
}

func cognitiveToRuntimeTurn(t cog.Turn) runtime.CognitiveTurn {
	calls := make([]runtime.ToolCall, 0, len(t.ToolCalls))
	for _, c := range t.ToolCalls {
		calls = append(calls, runtime.ToolCall{
			ID:     c.ID,
			Tool:   c.Tool,
			Args:   c.Args,
			Reason: c.Reason,
		})
	}
	return runtime.CognitiveTurn{
		Final:      t.Final,
		Text:       t.Text,
		ToolCalls:  calls,
		TokensIn:   t.TokensIn,
		TokensOut:  t.TokensOut,
		UsageKnown: t.UsageKnown,
	}
}

func cognitiveToRuntimeDecision(d cog.ReflectionDecision) runtime.ReflectionDecision {
	return runtime.ReflectionDecision{
		Replan: d.Replan,
		// Path as well as Body: dropping it here left the patch adapter with no
		// target, which is a hard refusal ("missing target path") rather than a
		// degraded apply.
		Patch: runtime.PatchOp{Path: d.Patch.Path, Body: d.Patch.Body},
		Abort: d.Abort,
		Note:  d.Note,
	}
}

// newRealCognitiveCore builds a cognitive.Core with the supplied provider and
// the shared bus. The standard tool set is passed so the provider can include
// native function/tool definitions in the API request (models that support
// structured tool_calls when tools are provided).
func newRealCognitiveCore(provider cog.Provider, bus *event.Bus) runtime.CognitiveCore {
	_, adapter := newCognitiveCore(provider, bus)
	return adapter
}

// newCognitiveCore builds the raw cognitive.Core (provider + bus + tools) and
// returns it alongside the CognitiveCore adapter. The raw *cog.Core lets the
// TUI driver swap providers at runtime (slash command /model, /provider); the
// adapter is what the runtime consumes.
//
// The tool set comes from cog.DefaultTools() rather than a list retyped here:
// it is the same list that produces the provider's tool schemas, so what the
// model is offered and what the Core will admit cannot drift apart.
func newCognitiveCore(provider cog.Provider, bus *event.Bus) (*cog.Core, runtime.CognitiveCore) {
	tools := cog.DefaultTools()
	core := cog.New(provider, bus, tools...)
	core.SetToolPolicy(cog.NewToolPolicy(routableTools(tools)))
	return core, &cognitiveAdapter{core: core}
}

// routableTools is the set of tool names the model is permitted to emit: the
// advertised tools plus "patch". Naming patch here rather than in
// internal/cognitive keeps the rule where the route is — this package is the
// only layer that knows which tools have a dispatch path at all, because it is
// the layer that wires them (execAdapter.Dispatch sends "patch" to patch.Engine,
// which internal/exec may not import).
//
// patch is deliberately allowed even though nothing advertises it. It has a
// real route and, since the unprompted-write incident, a real approval gate at
// exec_adapter.patchRisk; the gate is what decides whether a model-chosen patch
// runs, and refusing it here would only move that decision somewhere it is not
// reviewed. The drift this arrangement can produce is the safe direction: a new
// route added to Dispatch without a line here is unreachable by the model, not
// silently reachable.
func routableTools(advertised []string) []string {
	return append(append([]string{}, advertised...), "patch")
}
