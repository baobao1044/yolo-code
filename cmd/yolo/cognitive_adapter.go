// Adapter wiring cognitive.Core into runtime.CognitiveCore (Sprint 12 INT-004).
// The runtime's CognitiveCore port is opaque so this bridge lives in the
// composition root and keeps internal/runtime free of cognitive imports
// (§15.15.2 import matrix). contextAdapter and promptAdapter already live in
// adapters.go; this file adds the cognitive bridge only.

package main

import (
	"context"

	cog "github.com/yolo-code/yolo/internal/cognitive"
	"github.com/yolo-code/yolo/internal/event"
	"github.com/yolo-code/yolo/internal/prompt"
	"github.com/yolo-code/yolo/internal/runtime"
	"github.com/yolo-code/yolo/internal/session"
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
// into the cognitive Core's conversation history so the next Think sees it.
func (a *cognitiveAdapter) RecordToolResult(toolName, result string) {
	a.core.RecordToolResult(toolName, result)
}

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
			Tool:   c.Tool,
			Args:   c.Args,
			Reason: c.Reason,
		})
	}
	return runtime.CognitiveTurn{
		Final:     t.Final,
		Text:      t.Text,
		ToolCalls: calls,
	}
}

func cognitiveToRuntimeDecision(d cog.ReflectionDecision) runtime.ReflectionDecision {
	return runtime.ReflectionDecision{
		Replan: d.Replan,
		Patch:  runtime.PatchOp{Body: d.Patch.Body},
		Abort:  d.Abort,
		Note:   d.Note,
	}
}

// newRealCognitiveCore builds a cognitive.Core with the supplied provider and
// the shared bus. The standard tool set is passed so the provider can include
// native function/tool definitions in the API request (models like Kimi K2.7
// use structured tool_calls when tools are provided).
func newRealCognitiveCore(provider cog.Provider, bus *event.Bus) runtime.CognitiveCore {
	tools := []string{"list_files", "read_file", "edit_file", "bash"}
	return &cognitiveAdapter{core: cog.New(provider, bus, tools...)}
}
