// The provider-agnostic interface and the streaming shapes (File 07 §7.7.1). A
// Provider streams a response one Chunk at a time; the Core reads the channel,
// publishes token/thinking events as deltas arrive, and accumulates the text
// for parsing. This is the seam that makes S9 (add a provider without touching
// the runtime) true: one interface, one implementation per provider
// (OpenAI/Anthropic/Gemini/local llama.cpp).

package cognitive

import (
	"context"

	"github.com/baobao1044/yolo-code/internal/prompt"
	"github.com/baobao1044/yolo-code/internal/session"
)

// Request is the input to a Provider: the compiled prompt's messages plus the
// tool schemas the model may emit (File 07 §7.2.2). Sprint 3 carries tools as
// a list of names; the schemas are wired when the Tool Registry (File 08)
// lands.
type Request struct {
	Messages []prompt.Message
	Tools    []string // tool names the model may call; schemas wired in L7
}

// Chunk is one element of a streamed response. A turn is accumulated from the
// Delta strings; Thinking carries the model's chain-of-thought (rendered
// separately, File 07 §7.4); ToolCall is non-nil when the provider surfaces a
// structured tool call; Err terminates the stream with an error.
type Chunk struct {
	Delta    string    // a token/text delta of the visible answer
	Thinking string    // a chain-of-thought delta
	ToolCall *ToolCall // non-nil for a structured tool call
	Err      error     // non-nil terminates the stream with an error
}

// ToolCall is a tool the Planner chose (File 07 §7.2.3). The portable default
// parses tool calls from fenced ```tool blocks; providers with native tool
// calls surface them as Chunk.ToolCall. Args is the raw JSON args object.
type ToolCall struct {
	Tool   string
	Args   []byte // json.RawMessage
	Reason string
}

// Turn is the parsed result of one Planner call (File 07 §7.2.2): either a
// final answer (Final=true, Text set) or a set of tool calls (ToolCalls set).
// TokensIn/Out feed the Cost Controller's ledger.
type Turn struct {
	Text      string
	Final     bool
	ToolCalls []ToolCall
	TokensIn  int
	TokensOut int
}

// Provider is the provider-agnostic seam (File 07 §7.7.1). Stream returns a
// channel the caller drains; closing the channel signals a clean end, an Err
// chunk signals failure. Window returns the provider's context window (used by
// the Prompt Compiler's budget, File 06 §6.6.1).
type Provider interface {
	Stream(ctx context.Context, req Request) (<-chan Chunk, error)
	Window() int
}

// taskID retrieves the session task ID from the context (File 07 publishes
// TokenEvent{Task}; the runtime attaches the ID via session.WithTaskID).
func taskID(ctx context.Context) session.TaskID {
	return session.TaskIDFromContext(ctx)
}
