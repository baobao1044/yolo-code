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
	"fmt"
	"strings"

	"github.com/baobao1044/yolo-code/internal/event"
	"github.com/baobao1044/yolo-code/internal/prompt"
	"github.com/baobao1044/yolo-code/internal/session"
)

// Core is the Cognitive Core (File 07 §7.7). The provider streams responses;
// the bus carries token/thinking/tool-call events; the parser turns the
// accumulated text into a Turn. Sprint 3 wires provider + bus; the parser,
// policies, and cost controller land in L6-002…006.
type Core struct {
	provider Provider
	bus      *event.Bus
	tools    []string         // tool names the Planner may emit (passed to the provider for native tool calling)
	lastTurn Turn             // most recent Think result; HasMore consults this
	history  []prompt.Message // accumulated conversation across turns (tool calls + results)
}

// New constructs a Core. The bus is optional (unit tests can pass nil and
// inspect the returned Turn directly); Think is a no-op for publishing when
// bus is nil. Tools is optional (nil → the provider won't include tool
// definitions; the parser's ```tool block path still works).
func New(provider Provider, bus *event.Bus, tools ...string) *Core {
	return &Core{provider: provider, bus: bus, tools: tools}
}

// Think runs one Planner turn (File 07 §7.2.2): stream the provider, publish
// token/thinking deltas as events, accumulate the text, and return a parsed
// Turn (final answer or tool calls). The task ID is threaded via context
// (session.WithTaskID) so TokenEvent carries the right Task without changing
// the spec's Think(ctx, msgs) signature.
//
// For multi-turn agent loops, Think accumulates the conversation history
// across turns. On the first call, the compiled prompt messages initialize the
// history. On subsequent calls (after tool execution loops back to PLAN),
// the history already carries the prior conversation and the tool results
// injected by RecordToolResult — the new compiled prompt messages are skipped
// to avoid duplication.
func (c *Core) Think(ctx context.Context, msgs []prompt.Message) (Turn, error) {
	// On the first call, initialize history from the compiled prompt.
	// On subsequent calls, history already contains the prior conversation
	// (including tool results from RecordToolResult), so we skip the
	// re-compiled prompt to avoid duplicating the system/user messages.
	if len(c.history) == 0 {
		c.history = append(c.history, msgs...)
	}

	req := Request{Messages: c.history}

	// When the Core has been told which tools are available, pass them to the
	// provider so it can include native tool definitions in the request (e.g.
	// OpenAI function calling). This lets models that support structured tool
	// calling emit delta.tool_calls instead of inline tool tokens.
	if len(c.tools) > 0 {
		req.Tools = c.tools
	}

	stream, err := c.provider.Stream(ctx, req)
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
	c.lastTurn = turn

	// Record the assistant's response in the conversation history so the next
	// Think() call includes it in the prompt. For tool calls, we record the
	// assistant message with its tool call details; the tool results are
	// appended separately via RecordToolResult.
	if turn.Text != "" || len(turn.ToolCalls) > 0 {
		assistantMsg := prompt.Message{Role: "assistant", Content: turn.Text}
		c.history = append(c.history, assistantMsg)
	}

	return turn, nil
}

// RecordToolResult records a tool execution result in the conversation history.
// After a tool runs, the runtime calls this so the next Think() includes the
// tool's output in the messages sent to the model. This implements the
// multi-turn agent loop: Think → tool call → execute → RecordToolResult → Think.
// We use role "user" for tool results because it's universally supported by
// all chat completion endpoints (some don't support role "tool").
func (c *Core) RecordToolResult(toolName, result string) {
	c.history = append(c.history, prompt.Message{
		Role:    "user",
		Content: fmt.Sprintf("[Tool Result: %s]\n%s", toolName, result),
	})
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

// HasMore reports whether the task has remaining work (File 07 §7.5.3) —
// more todos or loop iterations. The runtime uses it to decide VERIFY→PLAN
// vs VERIFY→DONE. When the last Think produced tool calls, the agent still
// has work (it needs to loop back to PLAN to let the model reason about the
// tool results and decide next steps). When the model gave a direct answer
// (Final=true), there is nothing more to do.
func (c *Core) HasMore(*session.Task) bool { return !c.lastTurn.Final }
