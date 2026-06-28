// The Reasoner (File 07 §7.4) and the tool-call emission that turns a parsed
// plan into dispatchable work. Per §7.4, the Reasoner is not a separate call —
// it is the chain-of-thought the Planner/Reflection emit, already streamed as
// llm.thinking events by Think (L6-001). What remains for L6-004 is the
// emission step: a Planner turn that produced tool calls (the "plan") is
// turned into one tool.call event per call so the Executor can consume them.
// This is where the Cognitive Core hands work to Layer 7.

package cognitive

import (
	"context"

	"github.com/yolo-code/yolo/internal/event"
	"github.com/yolo-code/yolo/internal/session"
)

// EmitToolCalls publishes one tool.call event per tool call in the turn (File
// 07 §7.2.3 / §5.4.3): this is how a parsed plan becomes dispatchable work.
// It is the seam between the Cognitive Core (Layer 6) and the Execution
// Engine (Layer 7). A nil bus makes it a no-op so unit tests can inspect the
// Turn's calls directly without an event trace.
//
// The runtime's drive loop calls this after Think when the turn is not Final;
// a deny from the Tool Policy (L6-005) intercepts before dispatch and never
// reaches this emission. Sprint 3 emits the calls here; the runtime wires the
// dispatch in a later sprint.
func (c *Core) EmitToolCalls(ctx context.Context, turn Turn) {
	if c.bus == nil || len(turn.ToolCalls) == 0 {
		return
	}
	tid := event.TaskID(taskID(ctx))
	for _, call := range turn.ToolCalls {
		_ = c.bus.Publish(ctx, &event.ToolCallEvent{
			Task:   tid,
			Tool:   call.Tool,
			Args:   call.Args,
			Reason: call.Reason,
		})
	}
}

// Plan is the Reasoner's view of a turn that produced work: the visible
// reasoning (the chain-of-thought, §7.4) and the tool calls to dispatch. The
// runtime turns the plan into EmitToolCalls → Executor. Sprint 3 surfaces this
// so a test can assert "a plan + reflection turns into a tool call" end-to-end
// without the runtime's full drive loop.
type Plan struct {
	Thinking  string // the model's chain-of-thought (§7.4), for transparency
	ToolCalls []ToolCall
}

// Reason turns a Planner turn into a Plan (File 07 §7.4): it separates the
// chain-of-thought (the streamed thinking deltas, already published as
// llm.thinking) from the tool calls the turn produced. The "reasoner" itself
// has no decisions — it is the model's working shown to the user for
// transparency (P4); this method just frames it. Reflection's note can be
// folded into Thinking so the next PLAN iteration sees the root cause.
func (c *Core) Reason(turn Turn, reflectionNote string) Plan {
	return Plan{
		Thinking:  joinThinking(turn.Text, reflectionNote),
		ToolCalls: turn.ToolCalls,
	}
}

// joinThinking combines the visible reasoning and an optional reflection note
// into the plan's thinking field. When there's no reflection (the direct-plan
// path), the thinking is just the visible answer; when reflection fed in, the
// note prefixes it so the next iteration carries the root cause.
func joinThinking(answer, reflection string) string {
	if reflection == "" {
		return answer
	}
	return reflection + "\n" + answer
}

// Compile-time assertion that session is used (taskID/Reason reference it via
// ctx and the bus's Task field which derives from session.TaskID).
var _ session.TaskID
