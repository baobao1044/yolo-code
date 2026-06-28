// The Cognitive Core (File 07 §7.1/§7.7). The Core is a set of specialized,
// prompt-driven sub-roles invoked by the runtime at the right FSM state:
// the Planner (Think) at PLAN, Reflection at VERIFY failure, the Reasoner as
// the streamed chain-of-thought. Sprint 3 (L6-001…007) lands the Planner's
// streaming Think, the plan + tool-call parsing, reflection, the tool/verify
// policies, and the Cost Controller's degradation ladder — against mock and
// deterministic-stub providers (no real LLM call yet).

package cognitive

import (
	"context"
	"strings"

	"github.com/yolo-code/yolo/internal/event"
	"github.com/yolo-code/yolo/internal/prompt"
	"github.com/yolo-code/yolo/internal/session"
)

// Core is the Cognitive Core (File 07 §7.7). The provider streams responses;
// the bus carries token/thinking/tool-call events; the parser turns the
// accumulated text into a Turn. Sprint 3 wires provider + bus; the parser,
// policies, and cost controller land in L6-002…006.
type Core struct {
	provider Provider
	bus      *event.Bus
}

// New constructs a Core. The bus is optional (unit tests can pass nil and
// inspect the returned Turn directly); Think is a no-op for publishing when
// bus is nil.
func New(provider Provider, bus *event.Bus) *Core {
	return &Core{provider: provider, bus: bus}
}

// Think runs one Planner turn (File 07 §7.2.2): stream the provider, publish
// token/thinking deltas as events, accumulate the text, and return a parsed
// Turn (final answer or tool calls). The task ID is threaded via context
// (session.WithTaskID) so TokenEvent carries the right Task without changing
// the spec's Think(ctx, msgs) signature.
func (c *Core) Think(ctx context.Context, msgs []prompt.Message) (Turn, error) {
	stream, err := c.provider.Stream(ctx, Request{Messages: msgs})
	if err != nil {
		return Turn{}, err
	}

	var (
		buf      strings.Builder
		toolCall []ToolCall
	)
	for chunk := range stream {
		if chunk.Err != nil {
			return Turn{}, chunk.Err
		}
		if chunk.Delta != "" {
			buf.WriteString(chunk.Delta)
			c.publishTokens(ctx, chunk.Delta)
		}
		if chunk.Thinking != "" {
			c.publishThinking(ctx, chunk.Thinking)
		}
		if chunk.ToolCall != nil {
			toolCall = append(toolCall, *chunk.ToolCall)
		}
	}

	// Parse the accumulated text for fenced ```tool blocks (File 07 §7.2.3,
	// the provider-agnostic portable path). The parsed Turn carries the
	// visible answer as Text and the parsed tool calls; Final iff no calls.
	// Providers with native tool calls surface them as Chunk.ToolCall — those
	// merge with the parsed ones so both paths produce a Turn.
	turn := parseTurn(buf.String())
	if len(toolCall) > 0 {
		turn.ToolCalls = append(turn.ToolCalls, toolCall...)
		turn.Final = false
	}
	return turn, nil
}

// publishTokens emits an llm.token event for a delta (File 07 §7.2.2). A nil
// bus makes this a no-op so unit tests can drive the Core without a bus.
func (c *Core) publishTokens(ctx context.Context, delta string) {
	if c.bus == nil || delta == "" {
		return
	}
	_ = c.bus.Publish(ctx, &event.TokenEvent{Task: event.TaskID(taskID(ctx)), Delta: delta})
}

// publishThinking emits an llm.thinking event for a chain-of-thought delta
// (File 07 §7.4).
func (c *Core) publishThinking(ctx context.Context, delta string) {
	if c.bus == nil || delta == "" {
		return
	}
	_ = c.bus.Publish(ctx, &event.ThinkingEvent{Task: event.TaskID(taskID(ctx)), Delta: delta})
}

// HasMore reports whether the task has remaining work (File 07 §7.5.3) — more
// todos or loop iterations. The runtime uses it to decide VERIFY→PLAN vs
// VERIFY→DONE. Sprint 3 returns false (single-turn); the todo/loop model
// arrives with the plan parser (L6-002) and reflection loop (L6-003).
func (c *Core) HasMore(*session.Task) bool { return false }
